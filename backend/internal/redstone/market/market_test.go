package market

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func money(value string) decimal.Decimal {
	return decimal.RequireFromString(value)
}

func TestSellerListingLimit(t *testing.T) {
	limit, err := SellerListingLimit(money("29.99999999"), money("1"))
	require.NoError(t, err)
	require.Zero(t, limit)

	limit, err = SellerListingLimit(money("30"), money("1"))
	require.NoError(t, err)
	require.Equal(t, 3, limit)

	limit, err = SellerListingLimit(money("70"), money("1"))
	require.NoError(t, err)
	require.Equal(t, 5, limit)

	limit, err = SellerListingLimit(money("15"), money("2"))
	require.NoError(t, err)
	require.Equal(t, 3, limit)
}

func TestSellerListingLimitRejectsInvalidInputs(t *testing.T) {
	_, err := SellerListingLimit(money("-1"), money("1"))
	require.ErrorIs(t, err, ErrInvalidBalance)
	_, err = SellerListingLimit(money("1"), decimal.Zero)
	require.ErrorIs(t, err, ErrInvalidRechargeRate)
}

func TestCalculateOrderAmounts(t *testing.T) {
	amounts, err := CalculateOrderAmounts(money("10.00000000"), SellerUser, money(DefaultUserServiceFeeRate))
	require.NoError(t, err)
	require.True(t, amounts.FeeAmount.Equal(money("0.50000000")))
	require.True(t, amounts.SellerNet.Equal(money("9.50000000")))

	official, err := CalculateOrderAmounts(money("10.00000000"), SellerOfficial, money("0.20000000"))
	require.NoError(t, err)
	require.True(t, official.FeeAmount.IsZero())
	require.True(t, official.SellerNet.Equal(money("10.00000000")))
}

func TestCreateOrderRequestAndPurchasableProductPolicy(t *testing.T) {
	request := CreateOrderRequest{BuyerUserID: 9, ProductID: 22, IdempotencyKey: "market-order-22"}
	require.NoError(t, request.Validate())
	require.NotEqual(t, request.fingerprint(), CreateOrderRequest{
		BuyerUserID: 9, ProductID: 23, IdempotencyKey: "market-order-22",
	}.fingerprint())

	product := Product{
		ID: 22, SellerUserID: 7, SellerKind: SellerUser, UnitPrice: money("12.50000000"),
		InventoryTotal: 1, InventoryReserved: 0, Status: "active", RiskStatus: "passed",
	}
	require.NoError(t, ValidatePurchasableProduct(9, product))
	require.ErrorIs(t, ValidatePurchasableProduct(7, product), ErrSelfPurchase)

	product.Status = "suspended"
	require.ErrorIs(t, ValidatePurchasableProduct(9, product), ErrProductUnavailable)
	product.Status = "active"
	product.InventoryReserved = 1
	require.ErrorIs(t, ValidatePurchasableProduct(9, product), ErrInventoryUnavailable)
}

func TestCreateOrderRequestRejectsMalformedIdempotencyAndSettlementWindow(t *testing.T) {
	require.ErrorIs(t, (CreateOrderRequest{BuyerUserID: 1, ProductID: 1}).Validate(), ErrIdempotencyKeyRequired)
	require.ErrorIs(t, (CreateOrderRequest{BuyerUserID: 1, ProductID: 0, IdempotencyKey: "k"}).Validate(), ErrInvalidProductID)
	require.ErrorIs(t, (CreateOrderRequest{BuyerUserID: 1, ProductID: 1, IdempotencyKey: " with-space"}).Validate(), ErrIdempotencyKeyRequired)

	createdAt := time.Date(2026, 8, 13, 12, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	require.Equal(t, createdAt.UTC().Add(24*time.Hour), settlementDueAt(createdAt))
}
