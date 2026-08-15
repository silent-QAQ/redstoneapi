package market

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/silent-QAQ/redstoneapi/internal/pkg/errors"
	"github.com/silent-QAQ/redstoneapi/internal/redstone/wallet"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

const (
	marketOrderReferenceType = "marketplace_order"
	marketOrderIdempotencyOp = wallet.OperationMarketplaceDebit
	marketSettlementWindow   = 24 * time.Hour
)

var (
	ErrInvalidBuyer             = errors.New("market buyer user id must be positive")
	ErrInvalidProductID         = errors.New("market product id must be positive")
	ErrIdempotencyKeyRequired   = errors.New("market idempotency key is required")
	ErrIdempotencyConflict      = errors.New("market idempotency key conflicts with an existing request")
	ErrProductUnavailable       = errors.New("market product is unavailable")
	ErrSelfPurchase             = errors.New("market seller cannot buy its own product")
	ErrInventoryUnavailable     = errors.New("market product inventory is unavailable")
	ErrInsufficientNormalFunds  = errors.New("market normal balance is insufficient")
	ErrInvalidPagination        = errors.New("market pagination is invalid")
	ErrIncompleteIdempotencyLog = errors.New("market idempotency receipt has no completed order")
)

// CreateOrderRequest intentionally contains no amount or delivery item ID.
// Both are selected inside the transaction from the locked product, so a
// client cannot alter a price or reserve a delivery item it does not own.
type CreateOrderRequest struct {
	BuyerUserID    int64
	ProductID      int64
	IdempotencyKey string
}

func (r CreateOrderRequest) Validate() error {
	if r.BuyerUserID <= 0 {
		return ErrInvalidBuyer
	}
	if r.ProductID <= 0 {
		return ErrInvalidProductID
	}
	if !validIdempotencyKey(r.IdempotencyKey) {
		return ErrIdempotencyKeyRequired
	}
	return nil
}

func (r CreateOrderRequest) fingerprint() string {
	value := "market_order\x00" + strconv.FormatInt(r.BuyerUserID, 10) + "\x00" + strconv.FormatInt(r.ProductID, 10)
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])
}

// CreateOrderResult records whether this HTTP-safe command was newly applied
// or was recovered from the immutable wallet operation receipt.
type CreateOrderResult struct {
	Order    Order
	Replayed bool
}

// Order is the buyer-safe order projection. It deliberately excludes encrypted
// object keys, wrapping data, and the wallet idempotency key.
type Order struct {
	ID               int64           `json:"id"`
	OrderNo          string          `json:"order_no"`
	BuyerUserID      int64           `json:"buyer_user_id"`
	SellerUserID     int64           `json:"seller_user_id"`
	ProductID        int64           `json:"product_id"`
	ProductTitle     string          `json:"product_title"`
	DeliveryItemID   int64           `json:"delivery_item_id"`
	Status           string          `json:"status"`
	UnitPrice        decimal.Decimal `json:"unit_price"`
	ServiceFeeRate   decimal.Decimal `json:"service_fee_rate"`
	ServiceFeeAmount decimal.Decimal `json:"service_fee_amount"`
	SellerNetAmount  decimal.Decimal `json:"seller_net_amount"`
	SettlementDueAt  time.Time       `json:"settlement_due_at"`
	DeliveredAt      *time.Time      `json:"delivered_at,omitempty"`
	SettledAt        *time.Time      `json:"settled_at,omitempty"`
	RefundedAt       *time.Time      `json:"refunded_at,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
}

// ValidatePurchasableProduct is a pure policy check shared by the locked SQL
// path and unit tests. Inventory is rechecked by conditional SQL updates.
func ValidatePurchasableProduct(buyerUserID int64, product Product) error {
	if buyerUserID <= 0 {
		return ErrInvalidBuyer
	}
	// A sold-out product is still a valid listing snapshot; checkout should
	// report the actionable inventory conflict instead of disguising a
	// concurrent purchase as a missing product.
	if product.InventoryReserved >= product.InventoryTotal &&
		(product.Status == "active" || product.Status == "sold_out") {
		return ErrInventoryUnavailable
	}
	if product.ID <= 0 || product.Status != "active" || product.RiskStatus != "passed" ||
		!product.SellerKind.Valid() || product.SellerUserID <= 0 || !product.UnitPrice.IsPositive() ||
		!product.UnitPrice.Equal(product.UnitPrice.Round(wallet.MonetaryScale)) {
		return ErrProductUnavailable
	}
	if product.SellerUserID == buyerUserID {
		return ErrSelfPurchase
	}
	return nil
}

func settlementDueAt(createdAt time.Time) time.Time {
	return createdAt.UTC().Add(marketSettlementWindow)
}

func validIdempotencyKey(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 128
}

func marketApplicationError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*infraerrors.ApplicationError); ok {
		return err
	}
	switch {
	case errors.Is(err, ErrInvalidBuyer),
		errors.Is(err, ErrInvalidProductID),
		errors.Is(err, ErrIdempotencyKeyRequired),
		errors.Is(err, ErrInvalidPagination),
		errors.Is(err, ErrInvalidFeeRate),
		errors.Is(err, ErrFeePolicyReason):
		return infraerrors.BadRequest("MARKET_INVALID_REQUEST", err.Error()).WithCause(err)
	case errors.Is(err, ErrSelfPurchase):
		return infraerrors.Forbidden("MARKET_SELF_PURCHASE", "Sellers cannot purchase their own products").WithCause(err)
	case errors.Is(err, ErrProductUnavailable):
		return infraerrors.NotFound("MARKET_PRODUCT_UNAVAILABLE", "Product is unavailable").WithCause(err)
	case errors.Is(err, ErrInventoryUnavailable):
		return infraerrors.Conflict("MARKET_INVENTORY_UNAVAILABLE", "Product inventory is unavailable").WithCause(err)
	case errors.Is(err, ErrInsufficientNormalFunds):
		return infraerrors.Conflict("MARKET_INSUFFICIENT_NORMAL_BALANCE", "Insufficient normal balance").WithCause(err)
	case errors.Is(err, ErrIdempotencyConflict):
		return infraerrors.Conflict("MARKET_IDEMPOTENCY_CONFLICT", "Idempotency key was used for a different request").WithCause(err)
	case errors.Is(err, ErrAppealForbidden):
		return infraerrors.Forbidden("MARKET_APPEAL_FORBIDDEN", "Only the buyer can appeal this order").WithCause(err)
	case errors.Is(err, ErrAppealExists), errors.Is(err, ErrAppealNotOpen), errors.Is(err, ErrAppealNotAllowed),
		errors.Is(err, ErrSettlementNotAllowed), errors.Is(err, ErrSettlementNotDue), errors.Is(err, ErrRefundNotAllowed),
		errors.Is(err, ErrSettlementFrozen), errors.Is(err, ErrReversalNotAllowed):
		return infraerrors.Conflict("MARKET_SETTLEMENT_STATE_CONFLICT", "Marketplace order is not in a compatible state").WithCause(err)
	case errors.Is(err, ErrReportExists):
		return infraerrors.Conflict("MARKET_REPORT_EXISTS", "A report for this product already exists").WithCause(err)
	case errors.Is(err, ErrReportForbidden):
		return infraerrors.Forbidden("MARKET_REPORT_FORBIDDEN", "This product cannot be reported by the current user").WithCause(err)
	case errors.Is(err, ErrReversalInsufficientSellerFund):
		return infraerrors.Conflict("MARKET_REVERSAL_INSUFFICIENT_FUNDS", "Seller balance is insufficient for reversal").WithCause(err)
	case errors.Is(err, ErrInvalidReportID), errors.Is(err, ErrReportReasonRequired), errors.Is(err, ErrInvalidReportResolution):
		return infraerrors.BadRequest("MARKET_GOVERNANCE_INVALID_REQUEST", "Invalid marketplace governance request").WithCause(err)
	case errors.Is(err, ErrReportNotFound):
		return infraerrors.NotFound("MARKET_REPORT_NOT_FOUND", "Marketplace report was not found").WithCause(err)
	case errors.Is(err, ErrReportNotOpen):
		return infraerrors.Conflict("MARKET_REPORT_STATE_CONFLICT", "Marketplace report is not open").WithCause(err)
	case errors.Is(err, ErrAppealReasonRequired), errors.Is(err, ErrInvalidOrderID), errors.Is(err, ErrInvalidActor):
		return infraerrors.BadRequest("MARKET_SETTLEMENT_INVALID_REQUEST", "Invalid marketplace settlement request").WithCause(err)
	case errors.Is(err, ErrDeliveryForbidden):
		return infraerrors.Forbidden("MARKET_DELIVERY_FORBIDDEN", "Only the buyer can access this delivery").WithCause(err)
	case errors.Is(err, ErrDeliveryAlreadyViewed):
		return infraerrors.Conflict("MARKET_DELIVERY_ALREADY_VIEWED", "This delivery can only be viewed once").WithCause(err)
	case errors.Is(err, ErrDeliveryNotAllowed), errors.Is(err, ErrDeliveryItemUnavailable):
		return infraerrors.Conflict("MARKET_DELIVERY_UNAVAILABLE", "Delivery is unavailable").WithCause(err)
	case errors.Is(err, ErrDeliveryResolverMissing),
		errors.Is(err, ErrDeliveryContentIntegrity),
		errors.Is(err, ErrDeliveryKeyVersion),
		errors.Is(err, ErrEnvelopeCiphertext),
		errors.Is(err, ErrEnvelopeWrappedKey),
		errors.Is(err, ErrPrivateObjectNotFound),
		errors.Is(err, ErrPrivateObjectStoreNil),
		errors.Is(err, ErrEncryptedResolverNil):
		return infraerrors.ServiceUnavailable("MARKET_DELIVERY_UNAVAILABLE", "Delivery content is not available yet").WithCause(err)
	case errors.Is(err, ErrDeliveryUnavailable):
		return infraerrors.ServiceUnavailable("MARKET_DELIVERY_UNAVAILABLE", "Delivery content is not available yet").WithCause(err)
	case errors.Is(err, ErrFeePolicyUnavailable):
		return infraerrors.ServiceUnavailable("MARKET_FEE_POLICY_UNAVAILABLE", "Marketplace fee policy is not available").WithCause(err)
	case errors.Is(err, ErrContentModerationUnavailable):
		return infraerrors.ServiceUnavailable("MARKET_CONTENT_MODERATION_UNAVAILABLE", "Marketplace content moderation is not available").WithCause(err)
	case errors.Is(err, ErrContentModerationRejected):
		return infraerrors.Conflict("MARKET_CONTENT_REJECTED", "Marketplace content was rejected").WithCause(err)
	case errors.Is(err, ErrInvalidContentReviewID), errors.Is(err, ErrInvalidContentReviewAction):
		return infraerrors.BadRequest("MARKET_CONTENT_REVIEW_INVALID_REQUEST", "Invalid marketplace content review request").WithCause(err)
	case errors.Is(err, ErrContentReviewNotFound):
		return infraerrors.NotFound("MARKET_CONTENT_REVIEW_NOT_FOUND", "Marketplace content review was not found").WithCause(err)
	case errors.Is(err, ErrContentReviewNotOpen):
		return infraerrors.Conflict("MARKET_CONTENT_REVIEW_STATE_CONFLICT", "Marketplace content review is not open").WithCause(err)
	default:
		return infraerrors.InternalServer("MARKET_OPERATION_FAILED", "Marketplace operation failed").WithCause(err)
	}
}

// CreateOrder executes every payment-side mutation in one PostgreSQL
// transaction. The marketplace owns inventory and order state, while the
// wallet service owns the normal-balance debit, idempotency receipt, and
// immutable ledger entry through the same SQL transaction.
func (r *sqlRepository) CreateOrder(ctx context.Context, request CreateOrderRequest) (_ CreateOrderResult, err error) {
	if err := request.Validate(); err != nil {
		return CreateOrderResult{}, err
	}
	if r.wallet == nil {
		return CreateOrderResult{}, ErrMarketplaceUnavailable
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return CreateOrderResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	replayed, err := acquireMarketWalletOperation(ctx, tx, request)
	if err != nil {
		return CreateOrderResult{}, err
	}
	if replayed {
		order, err := findReplayedOrder(ctx, tx, request)
		if err != nil {
			return CreateOrderResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return CreateOrderResult{}, err
		}
		return CreateOrderResult{Order: order, Replayed: true}, nil
	}

	product, err := lockProduct(ctx, tx, request.ProductID)
	if err != nil {
		return CreateOrderResult{}, err
	}
	if err := ValidatePurchasableProduct(request.BuyerUserID, product); err != nil {
		return CreateOrderResult{}, err
	}

	deliveryItemID, err := lockAvailableDeliveryItem(ctx, tx, product.ID)
	if err != nil {
		return CreateOrderResult{}, err
	}
	feeRate := decimal.Zero
	if product.SellerKind == SellerUser {
		feeRate, err = r.lockUserServiceFeeRate(ctx, tx)
		if err != nil {
			return CreateOrderResult{}, err
		}
	}
	amounts, err := CalculateOrderAmounts(product.UnitPrice, product.SellerKind, feeRate)
	if err != nil {
		return CreateOrderResult{}, err
	}

	now := time.Now().UTC()
	order, err := insertOrder(ctx, tx, request, product, deliveryItemID, amounts, now)
	if err != nil {
		return CreateOrderResult{}, err
	}
	if err := reserveInventory(ctx, tx, product.ID); err != nil {
		return CreateOrderResult{}, err
	}
	if err := reserveDeliveryItem(ctx, tx, deliveryItemID); err != nil {
		return CreateOrderResult{}, err
	}
	if product.ProductType == "account_reference" {
		if err := reserveAccountEscrowForDelivery(ctx, tx, deliveryItemID); err != nil {
			return CreateOrderResult{}, err
		}
	}
	_, err = r.wallet.DebitMarketplaceInExecutor(ctx, tx, wallet.MarketplaceDebitRequest{
		UserID: request.BuyerUserID,
		Amount: amounts.Price,
		Reference: wallet.Reference{
			Type: marketOrderReferenceType,
			ID:   strconv.FormatInt(order.ID, 10),
		},
		IdempotencyKey: marketWalletDebitKey(request.IdempotencyKey),
	})
	if errors.Is(err, wallet.ErrInsufficientFunds) {
		return CreateOrderResult{}, ErrInsufficientNormalFunds
	}
	if err != nil {
		return CreateOrderResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CreateOrderResult{}, err
	}
	return CreateOrderResult{Order: order}, nil
}

func marketWalletDebitKey(orderKey string) string {
	const prefix = "market-debit-"
	if len(prefix)+len(orderKey) <= 128 {
		return prefix + orderKey
	}
	sum := sha256.Sum256([]byte(orderKey))
	return prefix + fmt.Sprintf("%x", sum[:])
}

func (r *sqlRepository) ListOrdersByBuyer(ctx context.Context, buyerUserID int64, limit, offset int) ([]Order, int, error) {
	if buyerUserID <= 0 {
		return nil, 0, ErrInvalidBuyer
	}
	if limit <= 0 || limit > 100 || offset < 0 {
		return nil, 0, ErrInvalidPagination
	}
	var total int
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM redstone_market_orders WHERE buyer_user_id = $1
	`, buyerUserID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, orderSelect+`
		WHERE o.buyer_user_id = $1
		ORDER BY o.created_at DESC, o.id DESC
		LIMIT $2 OFFSET $3
	`, buyerUserID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	orders := make([]Order, 0)
	for rows.Next() {
		order, err := scanOrder(rows)
		if err != nil {
			return nil, 0, err
		}
		orders = append(orders, order)
	}
	return orders, total, rows.Err()
}

func acquireMarketWalletOperation(ctx context.Context, tx *sql.Tx, request CreateOrderRequest) (bool, error) {
	inserted := false
	err := tx.QueryRowContext(ctx, `
		INSERT INTO redstone_wallet_operations (user_id, operation, idempotency_key, request_fingerprint)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, idempotency_key) DO NOTHING
		RETURNING true
	`, request.BuyerUserID, marketOrderIdempotencyOp, request.IdempotencyKey, request.fingerprint()).Scan(&inserted)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		inserted = false
	}

	var operation wallet.LedgerOperation
	var fingerprint string
	if err := tx.QueryRowContext(ctx, `
		SELECT operation, request_fingerprint
		FROM redstone_wallet_operations
		WHERE user_id = $1 AND idempotency_key = $2
		FOR UPDATE
	`, request.BuyerUserID, request.IdempotencyKey).Scan(&operation, &fingerprint); err != nil {
		return false, err
	}
	if operation != marketOrderIdempotencyOp || fingerprint != request.fingerprint() {
		return false, ErrIdempotencyConflict
	}
	return !inserted, nil
}

func lockProduct(ctx context.Context, tx *sql.Tx, productID int64) (Product, error) {
	var product Product
	err := tx.QueryRowContext(ctx, `
		SELECT id, seller_user_id, seller_kind, product_type, title, description,
			unit_price, inventory_total, inventory_reserved, status, risk_status, account_id
		FROM redstone_market_products
		WHERE id = $1
		FOR UPDATE
	`, productID).Scan(
		&product.ID, &product.SellerUserID, &product.SellerKind, &product.ProductType, &product.Title, &product.Description,
		&product.UnitPrice, &product.InventoryTotal, &product.InventoryReserved, &product.Status, &product.RiskStatus, &product.AccountID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Product{}, ErrProductUnavailable
	}
	if err != nil {
		return Product{}, err
	}
	return product, nil
}

func lockAvailableDeliveryItem(ctx context.Context, tx *sql.Tx, productID int64) (int64, error) {
	var itemID int64
	err := tx.QueryRowContext(ctx, `
		SELECT id
		FROM redstone_market_delivery_items
		WHERE product_id = $1 AND status = 'available'
		ORDER BY ordinal, id
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`, productID).Scan(&itemID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrInventoryUnavailable
	}
	if err != nil {
		return 0, err
	}
	return itemID, nil
}

func insertOrder(ctx context.Context, tx *sql.Tx, request CreateOrderRequest, product Product, deliveryItemID int64, amounts OrderAmounts, now time.Time) (Order, error) {
	order := Order{
		OrderNo:          "mkt_" + uuid.NewString(),
		BuyerUserID:      request.BuyerUserID,
		SellerUserID:     product.SellerUserID,
		ProductID:        product.ID,
		ProductTitle:     product.Title,
		DeliveryItemID:   deliveryItemID,
		Status:           "paid",
		UnitPrice:        amounts.Price,
		ServiceFeeRate:   amounts.FeeRate,
		ServiceFeeAmount: amounts.FeeAmount,
		SellerNetAmount:  amounts.SellerNet,
		SettlementDueAt:  settlementDueAt(now),
		CreatedAt:        now,
	}
	err := tx.QueryRowContext(ctx, `
		INSERT INTO redstone_market_orders (
			order_no, buyer_user_id, seller_user_id, product_id, delivery_item_id, status,
			unit_price, service_fee_rate, service_fee_amount, seller_net_amount, settlement_due_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, 'paid', $6, $7, $8, $9, $10, $11, $11)
		RETURNING id
	`, order.OrderNo, order.BuyerUserID, order.SellerUserID, order.ProductID, order.DeliveryItemID,
		order.UnitPrice, order.ServiceFeeRate, order.ServiceFeeAmount, order.SellerNetAmount, order.SettlementDueAt, now).Scan(&order.ID)
	if err != nil {
		return Order{}, err
	}
	return order, nil
}

func reserveInventory(ctx context.Context, tx *sql.Tx, productID int64) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE redstone_market_products
		SET inventory_reserved = inventory_reserved + 1,
			status = CASE WHEN inventory_reserved + 1 >= inventory_total THEN 'sold_out' ELSE status END,
			updated_at = NOW()
		WHERE id = $1 AND status = 'active' AND risk_status = 'passed'
			AND inventory_reserved < inventory_total
	`, productID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrInventoryUnavailable
	}
	return nil
}

func reserveDeliveryItem(ctx context.Context, tx *sql.Tx, deliveryItemID int64) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE redstone_market_delivery_items
		SET status = 'reserved'
		WHERE id = $1 AND status = 'available'
	`, deliveryItemID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrInventoryUnavailable
	}
	return nil
}

func reserveAccountEscrowForDelivery(ctx context.Context, tx *sql.Tx, deliveryItemID int64) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE redstone_market_account_escrows e
		SET state = 'reserved', updated_at = NOW()
		FROM redstone_market_delivery_items di
		WHERE di.id = $1 AND e.account_id = di.account_id AND e.state = 'listed'
	`, deliveryItemID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	var accountID sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT account_id FROM redstone_market_delivery_items WHERE id = $1`, deliveryItemID).Scan(&accountID); err != nil {
		return err
	}
	if accountID.Valid {
		return ErrAccountReferenceUnavailable
	}
	return nil
}

func findReplayedOrder(ctx context.Context, tx *sql.Tx, request CreateOrderRequest) (Order, error) {
	row := tx.QueryRowContext(ctx, orderSelect+`
		JOIN redstone_wallet_ledger l
			ON l.user_id = o.buyer_user_id
			AND l.reference_type = $3
			AND l.reference_id = o.id::text
		WHERE l.user_id = $1
			AND l.operation = $2
			AND l.idempotency_key = $4
		ORDER BY l.id
		LIMIT 1
	`, request.BuyerUserID, marketOrderIdempotencyOp, marketOrderReferenceType, marketWalletDebitKey(request.IdempotencyKey))
	order, err := scanOrder(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Order{}, ErrIncompleteIdempotencyLog
	}
	if err != nil {
		return Order{}, err
	}
	return order, nil
}

const orderSelect = `
	SELECT o.id, o.order_no, o.buyer_user_id, o.seller_user_id, o.product_id,
		p.title, o.delivery_item_id, o.status, o.unit_price, o.service_fee_rate,
		o.service_fee_amount, o.seller_net_amount, o.settlement_due_at,
		o.delivered_at, o.settled_at, o.refunded_at, o.created_at
	FROM redstone_market_orders o
	JOIN redstone_market_products p ON p.id = o.product_id
`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanOrder(scanner rowScanner) (Order, error) {
	var order Order
	var deliveredAt, settledAt, refundedAt sql.NullTime
	err := scanner.Scan(
		&order.ID, &order.OrderNo, &order.BuyerUserID, &order.SellerUserID, &order.ProductID,
		&order.ProductTitle, &order.DeliveryItemID, &order.Status, &order.UnitPrice, &order.ServiceFeeRate,
		&order.ServiceFeeAmount, &order.SellerNetAmount, &order.SettlementDueAt,
		&deliveredAt, &settledAt, &refundedAt, &order.CreatedAt,
	)
	if err != nil {
		return Order{}, err
	}
	if deliveredAt.Valid {
		value := deliveredAt.Time.UTC()
		order.DeliveredAt = &value
	}
	if settledAt.Valid {
		value := settledAt.Time.UTC()
		order.SettledAt = &value
	}
	if refundedAt.Valid {
		value := refundedAt.Time.UTC()
		order.RefundedAt = &value
	}
	return order, nil
}
