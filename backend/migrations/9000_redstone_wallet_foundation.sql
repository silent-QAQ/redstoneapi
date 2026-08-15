-- 9000_redstone_wallet_foundation.sql
-- Redstone dual-wallet foundation. This migration is forward-only and safe to
-- replay on a partially provisioned PostgreSQL database.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS bound_balance DECIMAL(20,8) NOT NULL DEFAULT 0;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'users_bound_balance_nonnegative'
          AND conrelid = 'users'::regclass
    ) THEN
        ALTER TABLE users
            ADD CONSTRAINT users_bound_balance_nonnegative CHECK (bound_balance >= 0);
    END IF;
END $$;

-- One immutable operation receipt represents one idempotent wallet command.
-- A token charge can append two ledger rows (bound + normal), so request keys
-- are unique here instead of on a single ledger row.
CREATE TABLE IF NOT EXISTS redstone_wallet_operations (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    operation VARCHAR(32) NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL,
    request_fingerprint CHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT redstone_wallet_operations_operation CHECK (operation IN (
        'admin_grant', 'redeem_code', 'payment', 'settlement', 'refund',
        'opening_balance', 'token_charge', 'marketplace_debit'
    )),
    CONSTRAINT redstone_wallet_operations_idempotency_unique
        UNIQUE (user_id, idempotency_key)
);

CREATE OR REPLACE FUNCTION redstone_wallet_operations_immutable()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'redstone_wallet_operations is append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_redstone_wallet_operations_immutable ON redstone_wallet_operations;
CREATE TRIGGER trg_redstone_wallet_operations_immutable
    BEFORE UPDATE OR DELETE ON redstone_wallet_operations
    FOR EACH ROW EXECUTE FUNCTION redstone_wallet_operations_immutable();

CREATE TABLE IF NOT EXISTS redstone_wallet_ledger (
    id BIGSERIAL PRIMARY KEY,
    -- User deletion must be blocked so a financial audit trail cannot be
    -- removed through a foreign-key cascade. sub2api users are soft-deleted.
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    asset_type VARCHAR(16) NOT NULL,
    operation VARCHAR(32) NOT NULL,
    delta DECIMAL(20,8) NOT NULL,
    balance_after DECIMAL(20,8) NOT NULL,
    reference_type VARCHAR(64) NOT NULL,
    reference_id VARCHAR(128) NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL,
    request_fingerprint CHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT redstone_wallet_ledger_asset_type CHECK (asset_type IN ('normal', 'bound')),
    CONSTRAINT redstone_wallet_ledger_operation CHECK (operation IN (
        'admin_grant', 'redeem_code', 'payment', 'settlement', 'refund',
        'opening_balance', 'token_charge', 'marketplace_debit'
    )),
    CONSTRAINT redstone_wallet_ledger_nonzero_delta CHECK (delta <> 0),
    -- Legacy sub2 normal balances can be negative. Only their one-time opening
    -- snapshot may preserve that state; all Redstone runtime balances stay >= 0.
    CONSTRAINT redstone_wallet_ledger_runtime_balance_nonnegative
        CHECK (balance_after >= 0 OR (asset_type = 'normal' AND operation = 'opening_balance')),
    CONSTRAINT redstone_wallet_ledger_bound_operation
        CHECK (asset_type <> 'bound' OR operation IN ('admin_grant', 'redeem_code', 'token_charge')),
    CONSTRAINT redstone_wallet_ledger_opening_normal_only
        CHECK (operation <> 'opening_balance' OR asset_type = 'normal'),
    CONSTRAINT redstone_wallet_ledger_marketplace_normal_debit
        CHECK (operation <> 'marketplace_debit' OR (asset_type = 'normal' AND delta < 0)),
    CONSTRAINT redstone_wallet_ledger_reference_asset_unique
        UNIQUE (user_id, operation, asset_type, reference_type, reference_id, idempotency_key)
);

CREATE OR REPLACE FUNCTION redstone_wallet_ledger_immutable()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'redstone_wallet_ledger is append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_redstone_wallet_ledger_immutable ON redstone_wallet_ledger;
CREATE TRIGGER trg_redstone_wallet_ledger_immutable
    BEFORE UPDATE OR DELETE ON redstone_wallet_ledger
    FOR EACH ROW EXECUTE FUNCTION redstone_wallet_ledger_immutable();

CREATE INDEX IF NOT EXISTS idx_redstone_wallet_ledger_user_created_at
    ON redstone_wallet_ledger (user_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_redstone_wallet_operations_user_created_at
    ON redstone_wallet_operations (user_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_redstone_wallet_ledger_user_idempotency
    ON redstone_wallet_ledger (user_id, idempotency_key);

-- Preserve pre-Redstone normal balances as auditable opening entries. Existing
-- normal balances can be negative in legacy deployments, so only bound balance
-- is constrained nonnegative at the ledger level.
INSERT INTO redstone_wallet_operations (
    user_id, operation, idempotency_key, request_fingerprint, created_at
)
SELECT
    id, 'opening_balance',
    'redstone-wallet-opening-' || id::text,
    md5('redstone-wallet-opening-' || id::text)::text || md5('9000_redstone_wallet_foundation')::text,
    COALESCE(created_at, NOW())
FROM users
WHERE balance <> 0
ON CONFLICT (user_id, idempotency_key) DO NOTHING;

INSERT INTO redstone_wallet_ledger (
    user_id, asset_type, operation, delta, balance_after,
    reference_type, reference_id, idempotency_key, request_fingerprint, created_at
)
SELECT
    id, 'normal', 'opening_balance', balance, balance,
    'migration', '9000_redstone_wallet_foundation',
    'redstone-wallet-opening-' || id::text,
    md5('redstone-wallet-opening-' || id::text)::text || md5('9000_redstone_wallet_foundation')::text,
    COALESCE(created_at, NOW())
FROM users
WHERE balance <> 0
ON CONFLICT (user_id, operation, asset_type, reference_type, reference_id, idempotency_key) DO NOTHING;

COMMENT ON COLUMN users.bound_balance IS 'Redstone binding balance: non-withdrawable and token-only';
COMMENT ON TABLE redstone_wallet_operations IS 'Redstone immutable idempotency receipt for wallet commands';
COMMENT ON TABLE redstone_wallet_ledger IS 'Redstone immutable dual-wallet ledger; corrections use compensating entries';
