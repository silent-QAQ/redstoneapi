//go:build unit

package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	redstonewallet "github.com/silent-QAQ/redstoneapi/internal/redstone/wallet"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/silent-QAQ/redstoneapi/internal/service"
)

func TestApplyUsageBillingEffects_RejectsMissingWallet(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	mock.ExpectRollback()

	result := &service.UsageBillingApplyResult{Applied: true}
	err = (&usageBillingRepository{}).applyUsageBillingEffects(ctx, tx, &service.UsageBillingCommand{
		UserID:      42,
		BalanceCost: 10,
	}, result)
	require.EqualError(t, err, "usage billing repository wallet is nil")
	require.Nil(t, result.NewBalance)
	require.False(t, result.BalanceOverdrafted)
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyUsageBillingEffects_ChargesBoundBeforeNormalInCallerTransaction(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	walletRepository, err := redstonewallet.NewPostgresRepository(db)
	require.NoError(t, err)
	walletService, err := redstonewallet.NewService(walletRepository)
	require.NoError(t, err)
	repo := &usageBillingRepository{db: db, wallet: walletService}

	cmd := &service.UsageBillingCommand{RequestID: "wallet-order", APIKeyID: 9, UserID: 42, BalanceCost: 8}
	walletRequestID := walletUsageRequestID(cmd.RequestID, cmd.APIKeyID)
	walletRequest := redstonewallet.TokenChargeRequest{
		UserID: cmd.UserID,
		Amount: decimal.RequireFromString("8.00000000"),
		Reference: redstonewallet.Reference{
			Type: "usage_billing",
			ID:   walletRequestID,
		},
		IdempotencyKey: walletRequestID,
	}

	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	mock.ExpectQuery(`(?s)INSERT INTO redstone_wallet_operations.*RETURNING true`).
		WillReturnRows(sqlmock.NewRows([]string{"inserted"}).AddRow(true))
	mock.ExpectQuery(`(?s)SELECT operation, request_fingerprint.*FROM redstone_wallet_operations.*FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"operation", "request_fingerprint"}).
			AddRow(redstonewallet.OperationTokenCharge, walletRequest.Fingerprint()))
	mock.ExpectQuery(`(?s)SELECT balance, bound_balance.*FROM users.*FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"balance", "bound_balance"}).AddRow("10.00000000", "3.00000000"))
	mock.ExpectExec(`(?s)UPDATE users.*SET balance = \$1, bound_balance = \$2`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)INSERT INTO redstone_wallet_ledger`).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`(?s)INSERT INTO redstone_wallet_ledger`).
		WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectCommit()

	result := &service.UsageBillingApplyResult{Applied: true}
	err = repo.applyUsageBillingEffects(ctx, tx, cmd, result)
	require.NoError(t, err)
	require.NotNil(t, result.NewBalance)
	require.InDelta(t, 5, *result.NewBalance, 0.00000001)
	require.NotNil(t, result.NormalBalanceCost)
	require.InDelta(t, 5, *result.NormalBalanceCost, 0.00000001)
	require.False(t, result.BalanceOverdrafted)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyUsageBillingEffects_InsufficientWalletRollsBackSubscriptionUpdate(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	walletRepository, err := redstonewallet.NewPostgresRepository(db)
	require.NoError(t, err)
	walletService, err := redstonewallet.NewService(walletRepository)
	require.NoError(t, err)
	repo := &usageBillingRepository{db: db, wallet: walletService}
	subscriptionID := int64(7)
	cmd := &service.UsageBillingCommand{
		RequestID:        "wallet-insufficient",
		APIKeyID:         9,
		UserID:           42,
		SubscriptionID:   &subscriptionID,
		SubscriptionCost: 1,
		BalanceCost:      4,
	}
	walletRequestID := walletUsageRequestID(cmd.RequestID, cmd.APIKeyID)
	walletRequest := redstonewallet.TokenChargeRequest{
		UserID: cmd.UserID,
		Amount: decimal.RequireFromString("4.00000000"),
		Reference: redstonewallet.Reference{
			Type: "usage_billing",
			ID:   walletRequestID,
		},
		IdempotencyKey: walletRequestID,
	}

	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	mock.ExpectQuery(`(?s)SELECT us.daily_usage_usd.*FOR UPDATE OF us`).
		WithArgs(subscriptionID).
		WillReturnRows(sqlmock.NewRows([]string{"daily_usage_usd", "weekly_usage_usd", "monthly_usage_usd", "daily_limit_usd", "weekly_limit_usd", "monthly_limit_usd"}).
			AddRow(0.0, 0.0, 0.0, 0.0, nil, nil))
	mock.ExpectExec(`(?s)UPDATE user_subscriptions\s+SET daily_usage_usd`).
		WithArgs(0.0, subscriptionID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)INSERT INTO redstone_wallet_operations.*RETURNING true`).
		WillReturnRows(sqlmock.NewRows([]string{"inserted"}).AddRow(true))
	mock.ExpectQuery(`(?s)SELECT operation, request_fingerprint.*FROM redstone_wallet_operations.*FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"operation", "request_fingerprint"}).
			AddRow(redstonewallet.OperationTokenCharge, walletRequest.Fingerprint()))
	mock.ExpectQuery(`(?s)SELECT balance, bound_balance.*FROM users.*FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"balance", "bound_balance"}).AddRow("2.00000000", "1.00000000"))
	mock.ExpectRollback()

	result := &service.UsageBillingApplyResult{Applied: true}
	err = repo.applyUsageBillingEffects(ctx, tx, cmd, result)
	require.ErrorIs(t, err, service.ErrInsufficientBalance)
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyUsageBillingEffects_MapsWalletUserForeignKeyError(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	walletRepository, err := redstonewallet.NewPostgresRepository(db)
	require.NoError(t, err)
	walletService, err := redstonewallet.NewService(walletRepository)
	require.NoError(t, err)
	repo := &usageBillingRepository{db: db, wallet: walletService}

	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	mock.ExpectQuery(`(?s)INSERT INTO redstone_wallet_operations.*RETURNING true`).
		WillReturnError(&pq.Error{
			Code:       pq.ErrorCode("23503"),
			Constraint: "redstone_wallet_operations_user_id_fkey",
		})
	mock.ExpectRollback()

	result := &service.UsageBillingApplyResult{Applied: true}
	err = repo.applyUsageBillingEffects(ctx, tx, &service.UsageBillingCommand{
		RequestID:   "missing-wallet-user",
		APIKeyID:    9,
		UserID:      42,
		BalanceCost: 1,
	}, result)
	require.ErrorIs(t, err, service.ErrUserNotFound)
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBatchImageWalletOperationKey_IsStableAndScoped(t *testing.T) {
	command := &service.BatchImageBalanceHoldCommand{UserID: 42, APIKeyID: 7, BatchID: "imgbatch"}
	require.Equal(t, batchImageWalletOperationKey("hold", command), batchImageWalletOperationKey("hold", command))
	require.NotEqual(t, batchImageWalletOperationKey("hold", command), batchImageWalletOperationKey("release", command))

	otherUser := *command
	otherUser.UserID++
	require.NotEqual(t, batchImageWalletOperationKey("hold", command), batchImageWalletOperationKey("hold", &otherUser))
}
