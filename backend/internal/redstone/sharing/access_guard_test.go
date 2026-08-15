package sharing

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/silent-QAQ/redstoneapi/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestAccessGuardAllowsDrainingBoundAccountWithExistingLease(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	groupID := int64(31)
	mock.ExpectQuery(`(?s)WITH lease_activity AS \(.*binding\.state IN \('active', 'draining'\).*binding\.state IN \('active', 'draining'\)`).
		WithArgs(sqlmock.AnyArg(), int64(9), groupID).
		WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow(int64(2)).AddRow(int64(5)))

	guard := NewAccessGuard(db)
	allowed, err := guard.AllowedAccountIDs(context.Background(), 9, &groupID, []int64{1, 2, 5})
	require.NoError(t, err)
	_, firstAllowed := allowed[2]
	_, secondAllowed := allowed[5]
	_, denied := allowed[1]
	require.True(t, firstAllowed)
	require.True(t, secondAllowed)
	require.False(t, denied)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccessGuardRejectsNilDatabase(t *testing.T) {
	groupID := int64(31)
	_, err := NewAccessGuard(nil).AllowedAccountIDs(context.Background(), 9, &groupID, []int64{2})
	require.ErrorIs(t, err, ErrRepositoryRequired)
}

func TestAccessGuardRoomModeDoesNotFallBackToUnboundGroupAccounts(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	groupID := int64(31)
	roomID := int64(44)
	ctx := context.WithValue(context.Background(), ctxkey.SharingRoomID, roomID)
	mock.ExpectQuery(`(?s)WHERE FALSE\s+OR EXISTS.*room\.id = \$4`).
		WithArgs(sqlmock.AnyArg(), int64(9), groupID, roomID).
		WillReturnRows(sqlmock.NewRows([]string{"account_id"}))

	allowed, err := NewAccessGuard(db).AllowedAccountIDs(ctx, 9, &groupID, []int64{2})
	require.NoError(t, err)
	require.Empty(t, allowed)
	require.NoError(t, mock.ExpectationsWereMet())
}
