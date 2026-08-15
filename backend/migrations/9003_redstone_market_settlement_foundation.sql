-- Redstone marketplace settlement/audit support. This migration is forward-only
-- and may be applied repeatedly on an existing 9000-9002 installation.

CREATE INDEX IF NOT EXISTS idx_redstone_market_orders_due_delivery
    ON redstone_market_orders (settlement_due_at, delivered_at, id)
    WHERE status = 'delivered';

CREATE TABLE IF NOT EXISTS redstone_market_financial_events (
    id BIGSERIAL PRIMARY KEY,
    order_id BIGINT NOT NULL REFERENCES redstone_market_orders(id) ON DELETE RESTRICT,
    action VARCHAR(16) NOT NULL CHECK (action IN ('settlement', 'refund')),
    recipient_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    amount DECIMAL(20,8) NOT NULL CHECK (amount > 0),
    wallet_operation_key VARCHAR(128) NOT NULL,
    actor_user_id BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT redstone_market_financial_events_order_action_unique UNIQUE (order_id, action),
    CONSTRAINT redstone_market_financial_events_wallet_key_unique UNIQUE (wallet_operation_key)
);

CREATE INDEX IF NOT EXISTS idx_redstone_market_financial_events_recipient
    ON redstone_market_financial_events (recipient_user_id, created_at DESC);

CREATE OR REPLACE FUNCTION redstone_market_financial_events_immutable()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'redstone_market_financial_events is append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_redstone_market_financial_events_immutable ON redstone_market_financial_events;
CREATE TRIGGER trg_redstone_market_financial_events_immutable
    BEFORE UPDATE OR DELETE ON redstone_market_financial_events
    FOR EACH ROW EXECUTE FUNCTION redstone_market_financial_events_immutable();

COMMENT ON TABLE redstone_market_financial_events IS
    'Immutable marketplace settlement/refund audit; wallet ledger remains the accounting source of truth';
