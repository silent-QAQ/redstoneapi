-- Redstone marketplace governance. These controls freeze marketplace proceeds
-- rather than a user's wallet, keeping the wallet domain as the sole owner of
-- balance mutations while preserving a complete marketplace audit trail.

CREATE UNIQUE INDEX IF NOT EXISTS idx_redstone_market_delivery_account_once
    ON redstone_market_delivery_items (account_id)
    WHERE account_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_redstone_market_reports_reporter_once
    ON redstone_market_reports (product_id, reporter_user_id);

CREATE TABLE IF NOT EXISTS redstone_market_seller_controls (
    seller_user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE RESTRICT,
    frozen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    frozen_by_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    reason VARCHAR(500) NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS redstone_market_order_holds (
    order_id BIGINT NOT NULL REFERENCES redstone_market_orders(id) ON DELETE RESTRICT,
    source VARCHAR(24) NOT NULL CHECK (source IN ('seller_freeze', 'report')),
    source_id BIGINT NOT NULL DEFAULT 0,
    reason VARCHAR(500) NOT NULL,
    actor_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (order_id, source, source_id)
);

CREATE INDEX IF NOT EXISTS idx_redstone_market_order_holds_order
    ON redstone_market_order_holds (order_id);

CREATE TABLE IF NOT EXISTS redstone_market_reversals (
    order_id BIGINT PRIMARY KEY REFERENCES redstone_market_orders(id) ON DELETE RESTRICT,
    seller_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    buyer_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    seller_debit_amount DECIMAL(20,8) NOT NULL CHECK (seller_debit_amount > 0),
    buyer_credit_amount DECIMAL(20,8) NOT NULL CHECK (buyer_credit_amount > 0),
    seller_wallet_operation_key VARCHAR(128) NOT NULL UNIQUE,
    buyer_wallet_operation_key VARCHAR(128) NOT NULL UNIQUE,
    actor_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    reason VARCHAR(500) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS redstone_market_governance_audit (
    id BIGSERIAL PRIMARY KEY,
    entity_type VARCHAR(32) NOT NULL,
    entity_id BIGINT NOT NULL,
    action VARCHAR(48) NOT NULL,
    actor_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    reason VARCHAR(500) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_redstone_market_governance_audit_entity
    ON redstone_market_governance_audit (entity_type, entity_id, created_at DESC);

CREATE OR REPLACE FUNCTION redstone_market_governance_immutable()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'redstone marketplace governance records are append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_redstone_market_reversals_immutable ON redstone_market_reversals;
CREATE TRIGGER trg_redstone_market_reversals_immutable
    BEFORE UPDATE OR DELETE ON redstone_market_reversals
    FOR EACH ROW EXECUTE FUNCTION redstone_market_governance_immutable();

DROP TRIGGER IF EXISTS trg_redstone_market_governance_audit_immutable ON redstone_market_governance_audit;
CREATE TRIGGER trg_redstone_market_governance_audit_immutable
    BEFORE UPDATE OR DELETE ON redstone_market_governance_audit
    FOR EACH ROW EXECUTE FUNCTION redstone_market_governance_immutable();

COMMENT ON TABLE redstone_market_order_holds IS
    'Marketplace-only payout holds. A hold prevents automatic settlement but does not alter wallet balances.';
