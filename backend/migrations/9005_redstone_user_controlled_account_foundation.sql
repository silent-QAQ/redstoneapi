-- User-controlled accounts are deliberately separate from normal gateway
-- accounts. The accounts row retains only non-secret runtime metadata; its
-- credentials remain empty and it is never eligible for global scheduling.

CREATE TABLE IF NOT EXISTS redstone_user_controlled_accounts (
    account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE RESTRICT,
    owner_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    provider VARCHAR(50) NOT NULL,
    lifecycle VARCHAR(24) NOT NULL DEFAULT 'pending'
        CHECK (lifecycle IN ('pending', 'active', 'frozen', 'revoked')),
    visibility VARCHAR(24) NOT NULL DEFAULT 'private'
        CHECK (visibility IN ('private', 'shared', 'public')),
    health_state VARCHAR(24) NOT NULL DEFAULT 'unknown'
        CHECK (health_state IN ('unknown', 'healthy', 'degraded', 'unhealthy')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_redstone_user_controlled_accounts_owner
    ON redstone_user_controlled_accounts (owner_user_id, lifecycle, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_redstone_user_controlled_accounts_visibility
    ON redstone_user_controlled_accounts (visibility, lifecycle, created_at DESC);

-- Credentials are envelope encrypted with a Redstone-specific account KEK.
-- Neither the raw credential nor a reusable plaintext credential projection
-- belongs in accounts.credentials, a cache, or an audit row.
CREATE TABLE IF NOT EXISTS redstone_user_account_secrets (
    account_id BIGINT PRIMARY KEY REFERENCES redstone_user_controlled_accounts(account_id) ON DELETE CASCADE,
    ciphertext BYTEA NOT NULL,
    key_version VARCHAR(80) NOT NULL,
    wrapped_dek BYTEA NOT NULL,
    aad_version SMALLINT NOT NULL DEFAULT 1 CHECK (aad_version = 1),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE OR REPLACE FUNCTION redstone_verify_user_controlled_account()
RETURNS TRIGGER AS $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM accounts a
        WHERE a.id = NEW.account_id
          AND (
              a.schedulable
              OR a.credentials <> '{}'::jsonb
              OR a.proxy_id IS NOT NULL
          )
    ) THEN
        RAISE EXCEPTION 'redstone user-controlled account must be unschedulable, credential-empty, and proxy-free';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_redstone_verify_user_controlled_account
    ON redstone_user_controlled_accounts;
CREATE TRIGGER trg_redstone_verify_user_controlled_account
    BEFORE INSERT OR UPDATE OF account_id
    ON redstone_user_controlled_accounts
    FOR EACH ROW EXECUTE FUNCTION redstone_verify_user_controlled_account();

CREATE OR REPLACE FUNCTION redstone_guard_user_controlled_account_runtime()
RETURNS TRIGGER AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM redstone_user_controlled_accounts r
        WHERE r.account_id = NEW.id
    ) AND (
        NEW.schedulable
        OR NEW.credentials <> '{}'::jsonb
        OR NEW.proxy_id IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'redstone user-controlled account cannot enter global scheduling or store credentials in accounts';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_redstone_guard_user_controlled_account_runtime
    ON accounts;
CREATE TRIGGER trg_redstone_guard_user_controlled_account_runtime
    BEFORE UPDATE OF schedulable, credentials, proxy_id
    ON accounts
    FOR EACH ROW EXECUTE FUNCTION redstone_guard_user_controlled_account_runtime();

CREATE OR REPLACE FUNCTION redstone_reject_user_controlled_account_group()
RETURNS TRIGGER AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM redstone_user_controlled_accounts r
        WHERE r.account_id = NEW.account_id
    ) THEN
        RAISE EXCEPTION 'redstone user-controlled account cannot belong to a global account group';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_redstone_reject_user_controlled_account_group
    ON account_groups;
CREATE TRIGGER trg_redstone_reject_user_controlled_account_group
    BEFORE INSERT OR UPDATE OF account_id
    ON account_groups
    FOR EACH ROW EXECUTE FUNCTION redstone_reject_user_controlled_account_group();
