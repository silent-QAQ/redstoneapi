-- Marketplace operational controls. The current user seller fee is stored
-- separately from immutable order snapshots so an administrator can change
-- future checkout pricing without rewriting historical settlement records.

CREATE TABLE IF NOT EXISTS redstone_market_fee_policy (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    user_service_fee_rate DECIMAL(9,8) NOT NULL DEFAULT 0.05000000
        CHECK (user_service_fee_rate >= 0 AND user_service_fee_rate <= 1),
    updated_by_user_id BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO redstone_market_fee_policy (singleton, user_service_fee_rate)
VALUES (TRUE, 0.05000000)
ON CONFLICT (singleton) DO NOTHING;

CREATE TABLE IF NOT EXISTS redstone_market_fee_policy_audit (
    id BIGSERIAL PRIMARY KEY,
    prior_user_service_fee_rate DECIMAL(9,8) NOT NULL
        CHECK (prior_user_service_fee_rate >= 0 AND prior_user_service_fee_rate <= 1),
    next_user_service_fee_rate DECIMAL(9,8) NOT NULL
        CHECK (next_user_service_fee_rate >= 0 AND next_user_service_fee_rate <= 1),
    actor_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    reason VARCHAR(500) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_redstone_market_fee_policy_audit_created
    ON redstone_market_fee_policy_audit (created_at DESC, id DESC);

CREATE OR REPLACE FUNCTION redstone_market_fee_policy_audit_immutable()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'redstone marketplace fee policy audits are append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_redstone_market_fee_policy_audit_immutable ON redstone_market_fee_policy_audit;
CREATE TRIGGER trg_redstone_market_fee_policy_audit_immutable
    BEFORE UPDATE OR DELETE ON redstone_market_fee_policy_audit
    FOR EACH ROW EXECUTE FUNCTION redstone_market_fee_policy_audit_immutable();

COMMENT ON TABLE redstone_market_fee_policy IS
    'Singleton current fee for future user-seller orders; official orders always snapshot zero.';
COMMENT ON TABLE redstone_market_fee_policy_audit IS
    'Append-only administrator audit of marketplace user seller fee policy changes.';
