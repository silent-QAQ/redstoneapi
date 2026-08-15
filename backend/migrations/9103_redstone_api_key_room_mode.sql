-- API Key room mode keeps the selected sharing room separate from the
-- upstream api_keys.group_id field. group_id remains the scheduler's native
-- routing key while this binding supplies the room-level authorization scope.

CREATE TABLE IF NOT EXISTS redstone_api_key_room_bindings (
    api_key_id BIGINT PRIMARY KEY REFERENCES api_keys(id) ON DELETE CASCADE,
    room_id BIGINT NOT NULL REFERENCES redstone_account_share_rooms(id) ON DELETE RESTRICT,
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE RESTRICT,
    rate_multiplier DECIMAL(10,8) NOT NULL DEFAULT 1
        CHECK (rate_multiplier >= 0 AND rate_multiplier <= 1),
    bound_by_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_redstone_api_key_room_bindings_room
    ON redstone_api_key_room_bindings(room_id, api_key_id);
CREATE INDEX IF NOT EXISTS idx_redstone_api_key_room_bindings_group
    ON redstone_api_key_room_bindings(group_id, api_key_id);

CREATE TABLE IF NOT EXISTS redstone_api_key_room_binding_audits (
    id BIGSERIAL PRIMARY KEY,
    api_key_id BIGINT NOT NULL,
    room_id BIGINT REFERENCES redstone_account_share_rooms(id) ON DELETE SET NULL,
    group_id BIGINT REFERENCES groups(id) ON DELETE SET NULL,
    actor_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    action VARCHAR(32) NOT NULL CHECK (action IN ('bound', 'unbound')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_redstone_api_key_room_binding_audits_key
    ON redstone_api_key_room_binding_audits(api_key_id, created_at DESC);

CREATE OR REPLACE FUNCTION redstone_api_key_room_binding_audit_append_only()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'redstone API key room binding audit is append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_redstone_api_key_room_binding_audit_append_only ON redstone_api_key_room_binding_audits;
CREATE TRIGGER trg_redstone_api_key_room_binding_audit_append_only
    BEFORE UPDATE OR DELETE ON redstone_api_key_room_binding_audits
    FOR EACH ROW EXECUTE FUNCTION redstone_api_key_room_binding_audit_append_only();
