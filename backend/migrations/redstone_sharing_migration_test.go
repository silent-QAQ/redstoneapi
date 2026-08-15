package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedstoneSharingMigrationUsesDedicated9100Namespace(t *testing.T) {
	content, err := FS.ReadFile("9100_redstone_sharing_foundation.sql")
	require.NoError(t, err)
	sql := string(content)

	for _, table := range []string{
		"redstone_account_share_rooms",
		"redstone_account_share_room_accounts",
		"redstone_account_share_leases",
		"redstone_account_share_settlement_intents",
		"redstone_account_share_payout_receipts",
		"redstone_account_share_quota_policies",
		"redstone_account_share_policies",
	} {
		require.Contains(t, sql, table)
	}
	require.Contains(t, sql, "uq_redstone_share_room_user_live")
	require.Contains(t, sql, "redstone_verify_account_share_binding")
	require.NotContains(t, strings.ToLower(sql), "9007_redstone_account_sharing")
}
