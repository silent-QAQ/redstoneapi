package market

import (
	"testing"
	"time"

	infraerrors "github.com/silent-QAQ/redstoneapi/internal/pkg/errors"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestCreateAppealRequestValidation(t *testing.T) {
	valid := CreateAppealRequest{BuyerUserID: 8, OrderID: 19, Reason: "The supplied item is invalid."}
	require.NoError(t, valid.Validate())
	require.ErrorIs(t, (CreateAppealRequest{BuyerUserID: 8, OrderID: 19, Reason: "  "}).Validate(), ErrAppealReasonRequired)
	require.ErrorIs(t, (CreateAppealRequest{BuyerUserID: 8, OrderID: 0, Reason: "missing"}).Validate(), ErrInvalidOrderID)
	require.ErrorIs(t, (CreateAppealRequest{BuyerUserID: 0, OrderID: 19, Reason: "missing"}).Validate(), ErrInvalidBuyer)
}

func TestAdminSettlementRequestValidation(t *testing.T) {
	require.NoError(t, (AdminOrderRequest{ActorUserID: 2, OrderID: 9}).Validate())
	require.ErrorIs(t, (AdminOrderRequest{ActorUserID: 0, OrderID: 9}).Validate(), ErrInvalidActor)
	require.ErrorIs(t, (AdminOrderRequest{ActorUserID: 2, OrderID: 0}).Validate(), ErrInvalidOrderID)

	require.NoError(t, (ResolveAppealRequest{ActorUserID: 2, OrderID: 9, Action: ResolveAppealRefund}).Validate())
	require.NoError(t, (ResolveAppealRequest{ActorUserID: 2, OrderID: 9, Action: ResolveAppealRelease}).Validate())
	require.Error(t, (ResolveAppealRequest{ActorUserID: 2, OrderID: 9, Action: "cancel"}).Validate())
}

func TestFinancialActionFingerprintIsActionAndRecipientSpecific(t *testing.T) {
	amount := decimal.RequireFromString("12.50000000")
	settlement := financialActionFingerprint(FinancialSettlement, 10, 8, amount)
	require.Len(t, settlement, 64)
	require.NotEqual(t, settlement, financialActionFingerprint(FinancialRefund, 10, 8, amount))
	require.NotEqual(t, settlement, financialActionFingerprint(FinancialSettlement, 10, 9, amount))
	require.NotEqual(t, settlement, financialActionFingerprint(FinancialSettlement, 10, 8, decimal.RequireFromString("12.50000001")))
}

func TestAutomaticSettlementUsesDeliveryTimeNotPurchaseDueDate(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 1, 0, time.UTC)
	delivered := now.Add(-24 * time.Hour)
	// The contract is strictly after 24 hours. This protects a buyer's full
	// 24-hour appeal window and avoids settling exactly at the boundary.
	require.False(t, delivered.Add(marketSettlementWindow).Before(now))
	require.True(t, delivered.Add(-time.Nanosecond).Add(marketSettlementWindow).Before(now))
}

func TestMarketApplicationErrorPreservesAppealOwnershipAndState(t *testing.T) {
	require.Equal(t, int32(403), infraerrors.FromError(marketApplicationError(ErrAppealForbidden)).Code)
	require.Equal(t, int32(409), infraerrors.FromError(marketApplicationError(ErrAppealExists)).Code)
	require.Equal(t, int32(409), infraerrors.FromError(marketApplicationError(ErrSettlementNotDue)).Code)
	require.Equal(t, int32(400), infraerrors.FromError(marketApplicationError(ErrInvalidOrderID)).Code)
}
