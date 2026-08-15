package market

import (
	"context"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/silent-QAQ/redstoneapi/internal/redstone/wallet"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestPostgresCreateOrderCommitsOneNormalBalanceTransaction(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	walletRepository, err := wallet.NewPostgresRepository(db)
	require.NoError(t, err)
	walletService, err := wallet.NewService(walletRepository)
	require.NoError(t, err)
	repository := &sqlRepository{db: db, wallet: walletService}
	request := CreateOrderRequest{BuyerUserID: 81, ProductID: 19, IdempotencyKey: "market-order-19"}
	fingerprint := request.fingerprint()
	walletDebit := wallet.MarketplaceDebitRequest{
		UserID:         request.BuyerUserID,
		Amount:         decimal.RequireFromString("12.50000000"),
		Reference:      wallet.Reference{Type: marketOrderReferenceType, ID: "501"},
		IdempotencyKey: marketWalletDebitKey(request.IdempotencyKey),
	}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO redstone_wallet_operations")).
		WithArgs(request.BuyerUserID, wallet.OperationMarketplaceDebit, request.IdempotencyKey, fingerprint).
		WillReturnRows(sqlmock.NewRows([]string{"?column?"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT operation, request_fingerprint")).
		WithArgs(request.BuyerUserID, request.IdempotencyKey).
		WillReturnRows(sqlmock.NewRows([]string{"operation", "request_fingerprint"}).AddRow(wallet.OperationMarketplaceDebit, fingerprint))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, seller_user_id, seller_kind, product_type, title, description,")).
		WithArgs(request.ProductID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "seller_user_id", "seller_kind", "product_type", "title", "description",
			"unit_price", "inventory_total", "inventory_reserved", "status", "risk_status", "account_id",
		}).AddRow(19, 22, string(SellerUser), "text_key", "Test item", "", "12.50000000", 1, 0, "active", "passed", nil))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id\n\t\tFROM redstone_market_delivery_items")).
		WithArgs(request.ProductID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(32))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT user_service_fee_rate\n\t\tFROM redstone_market_fee_policy WHERE singleton = TRUE FOR SHARE")).
		WillReturnRows(sqlmock.NewRows([]string{"user_service_fee_rate"}).AddRow("0.07500000"))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO redstone_market_orders (")).
		WithArgs(
			sqlmock.AnyArg(), request.BuyerUserID, int64(22), request.ProductID, int64(32),
			decimal.RequireFromString("12.50000000"), decimal.RequireFromString("0.07500000"),
			decimal.RequireFromString("0.93750000"), decimal.RequireFromString("11.56250000"), sqlmock.AnyArg(), sqlmock.AnyArg(),
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(501))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE redstone_market_products")).
		WithArgs(request.ProductID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE redstone_market_delivery_items")).
		WithArgs(int64(32)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// The wallet owns the debit, balance snapshot and immutable ledger. A
	// normal-only balance update must not inspect or consume bound balance.
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO redstone_wallet_operations")).
		WithArgs(request.BuyerUserID, wallet.OperationMarketplaceDebit, walletDebit.IdempotencyKey, walletDebit.Fingerprint()).
		WillReturnRows(sqlmock.NewRows([]string{"?column?"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT operation, request_fingerprint")).
		WithArgs(request.BuyerUserID, walletDebit.IdempotencyKey).
		WillReturnRows(sqlmock.NewRows([]string{"operation", "request_fingerprint"}).AddRow(wallet.OperationMarketplaceDebit, walletDebit.Fingerprint()))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT balance, bound_balance")).
		WithArgs(request.BuyerUserID).
		WillReturnRows(sqlmock.NewRows([]string{"balance", "bound_balance"}).AddRow("100.00000000", "999.00000000"))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE users SET balance = $1, updated_at = NOW() WHERE id = $2 AND deleted_at IS NULL")).
		WithArgs(decimal.RequireFromString("87.50000000"), request.BuyerUserID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO redstone_wallet_ledger (")).
		WithArgs(
			request.BuyerUserID, wallet.AssetNormal, wallet.OperationMarketplaceDebit, decimal.RequireFromString("-12.50000000"), decimal.RequireFromString("87.50000000"),
			marketOrderReferenceType, "501", walletDebit.IdempotencyKey, walletDebit.Fingerprint(), sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	result, err := repository.CreateOrder(context.Background(), request)
	require.NoError(t, err)
	require.False(t, result.Replayed)
	require.Equal(t, int64(501), result.Order.ID)
	require.Equal(t, int64(32), result.Order.DeliveryItemID)
	require.True(t, result.Order.UnitPrice.Equal(decimal.RequireFromString("12.50000000")))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPostgresCreateOrderRollsBackWhenNormalBalanceIsInsufficient(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	walletRepository, err := wallet.NewPostgresRepository(db)
	require.NoError(t, err)
	walletService, err := wallet.NewService(walletRepository)
	require.NoError(t, err)
	repository := &sqlRepository{db: db, wallet: walletService}
	request := CreateOrderRequest{BuyerUserID: 81, ProductID: 19, IdempotencyKey: "market-order-no-funds"}
	fingerprint := request.fingerprint()
	walletDebit := wallet.MarketplaceDebitRequest{
		UserID:         request.BuyerUserID,
		Amount:         decimal.RequireFromString("12.50000000"),
		Reference:      wallet.Reference{Type: marketOrderReferenceType, ID: "501"},
		IdempotencyKey: marketWalletDebitKey(request.IdempotencyKey),
	}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO redstone_wallet_operations")).
		WithArgs(request.BuyerUserID, wallet.OperationMarketplaceDebit, request.IdempotencyKey, fingerprint).
		WillReturnRows(sqlmock.NewRows([]string{"?column?"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT operation, request_fingerprint")).
		WithArgs(request.BuyerUserID, request.IdempotencyKey).
		WillReturnRows(sqlmock.NewRows([]string{"operation", "request_fingerprint"}).AddRow(wallet.OperationMarketplaceDebit, fingerprint))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, seller_user_id, seller_kind, product_type, title, description,")).
		WithArgs(request.ProductID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "seller_user_id", "seller_kind", "product_type", "title", "description",
			"unit_price", "inventory_total", "inventory_reserved", "status", "risk_status", "account_id",
		}).AddRow(19, 22, string(SellerUser), "text_key", "Test item", "", "12.50000000", 1, 0, "active", "passed", nil))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id\n\t\tFROM redstone_market_delivery_items")).
		WithArgs(request.ProductID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(32))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT user_service_fee_rate\n\t\tFROM redstone_market_fee_policy WHERE singleton = TRUE FOR SHARE")).
		WillReturnRows(sqlmock.NewRows([]string{"user_service_fee_rate"}).AddRow("0.05000000"))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO redstone_market_orders (")).
		WithArgs(
			sqlmock.AnyArg(), request.BuyerUserID, int64(22), request.ProductID, int64(32),
			decimal.RequireFromString("12.50000000"), decimal.RequireFromString("0.05000000"),
			decimal.RequireFromString("0.62500000"), decimal.RequireFromString("11.87500000"), sqlmock.AnyArg(), sqlmock.AnyArg(),
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(501))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE redstone_market_products")).
		WithArgs(request.ProductID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE redstone_market_delivery_items")).
		WithArgs(int64(32)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO redstone_wallet_operations")).
		WithArgs(request.BuyerUserID, wallet.OperationMarketplaceDebit, walletDebit.IdempotencyKey, walletDebit.Fingerprint()).
		WillReturnRows(sqlmock.NewRows([]string{"?column?"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT operation, request_fingerprint")).
		WithArgs(request.BuyerUserID, walletDebit.IdempotencyKey).
		WillReturnRows(sqlmock.NewRows([]string{"operation", "request_fingerprint"}).AddRow(wallet.OperationMarketplaceDebit, walletDebit.Fingerprint()))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT balance, bound_balance")).
		WithArgs(request.BuyerUserID).
		WillReturnRows(sqlmock.NewRows([]string{"balance", "bound_balance"}).AddRow("10.00000000", "999.00000000"))
	mock.ExpectRollback()

	_, err = repository.CreateOrder(context.Background(), request)
	require.ErrorIs(t, err, ErrInsufficientNormalFunds)
	require.NoError(t, mock.ExpectationsWereMet())
}
