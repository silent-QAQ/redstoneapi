package sharing

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	infraerrors "github.com/silent-QAQ/redstoneapi/internal/pkg/errors"
	"github.com/silent-QAQ/redstoneapi/internal/redstone/wallet"
	"github.com/stretchr/testify/require"
)

type apiKeyRoomOptionRepositoryStub struct {
	fakeRepository
	userID        int64
	limit, offset int
}

func (r *apiKeyRoomOptionRepositoryStub) ListAPIKeyRoomOptions(_ context.Context, userID int64, limit, offset int) ([]APIKeyRoomOption, int, error) {
	r.userID, r.limit, r.offset = userID, limit, offset
	return []APIKeyRoomOption{{RoomID: 12, GroupID: 44, Visibility: VisibilityPrivate, RateMultiplier: 1, FreeForOwner: true}}, 1, nil
}

func TestListAPIKeyRoomOptionsRejectsInvalidPageBeforeRepository(t *testing.T) {
	repository := &apiKeyRoomOptionRepositoryStub{}
	service := &Service{repository: repository, wallet: &wallet.Service{}}
	_, _, err := service.ListAPIKeyRoomOptions(context.Background(), 7, 0, 0)
	require.Equal(t, int32(400), infraerrors.FromError(err).Code)
	require.Zero(t, repository.userID)
}

func TestListAPIKeyRoomOptionsUsesDedicatedProjectionRepository(t *testing.T) {
	repository := &apiKeyRoomOptionRepositoryStub{}
	service := &Service{repository: repository, wallet: &wallet.Service{}}
	items, total, err := service.ListAPIKeyRoomOptions(context.Background(), 7, 20, 0)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, int64(44), items[0].GroupID)
	require.Equal(t, int64(7), repository.userID)
	require.Equal(t, 20, repository.limit)
}

func TestPostgresListAPIKeyRoomOptionsIncludesPublicOwnerAndActiveMembership(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repository, err := NewPostgresRepository(db)
	require.NoError(t, err)

	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*redstone_account_share_rooms.*room\.visibility = 'public'.*membership\.status = 'active'`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(`(?s)SELECT room\.id, room_group\.group_id, room\.name, room\.platform, room\.visibility.*rate_multiplier.*free_for_owner.*has_active_membership.*redstone_account_share_rooms.*ORDER BY`).
		WithArgs(int64(7), 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "group_id", "name", "platform", "visibility", "rate_multiplier", "free_for_owner", "has_active_membership"}).
			AddRow(int64(12), int64(44), "我的私有房", "openai", "private", 1.0, true, false).
			AddRow(int64(13), int64(45), "公共房", "openai", "public", 1.0, false, false))

	items, total, err := repository.ListAPIKeyRoomOptions(context.Background(), 7, 20, 0)
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, items, 2)
	require.Equal(t, int64(12), items[0].RoomID)
	require.True(t, items[0].FreeForOwner)
	require.Equal(t, float64(1), items[1].RateMultiplier)
	require.NoError(t, mock.ExpectationsWereMet())
}
