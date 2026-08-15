-- 9400_redstone_account_verification.sql
-- Adds per-account verification history for user-controlled accounts.
-- Redstone namespace: 9400-9499 reserved for verification subsystem.
-- Forward-only, idempotent, no destructive changes.

ALTER TABLE redstone_user_controlled_accounts
    ADD COLUMN IF NOT EXISTS last_verified_at   TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS verify_score        SMALLINT,     -- 0-100
    ADD COLUMN IF NOT EXISTS verify_verdict      VARCHAR(20)   -- 'passed','marginal','failed','error'
        CHECK (verify_verdict IS NULL OR verify_verdict IN ('passed','marginal','failed','error')),
    ADD COLUMN IF NOT EXISTS verify_fail_streak  SMALLINT NOT NULL DEFAULT 0;

-- Append-only verification run log per account.
CREATE TABLE IF NOT EXISTS redstone_account_verify_runs (
    id              BIGSERIAL PRIMARY KEY,
    account_id      BIGINT NOT NULL REFERENCES redstone_user_controlled_accounts(account_id) ON DELETE CASCADE,
    triggered_by    VARCHAR(20) NOT NULL DEFAULT 'scheduler'
        CHECK (triggered_by IN ('scheduler', 'manual', 'on_create')),
    protocol        VARCHAR(20) NOT NULL
        CHECK (protocol IN ('anthropic', 'openai', 'gemini', 'unknown')),
    verdict         VARCHAR(20) NOT NULL
        CHECK (verdict IN ('passed', 'marginal', 'failed', 'error')),
    score           SMALLINT,
    details         JSONB NOT NULL DEFAULT '{}',
    duration_ms     INT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_redstone_account_verify_runs_account
    ON redstone_account_verify_runs (account_id, created_at DESC);

-- Immutable guard: once a run row is inserted it must never change.
CREATE OR REPLACE FUNCTION redstone_account_verify_runs_immutable()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'redstone_account_verify_runs is append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_redstone_account_verify_runs_immutable
    ON redstone_account_verify_runs;
CREATE TRIGGER trg_redstone_account_verify_runs_immutable
    BEFORE UPDATE OR DELETE ON redstone_account_verify_runs
    FOR EACH ROW EXECUTE FUNCTION redstone_account_verify_runs_immutable();

COMMENT ON TABLE  redstone_account_verify_runs IS
    'Append-only log of verification probes for user-controlled accounts.';
COMMENT ON COLUMN redstone_user_controlled_accounts.verify_score IS
    'Latest veridrop-style weighted score (0-100); NULL = never verified.';
COMMENT ON COLUMN redstone_user_controlled_accounts.verify_verdict IS
    'Latest verdict from verification run.';
COMMENT ON COLUMN redstone_user_controlled_accounts.verify_fail_streak IS
    'Consecutive failed/error runs. Auto-freeze threshold configurable (default 3).';
