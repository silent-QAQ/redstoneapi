package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedstoneOwnedAccountsMigrationUpgradesLegacyControlledAccounts(t *testing.T) {
	legacyContent, err := FS.ReadFile("9005_redstone_user_controlled_account_foundation.sql")
	require.NoError(t, err)
	legacySQL := string(legacyContent)

	for _, legacyRestriction := range []string{
		"trg_redstone_verify_user_controlled_account",
		"trg_redstone_guard_user_controlled_account_runtime",
		"trg_redstone_reject_user_controlled_account_group",
	} {
		require.Contains(t, legacySQL, "CREATE TRIGGER "+legacyRestriction)
	}

	content, err := FS.ReadFile("9006_redstone_owned_accounts_owner_scope.sql")
	require.NoError(t, err)
	sql := string(content)

	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS owner_user_id BIGINT")
	require.Contains(t, sql, "table_name = 'redstone_user_controlled_accounts'")
	require.Contains(t, sql, "SET owner_user_id = r.owner_user_id")
	require.Contains(t, sql, "WHERE r.account_id = a.id")

	for _, legacyRestriction := range []string{
		"trg_redstone_verify_user_controlled_account",
		"trg_redstone_guard_user_controlled_account_runtime",
		"trg_redstone_reject_user_controlled_account_group",
	} {
		require.Contains(t, sql, "DROP TRIGGER IF EXISTS "+legacyRestriction)
	}

	for _, legacyFunction := range []string{
		"redstone_verify_user_controlled_account",
		"redstone_guard_user_controlled_account_runtime",
		"redstone_reject_user_controlled_account_group",
	} {
		require.Contains(t, sql, "DROP FUNCTION IF EXISTS "+legacyFunction+"()")
	}

	require.Contains(t, sql, "FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT")
	require.NotContains(t, strings.ToUpper(sql), "OWNER_USER_ID) REFERENCES USERS(ID) ON DELETE SET NULL")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_accounts_owner_user_id_created_at")
	require.Contains(t, sql, "FOR owner_fk_name IN")
}
