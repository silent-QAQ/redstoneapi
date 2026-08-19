package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedstoneAPIKeyRoomModeMigration(t *testing.T) {
	content, err := FS.ReadFile("9103_redstone_api_key_room_mode.sql")
	require.NoError(t, err)
	sql := string(content)
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS redstone_api_key_room_bindings")
	require.Contains(t, sql, "api_key_id BIGINT PRIMARY KEY")
	require.Contains(t, sql, "room_id BIGINT NOT NULL")
	require.Contains(t, sql, "group_id BIGINT NOT NULL")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS redstone_api_key_room_binding_audits")
	require.Contains(t, sql, "trg_redstone_api_key_room_binding_audit_append_only")
}

func TestRedstoneRoomModeAuthCacheInvalidationMigration(t *testing.T) {
	content, err := FS.ReadFile("9403_redstone_room_mode_auth_cache_invalidation.sql")
	require.NoError(t, err)
	sql := string(content)
	require.Contains(t, sql, "enqueue_redstone_room_mode_auth_cache_invalidation")
	require.Contains(t, sql, "AFTER INSERT OR UPDATE OR DELETE ON redstone_api_key_room_bindings")
	require.Contains(t, sql, "enqueue_auth_cache_invalidation(raw_api_key)")
}
