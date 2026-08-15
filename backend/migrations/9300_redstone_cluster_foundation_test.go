package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedstoneClusterFoundationMigration(t *testing.T) {
	content, err := FS.ReadFile("9300_redstone_cluster_foundation.sql")
	require.NoError(t, err)
	sql := string(content)
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS redstone_cluster_nodes")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS redstone_cluster_task_leases")
	require.Contains(t, sql, "lease_token UUID NOT NULL")
	require.Contains(t, sql, "fencing_token BIGINT NOT NULL DEFAULT 1")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS redstone_cluster_cache_epochs")
}
