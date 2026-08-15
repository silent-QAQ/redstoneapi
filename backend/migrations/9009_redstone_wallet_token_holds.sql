-- 9009_redstone_wallet_token_holds.sql
-- Forward-only extension for reserving dual-wallet funds during asynchronous
-- token work. Financial movements remain in redstone_wallet_ledger.

ALTER TABLE redstone_wallet_operations
    DROP CONSTRAINT IF EXISTS redstone_wallet_operations_operation;
ALTER TABLE redstone_wallet_operations
    ADD CONSTRAINT redstone_wallet_operations_operation CHECK (operation IN (
        'admin_grant', 'redeem_code', 'payment', 'settlement', 'refund',
        'opening_balance', 'token_charge', 'token_hold', 'token_release',
        'marketplace_debit'
    ));

ALTER TABLE redstone_wallet_ledger
    DROP CONSTRAINT IF EXISTS redstone_wallet_ledger_operation;
ALTER TABLE redstone_wallet_ledger
    ADD CONSTRAINT redstone_wallet_ledger_operation CHECK (operation IN (
        'admin_grant', 'redeem_code', 'payment', 'settlement', 'refund',
        'opening_balance', 'token_charge', 'token_hold', 'token_release',
        'marketplace_debit'
    ));

ALTER TABLE redstone_wallet_ledger
    DROP CONSTRAINT IF EXISTS redstone_wallet_ledger_bound_operation;
ALTER TABLE redstone_wallet_ledger
    ADD CONSTRAINT redstone_wallet_ledger_bound_operation
        CHECK (asset_type <> 'bound' OR operation IN (
            'admin_grant', 'redeem_code', 'token_charge', 'token_hold', 'token_release'
        ));

CREATE TABLE IF NOT EXISTS redstone_wallet_token_holds (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    hold_key VARCHAR(128) NOT NULL,
    reference_type VARCHAR(64) NOT NULL,
    reference_id VARCHAR(128) NOT NULL,
    bound_amount DECIMAL(20,8) NOT NULL DEFAULT 0,
    normal_amount DECIMAL(20,8) NOT NULL DEFAULT 0,
    captured_bound_amount DECIMAL(20,8) NOT NULL DEFAULT 0,
    captured_normal_amount DECIMAL(20,8) NOT NULL DEFAULT 0,
    state VARCHAR(16) NOT NULL DEFAULT 'held',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    settled_at TIMESTAMPTZ NULL,
    CONSTRAINT redstone_wallet_token_holds_key_unique UNIQUE (user_id, hold_key),
    CONSTRAINT redstone_wallet_token_holds_state CHECK (state IN ('held', 'captured', 'released')),
    CONSTRAINT redstone_wallet_token_holds_amounts_nonnegative CHECK (
        bound_amount >= 0 AND normal_amount >= 0
        AND captured_bound_amount >= 0 AND captured_normal_amount >= 0
        AND bound_amount + normal_amount > 0
        AND captured_bound_amount <= bound_amount
        AND captured_normal_amount <= normal_amount
    ),
    CONSTRAINT redstone_wallet_token_holds_settlement_consistent CHECK (
        (state = 'held' AND settled_at IS NULL AND captured_bound_amount = 0 AND captured_normal_amount = 0)
        OR (state = 'captured' AND settled_at IS NOT NULL)
        OR (state = 'released' AND settled_at IS NOT NULL AND captured_bound_amount = 0 AND captured_normal_amount = 0)
    )
);

CREATE INDEX IF NOT EXISTS idx_redstone_wallet_token_holds_user_state
    ON redstone_wallet_token_holds (user_id, state, created_at);

CREATE OR REPLACE FUNCTION redstone_wallet_token_holds_state_transition()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.state <> 'held' THEN
        RAISE EXCEPTION 'redstone_wallet_token_holds may only settle once';
    END IF;
    IF NEW.state = 'held' THEN
        RAISE EXCEPTION 'redstone_wallet_token_holds state cannot remain held on update';
    END IF;
    IF NEW.user_id <> OLD.user_id
        OR NEW.hold_key <> OLD.hold_key
        OR NEW.reference_type <> OLD.reference_type
        OR NEW.reference_id <> OLD.reference_id
        OR NEW.bound_amount <> OLD.bound_amount
        OR NEW.normal_amount <> OLD.normal_amount
        OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'redstone_wallet_token_holds immutable fields cannot be changed';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_redstone_wallet_token_holds_state_transition ON redstone_wallet_token_holds;
CREATE TRIGGER trg_redstone_wallet_token_holds_state_transition
    BEFORE UPDATE ON redstone_wallet_token_holds
    FOR EACH ROW EXECUTE FUNCTION redstone_wallet_token_holds_state_transition();

CREATE OR REPLACE FUNCTION redstone_wallet_token_holds_no_delete()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'redstone_wallet_token_holds is append-only except settlement state';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_redstone_wallet_token_holds_no_delete ON redstone_wallet_token_holds;
CREATE TRIGGER trg_redstone_wallet_token_holds_no_delete
    BEFORE DELETE ON redstone_wallet_token_holds
    FOR EACH ROW EXECUTE FUNCTION redstone_wallet_token_holds_no_delete();
