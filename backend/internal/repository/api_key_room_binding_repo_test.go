package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/silent-QAQ/redstoneapi/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyRepositoryBindSharingRoomUsesSingleTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &apiKeyRepository{sql: db}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id\s+FROM api_keys`).
		WithArgs(int64(7), int64(11)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(7)))
	mock.ExpectQuery(`SELECT rpg.group_id`).
		WithArgs(int64(23), int64(11)).
		WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(42)))
	mock.ExpectExec(`UPDATE api_keys`).
		WithArgs(int64(42), int64(7), int64(11)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO redstone_api_key_room_bindings`).
		WithArgs(int64(7), int64(23), int64(42), float64(1), int64(11)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO redstone_api_key_room_binding_audits`).
		WithArgs(int64(7), int64(23), int64(42), int64(11)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	binding, err := repo.BindSharingRoom(context.Background(), 7, 11, 23)
	require.NoError(t, err)
	require.Equal(t, &service.APIKeyRoomBinding{APIKeyID: 7, RoomID: 23, GroupID: 42, RateMultiplier: 1}, binding)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAPIKeyRepositoryUnbindSharingRoomDoesNotClearOrdinaryGroupWhenAlreadyUnbound(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &apiKeyRepository{sql: db}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id\s+FROM api_keys`).
		WithArgs(int64(7), int64(11)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(7)))
	mock.ExpectQuery(`DELETE FROM redstone_api_key_room_bindings`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"room_id", "group_id"}))
	mock.ExpectCommit()

	err = repo.UnbindSharingRoom(context.Background(), 7, 11)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAPIKeyRepositoryUnbindSharingRoomClearsMappedGroupAndAudits(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &apiKeyRepository{sql: db}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id\s+FROM api_keys`).
		WithArgs(int64(7), int64(11)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(7)))
	mock.ExpectQuery(`DELETE FROM redstone_api_key_room_bindings`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"room_id", "group_id"}).AddRow(int64(23), int64(42)))
	mock.ExpectExec(`UPDATE api_keys`).
		WithArgs(int64(7), int64(11)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO redstone_api_key_room_binding_audits`).
		WithArgs(int64(7), int64(23), int64(42), int64(11)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err = repo.UnbindSharingRoom(context.Background(), 7, 11)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
