-- Account-reference products transfer existing sub2 accounts, never copied
-- credential blobs. This escrow record closes the interval between listing
-- and delivery: the seller cannot edit, delete, schedule, or list the account
-- again while it is available or reserved for a buyer.

CREATE TABLE IF NOT EXISTS redstone_market_account_escrows (
    account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE RESTRICT,
    product_id BIGINT NOT NULL UNIQUE REFERENCES redstone_market_products(id) ON DELETE RESTRICT,
    seller_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    prior_schedulable BOOLEAN NOT NULL,
    state VARCHAR(24) NOT NULL CHECK (state IN ('listed', 'reserved', 'transferring', 'transferred', 'released')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    released_at TIMESTAMPTZ NULL
);

CREATE INDEX IF NOT EXISTS idx_redstone_market_account_escrows_product_state
    ON redstone_market_account_escrows (product_id, state);

CREATE OR REPLACE FUNCTION redstone_market_guard_escrow_account()
RETURNS TRIGGER AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM redstone_market_account_escrows
        WHERE account_id = CASE WHEN TG_OP = 'DELETE' THEN OLD.id ELSE NEW.id END
          AND state IN ('listed', 'reserved')
    ) THEN
        RAISE EXCEPTION 'redstone marketplace escrow account is locked';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_redstone_market_guard_escrow_account_update ON accounts;
CREATE TRIGGER trg_redstone_market_guard_escrow_account_update
    BEFORE UPDATE OR DELETE ON accounts
    FOR EACH ROW EXECUTE FUNCTION redstone_market_guard_escrow_account();

COMMENT ON TABLE redstone_market_account_escrows IS
    'Locks existing accounts while listed/reserved. Credentials stay in the sub2 account domain and are never copied into marketplace rows.';
