package market

import (
	"context"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestLockedSellerAccessRejectsGovernanceFreeze(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repository := &sqlRepository{db: db}

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT balance FROM users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE")).
		WithArgs(int64(17)).
		WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow("30.00000000"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS (\n\t\t\tSELECT 1 FROM redstone_market_seller_controls WHERE seller_user_id = $1\n\t\t)")).
		WithArgs(int64(17)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectRollback()

	balance, err := repository.lockSellerNormalBalance(context.Background(), tx, 17)
	require.ErrorIs(t, err, ErrSellerFrozen)
	require.True(t, balance.Equal(decimal.Zero))
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGovernanceLocksAllSellerProductsBeforeSellerRow(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM redstone_market_products\n\t\tWHERE seller_user_id = $1 AND seller_kind = 'user'\n\t\tFOR UPDATE")).
		WithArgs(int64(17)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(81).AddRow(82))
	mock.ExpectRollback()

	require.NoError(t, lockSellerProductsForGovernance(context.Background(), tx, 17))
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}
