-- A sharing room routes only through its owner's Redstone private group.
-- The group remains a normal sub2 exclusive group; this table records why a
-- temporary user_allowed_groups grant exists so lease completion can revoke it.

CREATE TABLE IF NOT EXISTS redstone_account_share_room_private_groups (
    room_id BIGINT PRIMARY KEY REFERENCES redstone_account_share_rooms(id) ON DELETE CASCADE,
    group_id BIGINT NOT NULL REFERENCES redstone_private_groups(group_id) ON DELETE RESTRICT,
    bound_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (group_id, room_id)
);
CREATE INDEX IF NOT EXISTS idx_redstone_share_room_private_groups_group
    ON redstone_account_share_room_private_groups(group_id);

CREATE TABLE IF NOT EXISTS redstone_account_share_group_grants (
    membership_id BIGINT NOT NULL REFERENCES redstone_account_share_memberships(id) ON DELETE RESTRICT,
    group_id BIGINT NOT NULL REFERENCES redstone_private_groups(group_id) ON DELETE RESTRICT,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    status VARCHAR(16) NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'revoked')),
    granted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ,
    PRIMARY KEY (membership_id, group_id),
    CHECK ((status = 'active' AND revoked_at IS NULL) OR (status = 'revoked' AND revoked_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS idx_redstone_share_group_grants_user
    ON redstone_account_share_group_grants(user_id, group_id, status);
CREATE INDEX IF NOT EXISTS idx_redstone_share_group_grants_group
    ON redstone_account_share_group_grants(group_id, status);
