package market

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/silent-QAQ/redstoneapi/internal/redstone/wallet"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestPostgresCreateAppealLocksOrderAndAtomicallyMarksAppealed(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repository := &sqlRepository{db: db}
	request := CreateAppealRequest{BuyerUserID: 81, OrderID: 501, Reason: "Delivery is unusable"}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, buyer_user_id, seller_user_id, status, unit_price, seller_net_amount,")).
		WithArgs(request.OrderID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "buyer_user_id", "seller_user_id", "status", "unit_price", "seller_net_amount", "settlement_due_at", "delivered_at"}).
			AddRow(501, 81, 22, "delivered", "12.50000000", "11.87500000", time.Now().Add(time.Hour), time.Now()))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT status FROM redstone_market_appeals WHERE order_id = $1 FOR UPDATE")).
		WithArgs(request.OrderID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO redstone_market_appeals (")).
		WithArgs(request.OrderID, request.BuyerUserID, request.Reason, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "order_id", "buyer_user_id", "status", "reason", "resolution_note", "resolved_by_user_id", "created_at", "resolved_at"}).
			AddRow(77, request.OrderID, request.BuyerUserID, "open", request.Reason, "", nil, time.Now(), nil))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE redstone_market_orders SET status = 'appealed', updated_at = $2")).
		WithArgs(request.OrderID, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	appeal, err := repository.CreateAppeal(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, int64(77), appeal.ID)
	require.Equal(t, "open", appeal.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPostgresCreateAppealRejectsDeliveredOrderAfterAppealWindow(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repository := &sqlRepository{db: db}
	request := CreateAppealRequest{BuyerUserID: 81, OrderID: 501, Reason: "Delivered item is unusable"}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, buyer_user_id, seller_user_id, status, unit_price, seller_net_amount,")).
		WithArgs(request.OrderID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "buyer_user_id", "seller_user_id", "status", "unit_price", "seller_net_amount", "settlement_due_at", "delivered_at"}).
			AddRow(501, 81, 22, "delivered", "12.50000000", "11.87500000", time.Now().Add(-time.Nanosecond), time.Now().Add(-24*time.Hour)))
	mock.ExpectRollback()

	_, err = repository.CreateAppeal(context.Background(), request)
	require.ErrorIs(t, err, ErrAppealNotAllowed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPostgresAutomaticSettlementUsesOrdinaryBalanceAndWalletReceipt(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	walletRepository, err := wallet.NewPostgresRepository(db)
	require.NoError(t, err)
	walletService, err := wallet.NewService(walletRepository)
	require.NoError(t, err)
	repository := &sqlRepository{db: db, wallet: walletService}
	orderID := int64(501)
	now := time.Date(2026, 8, 13, 12, 0, 1, 0, time.UTC)
	delivered := now.Add(-24*time.Hour - time.Second)
	amount := decimal.RequireFromString("11.87500000")
	key := marketSettlementOperationPrefix + "501"
	walletCredit := wallet.CreditRequest{
		UserID: int64(22), Asset: wallet.AssetNormal, Amount: amount, Reason: wallet.CreditSettlement,
		Reference: wallet.Reference{Type: marketFinancialReferenceType, ID: "501"}, IdempotencyKey: key,
	}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, buyer_user_id, seller_user_id, status, unit_price, seller_net_amount,")).
		WithArgs(orderID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "buyer_user_id", "seller_user_id", "status", "unit_price", "seller_net_amount", "settlement_due_at", "delivered_at"}).
			AddRow(orderID, 81, 22, "delivered", "12.50000000", amount, delivered.Add(24*time.Hour), delivered))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT recipient_user_id, amount FROM redstone_market_financial_events")).
		WithArgs(orderID, FinancialSettlement).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS (SELECT 1 FROM redstone_market_order_holds WHERE order_id = $1)")).
		WithArgs(orderID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS (SELECT 1 FROM redstone_market_appeals WHERE order_id = $1)")).
		WithArgs(orderID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO redstone_wallet_operations (user_id, operation, idempotency_key, request_fingerprint)")).
		WithArgs(int64(22), "settlement", key, walletCredit.Fingerprint()).
		WillReturnRows(sqlmock.NewRows([]string{"?column?"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT operation, request_fingerprint FROM redstone_wallet_operations")).
		WithArgs(int64(22), key).
		WillReturnRows(sqlmock.NewRows([]string{"operation", "request_fingerprint"}).AddRow("settlement", walletCredit.Fingerprint()))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT balance, bound_balance")).
		WithArgs(int64(22)).
		WillReturnRows(sqlmock.NewRows([]string{"balance", "bound_balance"}).AddRow("4.00000000", "0.00000000"))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE users SET balance = $1, updated_at = NOW() WHERE id = $2 AND deleted_at IS NULL")).
		WithArgs(decimal.RequireFromString("15.87500000"), int64(22)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO redstone_wallet_ledger (")).
		WithArgs(int64(22), wallet.AssetNormal, wallet.OperationSettlement, amount, decimal.RequireFromString("15.87500000"), marketFinancialReferenceType, "501", key, walletCredit.Fingerprint(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO redstone_market_financial_events (")).
		WithArgs(orderID, FinancialSettlement, int64(22), amount, key, int64(0), now).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE redstone_market_orders")).
		WithArgs(orderID, "settled", "settled", now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	result, err := repository.applyFinancialAction(context.Background(), orderID, 0, FinancialSettlement, false, true, "", now)
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.Equal(t, int64(22), result.RecipientUserID)
	require.True(t, result.Amount.Equal(amount))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPostgresSettlementWorkerClaimsDueOrderWithSkipLocked(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repository := &sqlRepository{db: db}
	now := time.Date(2026, 8, 13, 12, 0, 1, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("AND o.settlement_due_at < $1::timestamptz")).
		WithArgs(now).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectCommit()
	result, err := repository.SettleDueOrders(context.Background(), now, 10)
	require.NoError(t, err)
	require.Zero(t, result.Processed)
	require.Zero(t, result.Skipped)
	require.NoError(t, mock.ExpectationsWereMet())
}
