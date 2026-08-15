//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	dbmigrations "github.com/silent-QAQ/redstoneapi/migrations"
	"github.com/stretchr/testify/require"
)

func TestRedstoneOwnedAccountsMigration_EmptyDatabaseFinalSchema(t *testing.T) {
	tx := testTx(t)

	requireColumn(t, tx, "accounts", "owner_user_id", "bigint", 0, true)
	requireForeignKeyOnDelete(t, tx, "accounts", "owner_user_id", "users", "RESTRICT")
	requireIndex(t, tx, "accounts", "idx_accounts_owner_user_id_created_at")
	requireLegacyControlledAccountRestrictionsAbsent(t, tx)
}

func TestRedstoneOwnedAccountsMigration_Upgrades9005AndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)

	legacySQL := readEmbeddedMigration(t, "9005_redstone_user_controlled_account_foundation.sql")
	ownerSQL := readEmbeddedMigration(t, "9006_redstone_owned_accounts_owner_scope.sql")

	_, err := tx.ExecContext(ctx, legacySQL)
	require.NoError(t, err)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var userID, proxyID, groupID, accountID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO users (email, password_hash)
VALUES ($1, 'migration-test')
RETURNING id
`, "redstone-owner-"+suffix+"@example.test").Scan(&userID))
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO proxies (name, protocol, host, port)
VALUES ($1, 'http', '127.0.0.1', 8080)
RETURNING id
`, "redstone-owner-proxy-"+suffix).Scan(&proxyID))
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO groups (name)
VALUES ($1)
RETURNING id
`, "redstone-owner-group-"+suffix).Scan(&groupID))
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO accounts (name, platform, type, schedulable)
VALUES ($1, 'openai', 'oauth', FALSE)
RETURNING id
`, "redstone-owner-account-"+suffix).Scan(&accountID))

	_, err = tx.ExecContext(ctx, `
INSERT INTO redstone_user_controlled_accounts (account_id, owner_user_id, provider)
VALUES ($1, $2, 'openai')
`, accountID, userID)
	require.NoError(t, err)

	_, err = tx.ExecContext(ctx, ownerSQL)
	require.NoError(t, err)

	var ownerUserID sql.NullInt64
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT owner_user_id FROM accounts WHERE id = $1
`, accountID).Scan(&ownerUserID))
	require.True(t, ownerUserID.Valid)
	require.Equal(t, userID, ownerUserID.Int64)

	requireLegacyControlledAccountRestrictionsAbsent(t, tx)
	requireForeignKeyOnDelete(t, tx, "accounts", "owner_user_id", "users", "RESTRICT")

	_, err = tx.ExecContext(ctx, `
UPDATE accounts
SET credentials = '{"access_token":"test-token"}'::jsonb,
    schedulable = TRUE,
    proxy_id = $2
WHERE id = $1
`, accountID, proxyID)
	require.NoError(t, err, "legacy controlled account should reuse normal credential, scheduling and proxy fields")

	_, err = tx.ExecContext(ctx, `
INSERT INTO account_groups (account_id, group_id)
VALUES ($1, $2)
`, accountID, groupID)
	require.NoError(t, err, "legacy controlled account should reuse normal account groups")

	_, err = tx.ExecContext(ctx, ownerSQL)
	require.NoError(t, err, "migration must be safe to replay")
	requireLegacyControlledAccountRestrictionsAbsent(t, tx)
}

func readEmbeddedMigration(t *testing.T, name string) string {
	t.Helper()
	content, err := dbmigrations.FS.ReadFile(name)
	require.NoError(t, err)
	return string(content)
}

func requireLegacyControlledAccountRestrictionsAbsent(t *testing.T, tx *sql.Tx) {
	t.Helper()

	for table, trigger := range map[string]string{
		"redstone_user_controlled_accounts": "trg_redstone_verify_user_controlled_account",
		"accounts":                          "trg_redstone_guard_user_controlled_account_runtime",
		"account_groups":                    "trg_redstone_reject_user_controlled_account_group",
	} {
		var exists bool
		err := tx.QueryRowContext(context.Background(), `
SELECT EXISTS (
    SELECT 1
    FROM pg_trigger trigger
    JOIN pg_class table_ref ON table_ref.oid = trigger.tgrelid
    JOIN pg_namespace namespace_ref ON namespace_ref.oid = table_ref.relnamespace
    WHERE namespace_ref.nspname = 'public'
      AND table_ref.relname = $1
      AND trigger.tgname = $2
      AND NOT trigger.tgisinternal
)
`, table, trigger).Scan(&exists)
		require.NoError(t, err)
		require.False(t, exists, "legacy restriction trigger %s must be absent", trigger)
	}
}
