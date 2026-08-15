-- Redstone sharing migrations use the independent 9100+ namespace.
-- This domain references the existing accounts table by ID only. Credentials,
-- OAuth tokens, proxies and account lifecycle continue to belong to sub2.

CREATE TABLE IF NOT EXISTS redstone_account_share_rooms (
    id BIGSERIAL PRIMARY KEY,
    owner_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    name VARCHAR(120) NOT NULL,
    description VARCHAR(2000) NOT NULL DEFAULT '',
    platform VARCHAR(50) NOT NULL,
    visibility VARCHAR(16) NOT NULL DEFAULT 'private'
        CHECK (visibility IN ('private', 'public')),
    status VARCHAR(16) NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft', 'pending_review', 'active', 'suspended', 'closed')),
    requires_approval BOOLEAN NOT NULL DEFAULT FALSE,
    seat_limit INTEGER NOT NULL DEFAULT 1 CHECK (seat_limit BETWEEN 1 AND 30),
    lease_seconds INTEGER NOT NULL DEFAULT 3600 CHECK (lease_seconds BETWEEN 60 AND 86400),
    idle_timeout_seconds INTEGER NOT NULL DEFAULT 1800 CHECK (idle_timeout_seconds BETWEEN 60 AND 86400),
    lease_price DECIMAL(20,8) NOT NULL DEFAULT 0 CHECK (lease_price >= 0),
    platform_fee_rate DECIMAL(10,8) NOT NULL DEFAULT 0.05000000
        CHECK (platform_fee_rate >= 0 AND platform_fee_rate <= 1),
    review_note VARCHAR(1000) NOT NULL DEFAULT '',
    reviewed_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    reviewed_at TIMESTAMPTZ,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT redstone_account_share_rooms_public_reviewed
        CHECK (visibility = 'private' OR status IN ('pending_review', 'active', 'suspended', 'closed'))
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_redstone_share_room_owner_name_live
    ON redstone_account_share_rooms(owner_user_id, LOWER(name))
    WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_redstone_share_room_square
    ON redstone_account_share_rooms(platform, updated_at DESC, id DESC)
    WHERE deleted_at IS NULL AND visibility = 'public' AND status = 'active';

CREATE TABLE IF NOT EXISTS redstone_account_share_room_accounts (
    room_id BIGINT NOT NULL REFERENCES redstone_account_share_rooms(id) ON DELETE CASCADE,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
    state VARCHAR(16) NOT NULL DEFAULT 'active'
        CHECK (state IN ('active', 'draining', 'removed')),
    bound_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    unbound_at TIMESTAMPTZ,
    PRIMARY KEY (room_id, account_id),
    CONSTRAINT redstone_account_share_room_account_unbound
        CHECK ((state = 'removed') = (unbound_at IS NOT NULL))
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_redstone_share_live_account_binding
    ON redstone_account_share_room_accounts(account_id)
    WHERE state IN ('active', 'draining');
CREATE INDEX IF NOT EXISTS idx_redstone_share_room_accounts_room
    ON redstone_account_share_room_accounts(room_id, state, bound_at);

CREATE TABLE IF NOT EXISTS redstone_account_share_invites (
    id BIGSERIAL PRIMARY KEY,
    room_id BIGINT NOT NULL REFERENCES redstone_account_share_rooms(id) ON DELETE CASCADE,
    invited_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    invited_by_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    status VARCHAR(16) NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'accepted', 'revoked', 'expired')),
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_redstone_share_active_invite
    ON redstone_account_share_invites(room_id, invited_user_id)
    WHERE status = 'active';

CREATE TABLE IF NOT EXISTS redstone_account_share_memberships (
    id BIGSERIAL PRIMARY KEY,
    room_id BIGINT NOT NULL REFERENCES redstone_account_share_rooms(id) ON DELETE RESTRICT,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    invite_id BIGINT REFERENCES redstone_account_share_invites(id) ON DELETE SET NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'active', 'ending', 'ended', 'revoked')),
    queued_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    joined_at TIMESTAMPTZ,
    ended_at TIMESTAMPTZ,
    end_reason VARCHAR(64) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT redstone_account_share_membership_state
        CHECK (
            (status = 'queued' AND joined_at IS NULL AND ended_at IS NULL)
            OR (status IN ('active', 'ending') AND joined_at IS NOT NULL AND ended_at IS NULL)
            OR (status IN ('ended', 'revoked') AND ended_at IS NOT NULL)
        )
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_redstone_share_room_user_live
    ON redstone_account_share_memberships(room_id, user_id)
    WHERE status IN ('queued', 'active', 'ending');
CREATE INDEX IF NOT EXISTS idx_redstone_share_membership_queue
    ON redstone_account_share_memberships(room_id, status, queued_at, id);
CREATE INDEX IF NOT EXISTS idx_redstone_share_membership_user
    ON redstone_account_share_memberships(user_id, updated_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS redstone_account_share_leases (
    id BIGSERIAL PRIMARY KEY,
    room_id BIGINT NOT NULL REFERENCES redstone_account_share_rooms(id) ON DELETE RESTRICT,
    membership_id BIGINT NOT NULL REFERENCES redstone_account_share_memberships(id) ON DELETE RESTRICT,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    state VARCHAR(16) NOT NULL DEFAULT 'active'
        CHECK (state IN ('active', 'released', 'expired')),
    granted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    released_at TIMESTAMPTZ,
    release_reason VARCHAR(64) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT redstone_account_share_lease_expiry CHECK (expires_at > granted_at),
    CONSTRAINT redstone_account_share_lease_release CHECK ((state = 'active') = (released_at IS NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_redstone_share_active_lease_membership
    ON redstone_account_share_leases(membership_id) WHERE state = 'active';
CREATE INDEX IF NOT EXISTS idx_redstone_share_active_lease_expiry
    ON redstone_account_share_leases(room_id, expires_at) WHERE state = 'active';

CREATE TABLE IF NOT EXISTS redstone_account_share_reviews (
    id BIGSERIAL PRIMARY KEY,
    room_id BIGINT NOT NULL REFERENCES redstone_account_share_rooms(id) ON DELETE RESTRICT,
    membership_id BIGINT NOT NULL REFERENCES redstone_account_share_memberships(id) ON DELETE RESTRICT,
    reviewer_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    rating SMALLINT NOT NULL CHECK (rating BETWEEN 1 AND 5),
    body VARCHAR(1000) NOT NULL DEFAULT '',
    moderation_status VARCHAR(16) NOT NULL DEFAULT 'visible'
        CHECK (moderation_status IN ('visible', 'hidden', 'removed')),
    moderated_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    moderation_note VARCHAR(1000) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (membership_id)
);
CREATE INDEX IF NOT EXISTS idx_redstone_share_reviews_room
    ON redstone_account_share_reviews(room_id, moderation_status, created_at DESC);

CREATE TABLE IF NOT EXISTS redstone_account_share_policies (
    id BIGSERIAL PRIMARY KEY,
    version BIGINT NOT NULL UNIQUE CHECK (version > 0),
    status VARCHAR(16) NOT NULL CHECK (status IN ('active', 'superseded')),
    public_room_allowed BOOLEAN NOT NULL DEFAULT TRUE,
    max_lease_seconds INTEGER NOT NULL CHECK (max_lease_seconds BETWEEN 60 AND 86400),
    default_platform_fee_rate DECIMAL(10,8) NOT NULL
        CHECK (default_platform_fee_rate >= 0 AND default_platform_fee_rate <= 1),
    created_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    reason VARCHAR(1000) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_redstone_share_active_policy
    ON redstone_account_share_policies(status) WHERE status = 'active';
INSERT INTO redstone_account_share_policies
    (version, status, public_room_allowed, max_lease_seconds, default_platform_fee_rate, reason)
SELECT 1, 'active', TRUE, 86400, 0.05000000, 'initial Redstone sharing policy'
WHERE NOT EXISTS (
    SELECT 1 FROM redstone_account_share_policies WHERE status = 'active'
);

CREATE TABLE IF NOT EXISTS redstone_account_share_quota_policies (
    id BIGSERIAL PRIMARY KEY,
    scope VARCHAR(16) NOT NULL CHECK (scope IN ('global', 'owner')),
    owner_user_id BIGINT REFERENCES users(id) ON DELETE RESTRICT,
    max_live_rooms INTEGER NOT NULL CHECK (max_live_rooms BETWEEN 1 AND 1000000),
    max_accounts_per_room INTEGER NOT NULL CHECK (max_accounts_per_room BETWEEN 1 AND 30),
    max_rooms_created_per_day INTEGER NOT NULL CHECK (max_rooms_created_per_day BETWEEN 1 AND 1000000),
    active BOOLEAN NOT NULL DEFAULT TRUE,
    expires_at TIMESTAMPTZ,
    reason VARCHAR(1000) NOT NULL,
    created_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT redstone_account_share_quota_scope CHECK ((scope = 'global') = (owner_user_id IS NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_redstone_share_active_global_quota
    ON redstone_account_share_quota_policies(scope) WHERE scope = 'global' AND active;
CREATE INDEX IF NOT EXISTS idx_redstone_share_owner_quota
    ON redstone_account_share_quota_policies(owner_user_id, active, expires_at DESC)
    WHERE scope = 'owner';
INSERT INTO redstone_account_share_quota_policies
    (scope, owner_user_id, max_live_rooms, max_accounts_per_room, max_rooms_created_per_day, reason)
SELECT 'global', NULL, 5, 10, 5, 'initial Redstone sharing quota'
WHERE NOT EXISTS (
    SELECT 1 FROM redstone_account_share_quota_policies WHERE scope = 'global' AND active
);

CREATE TABLE IF NOT EXISTS redstone_account_share_settlement_intents (
    id BIGSERIAL PRIMARY KEY,
    lease_id BIGINT NOT NULL REFERENCES redstone_account_share_leases(id) ON DELETE RESTRICT,
    membership_id BIGINT NOT NULL REFERENCES redstone_account_share_memberships(id) ON DELETE RESTRICT,
    room_id BIGINT NOT NULL REFERENCES redstone_account_share_rooms(id) ON DELETE RESTRICT,
    payer_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    owner_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    subscription_id BIGINT REFERENCES user_subscriptions(id) ON DELETE SET NULL,
    gross_amount DECIMAL(20,8) NOT NULL CHECK (gross_amount >= 0),
    platform_fee_amount DECIMAL(20,8) NOT NULL CHECK (platform_fee_amount >= 0),
    owner_amount DECIMAL(20,8) NOT NULL CHECK (owner_amount >= 0),
    payment_source VARCHAR(24) NOT NULL DEFAULT 'pending'
        CHECK (payment_source IN ('pending', 'subscription', 'bound', 'normal', 'bound_then_normal')),
    status VARCHAR(24) NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'charging', 'settled', 'failed', 'reversed')),
    idempotency_key VARCHAR(128) NOT NULL UNIQUE,
    failure_reason VARCHAR(500) NOT NULL DEFAULT '',
    settled_at TIMESTAMPTZ,
    reversed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT redstone_account_share_settlement_amounts
        CHECK (gross_amount = platform_fee_amount + owner_amount),
    CONSTRAINT redstone_account_share_settlement_final
        CHECK (status <> 'settled' OR settled_at IS NOT NULL)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_redstone_share_lease_settlement
    ON redstone_account_share_settlement_intents(lease_id);
CREATE INDEX IF NOT EXISTS idx_redstone_share_settlement_owner
    ON redstone_account_share_settlement_intents(owner_user_id, status, created_at DESC);

CREATE TABLE IF NOT EXISTS redstone_account_share_payout_receipts (
    id BIGSERIAL PRIMARY KEY,
    settlement_intent_id BIGINT NOT NULL UNIQUE
        REFERENCES redstone_account_share_settlement_intents(id) ON DELETE RESTRICT,
    owner_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    receipt_number VARCHAR(80) NOT NULL UNIQUE,
    amount DECIMAL(20,8) NOT NULL CHECK (amount >= 0),
    wallet_operation_key VARCHAR(128) NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS redstone_account_share_audits (
    id BIGSERIAL PRIMARY KEY,
    room_id BIGINT REFERENCES redstone_account_share_rooms(id) ON DELETE RESTRICT,
    actor_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    action VARCHAR(64) NOT NULL,
    detail VARCHAR(1000) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_redstone_share_audit_room ON redstone_account_share_audits(room_id, created_at DESC);

CREATE OR REPLACE FUNCTION redstone_account_share_append_only()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'redstone account-share record is append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_redstone_share_receipt_append_only ON redstone_account_share_payout_receipts;
CREATE TRIGGER trg_redstone_share_receipt_append_only
    BEFORE UPDATE OR DELETE ON redstone_account_share_payout_receipts
    FOR EACH ROW EXECUTE FUNCTION redstone_account_share_append_only();
DROP TRIGGER IF EXISTS trg_redstone_share_audit_append_only ON redstone_account_share_audits;
CREATE TRIGGER trg_redstone_share_audit_append_only
    BEFORE UPDATE OR DELETE ON redstone_account_share_audits
    FOR EACH ROW EXECUTE FUNCTION redstone_account_share_append_only();

CREATE OR REPLACE FUNCTION redstone_verify_account_share_binding()
RETURNS TRIGGER AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM redstone_account_share_rooms r
        JOIN accounts a ON a.id = NEW.account_id
        WHERE r.id = NEW.room_id
          AND r.owner_user_id = a.owner_user_id
          AND a.deleted_at IS NULL
          AND a.status = 'active'
    ) THEN
        RAISE EXCEPTION 'shared account must be an active account owned by the room owner';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS trg_redstone_verify_account_share_binding ON redstone_account_share_room_accounts;
CREATE TRIGGER trg_redstone_verify_account_share_binding
    BEFORE INSERT OR UPDATE OF room_id, account_id ON redstone_account_share_room_accounts
    FOR EACH ROW EXECUTE FUNCTION redstone_verify_account_share_binding();
