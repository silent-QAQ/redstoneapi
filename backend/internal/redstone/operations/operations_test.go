package operations

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/silent-QAQ/redstoneapi/internal/redstone/wallet"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestRequestWithdrawalDebitsNormalBalanceInsideBusinessTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	walletRepository := &operationsWalletRepository{}
	walletService, err := wallet.NewService(walletRepository)
	require.NoError(t, err)
	service, err := NewService(db, walletService)
	require.NoError(t, err)

	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery("FROM redstone_operations_withdrawals").WithArgs(int64(7), "withdrawal-key").WillReturnRows(sqlmock.NewRows(withdrawalColumns()))
	mock.ExpectQuery("INSERT INTO redstone_operations_withdrawals").
		WithArgs(int64(7), money("10.00000000"), money("1.25000000"), money("11.25000000"), "alipay", "payment-profile-42", "withdrawal-key", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(41), now, now))
	mock.ExpectExec("UPDATE redstone_operations_withdrawals SET status = 'pending_review'").WithArgs(int64(41)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO redstone_operations_audits").WithArgs(int64(7), "withdrawal", "41", "withdrawal_requested", "{}").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	item, created, err := service.RequestWithdrawal(context.Background(), WithdrawalRequest{
		UserID: 7, Amount: money("10.00000000"), FeeAmount: money("1.25000000"),
		PayoutMethod: "alipay", PayoutReference: "payment-profile-42", IdempotencyKey: "withdrawal-key",
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, int64(41), item.ID)
	require.True(t, item.TotalDebited.Equal(money("11.25000000")))
	require.Equal(t, 1, walletRepository.adjustCalls)
	require.True(t, walletRepository.adjustment.Delta.Equal(money("-11.25000000")))
	require.Equal(t, wallet.OperationWithdrawal, walletRepository.adjustment.Operation)
	require.Equal(t, wallet.Reference{Type: "operations_withdrawal", ID: "41"}, walletRepository.adjustment.Reference)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestClaimCampaignCreditsNormalWalletOnce(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	walletRepository := &operationsWalletRepository{}
	walletService, err := wallet.NewService(walletRepository)
	require.NoError(t, err)
	service, err := NewService(db, walletService)
	require.NoError(t, err)

	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery("FROM redstone_operations_campaigns WHERE id = \\$1 FOR UPDATE").WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows(campaignColumns()).AddRow(int64(9), "Welcome reward", "", "active", now.Add(-time.Hour), now.Add(time.Hour), money("3.00000000"), 1, now, now))
	mock.ExpectQuery("FROM redstone_operations_campaign_claims WHERE campaign_id = \\$1 AND user_id = \\$2 FOR UPDATE").WithArgs(int64(9), int64(3)).WillReturnRows(sqlmock.NewRows([]string{"id", "amount", "status"}))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM redstone_operations_campaign_claims`).WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("INSERT INTO redstone_operations_campaign_claims").WithArgs(int64(9), int64(3), money("3.00000000")).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(77)))
	mock.ExpectExec("UPDATE redstone_operations_campaign_claims SET wallet_operation_key").WithArgs("operations-campaign-claim-77", int64(77)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE redstone_operations_campaign_claims SET status = 'granted'").WithArgs(int64(77)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	amount, created, err := service.ClaimCampaign(context.Background(), 3, 9)
	require.NoError(t, err)
	require.True(t, created)
	require.True(t, amount.Equal(money("3.00000000")))
	require.Equal(t, 1, walletRepository.creditCalls)
	require.Equal(t, wallet.AssetNormal, walletRepository.credit.Asset)
	require.Equal(t, wallet.CreditActivityReward, walletRepository.credit.Reason)
	require.True(t, walletRepository.credit.Amount.Equal(money("3.00000000")))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestListAvailableProxiesFiltersOwnerAndCapacity(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	walletService, err := wallet.NewService(&operationsWalletRepository{})
	require.NoError(t, err)
	service, err := NewService(db, walletService)
	require.NoError(t, err)
	mock.ExpectQuery("FROM proxies p").WithArgs(int64(12)).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "protocol", "host", "port", "owner_user_id", "max_accounts", "account_count"}).AddRow(int64(3), "owned", "http", "127.0.0.1", 8080, int64(12), 5, int64(2)))

	items, err := service.ListAvailableProxies(context.Background(), 12)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, int64(12), *items[0].OwnerUserID)
	require.Equal(t, 5, items[0].MaxAccounts)
	require.Equal(t, int64(2), items[0].AccountCount)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOperationsMigrationUsesOnlyAllowedActivityAndWalletModels(t *testing.T) {
	content, err := os.ReadFile("../../../migrations/9200_redstone_operations_foundation.sql")
	require.NoError(t, err)
	text := string(content)
	require.Contains(t, text, "redstone_operations_withdrawals")
	require.Contains(t, text, "redstone_operations_referral_rewards")
	require.Contains(t, text, "redstone_operations_content_cases")
	require.Contains(t, text, "'withdrawal'")
	require.NotContains(t, text, "consumption_lottery")
	require.NotContains(t, text, "'points'")
	require.NotContains(t, text, "subsite_control_plane")
}

func money(value string) decimal.Decimal { return decimal.RequireFromString(value) }

func withdrawalColumns() []string {
	return []string{"id", "user_id", "amount", "fee_amount", "total_debited", "payout_method", "status", "admin_note", "processed_by_user_id", "processed_at", "created_at", "updated_at"}
}
func campaignColumns() []string {
	return []string{"id", "name", "description", "status", "starts_at", "ends_at", "reward_amount", "max_claims", "created_at", "updated_at"}
}

type operationsWalletRepository struct {
	creditCalls int
	credit      wallet.CreditRequest
	adjustCalls int
	adjustment  wallet.NormalAdjustmentRequest
}

func (r *operationsWalletRepository) GetSnapshot(context.Context, int64) (wallet.Snapshot, error) {
	return wallet.Snapshot{}, nil
}
func (r *operationsWalletRepository) Credit(_ context.Context, req wallet.CreditRequest) (wallet.CreditResult, error) {
	r.creditCalls++
	r.credit = req
	return wallet.CreditResult{Applied: true}, nil
}
func (r *operationsWalletRepository) ChargeToken(context.Context, wallet.TokenChargeRequest) (wallet.TokenChargeResult, error) {
	return wallet.TokenChargeResult{}, nil
}
func (r *operationsWalletRepository) ReserveTokenHold(context.Context, wallet.TokenHoldRequest) (wallet.TokenHoldResult, error) {
	return wallet.TokenHoldResult{}, nil
}
func (r *operationsWalletRepository) CaptureTokenHold(context.Context, wallet.TokenHoldCaptureRequest) (wallet.TokenHoldResult, error) {
	return wallet.TokenHoldResult{}, nil
}
func (r *operationsWalletRepository) ReleaseTokenHold(context.Context, wallet.TokenHoldReleaseRequest) (wallet.TokenHoldResult, error) {
	return wallet.TokenHoldResult{}, nil
}
func (r *operationsWalletRepository) DebitMarketplace(context.Context, wallet.MarketplaceDebitRequest) (wallet.CreditResult, error) {
	return wallet.CreditResult{}, nil
}
func (r *operationsWalletRepository) ListLedger(context.Context, int64, int, int) (wallet.LedgerPage, error) {
	return wallet.LedgerPage{}, nil
}
func (r *operationsWalletRepository) CreditInExecutor(_ context.Context, _ wallet.SQLExecutor, req wallet.CreditRequest) (wallet.CreditResult, error) {
	r.creditCalls++
	r.credit = req
	return wallet.CreditResult{Applied: true}, nil
}
func (r *operationsWalletRepository) ChargeTokenInExecutor(context.Context, wallet.SQLExecutor, wallet.TokenChargeRequest) (wallet.TokenChargeResult, error) {
	return wallet.TokenChargeResult{}, nil
}
func (r *operationsWalletRepository) AdjustNormalInExecutor(_ context.Context, _ wallet.SQLExecutor, req wallet.NormalAdjustmentRequest) (wallet.CreditResult, error) {
	r.adjustCalls++
	r.adjustment = req
	return wallet.CreditResult{Applied: true}, nil
}
func (r *operationsWalletRepository) SetNormalInExecutor(context.Context, wallet.SQLExecutor, wallet.SetNormalBalanceRequest) (wallet.CreditResult, error) {
	return wallet.CreditResult{Applied: true}, nil
}
func (r *operationsWalletRepository) DebitMarketplaceInExecutor(context.Context, wallet.SQLExecutor, wallet.MarketplaceDebitRequest) (wallet.CreditResult, error) {
	return wallet.CreditResult{Applied: true}, nil
}

var _ wallet.Repository = (*operationsWalletRepository)(nil)
var _ wallet.ExecutorRepository = (*operationsWalletRepository)(nil)
