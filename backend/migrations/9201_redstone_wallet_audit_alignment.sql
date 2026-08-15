-- Keep the wallet's persisted operation policy aligned with the explicit
-- operating-event types introduced by 9200. This is forward-only because
-- deployed databases may already have the earlier constraint checksum.

ALTER TABLE redstone_wallet_ledger
    DROP CONSTRAINT IF EXISTS redstone_wallet_ledger_normal_debit;

ALTER TABLE redstone_wallet_ledger
    ADD CONSTRAINT redstone_wallet_ledger_normal_debit CHECK (
        (operation <> 'marketplace_debit' OR (asset_type = 'normal' AND delta < 0))
        AND (operation <> 'withdrawal' OR (asset_type = 'normal' AND delta < 0))
        AND (operation <> 'admin_adjustment' OR asset_type = 'normal')
    );

COMMENT ON CONSTRAINT redstone_wallet_ledger_normal_debit ON redstone_wallet_ledger IS
    'Marketplace and withdrawal are normal-balance debits; administrator adjustments remain normal-balance compensations.';

ALTER TABLE redstone_wallet_operations
    ADD COLUMN IF NOT EXISTS result_normal_after DECIMAL(20,8),
    ADD COLUMN IF NOT EXISTS result_bound_after DECIMAL(20,8);

ALTER TABLE redstone_wallet_operations
    DROP CONSTRAINT IF EXISTS redstone_wallet_operations_result_snapshot_pair;

ALTER TABLE redstone_wallet_operations
    ADD CONSTRAINT redstone_wallet_operations_result_snapshot_pair CHECK (
        (result_normal_after IS NULL AND result_bound_after IS NULL)
        OR (result_normal_after >= 0 AND result_bound_after >= 0)
    );

-- Wallet operation receipts are append-only except for recording their one
-- durable result snapshot immediately after the business mutation succeeds.
CREATE OR REPLACE FUNCTION redstone_wallet_operations_immutable()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.user_id = NEW.user_id
        AND OLD.operation = NEW.operation
        AND OLD.idempotency_key = NEW.idempotency_key
        AND OLD.request_fingerprint = NEW.request_fingerprint
        AND OLD.created_at = NEW.created_at
        AND OLD.result_normal_after IS NULL
        AND OLD.result_bound_after IS NULL
        AND NEW.result_normal_after IS NOT NULL
        AND NEW.result_bound_after IS NOT NULL THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'redstone_wallet_operations is append-only';
END;
$$ LANGUAGE plpgsql;

ALTER TABLE redstone_wallet_token_holds
    ADD COLUMN IF NOT EXISTS normal_balance_after DECIMAL(20,8),
    ADD COLUMN IF NOT EXISTS bound_balance_after DECIMAL(20,8);

ALTER TABLE redstone_wallet_token_holds
    DROP CONSTRAINT IF EXISTS redstone_wallet_token_holds_balance_snapshot_pair;

ALTER TABLE redstone_wallet_token_holds
    ADD CONSTRAINT redstone_wallet_token_holds_balance_snapshot_pair CHECK (
        (normal_balance_after IS NULL AND bound_balance_after IS NULL)
        OR (normal_balance_after >= 0 AND bound_balance_after >= 0)
    );
