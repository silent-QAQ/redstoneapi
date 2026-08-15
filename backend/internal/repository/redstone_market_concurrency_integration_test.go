//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/silent-QAQ/redstoneapi/internal/redstone/market"
	"github.com/silent-QAQ/redstoneapi/internal/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// These tests deliberately use the shared PostgreSQL integration harness. The
// constraints below must hold across independent SQL transactions, rather than
// only across mocked calls inside one process.
func TestRedstoneMarketCreateOrderConcurrentInventoryHasOneWinner(t *testing.T) {
	fixture := newMarketConcurrencyFixture(t, "file", 1, "10.00000000", "20.00000000", "20.00000000")
	repository := fixture.repository

	start := make(chan struct{})
	errs := make(chan error, len(fixture.buyers))
	var wg sync.WaitGroup
	for index, buyer := range fixture.buyers {
		wg.Add(1)
		go func(index int, buyerID int64) {
			defer wg.Done()
			<-start
			_, err := repository.CreateOrder(context.Background(), market.CreateOrderRequest{
				BuyerUserID: buyerID,
				ProductID:   fixture.productID,
				IdempotencyKey: fmt.Sprintf("market-inventory-race-%d-%s", index, uuid.NewString()),
			})
			errs <- err
		}(index, buyer.ID)
	}
	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		require.ErrorIs(t, err, market.ErrInventoryUnavailable)
	}
	require.Equal(t, 1, successes)

	var reserved, total int
	var status string
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `
		SELECT inventory_reserved, inventory_total, status
		FROM redstone_market_products WHERE id = $1
	`, fixture.productID).Scan(&reserved, &total, &status))
	require.Equal(t, 1, reserved)
	require.Equal(t, 1, total)
	require.Equal(t, "sold_out", status)
	require.Equal(t, 1, fixture.count(`SELECT COUNT(*) FROM redstone_market_orders WHERE product_id = $1`, fixture.productID))
	require.Equal(t, 1, fixture.count(`SELECT COUNT(*) FROM redstone_market_delivery_items WHERE product_id = $1 AND status = 'reserved'`, fixture.productID))
	for _, buyer := range fixture.buyers {
		require.LessOrEqual(t, fixture.count(`SELECT COUNT(*) FROM redstone_wallet_ledger WHERE user_id = $1 AND reference_type = 'marketplace_order'`, buyer.ID), 1)
	}

	var debitedBuyers int
	for _, buyer := range fixture.buyers {
		var balance decimal.Decimal
		require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT balance FROM users WHERE id = $1", buyer.ID).Scan(&balance))
		if balance.Equal(decimal.RequireFromString("10.00000000")) {
			debitedBuyers++
		} else {
			require.True(t, balance.Equal(decimal.RequireFromString("20.00000000")))
		}
	}
	require.Equal(t, 1, debitedBuyers)
}

func TestRedstoneMarketCreateOrderInsufficientBalanceRollsBackInventory(t *testing.T) {
	fixture := newMarketConcurrencyFixture(t, "file", 1, "10.00000000", "9.00000000")
	buyer := fixture.buyers[0]

	_, err := fixture.repository.CreateOrder(context.Background(), market.CreateOrderRequest{
		BuyerUserID: buyer.ID,
		ProductID:   fixture.productID,
		IdempotencyKey: "market-insufficient-funds-" + uuid.NewString(),
	})
	require.ErrorIs(t, err, market.ErrInsufficientNormalFunds)

	var reserved int
	var productStatus, deliveryStatus string
	var balance decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT inventory_reserved, status FROM redstone_market_products WHERE id = $1", fixture.productID).Scan(&reserved, &productStatus))
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT status FROM redstone_market_delivery_items WHERE id = $1", fixture.deliveryItemID).Scan(&deliveryStatus))
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT balance FROM users WHERE id = $1", buyer.ID).Scan(&balance))
	require.Zero(t, reserved)
	require.Equal(t, "active", productStatus)
	require.Equal(t, "available", deliveryStatus)
	require.True(t, balance.Equal(decimal.RequireFromString("9.00000000")))
	require.Zero(t, fixture.count(`SELECT COUNT(*) FROM redstone_market_orders WHERE product_id = $1`, fixture.productID))
	require.Zero(t, fixture.count(`SELECT COUNT(*) FROM redstone_wallet_ledger WHERE user_id = $1`, buyer.ID))
	require.Zero(t, fixture.count(`SELECT COUNT(*) FROM redstone_wallet_operations WHERE user_id = $1`, buyer.ID))
}

func TestRedstoneMarketClaimFileDeliveryConcurrentRequestsHaveOneWinner(t *testing.T) {
	fixture := newMarketConcurrencyFixture(t, "file", 1, "10.00000000", "20.00000000")
	buyer := fixture.buyers[0]
	order := fixture.createOrder(t, buyer.ID, "market-file-race-order-")
	files, ok := fixture.repository.(market.FileDeliveryRepository)
	require.True(t, ok)

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for index := 0; index < 2; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			_, err := files.ClaimFileDelivery(context.Background(), buyer.ID, order.ID, fmt.Sprintf("market-file-race-%d", index))
			errs <- err
		}(index)
	}
	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		require.ErrorIs(t, err, market.ErrDeliveryAlreadyViewed)
	}
	require.Equal(t, 1, successes)

	var orderStatus, itemStatus string
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT status FROM redstone_market_orders WHERE id = $1", order.ID).Scan(&orderStatus))
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT status FROM redstone_market_delivery_items WHERE id = $1", order.DeliveryItemID).Scan(&itemStatus))
	require.Equal(t, "delivered", orderStatus)
	require.Equal(t, "delivered", itemStatus)
	require.Equal(t, 1, fixture.count(`SELECT COUNT(*) FROM redstone_market_delivery_audit WHERE order_id = $1 AND event_type = 'downloaded'`, order.ID))
}

func TestRedstoneMarketAppealAndAutomaticSettlementRacePreservesSingleTerminalOutcome(t *testing.T) {
	fixture := newMarketConcurrencyFixture(t, "file", 1, "10.00000000", "20.00000000")
	buyer := fixture.buyers[0]
	order := fixture.createOrder(t, buyer.ID, "market-appeal-settlement-order-")
	files, ok := fixture.repository.(market.FileDeliveryRepository)
	require.True(t, ok)
	_, err := files.ClaimFileDelivery(context.Background(), buyer.ID, order.ID, "market-appeal-settlement-delivery")
	require.NoError(t, err)

	now := time.Now().UTC()
	_, err = integrationDB.ExecContext(context.Background(), `
		UPDATE redstone_market_orders
		SET settlement_due_at = $2, delivered_at = $3
		WHERE id = $1
	`, order.ID, now.Add(-time.Minute), now.Add(-25*time.Hour))
	require.NoError(t, err)
	settlement, ok := fixture.repository.(market.SettlementRepository)
	require.True(t, ok)

	start := make(chan struct{})
	var appealErr, settleErr error
	var batch market.SettlementBatchResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, appealErr = settlement.CreateAppeal(context.Background(), market.CreateAppealRequest{
			BuyerUserID: buyer.ID,
			OrderID:     order.ID,
			Reason:      "delivery dispute racing settlement",
		})
	}()
	go func() {
		defer wg.Done()
		<-start
		batch, settleErr = settlement.SettleDueOrders(context.Background(), now, 1)
	}()
	close(start)
	wg.Wait()
	require.NoError(t, settleErr)

	var status string
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT status FROM redstone_market_orders WHERE id = $1", order.ID).Scan(&status))
	financialEvents := fixture.count(`SELECT COUNT(*) FROM redstone_market_financial_events WHERE order_id = $1 AND action = 'settlement'`, order.ID)
	appeals := fixture.count(`SELECT COUNT(*) FROM redstone_market_appeals WHERE order_id = $1`, order.ID)

	switch status {
	case "appealed":
		require.NoError(t, appealErr)
		require.Zero(t, financialEvents)
		require.Equal(t, 1, appeals)
		require.Zero(t, batch.Processed)
	case "settled":
		require.ErrorIs(t, appealErr, market.ErrAppealNotAllowed)
		require.Equal(t, 1, financialEvents)
		require.Zero(t, appeals)
		require.Equal(t, 1, batch.Processed)
	default:
		t.Fatalf("unexpected terminal order status %q", status)
	}
}

type marketConcurrencyFixture struct {
	t             *testing.T
	repository    market.Repository
	productID     int64
	deliveryItemID int64
	seller        *service.User
	buyers        []*service.User
}

func newMarketConcurrencyFixture(t *testing.T, productType string, inventory int, price string, buyerBalances ...string) *marketConcurrencyFixture {
	t.Helper()
	ctx := context.Background()
	repository, err := market.NewPostgresRepository(integrationDB)
	require.NoError(t, err)

	fixture := &marketConcurrencyFixture{t: t, repository: repository}
	fixture.seller = mustCreateUser(t, integrationEntClient, &service.User{Email: "market-seller-" + uuid.NewString() + "@example.test", Balance: 0})
	for _, balance := range buyerBalances {
		fixture.buyers = append(fixture.buyers, mustCreateUser(t, integrationEntClient, &service.User{
			Email: "market-buyer-" + uuid.NewString() + "@example.test", Balance: decimal.RequireFromString(balance).InexactFloat64(),
		}))
	}

	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO redstone_market_products (
			seller_user_id, seller_kind, product_type, title, description, unit_price,
			inventory_total, inventory_reserved, status, risk_status, published_at
		) VALUES ($1, 'user', $2, 'PostgreSQL concurrency fixture', '', $3, $4, 0, 'active', 'passed', NOW())
		RETURNING id
	`, fixture.seller.ID, productType, decimal.RequireFromString(price), inventory).Scan(&fixture.productID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO redstone_market_delivery_items (
			product_id, ordinal, status, encrypted_object_key, key_version, wrapped_dek,
			content_sha256, content_type, byte_size
		) VALUES ($1, 0, 'available', $2, 'test-kek-v1', $3, $4, 'application/octet-stream', 1)
		RETURNING id
	`, fixture.productID, "market-test/"+uuid.NewString(), []byte{1}, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa").Scan(&fixture.deliveryItemID))
	t.Cleanup(fixture.cleanup)
	return fixture
}

func (f *marketConcurrencyFixture) createOrder(t *testing.T, buyerID int64, keyPrefix string) market.Order {
	t.Helper()
	result, err := f.repository.CreateOrder(context.Background(), market.CreateOrderRequest{
		BuyerUserID: buyerID,
		ProductID:   f.productID,
		IdempotencyKey: keyPrefix + uuid.NewString(),
	})
	require.NoError(t, err)
	return result.Order
}

func (f *marketConcurrencyFixture) count(query string, args ...any) int {
	f.t.Helper()
	var count int
	require.NoError(f.t, integrationDB.QueryRowContext(context.Background(), query, args...).Scan(&count))
	return count
}

func (f *marketConcurrencyFixture) cleanup() {
	ctx := context.Background()
	productID := f.productID
	for _, query := range []string{
		`DELETE FROM redstone_market_delivery_audit WHERE order_id IN (SELECT id FROM redstone_market_orders WHERE product_id = $1)`,
		`DELETE FROM redstone_market_financial_events WHERE order_id IN (SELECT id FROM redstone_market_orders WHERE product_id = $1)`,
		`DELETE FROM redstone_market_appeals WHERE order_id IN (SELECT id FROM redstone_market_orders WHERE product_id = $1)`,
		`DELETE FROM redstone_market_order_holds WHERE order_id IN (SELECT id FROM redstone_market_orders WHERE product_id = $1)`,
		`DELETE FROM redstone_market_orders WHERE product_id = $1`,
		`DELETE FROM redstone_market_delivery_scan_jobs WHERE product_id = $1`,
		`DELETE FROM redstone_market_delivery_items WHERE product_id = $1`,
		`DELETE FROM redstone_market_products WHERE id = $1`,
	} {
		_, _ = integrationDB.ExecContext(ctx, query, productID)
	}
	users := append([]*service.User{f.seller}, f.buyers...)
	for _, user := range users {
		if user == nil {
			continue
		}
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM redstone_wallet_ledger WHERE user_id = $1", user.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM redstone_wallet_operations WHERE user_id = $1", user.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM users WHERE id = $1", user.ID)
	}
}
