package wallet

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func amount(value string) decimal.Decimal {
	return decimal.RequireFromString(value)
}

func validReference() Reference {
	return Reference{Type: "usage_log", ID: "req_123"}
}

func TestAllocateTokenDebit_UsesBoundBeforeNormal(t *testing.T) {
	allocation, err := AllocateTokenDebit(Balances{Bound: amount("5.25000000"), Normal: amount("10.00000000")}, amount("12.00000000"))
	require.NoError(t, err)
	require.True(t, allocation.Bound.Equal(amount("5.25000000")))
	require.True(t, allocation.Normal.Equal(amount("6.75000000")))
	require.True(t, allocation.BoundBalanceAfter.IsZero())
	require.True(t, allocation.NormalBalanceAfter.Equal(amount("3.25000000")))
}

func TestAllocateTokenDebit_RejectsInsufficientOrInvalidBalances(t *testing.T) {
	_, err := AllocateTokenDebit(Balances{Bound: amount("1"), Normal: amount("2")}, amount("3.00000001"))
	require.ErrorIs(t, err, ErrInsufficientFunds)

	_, err = AllocateTokenDebit(Balances{Bound: amount("-1"), Normal: amount("10")}, amount("1"))
	require.ErrorIs(t, err, ErrInvalidBalance)

	_, err = AllocateTokenDebit(Balances{Normal: amount("10")}, amount("0.000000001"))
	require.ErrorIs(t, err, ErrInvalidAmount)
}

func TestBoundBalancePolicy(t *testing.T) {
	require.NoError(t, ValidateCreditPolicy(AssetBound, CreditAdminGrant))
	require.NoError(t, ValidateCreditPolicy(AssetBound, CreditRedeemCode))
	require.ErrorIs(t, ValidateCreditPolicy(AssetBound, CreditPayment), ErrBoundBalanceCreditSource)

	require.True(t, CanSpend(AssetBound, SpendToken))
	require.False(t, CanSpend(AssetBound, SpendWithdrawal))
	require.False(t, CanSpend(AssetBound, SpendMarketplace))
	require.ErrorIs(t, ValidateSpend(AssetBound, SpendMarketplace), ErrBoundBalanceSpend)
	require.NoError(t, ValidateSpend(AssetNormal, SpendWithdrawal))
	require.NoError(t, ValidateSpend(AssetNormal, SpendMarketplace))
}

func TestCreditRequest_RejectsInvalidBoundSourceAndIdempotency(t *testing.T) {
	request := CreditRequest{
		UserID:         7,
		Asset:          AssetBound,
		Amount:         amount("1"),
		Reason:         CreditSettlement,
		Reference:      validReference(),
		IdempotencyKey: "credit-1",
	}
	require.ErrorIs(t, request.Validate(), ErrBoundBalanceCreditSource)

	request.Reason = CreditAdminGrant
	request.IdempotencyKey = ""
	require.ErrorIs(t, request.Validate(), ErrIdempotencyKeyRequired)
}

func TestRequestsFingerprintIsStableAndSeparatesDistinctRequests(t *testing.T) {
	request := TokenChargeRequest{UserID: 7, Amount: amount("1.25000000"), Reference: validReference(), IdempotencyKey: "charge-1"}
	require.Equal(t, request.Fingerprint(), request.Fingerprint())

	changed := request
	changed.Amount = amount("1.26000000")
	require.NotEqual(t, request.Fingerprint(), changed.Fingerprint())

	invalid := request
	invalid.Amount = amount("1.250000001")
	require.NotEqual(t, request.Fingerprint(), invalid.Fingerprint())
}

func TestLedgerEntryValidation(t *testing.T) {
	entry := LedgerEntry{
		ID:             1,
		UserID:         7,
		Asset:          AssetBound,
		Operation:      OperationTokenCharge,
		Delta:          amount("-1.00000000"),
		BalanceAfter:   amount("2.00000000"),
		Reference:      validReference(),
		IdempotencyKey: "charge-1",
		CreatedAt:      time.Now(),
	}
	require.NoError(t, entry.Validate())

	entry.BalanceAfter = amount("-0.00000001")
	require.ErrorIs(t, entry.Validate(), ErrInvalidLedgerEntry)
}

func TestLedgerEntryValidation_AllowsOnlyLegacyNegativeNormalOpeningSnapshot(t *testing.T) {
	entry := LedgerEntry{
		UserID:         7,
		Asset:          AssetNormal,
		Operation:      OperationOpeningBalance,
		Delta:          amount("-1.00000000"),
		BalanceAfter:   amount("-1.00000000"),
		Reference:      Reference{Type: "migration", ID: "9000_redstone_wallet_foundation"},
		IdempotencyKey: "opening-7",
		CreatedAt:      time.Now(),
	}
	require.NoError(t, entry.Validate())

	entry.Operation = OperationTokenCharge
	require.ErrorIs(t, entry.Validate(), ErrInvalidLedgerEntry)
}

func TestLedgerEntryValidation_RejectsInvalidOrBoundCreditOperation(t *testing.T) {
	entry := LedgerEntry{
		UserID:         7,
		Asset:          AssetBound,
		Operation:      OperationPayment,
		Delta:          amount("1.00000000"),
		BalanceAfter:   amount("1.00000000"),
		Reference:      validReference(),
		IdempotencyKey: "payment-1",
		CreatedAt:      time.Now(),
	}
	require.ErrorIs(t, entry.Validate(), ErrInvalidLedgerEntry)

	entry.Asset = AssetNormal
	entry.Operation = LedgerOperation("unknown")
	require.ErrorIs(t, entry.Validate(), ErrInvalidLedgerEntry)
}

func TestLedgerEntryValidation_RejectsBoundOpeningAndNonDebitMarketplaceEntry(t *testing.T) {
	entry := LedgerEntry{
		UserID:         7,
		Asset:          AssetBound,
		Operation:      OperationOpeningBalance,
		Delta:          amount("1.00000000"),
		BalanceAfter:   amount("1.00000000"),
		Reference:      validReference(),
		IdempotencyKey: "opening-7",
		CreatedAt:      time.Now(),
	}
	require.ErrorIs(t, entry.Validate(), ErrInvalidLedgerEntry)

	entry.Asset = AssetNormal
	entry.Operation = OperationMarketplaceDebit
	require.ErrorIs(t, entry.Validate(), ErrInvalidLedgerEntry)

	entry.Delta = amount("-1.00000000")
	require.NoError(t, entry.Validate())
}

func TestLedgerEntryValidation_SeparatesOperatingEventTypes(t *testing.T) {
	entry := LedgerEntry{
		UserID:         7,
		Asset:          AssetNormal,
		Operation:      OperationWithdrawal,
		Delta:          amount("-1.00000000"),
		BalanceAfter:   amount("2.00000000"),
		Reference:      validReference(),
		IdempotencyKey: "withdrawal-1",
		CreatedAt:      time.Now(),
	}
	require.NoError(t, entry.Validate())

	entry.Delta = amount("1.00000000")
	require.ErrorIs(t, entry.Validate(), ErrInvalidLedgerEntry)

	entry.Operation = OperationAdminAdjustment
	require.NoError(t, entry.Validate())

	entry.Operation = OperationReferralReward
	require.NoError(t, entry.Validate())
	entry.Delta = amount("-1.00000000")
	require.ErrorIs(t, entry.Validate(), ErrInvalidLedgerEntry)
}

func TestNormalAdjustmentRequest_AllowsExplicitOperationalDebits(t *testing.T) {
	request := NormalAdjustmentRequest{
		UserID:         7,
		Delta:          amount("-1.00000000"),
		Operation:      OperationWithdrawal,
		Reference:      validReference(),
		IdempotencyKey: "withdrawal-1",
	}
	require.NoError(t, request.Validate())

	request.Operation = OperationAdminAdjustment
	require.NoError(t, request.Validate())
}

func TestServiceValidatesBeforeRepository(t *testing.T) {
	repository := &stubRepository{}
	service, err := NewService(repository)
	require.NoError(t, err)

	_, err = service.GrantBound(context.Background(), 7, amount("1"), CreditPayment, validReference(), "credit-1")
	require.ErrorIs(t, err, ErrBoundBalanceCreditSource)
	require.Equal(t, 0, repository.creditCalls)

	result, err := service.GrantBound(context.Background(), 7, amount("1"), CreditRedeemCode, validReference(), "credit-1")
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.Equal(t, 1, repository.creditCalls)
	require.Equal(t, AssetBound, repository.lastCredit.Asset)
}

func TestServiceDelegatesTokenChargeToTransactionalPort(t *testing.T) {
	repository := &stubRepository{chargeResult: TokenChargeResult{Applied: true}}
	service, err := NewService(repository)
	require.NoError(t, err)

	request := TokenChargeRequest{UserID: 7, Amount: amount("1.25"), Reference: validReference(), IdempotencyKey: "charge-1"}
	_, err = service.ChargeToken(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, 1, repository.chargeCalls)
	require.Equal(t, request, repository.lastCharge)
}

func TestMarketplaceDebitNeverUsesBoundBalance(t *testing.T) {
	repository := &stubRepository{creditResult: CreditResult{Applied: true}}
	service, err := NewService(repository)
	require.NoError(t, err)

	request := MarketplaceDebitRequest{UserID: 7, Amount: amount("1.25"), Reference: Reference{Type: "market_order", ID: "order_123"}, IdempotencyKey: "market-1"}
	_, err = service.DebitMarketplace(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, 1, repository.marketplaceCalls)
	require.Equal(t, request, repository.lastMarketplaceDebit)
}

func TestNewServiceRequiresRepository(t *testing.T) {
	service, err := NewService(nil)
	require.Nil(t, service)
	require.ErrorIs(t, err, ErrRepositoryRequired)
}

type stubRepository struct {
	creditCalls          int
	chargeCalls          int
	marketplaceCalls     int
	lastCredit           CreditRequest
	lastCharge           TokenChargeRequest
	lastMarketplaceDebit MarketplaceDebitRequest
	creditErr            error
	chargeErr            error
	creditResult         CreditResult
	chargeResult         TokenChargeResult
}

func (s *stubRepository) GetSnapshot(_ context.Context, userID int64) (Snapshot, error) {
	return Snapshot{UserID: userID}, nil
}

func (s *stubRepository) Credit(_ context.Context, request CreditRequest) (CreditResult, error) {
	s.creditCalls++
	s.lastCredit = request
	if s.creditErr != nil {
		return CreditResult{}, s.creditErr
	}
	if s.creditResult.Applied {
		return s.creditResult, nil
	}
	return CreditResult{Applied: true}, nil
}

func (s *stubRepository) DebitMarketplace(_ context.Context, request MarketplaceDebitRequest) (CreditResult, error) {
	s.marketplaceCalls++
	s.lastMarketplaceDebit = request
	return CreditResult{Applied: true}, nil
}

func (s *stubRepository) ChargeToken(_ context.Context, request TokenChargeRequest) (TokenChargeResult, error) {
	s.chargeCalls++
	s.lastCharge = request
	if s.chargeErr != nil {
		return TokenChargeResult{}, s.chargeErr
	}
	return s.chargeResult, nil
}

func (s *stubRepository) ReserveTokenHold(context.Context, TokenHoldRequest) (TokenHoldResult, error) {
	return TokenHoldResult{}, nil
}

func (s *stubRepository) CaptureTokenHold(context.Context, TokenHoldCaptureRequest) (TokenHoldResult, error) {
	return TokenHoldResult{}, nil
}

func (s *stubRepository) ReleaseTokenHold(context.Context, TokenHoldReleaseRequest) (TokenHoldResult, error) {
	return TokenHoldResult{}, nil
}

func (s *stubRepository) ListLedger(context.Context, int64, int, int) (LedgerPage, error) {
	return LedgerPage{}, errors.New("not implemented")
}
