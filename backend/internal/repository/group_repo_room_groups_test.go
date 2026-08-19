package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestListSharingRoomGroupIDsIncludesArchivedRoomMappings(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repository := newGroupRepositoryWithSQL(nil, db)

	mock.ExpectQuery(`SELECT DISTINCT room_group.group_id\s+FROM redstone_account_share_room_private_groups room_group`).
		WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(42)).AddRow(int64(43)))

	ids, err := repository.ListSharingRoomGroupIDs(context.Background())
	require.NoError(t, err)
	require.Equal(t, []int64{42, 43}, ids)
	require.NoError(t, mock.ExpectationsWereMet())
}
