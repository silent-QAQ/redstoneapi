package sharing

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	infraerrors "github.com/silent-QAQ/redstoneapi/internal/pkg/errors"
	"github.com/silent-QAQ/redstoneapi/internal/redstone/wallet"
	"github.com/stretchr/testify/require"
)

func TestMembershipInsertSQLKeepsStatusAndJoinedAtParametersSeparate(t *testing.T) {
	require.Contains(t, membershipInsertSQL, "VALUES ($1, $2, $3, $4)")
	require.NotContains(t, strings.ToUpper(membershipInsertSQL), "CASE")
}

func TestListUserMembershipsIncludesOnlyLiveLease(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Microsecond)
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM redstone_account_share_memberships WHERE user_id = \$1`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(`(?s)SELECT m.id, m.room_id, m.user_id.*LEFT JOIN LATERAL.*WHERE m.user_id = \$1`).
		WithArgs(int64(9), 50, 0).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "room_id", "user_id", "status", "queued_at", "joined_at", "ended_at", "end_reason",
			"lease_id", "lease_room_id", "lease_membership_id", "lease_account_id", "lease_user_id", "lease_state",
			"granted_at", "heartbeat_at", "expires_at", "released_at", "lease_reason",
		}).
			AddRow(int64(41), int64(17), int64(9), "active", now, now, nil, "",
				int64(51), int64(17), int64(41), int64(99), int64(9), "active", now, now, now.Add(30*time.Minute), nil, "").
			AddRow(int64(42), int64(18), int64(9), "ended", now, now, now, "lease_expired",
				nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil))

	items, total, err := repository.ListUserMemberships(context.Background(), 9, 50, 0)
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, items, 2)
	require.NotNil(t, items[0].Lease)
	require.Equal(t, int64(51), items[0].Lease.ID)
	require.Equal(t, now.Add(30*time.Minute), items[0].Lease.ExpiresAt)
	require.Nil(t, items[1].Lease)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBindAccountRejectsDifferentRoomOwner(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT platform FROM redstone_account_share_rooms`).
		WithArgs(int64(31), int64(7)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	err = repository.BindAccount(context.Background(), BindAccountRequest{OwnerUserID: 7, RoomID: 31, AccountID: 99})
	require.ErrorIs(t, err, ErrRoomForbidden)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBindAccountCreatesRoomPrivateGroupWhenClientOmitsIt(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT platform FROM redstone_account_share_rooms`).
		WithArgs(int64(31), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"platform"}).AddRow("openai"))
	mock.ExpectQuery(`SELECT platform FROM accounts`).
		WithArgs(int64(99), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"platform"}).AddRow("openai"))
	mock.ExpectQuery(`SELECT EXISTS`).
		WithArgs(int64(99)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`UPDATE redstone_account_share_room_accounts binding`).
		WithArgs(int64(99)).
		WillReturnRows(sqlmock.NewRows([]string{"room_id"}))
	mock.ExpectQuery(`SELECT binding.room_id\s+FROM redstone_account_share_room_accounts binding`).
		WithArgs(int64(99)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT max_accounts_per_room`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"max_accounts_per_room"}).AddRow(5))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM redstone_account_share_room_accounts`).
		WithArgs(int64(31)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT group_id FROM redstone_account_share_room_private_groups`).
		WithArgs(int64(31)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO groups`).
		WithArgs(sqlmock.AnyArg(), "Internal authorization group for account sharing room", "openai").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(61)))
	mock.ExpectExec(`INSERT INTO redstone_private_groups`).
		WithArgs(int64(61), int64(7), sqlmock.AnyArg(), "openai", "room-private-group:31", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO user_allowed_groups`).
		WithArgs(int64(7), int64(61)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO redstone_private_group_members`).
		WithArgs(int64(61), int64(7)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO redstone_account_share_room_private_groups`).
		WithArgs(int64(31), int64(61)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO redstone_private_group_audits`).
		WithArgs(int64(61), int64(7), "room_private_group_created", "31").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO redstone_account_share_room_accounts`).
		WithArgs(int64(31), int64(99)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO account_groups`).
		WithArgs(int64(99), int64(61)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO redstone_account_share_audits`).
		WithArgs(int64(31), int64(7), "account_bound", "99").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err = repository.BindAccount(context.Background(), BindAccountRequest{OwnerUserID: 7, RoomID: 31, AccountID: 99})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBindAccountMigratesIdleAccountFromAnotherOwnerRoom(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT platform FROM redstone_account_share_rooms`).
		WithArgs(int64(31), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"platform"}).AddRow("openai"))
	mock.ExpectQuery(`SELECT platform FROM accounts`).
		WithArgs(int64(99), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"platform"}).AddRow("openai"))
	mock.ExpectQuery(`SELECT EXISTS`).
		WithArgs(int64(99)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`UPDATE redstone_account_share_room_accounts binding`).
		WithArgs(int64(99)).
		WillReturnRows(sqlmock.NewRows([]string{"room_id"}))
	mock.ExpectQuery(`SELECT binding.room_id\s+FROM redstone_account_share_room_accounts binding`).
		WithArgs(int64(99)).
		WillReturnRows(sqlmock.NewRows([]string{"room_id"}).AddRow(int64(17)))
	mock.ExpectQuery(`SELECT owner_user_id FROM redstone_account_share_rooms`).
		WithArgs(int64(17)).
		WillReturnRows(sqlmock.NewRows([]string{"owner_user_id"}).AddRow(int64(7)))
	mock.ExpectQuery(`SELECT id FROM redstone_account_share_leases`).
		WithArgs(int64(17), int64(99)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`UPDATE redstone_account_share_room_accounts`).
		WithArgs(int64(17), int64(99)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT group_id FROM redstone_account_share_room_private_groups`).
		WithArgs(int64(17)).
		WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(61)))
	mock.ExpectExec(`DELETE FROM account_groups WHERE account_id = \$1 AND group_id = \$2`).
		WithArgs(int64(99), int64(61)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO redstone_account_share_audits`).
		WithArgs(int64(17), int64(7), "account_migrated_out", "99").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT max_accounts_per_room`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"max_accounts_per_room"}).AddRow(5))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM redstone_account_share_room_accounts`).
		WithArgs(int64(31)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT group_id FROM redstone_account_share_room_private_groups`).
		WithArgs(int64(31)).
		WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(62)))
	mock.ExpectQuery(`SELECT pg.owner_user_id, g.platform`).
		WithArgs(int64(62)).
		WillReturnRows(sqlmock.NewRows([]string{"owner_user_id", "platform"}).AddRow(int64(7), "openai"))
	mock.ExpectQuery(`SELECT group_id FROM redstone_account_share_room_private_groups`).
		WithArgs(int64(31)).
		WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(62)))
	mock.ExpectExec(`INSERT INTO redstone_account_share_room_accounts`).
		WithArgs(int64(31), int64(99)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO account_groups`).
		WithArgs(int64(99), int64(62)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO redstone_account_share_audits`).
		WithArgs(int64(31), int64(7), "account_bound", "99").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err = repository.BindAccount(context.Background(), BindAccountRequest{OwnerUserID: 7, RoomID: 31, AccountID: 99})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBindAccountRejectsMigrationWhenSourceAccountHasActiveLease(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT platform FROM redstone_account_share_rooms`).
		WithArgs(int64(31), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"platform"}).AddRow("openai"))
	mock.ExpectQuery(`SELECT platform FROM accounts`).
		WithArgs(int64(99), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"platform"}).AddRow("openai"))
	mock.ExpectQuery(`SELECT EXISTS`).
		WithArgs(int64(99)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`UPDATE redstone_account_share_room_accounts binding`).
		WithArgs(int64(99)).
		WillReturnRows(sqlmock.NewRows([]string{"room_id"}))
	mock.ExpectQuery(`SELECT binding.room_id\s+FROM redstone_account_share_room_accounts binding`).
		WithArgs(int64(99)).
		WillReturnRows(sqlmock.NewRows([]string{"room_id"}).AddRow(int64(17)))
	mock.ExpectQuery(`SELECT owner_user_id FROM redstone_account_share_rooms`).
		WithArgs(int64(17)).
		WillReturnRows(sqlmock.NewRows([]string{"owner_user_id"}).AddRow(int64(7)))
	mock.ExpectQuery(`SELECT id FROM redstone_account_share_leases`).
		WithArgs(int64(17), int64(99)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(71)))
	mock.ExpectRollback()

	err = repository.BindAccount(context.Background(), BindAccountRequest{OwnerUserID: 7, RoomID: 31, AccountID: 99})
	require.ErrorIs(t, err, ErrRoomAccountHasActiveLease)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestListOwnerRoomAccountsScopesResultToOwnerRoom(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	now := time.Now().UTC()
	mock.ExpectQuery(`SELECT id FROM redstone_account_share_rooms`).
		WithArgs(int64(17), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(17)))
	mock.ExpectQuery(`SELECT COUNT\(\*\).*redstone_account_share_room_accounts`).
		WithArgs(int64(17)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(`(?s)SELECT room_id, account_id, state, bound_at, unbound_at.*ORDER BY CASE state`).
		WithArgs(int64(17), 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"room_id", "account_id", "state", "bound_at", "unbound_at"}).
			AddRow(int64(17), int64(44), "active", now, nil).
			AddRow(int64(17), int64(45), "removed", now, now))

	items, total, err := repository.ListOwnerRoomAccounts(context.Background(), 7, 17, 20, 0)
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, items, 2)
	require.Equal(t, RoomAccountActive, items[0].State)
	require.NotNil(t, items[1].UnboundAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCloseRoomRejectsActiveMembersBeforeChangingState(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM redstone_account_share_rooms`).
		WithArgs(int64(17), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`SELECT COUNT\(\*\).*redstone_account_share_memberships`).
		WithArgs(int64(17)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectRollback()

	err = repository.CloseRoom(context.Background(), 7, 17)
	require.ErrorIs(t, err, ErrRoomHasActiveLeases)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCloseRoomReleasesBoundAccounts(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM redstone_account_share_rooms`).
		WithArgs(int64(17), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`SELECT COUNT\(\*\).*redstone_account_share_memberships`).
		WithArgs(int64(17)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(`UPDATE redstone_account_share_memberships`).
		WithArgs(int64(17)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`UPDATE redstone_account_share_room_accounts`).
		WithArgs(int64(17)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM account_groups account_group`).
		WithArgs(int64(17)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE redstone_account_share_rooms`).
		WithArgs(int64(17), int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO redstone_account_share_audits`).
		WithArgs(int64(17), int64(7), "room_closed", "").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err = repository.CloseRoom(context.Background(), 7, 17)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDeleteRoomRequiresClosedState(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM redstone_account_share_rooms`).
		WithArgs(int64(17), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectRollback()

	err = repository.DeleteRoom(context.Background(), 7, 17)
	require.ErrorIs(t, err, ErrRoomMustBeClosed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDeleteRoomRejectsActiveMemberships(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM redstone_account_share_rooms`).
		WithArgs(int64(17), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("closed"))
	mock.ExpectQuery(`SELECT COUNT\(\*\).*redstone_account_share_memberships`).
		WithArgs(int64(17)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectRollback()

	err = repository.DeleteRoom(context.Background(), 7, 17)
	require.ErrorIs(t, err, ErrRoomHasActiveLeases)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDeleteRoomArchivesClosedRoomAndClearsBoundKeys(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM redstone_account_share_rooms`).
		WithArgs(int64(17), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("closed"))
	mock.ExpectQuery(`SELECT COUNT\(\*\).*redstone_account_share_memberships`).
		WithArgs(int64(17)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(`UPDATE redstone_account_share_room_accounts`).
		WithArgs(int64(17)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`DELETE FROM account_groups account_group`).
		WithArgs(int64(17)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`UPDATE api_keys key`).
		WithArgs(int64(17)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM redstone_api_key_room_bindings`).
		WithArgs(int64(17)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE redstone_account_share_rooms`).
		WithArgs(int64(17), int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO redstone_account_share_audits`).
		WithArgs(int64(17), int64(7), "room_deleted", "").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err = repository.DeleteRoom(context.Background(), 7, 17)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReleaseClosedRoomAccountBindingsRemovesOnlyHistoricalBinding(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	mock.ExpectQuery(`UPDATE redstone_account_share_room_accounts binding`).
		WithArgs(int64(99)).
		WillReturnRows(sqlmock.NewRows([]string{"room_id"}).AddRow(int64(17)))
	mock.ExpectExec(`DELETE FROM account_groups account_group`).
		WithArgs(int64(99)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO redstone_account_share_audits`).
		WithArgs(int64(17), int64(7), "account_released_from_closed_room", "99").
		WillReturnResult(sqlmock.NewResult(1, 1))
	require.NoError(t, repository.releaseClosedRoomAccountBindings(context.Background(), tx, 99, 7))
	mock.ExpectCommit()
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDrainRoomAccountUsesOwnerScopedTransactionAndAudits(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	now := time.Now().UTC()
	rows := func(state string) *sqlmock.Rows {
		return sqlmock.NewRows([]string{"room_id", "account_id", "state", "bound_at", "unbound_at"}).
			AddRow(int64(17), int64(44), state, now, nil)
	}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM redstone_account_share_rooms`).
		WithArgs(int64(17), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(17)))
	mock.ExpectQuery(`(?s)SELECT room_id, account_id, state, bound_at, unbound_at.*FOR UPDATE`).
		WithArgs(int64(17), int64(44)).
		WillReturnRows(rows("active"))
	mock.ExpectQuery(`(?s)UPDATE redstone_account_share_room_accounts.*state = 'draining'.*RETURNING`).
		WithArgs(int64(17), int64(44)).
		WillReturnRows(rows("draining"))
	mock.ExpectExec(`INSERT INTO redstone_account_share_audits`).
		WithArgs(int64(17), int64(7), "account_draining", "44").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	account, err := repository.DrainRoomAccount(context.Background(), RoomAccountLifecycleRequest{OwnerUserID: 7, RoomID: 17, AccountID: 44})
	require.NoError(t, err)
	require.Equal(t, RoomAccountDraining, account.State)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDrainRoomAccountRejectsDifferentRoomOwner(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM redstone_account_share_rooms`).
		WithArgs(int64(17), int64(7)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, err = repository.DrainRoomAccount(context.Background(), RoomAccountLifecycleRequest{OwnerUserID: 7, RoomID: 17, AccountID: 44})
	require.ErrorIs(t, err, ErrRoomForbidden)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCreateLeaseSelectsOnlyActiveRoomAccounts(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	mock.ExpectQuery(`(?s)FROM redstone_account_share_room_accounts ra.*ra\.state = 'active'.*FOR UPDATE OF ra SKIP LOCKED`).
		WithArgs(int64(17), int64(7)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, _, err = createLeaseAndIntent(context.Background(), tx, Room{ID: 17, OwnerUserID: 7}, Membership{ID: 41, UserID: 9}, "lease-active-only")
	require.ErrorIs(t, err, ErrRoomUnavailable)
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRemoveRoomAccountRejectsActiveLease(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM redstone_account_share_rooms`).
		WithArgs(int64(17), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(17)))
	mock.ExpectQuery(`(?s)SELECT room_id, account_id, state, bound_at, unbound_at.*FOR UPDATE`).
		WithArgs(int64(17), int64(44)).
		WillReturnRows(sqlmock.NewRows([]string{"room_id", "account_id", "state", "bound_at", "unbound_at"}).
			AddRow(int64(17), int64(44), "draining", now, nil))
	mock.ExpectQuery(`(?s)SELECT id FROM redstone_account_share_leases.*state = 'active'.*FOR UPDATE`).
		WithArgs(int64(17), int64(44)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(91)))
	mock.ExpectRollback()

	err = repository.RemoveRoomAccount(context.Background(), RoomAccountLifecycleRequest{OwnerUserID: 7, RoomID: 17, AccountID: 44})
	require.ErrorIs(t, err, ErrRoomAccountHasActiveLease)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRemoveRoomAccountCompletesDrainingLifecycleAndAudits(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM redstone_account_share_rooms`).
		WithArgs(int64(17), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(17)))
	mock.ExpectQuery(`(?s)SELECT room_id, account_id, state, bound_at, unbound_at.*FOR UPDATE`).
		WithArgs(int64(17), int64(44)).
		WillReturnRows(sqlmock.NewRows([]string{"room_id", "account_id", "state", "bound_at", "unbound_at"}).
			AddRow(int64(17), int64(44), "draining", now, nil))
	mock.ExpectQuery(`(?s)SELECT id FROM redstone_account_share_leases.*state = 'active'.*FOR UPDATE`).
		WithArgs(int64(17), int64(44)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`(?s)UPDATE redstone_account_share_room_accounts.*state = 'removed'.*RETURNING account_id`).
		WithArgs(int64(17), int64(44)).
		WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow(int64(44)))
	mock.ExpectQuery(`SELECT group_id FROM redstone_account_share_room_private_groups`).
		WithArgs(int64(17)).
		WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(23)))
	mock.ExpectExec(`DELETE FROM account_groups`).
		WithArgs(int64(44), int64(23)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO redstone_account_share_audits`).
		WithArgs(int64(17), int64(7), "account_removed", "44").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err = repository.RemoveRoomAccount(context.Background(), RoomAccountLifecycleRequest{OwnerUserID: 7, RoomID: 17, AccountID: 44})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMarkSettlementChargingReturnsExistingSettledIntentForRetry(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	settledAt := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)FROM redstone_account_share_settlement_intents WHERE id = \$1 FOR UPDATE`).
		WithArgs(int64(18)).
		WillReturnRows(intentRows().AddRow(18, 4, 5, 6, 7, 8, nil, "2.00000000", "0.10000000", "1.90000000", "normal", "settled", "share-retry-18", "", settledAt))
	mock.ExpectCommit()

	intent, err := repository.MarkSettlementCharging(context.Background(), 18)
	require.NoError(t, err)
	require.Equal(t, SettlementSettled, intent.Status)
	require.Equal(t, PaymentNormal, intent.PaymentSource)
	require.NotNil(t, intent.SettledAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMarkSettlementChargingUsesLockedPendingTransition(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)FROM redstone_account_share_settlement_intents WHERE id = \$1 FOR UPDATE`).
		WithArgs(int64(19)).
		WillReturnRows(intentRows().AddRow(19, 4, 5, 6, 7, 8, nil, "2.00000000", "0.10000000", "1.90000000", "pending", "pending", "share-retry-19", "", nil))
	mock.ExpectExec(`UPDATE redstone_account_share_settlement_intents SET status = 'charging'`).
		WithArgs(int64(19)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	intent, err := repository.MarkSettlementCharging(context.Background(), 19)
	require.NoError(t, err)
	require.Equal(t, SettlementCharging, intent.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestFinalizeSettlementFailsClosedWithoutWalletSettlement(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	_, err = repository.FinalizeSettlement(context.Background(), 19, PaymentSubscription, "legacy-finalizer")
	require.ErrorIs(t, err, ErrSettlementState)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSettleIntentRejectsExpiredOrReleasedLeaseBeforeWalletMutation(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	mock.ExpectQuery(`(?s)FROM redstone_account_share_settlement_intents WHERE id = \$1 FOR UPDATE`).
		WithArgs(int64(19)).
		WillReturnRows(intentRows().AddRow(19, 4, 5, 6, 7, 8, nil, "2.00000000", "0.10000000", "1.90000000", "pending", "pending", "share-retry-19", "", nil))
	mock.ExpectQuery(`(?s)FROM redstone_account_share_rooms.*FOR UPDATE`).
		WithArgs(int64(6)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(6)))
	mock.ExpectQuery(`(?s)FROM redstone_account_share_leases l.*l\.state = 'active'.*FOR UPDATE OF l, m`).
		WithArgs(int64(4), int64(6), int64(5), int64(7)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, err = repository.settleIntentTx(context.Background(), tx, 19, nil)
	require.ErrorIs(t, err, ErrSettlementState)
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func intentRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "lease_id", "membership_id", "room_id", "payer_user_id", "owner_user_id", "subscription_id",
		"gross_amount", "platform_fee_amount", "owner_amount", "payment_source", "status", "idempotency_key", "failure_reason", "settled_at",
	})
}

func TestApplicationErrorPreservesOwnershipForbidden(t *testing.T) {
	err := applicationError(ErrAccountNotOwned)
	require.Equal(t, int32(403), infraerrors.FromError(err).Code)
}

func TestDecideMembershipRejectsQueuedMemberInOwnerScopedTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	now := time.Now().UTC()
	roomRows := sqlmock.NewRows([]string{
		"id", "owner_user_id", "name", "description", "platform", "visibility", "status", "requires_approval",
		"seat_limit", "lease_seconds", "idle_timeout_seconds", "lease_price", "platform_fee_rate", "created_at", "updated_at",
	}).AddRow(int64(17), int64(7), "approval room", "", "openai", "public", "active", true,
		2, 3600, 1800, "2.00000000", "0.10000000", now, now)
	membershipRows := sqlmock.NewRows([]string{"id", "room_id", "user_id", "status", "queued_at", "joined_at", "ended_at", "end_reason"}).
		AddRow(int64(41), int64(17), int64(9), "queued", now, nil, nil, "")
	revokedRows := sqlmock.NewRows([]string{"id", "room_id", "user_id", "status", "queued_at", "joined_at", "ended_at", "end_reason"}).
		AddRow(int64(41), int64(17), int64(9), "revoked", now, nil, now, "owner_rejected")

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT room_id FROM redstone_account_share_memberships WHERE id = \$1`).
		WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"room_id"}).AddRow(int64(17)))
	mock.ExpectQuery(`(?s)FROM redstone_account_share_rooms.*FOR UPDATE`).
		WithArgs(int64(17)).WillReturnRows(roomRows)
	mock.ExpectQuery(`(?s)FROM redstone_account_share_memberships.*WHERE id = \$1 AND room_id = \$2 FOR UPDATE`).
		WithArgs(int64(41), int64(17)).WillReturnRows(membershipRows)
	mock.ExpectQuery(`(?s)UPDATE redstone_account_share_memberships.*owner_rejected.*RETURNING`).
		WithArgs(int64(41)).WillReturnRows(revokedRows)
	mock.ExpectExec(`INSERT INTO redstone_account_share_audits`).
		WithArgs(int64(17), int64(7), "membership_rejected", "41").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	result, err := repository.DecideMembershipAndSettle(context.Background(), MembershipDecisionRequest{
		OwnerUserID: 7, RoomID: 17, MembershipID: 41, Decision: MembershipReject,
	}, &wallet.Service{})
	require.NoError(t, err)
	require.Equal(t, MembershipRevoked, result.Membership.Status)
	require.Equal(t, "owner_rejected", result.Membership.EndReason)
	require.Nil(t, result.Lease)
	require.Nil(t, result.Settlement)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReleaseLeasePromotesQueueWithoutGrantingPrivateGroup(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE redstone_account_share_leases`).
		WithArgs(int64(51), int64(9), "user_released").
		WillReturnRows(sqlmock.NewRows([]string{"room_id", "membership_id"}).AddRow(int64(17), int64(41)))
	mock.ExpectExec(`UPDATE redstone_account_share_memberships`).
		WithArgs(int64(41), "user_released").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT group_id FROM redstone_account_share_room_private_groups`).
		WithArgs(int64(17)).
		WillReturnRows(sqlmock.NewRows([]string{"group_id"}))
	mock.ExpectQuery(`WITH next_member AS`).
		WithArgs(int64(17)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id"}).AddRow(int64(42), int64(10)))
	mock.ExpectCommit()

	require.NoError(t, repository.ReleaseLease(context.Background(), 9, 51, "user_released"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLeaveMembershipReleasesLeaseRevokesAccessAndPromotesQueue(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT room_id FROM redstone_account_share_memberships`).
		WithArgs(int64(41), int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"room_id"}).AddRow(int64(17)))
	mock.ExpectQuery(`SELECT id FROM redstone_account_share_rooms`).
		WithArgs(int64(17)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(17)))
	mock.ExpectQuery(`(?s)SELECT id, room_id, user_id, status.*WHERE id = \$1 AND user_id = \$2 FOR UPDATE`).
		WithArgs(int64(41), int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "room_id", "user_id", "status", "queued_at", "joined_at", "ended_at", "end_reason"}).
			AddRow(int64(41), int64(17), int64(9), "active", now, now, nil, ""))
	mock.ExpectExec(`UPDATE redstone_account_share_leases`).
		WithArgs(int64(41), int64(9), "user_left").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE redstone_account_share_memberships`).
		WithArgs(int64(41), "user_left").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT group_id FROM redstone_account_share_group_grants.*FOR UPDATE`).
		WithArgs(int64(41), int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(23)))
	mock.ExpectExec(`UPDATE redstone_account_share_group_grants`).
		WithArgs(int64(41), int64(23), int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)DELETE FROM user_allowed_groups uag`).
		WithArgs(int64(9), int64(23)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`WITH next_member AS`).
		WithArgs(int64(17)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO redstone_account_share_audits`).
		WithArgs(int64(17), int64(9), "membership_left", "41").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	require.NoError(t, repository.LeaveMembership(context.Background(), 9, 41))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLeaveMembershipCancelsQueuedMemberWithoutLeaseOrGrant(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT room_id FROM redstone_account_share_memberships`).
		WithArgs(int64(41), int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"room_id"}).AddRow(int64(17)))
	mock.ExpectQuery(`SELECT id FROM redstone_account_share_rooms`).
		WithArgs(int64(17)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(17)))
	mock.ExpectQuery(`(?s)SELECT id, room_id, user_id, status.*WHERE id = \$1 AND user_id = \$2 FOR UPDATE`).
		WithArgs(int64(41), int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "room_id", "user_id", "status", "queued_at", "joined_at", "ended_at", "end_reason"}).
			AddRow(int64(41), int64(17), int64(9), "queued", now, nil, nil, ""))
	mock.ExpectExec(`UPDATE redstone_account_share_memberships`).
		WithArgs(int64(41), "user_cancelled").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO redstone_account_share_audits`).
		WithArgs(int64(17), int64(9), "membership_cancelled", "41").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	require.NoError(t, repository.LeaveMembership(context.Background(), 9, 41))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestExpireDueLeasesRevokesAccessAndPromotesWithoutGranting(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT r.id\s+FROM redstone_account_share_rooms r.*FOR UPDATE SKIP LOCKED`).
		WithArgs(10).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(17)))
	mock.ExpectQuery(`(?s)SELECT l.id,.*FROM redstone_account_share_leases l.*FOR UPDATE OF l SKIP LOCKED`).
		WithArgs(int64(17), 10).
		WillReturnRows(sqlmock.NewRows([]string{"id", "reason"}).AddRow(int64(51), "lease_expired"))
	mock.ExpectQuery(`(?s)UPDATE redstone_account_share_leases l.*heartbeat_at.*RETURNING l.membership_id, l.user_id`).
		WithArgs(int64(51), "lease_expired", int64(17)).
		WillReturnRows(sqlmock.NewRows([]string{"membership_id", "user_id"}).AddRow(int64(41), int64(9)))
	mock.ExpectExec(`UPDATE redstone_account_share_memberships`).
		WithArgs(int64(41), "lease_expired").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT group_id\s+FROM redstone_account_share_group_grants.*FOR UPDATE`).
		WithArgs(int64(41), int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(23)))
	mock.ExpectExec(`UPDATE redstone_account_share_group_grants`).
		WithArgs(int64(41), int64(23), int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)DELETE FROM user_allowed_groups uag`).
		WithArgs(int64(9), int64(23)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)WITH next_member AS.*FOR UPDATE SKIP LOCKED`).
		WithArgs(int64(17)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(42)))
	mock.ExpectCommit()

	result, err := repository.ExpireDueLeases(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, LeaseExpiryBatchResult{Processed: 1, Promoted: 1}, result)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestExpireDueLeasesSkipsEmptyBatchAndRejectsInvalidBatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	_, err = repository.ExpireDueLeases(context.Background(), 0)
	require.ErrorIs(t, err, ErrInvalidPagination)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT r.id\s+FROM redstone_account_share_rooms r.*FOR UPDATE SKIP LOCKED`).
		WithArgs(10).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectCommit()

	result, err := repository.ExpireDueLeases(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, LeaseExpiryBatchResult{}, result)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestListRoomReviewsReturnsOnlyVisibleReviews(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)
	now := time.Now().UTC()
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*moderation_status = 'visible'.*room.visibility = 'public'`).
		WithArgs(int64(9), int64(17)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`(?s)FROM redstone_account_share_reviews.*moderation_status = 'visible'.*ORDER BY review\.created_at DESC, review\.id DESC`).
		WithArgs(int64(9), int64(17), 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "room_id", "membership_id", "reviewer_user_id", "rating", "body", "created_at", "updated_at"}).
			AddRow(int64(3), int64(17), int64(29), int64(9), 5, "稳定", now, now))
	items, total, err := repository.ListRoomReviews(context.Background(), 9, 17, 20, 0)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, items, 1)
	require.Equal(t, "稳定", items[0].Body)
	require.NoError(t, mock.ExpectationsWereMet())
}
