package sharing

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	infraerrors "github.com/silent-QAQ/redstoneapi/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestCreatePrivateGroupWritesUpstreamAuthorizationInOneTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id FROM users").WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(7)))
	mock.ExpectQuery("FROM redstone_private_groups pg").WithArgs(int64(7), "private-group-key").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("INSERT INTO groups").WithArgs(sqlmock.AnyArg(), "Trusted team", "openai").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(31)))
	mock.ExpectQuery("INSERT INTO redstone_private_groups").
		WithArgs(int64(31), int64(7), "Team", "openai", "private-group-key", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "owner_user_id", "name", "platform", "status", "created_at", "updated_at"}).
			AddRow(int64(31), int64(7), "Team", "openai", "active", now, now))
	mock.ExpectExec("INSERT INTO user_allowed_groups").WithArgs(int64(7), int64(31)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO redstone_private_group_members").WithArgs(int64(31), int64(7)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO redstone_private_group_audits").WithArgs(int64(31), int64(7), "private_group_created", "Team").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	group, created, err := repository.CreatePrivateGroup(context.Background(), CreatePrivateGroupRequest{
		OwnerUserID: 7, Name: "Team", Description: "Trusted team", Platform: "openai", IdempotencyKey: "private-group-key",
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, int64(31), group.GroupID)
	require.Equal(t, 1, group.MemberCount)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPrivateGroupRequestValidationRunsBeforeRepository(t *testing.T) {
	service := &Service{repository: fakeRepository{}}
	_, _, err := service.CreatePrivateGroup(context.Background(), CreatePrivateGroupRequest{
		OwnerUserID: 7, Name: "team", Platform: "openai",
	})
	require.Equal(t, int32(400), infraerrors.FromError(err).Code)
}

func TestPrivateGroupErrorsMapToSafeHTTPResponses(t *testing.T) {
	require.Equal(t, int32(403), infraerrors.FromError(applicationError(ErrPrivateGroupForbidden)).Code)
	require.Equal(t, int32(404), infraerrors.FromError(applicationError(ErrPrivateGroupNotFound)).Code)
	require.Equal(t, int32(409), infraerrors.FromError(applicationError(ErrPrivateGroupOwnerRequired)).Code)
}

func TestShareGroupGrantAndRevocationUseOneTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	mock.ExpectExec("INSERT INTO redstone_account_share_group_grants").WithArgs(int64(41), int64(31), int64(9)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO user_allowed_groups").WithArgs(int64(9), int64(31)).WillReturnResult(sqlmock.NewResult(1, 1))
	require.NoError(t, grantShareGroupAccess(context.Background(), tx, 41, 9, 31))
	mock.ExpectCommit()
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())

	mock.ExpectBegin()
	tx, err = db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	mock.ExpectExec("UPDATE redstone_account_share_group_grants").WithArgs(int64(41), int64(31), int64(9)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("DELETE FROM user_allowed_groups").WithArgs(int64(9), int64(31)).WillReturnResult(sqlmock.NewResult(1, 1))
	require.NoError(t, revokeShareGroupAccess(context.Background(), tx, 41, 9, 31))
	mock.ExpectCommit()
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSharingGroupAccessMigrationUsesUpstreamGroupTables(t *testing.T) {
	content, err := os.ReadFile("../../../migrations/9102_redstone_sharing_group_access.sql")
	require.NoError(t, err)
	text := string(content)
	require.Contains(t, text, "redstone_account_share_room_private_groups")
	require.Contains(t, text, "redstone_account_share_group_grants")
	require.NotContains(t, text, "CREATE TABLE groups")
	require.NotContains(t, text, "CREATE TABLE api_keys")
}

func TestBindAccountAllowsAutomaticPrivateGroup(t *testing.T) {
	err := (BindAccountRequest{OwnerUserID: 7, RoomID: 8, AccountID: 9}).Validate()
	require.NoError(t, err)
	require.ErrorIs(t, (BindAccountRequest{OwnerUserID: 7, RoomID: 8, AccountID: 9, PrivateGroupID: -1}).Validate(), ErrInvalidAccount)
}
