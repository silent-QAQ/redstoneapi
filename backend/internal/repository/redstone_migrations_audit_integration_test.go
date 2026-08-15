//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"net/url"
	"sort"
	"testing"
	"testing/fstest"
	"time"

	dbmigrations "github.com/silent-QAQ/redstoneapi/migrations"
	"github.com/stretchr/testify/require"
)

func TestRedstoneMigrations_EmptyDatabaseFinalSchema(t *testing.T) {
	tx := testTx(t)

	requireColumn(t, tx, "users", "bound_balance", "numeric", 0, false)
	requireConstraintDefinitionContains(t, tx, "users", "users_bound_balance_nonnegative", "bound_balance >= (0)")

	for _, table := range []string{
		"redstone_wallet_operations",
		"redstone_wallet_ledger",
		"redstone_market_products",
		"redstone_market_delivery_items",
		"redstone_market_orders",
		"redstone_market_delivery_audit",
		"redstone_market_appeals",
		"redstone_market_reports",
		"redstone_market_financial_events",
		"redstone_market_delivery_scan_jobs",
		"redstone_user_controlled_accounts",
		"redstone_user_account_secrets",
		"redstone_account_share_rooms",
		"redstone_account_share_settlement_intents",
		"redstone_operations_withdrawals",
		"redstone_operations_tickets",
		"redstone_operations_campaigns",
		"redstone_cluster_nodes",
		"redstone_cluster_task_leases",
		"redstone_cluster_cache_epochs",
	} {
		requireRedstoneMigrationTable(t, tx, table)
	}

	requireConstraintDefinitionContains(t, tx, "redstone_wallet_operations", "redstone_wallet_operations_idempotency_unique", "UNIQUE (user_id, idempotency_key)")
	requireConstraintDefinitionContains(t, tx, "redstone_wallet_ledger", "redstone_wallet_ledger_reference_asset_unique", "UNIQUE (user_id, operation, asset_type, reference_type, reference_id, idempotency_key)")
	requireIndex(t, tx, "redstone_market_orders", "idx_redstone_market_orders_delivery_item_unique")
	requirePartialUniqueIndexDefinition(t, tx, "redstone_market_delivery_audit", "idx_redstone_market_delivery_one_view", "WHERE")
	requireColumn(t, tx, "redstone_market_products", "scan_failure_reason", "character varying", 160, false)
	requireColumn(t, tx, "accounts", "owner_user_id", "bigint", 0, true)
	requireForeignKeyOnDelete(t, tx, "accounts", "owner_user_id", "users", "RESTRICT")
}

func TestRedstoneMigrations_UpgradeReplayIsIdempotent(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	var userID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO users (email, password_hash, balance)
VALUES ($1, 'redstone-migration-test', 17.25)
RETURNING id
`, "redstone-upgrade-"+suffix+"@example.test").Scan(&userID))

	execEmbeddedRedstoneMigrations(t, tx, "9000_redstone_wallet_foundation.sql", "9001_redstone_market_foundation.sql", "9002_redstone_market_order_integrity.sql", "9003_redstone_market_settlement_foundation.sql", "9004_redstone_market_seller_foundation.sql", "9005_redstone_user_controlled_account_foundation.sql")

	var accountID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO accounts (name, platform, type, schedulable)
VALUES ($1, 'openai', 'oauth', FALSE)
RETURNING id
`, "redstone-upgrade-account-"+suffix).Scan(&accountID))
	require.NoError(t, execSQL(ctx, tx, `
INSERT INTO redstone_user_controlled_accounts (account_id, owner_user_id, provider)
VALUES ($1, $2, 'openai')
`, accountID, userID))

	execEmbeddedRedstoneMigrations(t, tx, "9006_redstone_owned_accounts_owner_scope.sql")
	requireOpeningBalanceRows(t, tx, userID, 1, 1)

	var ownerUserID sql.NullInt64
	require.NoError(t, tx.QueryRowContext(ctx, "SELECT owner_user_id FROM accounts WHERE id = $1", accountID).Scan(&ownerUserID))
	require.True(t, ownerUserID.Valid)
	require.Equal(t, userID, ownerUserID.Int64)

	execEmbeddedRedstoneMigrations(t, tx,
		"9000_redstone_wallet_foundation.sql",
		"9001_redstone_market_foundation.sql",
		"9002_redstone_market_order_integrity.sql",
		"9003_redstone_market_settlement_foundation.sql",
		"9004_redstone_market_seller_foundation.sql",
		"9005_redstone_user_controlled_account_foundation.sql",
		"9006_redstone_owned_accounts_owner_scope.sql",
		"9007_redstone_market_governance.sql",
		"9008_redstone_market_account_escrow.sql",
		"9010_redstone_market_integrity.sql",
		"9011_redstone_market_delivery_scan_jobs.sql",
		"9100_redstone_sharing_foundation.sql",
		"9200_redstone_operations_foundation.sql",
		"9201_redstone_wallet_audit_alignment.sql",
		"9300_redstone_cluster_foundation.sql",
	)
	requireOpeningBalanceRows(t, tx, userID, 1, 1)
	requireRedstoneLegacyRestrictionsAbsent(t, tx)
}

func TestRedstoneMigrations_UpgradeDatabaseWithRunner(t *testing.T) {
	ctx := context.Background()
	databaseName := fmt.Sprintf("redstone_upgrade_%d", time.Now().UnixNano())
	adminDB := openIntegrationDatabase(t, "postgres")
	t.Cleanup(func() {
		_, _ = adminDB.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+databaseName)
		_ = adminDB.Close()
	})
	require.NoError(t, execSQL(ctx, adminDB, "CREATE DATABASE "+databaseName))

	upgradeDB := openIntegrationDatabase(t, databaseName)
	t.Cleanup(func() { _ = upgradeDB.Close() })

	legacyFS := embeddedMigrationsThrough(t, "9005_redstone_user_controlled_account_foundation.sql")
	require.NoError(t, applyMigrationsFS(ctx, upgradeDB, legacyFS))
	requireMigrationRecorded(t, upgradeDB, "9005_redstone_user_controlled_account_foundation.sql")

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var userID, accountID int64
	require.NoError(t, upgradeDB.QueryRowContext(ctx, `
INSERT INTO users (email, password_hash, balance)
VALUES ($1, 'redstone-upgrade-runner', 17.25)
RETURNING id
`, "redstone-upgrade-runner-"+suffix+"@example.test").Scan(&userID))
	require.NoError(t, upgradeDB.QueryRowContext(ctx, `
INSERT INTO accounts (name, platform, type, schedulable)
VALUES ($1, 'openai', 'oauth', FALSE)
RETURNING id
`, "redstone-upgrade-runner-account-"+suffix).Scan(&accountID))
	require.NoError(t, execSQL(ctx, upgradeDB, `
INSERT INTO redstone_user_controlled_accounts (account_id, owner_user_id, provider)
VALUES ($1, $2, 'openai')
`, accountID, userID))

	require.NoError(t, ApplyMigrations(ctx, upgradeDB))
	requireMigrationRecorded(t, upgradeDB, "9006_redstone_owned_accounts_owner_scope.sql")
	requireMigrationRecorded(t, upgradeDB, "9100_redstone_sharing_foundation.sql")
	requireMigrationRecorded(t, upgradeDB, "9200_redstone_operations_foundation.sql")
	requireMigrationRecorded(t, upgradeDB, "9201_redstone_wallet_audit_alignment.sql")
	requireMigrationRecorded(t, upgradeDB, "9300_redstone_cluster_foundation.sql")
	var clusterTable sql.NullString
	require.NoError(t, upgradeDB.QueryRowContext(ctx, "SELECT to_regclass('public.redstone_cluster_task_leases')").Scan(&clusterTable))
	require.True(t, clusterTable.Valid)
	var operationsTable sql.NullString
	require.NoError(t, upgradeDB.QueryRowContext(ctx, "SELECT to_regclass('public.redstone_operations_withdrawals')").Scan(&operationsTable))
	require.True(t, operationsTable.Valid)

	var ownerUserID sql.NullInt64
	require.NoError(t, upgradeDB.QueryRowContext(ctx, "SELECT owner_user_id FROM accounts WHERE id = $1", accountID).Scan(&ownerUserID))
	require.True(t, ownerUserID.Valid)
	require.Equal(t, userID, ownerUserID.Int64)
	require.NoError(t, ApplyMigrations(ctx, upgradeDB), "a completed upgrade must pass the runner checksum check on restart")
}

func openIntegrationDatabase(t *testing.T, databaseName string) *sql.DB {
	t.Helper()
	parsed, err := url.Parse(integrationDSN)
	require.NoError(t, err)
	parsed.Path = "/" + databaseName
	db, err := openSQLWithRetry(context.Background(), parsed.String(), 15*time.Second)
	require.NoError(t, err)
	return db
}

func embeddedMigrationsThrough(t *testing.T, last string) fs.FS {
	t.Helper()
	names, err := fs.Glob(dbmigrations.FS, "*.sql")
	require.NoError(t, err)
	sort.Strings(names)
	files := make(map[string]*fstest.MapFile)
	for _, name := range names {
		if name > last {
			break
		}
		content, err := dbmigrations.FS.ReadFile(name)
		require.NoError(t, err)
		files[name] = &fstest.MapFile{Data: content}
	}
	return fstest.MapFS(files)
}

func requireMigrationRecorded(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	var checksum string
	require.NoError(t, db.QueryRowContext(context.Background(), "SELECT checksum FROM schema_migrations WHERE filename = $1", name).Scan(&checksum))
	require.Len(t, checksum, 64)
}

func execEmbeddedRedstoneMigrations(t *testing.T, tx *sql.Tx, names ...string) {
	t.Helper()
	ctx := context.Background()
	for _, name := range names {
		sqlText := readEmbeddedRedstoneMigration(t, name)
		require.NoError(t, execSQL(ctx, tx, sqlText), "replay migration %s", name)
	}
}

func readEmbeddedRedstoneMigration(t *testing.T, name string) string {
	t.Helper()
	content, err := dbmigrations.FS.ReadFile(name)
	require.NoError(t, err)
	return string(content)
}

type migrationAuditSQLExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func execSQL(ctx context.Context, executor migrationAuditSQLExecutor, query string, args ...any) error {
	_, err := executor.ExecContext(ctx, query, args...)
	return err
}

func requireOpeningBalanceRows(t *testing.T, tx *sql.Tx, userID int64, operationCount, ledgerCount int) {
	t.Helper()
	ctx := context.Background()
	var operations, ledger int
	require.NoError(t, tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM redstone_wallet_operations WHERE user_id = $1 AND operation = 'opening_balance'", userID).Scan(&operations))
	require.NoError(t, tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM redstone_wallet_ledger WHERE user_id = $1 AND operation = 'opening_balance'", userID).Scan(&ledger))
	require.Equal(t, operationCount, operations)
	require.Equal(t, ledgerCount, ledger)
}

func requireRedstoneMigrationTable(t *testing.T, tx *sql.Tx, table string) {
	t.Helper()
	var relation sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass($1)", "public."+table).Scan(&relation))
	require.True(t, relation.Valid, "expected table %s to exist", table)
}

func requireRedstoneLegacyRestrictionsAbsent(t *testing.T, tx *sql.Tx) {
	t.Helper()
	ctx := context.Background()
	for table, trigger := range map[string]string{
		"redstone_user_controlled_accounts": "trg_redstone_verify_user_controlled_account",
		"accounts":                          "trg_redstone_guard_user_controlled_account_runtime",
		"account_groups":                    "trg_redstone_reject_user_controlled_account_group",
	} {
		var exists bool
		require.NoError(t, tx.QueryRowContext(ctx, `
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
`, table, trigger).Scan(&exists))
		require.False(t, exists, "legacy trigger %s must be absent", trigger)
	}
}
