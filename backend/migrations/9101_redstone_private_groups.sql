-- Redstone-owned lifecycle metadata for sub2 exclusive groups.  The group
-- itself, API-key group selection and scheduling all remain owned by sub2.

CREATE TABLE IF NOT EXISTS redstone_private_groups (
    group_id BIGINT PRIMARY KEY REFERENCES groups(id) ON DELETE RESTRICT,
    owner_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    name VARCHAR(60) NOT NULL,
    platform VARCHAR(50) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'archived')),
    idempotency_key VARCHAR(128) NOT NULL,
    request_fingerprint CHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (owner_user_id, idempotency_key)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_redstone_private_groups_owner_name_active
    ON redstone_private_groups(owner_user_id, name) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_redstone_private_groups_owner
    ON redstone_private_groups(owner_user_id, status, updated_at DESC);

CREATE TABLE IF NOT EXISTS redstone_private_group_members (
    group_id BIGINT NOT NULL REFERENCES redstone_private_groups(group_id) ON DELETE RESTRICT,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    role VARCHAR(16) NOT NULL CHECK (role IN ('owner', 'member')),
    status VARCHAR(16) NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'revoked')),
    granted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ,
    PRIMARY KEY (group_id, user_id),
    CHECK ((status = 'active' AND revoked_at IS NULL) OR (status = 'revoked' AND revoked_at IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_redstone_private_group_owner_member
    ON redstone_private_group_members(group_id) WHERE role = 'owner';
CREATE INDEX IF NOT EXISTS idx_redstone_private_group_members_user
    ON redstone_private_group_members(user_id, status, granted_at DESC);

CREATE TABLE IF NOT EXISTS redstone_private_group_audits (
    id BIGSERIAL PRIMARY KEY,
    group_id BIGINT NOT NULL REFERENCES redstone_private_groups(group_id) ON DELETE RESTRICT,
    actor_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    action VARCHAR(64) NOT NULL,
    detail VARCHAR(1000) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_redstone_private_group_audits_group
    ON redstone_private_group_audits(group_id, created_at DESC);

CREATE OR REPLACE FUNCTION redstone_private_group_audit_append_only()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'redstone private-group audit is append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_redstone_private_group_audit_append_only ON redstone_private_group_audits;
CREATE TRIGGER trg_redstone_private_group_audit_append_only
    BEFORE UPDATE OR DELETE ON redstone_private_group_audits
    FOR EACH ROW EXECUTE FUNCTION redstone_private_group_audit_append_only();
