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

	"github.com/silent-QAQ/redstoneapi/internal/redstone/wallet"
	"github.com/shopspring/decimal"
)

const (
	marketSettlementOperationPrefix = "market-settlement-"
	marketRefundOperationPrefix     = "market-refund-"
	marketFinancialReferenceType    = "marketplace_order"
)

var (
	ErrInvalidOrderID        = errors.New("market order id must be positive")
	ErrInvalidActor          = errors.New("market actor user id must be positive")
	ErrAppealReasonRequired  = errors.New("market appeal reason is required")
	ErrAppealExists          = errors.New("market order already has an appeal")
	ErrAppealNotAllowed      = errors.New("market order cannot be appealed in its current state")
	ErrAppealForbidden       = errors.New("market appeal does not belong to the buyer")
	ErrAppealNotOpen         = errors.New("market appeal is not open")
	ErrSettlementNotAllowed  = errors.New("market order cannot be settled in its current state")
	ErrSettlementNotDue      = errors.New("market order settlement window has not elapsed")
	ErrRefundNotAllowed      = errors.New("market order cannot be refunded in its current state")
	ErrSettlementIncomplete  = errors.New("market settlement receipt has no financial event")
	ErrSettlementUnavailable = errors.New("market settlement repository is unavailable")
)

// Appeal is the buyer-visible portion of a marketplace dispute. Sensitive
// delivery content is intentionally not included.
type Appeal struct {
	ID               int64      `json:"id"`
	OrderID          int64      `json:"order_id"`
	BuyerUserID      int64      `json:"buyer_user_id"`
	Status           string     `json:"status"`
	Reason           string     `json:"reason"`
	ResolutionNote   string     `json:"resolution_note"`
	ResolvedByUserID *int64     `json:"resolved_by_user_id,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	ResolvedAt       *time.Time `json:"resolved_at,omitempty"`
}

type CreateAppealRequest struct {
	BuyerUserID int64
	OrderID     int64
	Reason      string
}

func (r CreateAppealRequest) Validate() error {
	if r.BuyerUserID <= 0 {
		return ErrInvalidBuyer
	}
	if r.OrderID <= 0 {
		return ErrInvalidOrderID
	}
	r.Reason = strings.TrimSpace(r.Reason)
	if r.Reason == "" || len(r.Reason) > 4000 {
		return ErrAppealReasonRequired
	}
	return nil
}

type MarkDeliveredRequest struct {
	OrderID int64
}

func (r MarkDeliveredRequest) Validate() error {
	if r.OrderID <= 0 {
		return ErrInvalidOrderID
	}
	return nil
}

type ResolveAppealAction string

const (
	ResolveAppealRefund  ResolveAppealAction = "refund"
	ResolveAppealRelease ResolveAppealAction = "release"
)

type ResolveAppealRequest struct {
	ActorUserID int64
	OrderID     int64
	Action      ResolveAppealAction
	Note        string
}

func (r ResolveAppealRequest) Validate() error {
	if r.ActorUserID <= 0 {
		return ErrInvalidActor
	}
	if r.OrderID <= 0 {
		return ErrInvalidOrderID
	}
	if r.Action != ResolveAppealRefund && r.Action != ResolveAppealRelease {
		return errors.New("market appeal resolution action is invalid")
	}
	if len(strings.TrimSpace(r.Note)) > 4000 {
		return errors.New("market appeal resolution note is too long")
	}
	return nil
}

type AdminOrderRequest struct {
	ActorUserID int64
	OrderID     int64
}

func (r AdminOrderRequest) Validate() error {
	if r.ActorUserID <= 0 {
		return ErrInvalidActor
	}
	if r.OrderID <= 0 {
		return ErrInvalidOrderID
	}
	return nil
}

type FinancialAction string

const (
	FinancialSettlement FinancialAction = "settlement"
	FinancialRefund     FinancialAction = "refund"
)

type SettlementResult struct {
	OrderID         int64           `json:"order_id"`
	Action          FinancialAction `json:"action"`
	RecipientUserID int64           `json:"recipient_user_id"`
	Amount          decimal.Decimal `json:"amount"`
	BalanceAfter    decimal.Decimal `json:"balance_after"`
	Applied         bool            `json:"applied"`
	Replayed        bool            `json:"replayed"`
}

type SettlementBatchResult struct {
	Processed int `json:"processed"`
	Skipped   int `json:"skipped"`
}

// AdminSettlementService is a service-level contract. HTTP registration is
// intentionally kept outside this package's financial transaction boundary.
type AdminSettlementService interface {
	SettleOrder(context.Context, AdminOrderRequest) (SettlementResult, error)
	RefundOrder(context.Context, AdminOrderRequest) (SettlementResult, error)
	ResolveAppeal(context.Context, ResolveAppealRequest) (SettlementResult, error)
	SettleDueOrders(context.Context, time.Time, int) (SettlementBatchResult, error)
}

// SettlementRepository is separate from Repository so existing read/list test
// doubles and integrations remain source-compatible until admin routes land.
type SettlementRepository interface {
	CreateAppeal(context.Context, CreateAppealRequest) (Appeal, error)
	MarkDelivered(context.Context, MarkDeliveredRequest) (Order, error)
	SettleOrder(context.Context, AdminOrderRequest) (SettlementResult, error)
	RefundOrder(context.Context, AdminOrderRequest) (SettlementResult, error)
	ResolveAppeal(context.Context, ResolveAppealRequest) (SettlementResult, error)
	SettleDueOrders(context.Context, time.Time, int) (SettlementBatchResult, error)
}

func (s *Service) settlementRepository() (SettlementRepository, error) {
	if s == nil || s.repository == nil {
		return nil, ErrSettlementUnavailable
	}
	repository, ok := s.repository.(SettlementRepository)
	if !ok {
		return nil, ErrSettlementUnavailable
	}
	return repository, nil
}

func (s *Service) CreateAppeal(ctx context.Context, request CreateAppealRequest) (Appeal, error) {
	if err := request.Validate(); err != nil {
		return Appeal{}, marketApplicationError(err)
	}
	repository, err := s.settlementRepository()
	if err != nil {
		return Appeal{}, marketApplicationError(err)
	}
	appeal, err := repository.CreateAppeal(ctx, request)
	if err != nil {
		return Appeal{}, marketApplicationError(err)
	}
	return appeal, nil
}

func (s *Service) MarkDelivered(ctx context.Context, request MarkDeliveredRequest) (Order, error) {
	if err := request.Validate(); err != nil {
		return Order{}, marketApplicationError(err)
	}
	repository, err := s.settlementRepository()
	if err != nil {
		return Order{}, marketApplicationError(err)
	}
	order, err := repository.MarkDelivered(ctx, request)
	if err != nil {
		return Order{}, marketApplicationError(err)
	}
	return order, nil
}

func (s *Service) SettleOrder(ctx context.Context, request AdminOrderRequest) (SettlementResult, error) {
	if err := request.Validate(); err != nil {
		return SettlementResult{}, marketApplicationError(err)
	}
	repository, err := s.settlementRepository()
	if err != nil {
		return SettlementResult{}, marketApplicationError(err)
	}
	result, err := repository.SettleOrder(ctx, request)
	if err != nil {
		return SettlementResult{}, marketApplicationError(err)
	}
	return result, nil
}

func (s *Service) RefundOrder(ctx context.Context, request AdminOrderRequest) (SettlementResult, error) {
	if err := request.Validate(); err != nil {
		return SettlementResult{}, marketApplicationError(err)
	}
	repository, err := s.settlementRepository()
	if err != nil {
		return SettlementResult{}, marketApplicationError(err)
	}
	result, err := repository.RefundOrder(ctx, request)
	if err != nil {
		return SettlementResult{}, marketApplicationError(err)
	}
	return result, nil
}

func (s *Service) ResolveAppeal(ctx context.Context, request ResolveAppealRequest) (SettlementResult, error) {
	if err := request.Validate(); err != nil {
		return SettlementResult{}, marketApplicationError(err)
	}
	repository, err := s.settlementRepository()
	if err != nil {
		return SettlementResult{}, marketApplicationError(err)
	}
	result, err := repository.ResolveAppeal(ctx, request)
	if err != nil {
		return SettlementResult{}, marketApplicationError(err)
	}
	return result, nil
}

func (s *Service) SettleDueOrders(ctx context.Context, now time.Time, limit int) (SettlementBatchResult, error) {
	if limit <= 0 || limit > 100 {
		return SettlementBatchResult{}, marketApplicationError(ErrInvalidPagination)
	}
	repository, err := s.settlementRepository()
	if err != nil {
		return SettlementBatchResult{}, marketApplicationError(err)
	}
	result, err := repository.SettleDueOrders(ctx, now, limit)
	if err != nil {
		return SettlementBatchResult{}, marketApplicationError(err)
	}
	return result, nil
}

type settlementOrder struct {
	ID              int64
	BuyerUserID     int64
	SellerUserID    int64
	Status          string
	UnitPrice       decimal.Decimal
	SellerNetAmount decimal.Decimal
	SettlementDueAt time.Time
	DeliveredAt     sql.NullTime
}

func (r *sqlRepository) CreateAppeal(ctx context.Context, request CreateAppealRequest) (_ Appeal, err error) {
	if err := request.Validate(); err != nil {
		return Appeal{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Appeal{}, err
	}
	defer func() { _ = tx.Rollback() }()
	order, err := lockSettlementOrder(ctx, tx, request.OrderID)
	if err != nil {
		return Appeal{}, err
	}
	if order.BuyerUserID != request.BuyerUserID {
		return Appeal{}, ErrAppealForbidden
	}
	if order.Status != "paid" && order.Status != "delivered" {
		return Appeal{}, ErrAppealNotAllowed
	}
	// A paid-but-undelivered order remains appealable so a buyer is never left
	// without a remedy. Once delivery occurred, the ordinary appeal window is
	// strictly the 24-hour settlement window; any later review is an explicit
	// administrator action rather than a buyer-side retry race with the worker.
	if order.Status == "delivered" && !order.SettlementDueAt.UTC().After(time.Now().UTC()) {
		return Appeal{}, ErrAppealNotAllowed
	}
	var existingStatus string
	err = tx.QueryRowContext(ctx, `
		SELECT status FROM redstone_market_appeals WHERE order_id = $1 FOR UPDATE
	`, request.OrderID).Scan(&existingStatus)
	if err == nil {
		return Appeal{}, ErrAppealExists
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Appeal{}, err
	}
	now := time.Now().UTC()
	var appeal Appeal
	err = tx.QueryRowContext(ctx, `
		INSERT INTO redstone_market_appeals (
			order_id, buyer_user_id, status, reason, created_at
		) VALUES ($1, $2, 'open', $3, $4)
		RETURNING id, order_id, buyer_user_id, status, reason, resolution_note, resolved_by_user_id, created_at, resolved_at
	`, request.OrderID, request.BuyerUserID, strings.TrimSpace(request.Reason), now).Scan(
		&appeal.ID, &appeal.OrderID, &appeal.BuyerUserID, &appeal.Status, &appeal.Reason,
		&appeal.ResolutionNote, &appeal.ResolvedByUserID, &appeal.CreatedAt, &appeal.ResolvedAt,
	)
	if err != nil {
		return Appeal{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE redstone_market_orders SET status = 'appealed', updated_at = $2
		WHERE id = $1 AND status IN ('paid', 'delivered')
	`, request.OrderID, now)
	if err != nil {
		return Appeal{}, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return Appeal{}, err
		}
		return Appeal{}, ErrAppealNotAllowed
	}
	if err := tx.Commit(); err != nil {
		return Appeal{}, err
	}
	return appeal, nil
}

func (r *sqlRepository) MarkDelivered(ctx context.Context, request MarkDeliveredRequest) (_ Order, err error) {
	if err := request.Validate(); err != nil {
		return Order{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Order{}, err
	}
	defer func() { _ = tx.Rollback() }()
	order, err := lockSettlementOrder(ctx, tx, request.OrderID)
	if err != nil {
		return Order{}, err
	}
	if order.Status == "delivered" {
		if err := tx.Commit(); err != nil {
			return Order{}, err
		}
		return loadOrder(ctx, r.db, request.OrderID)
	}
	if order.Status != "paid" {
		return Order{}, ErrSettlementNotAllowed
	}
	// Delivery itself is implemented by the one-time delivery handler. This
	// method only records the completion edge after that handler has made content
	// available and written its delivery audit row.
	now := time.Now().UTC()
	if err := markDeliveredTx(ctx, tx, request.OrderID, now); err != nil {
		return Order{}, err
	}
	if err := tx.Commit(); err != nil {
		return Order{}, err
	}
	return loadOrder(ctx, r.db, request.OrderID)
}

// markDeliveredTx is the shared state transition used by the one-time
// delivery transaction and the service-level delivery completion operation.
// The caller must already hold the order row lock and must commit/rollback.
func markDeliveredTx(ctx context.Context, tx *sql.Tx, orderID int64, now time.Time) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE redstone_market_orders
		SET status = 'delivered', delivered_at = $2, settlement_due_at = $3, updated_at = $2
		WHERE id = $1 AND status = 'paid'
	`, orderID, now, now.Add(marketSettlementWindow))
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return err
		}
		return ErrSettlementNotAllowed
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE redstone_market_delivery_items SET status = 'delivered'
		WHERE id = (SELECT delivery_item_id FROM redstone_market_orders WHERE id = $1)
			AND status = 'reserved'
	`, orderID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return err
		}
		return ErrSettlementNotAllowed
	}
	return nil
}

func (r *sqlRepository) SettleOrder(ctx context.Context, request AdminOrderRequest) (SettlementResult, error) {
	if err := request.Validate(); err != nil {
		return SettlementResult{}, err
	}
	return r.applyFinancialAction(ctx, request.OrderID, request.ActorUserID, FinancialSettlement, false, false, "", time.Time{})
}

func (r *sqlRepository) RefundOrder(ctx context.Context, request AdminOrderRequest) (SettlementResult, error) {
	if err := request.Validate(); err != nil {
		return SettlementResult{}, err
	}
	return r.applyFinancialAction(ctx, request.OrderID, request.ActorUserID, FinancialRefund, false, false, "", time.Time{})
}

func (r *sqlRepository) ResolveAppeal(ctx context.Context, request ResolveAppealRequest) (SettlementResult, error) {
	if err := request.Validate(); err != nil {
		return SettlementResult{}, err
	}
	action := FinancialSettlement
	if request.Action == ResolveAppealRefund {
		action = FinancialRefund
	}
	return r.applyFinancialAction(ctx, request.OrderID, request.ActorUserID, action, true, false, strings.TrimSpace(request.Note), time.Time{})
}

func (r *sqlRepository) SettleDueOrders(ctx context.Context, now time.Time, limit int) (SettlementBatchResult, error) {
	if limit <= 0 || limit > 100 {
		return SettlementBatchResult{}, ErrInvalidPagination
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	result := SettlementBatchResult{}
	for range limit {
		processed, skipped, err := r.settleNextDueOrder(ctx, now)
		if err != nil {
			return result, err
		}
		if !processed && !skipped {
			break
		}
		if processed {
			result.Processed++
		} else {
			result.Skipped++
		}
	}
	return result, nil
}

func (r *sqlRepository) applyFinancialAction(ctx context.Context, orderID, actorUserID int64, action FinancialAction, fromAppeal, dueOnly bool, resolutionNote string, actionTime time.Time) (_ SettlementResult, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return SettlementResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := applyFinancialActionTx(ctx, tx, r.wallet, orderID, actorUserID, action, fromAppeal, dueOnly, resolutionNote, actionTime)
	if err != nil {
		return SettlementResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return SettlementResult{}, err
	}
	return result, nil
}

// settleNextDueOrder keeps the candidate lock and financial action in one
// transaction. SKIP LOCKED distributes work across application instances
// without process-local worker ownership.
func (r *sqlRepository) settleNextDueOrder(ctx context.Context, now time.Time) (processed, skipped bool, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var orderID int64
	err = tx.QueryRowContext(ctx, `
		SELECT o.id
		FROM redstone_market_orders o
		WHERE o.status = 'delivered'
		  AND o.delivered_at IS NOT NULL
		  AND o.settlement_due_at < $1::timestamptz
		  AND NOT EXISTS (
			SELECT 1 FROM redstone_market_order_holds h
			WHERE h.order_id = o.id
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM redstone_market_appeals a
			WHERE a.order_id = o.id
		  )
		ORDER BY o.delivered_at, o.id
		FOR UPDATE OF o SKIP LOCKED
		LIMIT 1
	`, now).Scan(&orderID)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return false, false, err
		}
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	result, err := applyFinancialActionTx(ctx, tx, r.wallet, orderID, 0, FinancialSettlement, false, true, "", now)
	if errors.Is(err, ErrSettlementNotAllowed) || errors.Is(err, ErrSettlementNotDue) || errors.Is(err, ErrAppealExists) || errors.Is(err, ErrSettlementFrozen) {
		if err := tx.Commit(); err != nil {
			return false, false, err
		}
		return false, true, nil
	}
	if err != nil {
		return false, false, err
	}
	if err := tx.Commit(); err != nil {
		return false, false, err
	}
	return result.Applied || result.Replayed, false, nil
}

// applyFinancialActionTx contains the shared financial state machine. Its
// callers retain the order lock until the same transaction commits.
func applyFinancialActionTx(ctx context.Context, tx *sql.Tx, walletService *wallet.Service, orderID, actorUserID int64, action FinancialAction, fromAppeal, dueOnly bool, resolutionNote string, actionTime time.Time) (_ SettlementResult, err error) {
	if walletService == nil {
		return SettlementResult{}, ErrSettlementUnavailable
	}
	order, err := lockSettlementOrder(ctx, tx, orderID)
	if err != nil {
		return SettlementResult{}, err
	}
	// The financial event is an additional durable idempotency receipt. Check
	// it before evaluating the current state so retrying a completed command
	// remains a success after the order advanced to settled/refunded.
	existingEvent, eventErr := findFinancialEvent(ctx, tx, orderID, action)
	if eventErr == nil {
		expectedStatus := "settled"
		if action == FinancialRefund {
			expectedStatus = "refunded"
		}
		if order.Status != expectedStatus {
			return SettlementResult{}, ErrSettlementIncomplete
		}
		return SettlementResult{OrderID: orderID, Action: action, RecipientUserID: existingEvent.RecipientUserID, Amount: existingEvent.Amount, Applied: false, Replayed: true}, nil
	}
	if !errors.Is(eventErr, ErrSettlementIncomplete) {
		return SettlementResult{}, eventErr
	}
	if action == FinancialSettlement {
		held, holdErr := isOrderHeldTx(ctx, tx, orderID)
		if holdErr != nil {
			return SettlementResult{}, holdErr
		}
		if held {
			return SettlementResult{}, ErrSettlementFrozen
		}
		if fromAppeal {
			if order.Status != "appealed" {
				return SettlementResult{}, ErrSettlementNotAllowed
			}
		} else if order.Status != "delivered" {
			return SettlementResult{}, ErrSettlementNotAllowed
		}
		if !order.DeliveredAt.Valid {
			// The initial checkout flow predates one-time delivery and leaves an
			// order in paid until delivery is recorded. A manual administrator
			// release or appeal resolution remains compatible with that legacy
			// state, but the automatic worker must never settle it because no
			// verified delivery time exists.
			if dueOnly {
				return SettlementResult{}, ErrSettlementNotDue
			}
		}
		if dueOnly && !order.SettlementDueAt.UTC().Before(actionTime.UTC()) {
			return SettlementResult{}, ErrSettlementNotDue
		}
	} else {
		if fromAppeal {
			if order.Status != "appealed" {
				return SettlementResult{}, ErrRefundNotAllowed
			}
		} else if order.Status != "paid" && order.Status != "delivered" {
			return SettlementResult{}, ErrRefundNotAllowed
		}
	}
	if fromAppeal {
		var appealStatus string
		if err := tx.QueryRowContext(ctx, `
			SELECT status FROM redstone_market_appeals WHERE order_id = $1 FOR UPDATE
		`, orderID).Scan(&appealStatus); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return SettlementResult{}, ErrAppealNotOpen
			}
			return SettlementResult{}, err
		}
		if appealStatus != "open" {
			return SettlementResult{}, ErrAppealNotOpen
		}
	}
	if !fromAppeal && action == FinancialSettlement {
		var hasAppeal bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM redstone_market_appeals WHERE order_id = $1)`, orderID).Scan(&hasAppeal); err != nil {
			return SettlementResult{}, err
		}
		if hasAppeal {
			return SettlementResult{}, ErrAppealExists
		}
	}
	recipientID := order.SellerUserID
	amount := order.SellerNetAmount
	reason := wallet.CreditSettlement
	keyPrefix := marketSettlementOperationPrefix
	newStatus := "settled"
	if action == FinancialRefund {
		recipientID = order.BuyerUserID
		amount = order.UnitPrice
		reason = wallet.CreditRefund
		keyPrefix = marketRefundOperationPrefix
		newStatus = "refunded"
	}
	if !amount.IsPositive() || !amount.Equal(amount.Round(wallet.MonetaryScale)) {
		return SettlementResult{}, fmt.Errorf("market financial amount is invalid")
	}
	key := keyPrefix + strconv.FormatInt(orderID, 10)
	credit, err := walletService.CreditInExecutor(ctx, tx, wallet.CreditRequest{
		UserID: recipientID,
		Asset:  wallet.AssetNormal,
		Amount: amount,
		Reason: reason,
		Reference: wallet.Reference{
			Type: marketFinancialReferenceType,
			ID:   strconv.FormatInt(orderID, 10),
		},
		IdempotencyKey: key,
	})
	if err != nil {
		return SettlementResult{}, err
	}
	if !credit.Applied {
		return SettlementResult{}, ErrSettlementIncomplete
	}
	newBalance := credit.BalanceAfter
	now := time.Now().UTC()
	if !actionTime.IsZero() {
		now = actionTime.UTC()
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO redstone_market_financial_events (
			order_id, action, recipient_user_id, amount, wallet_operation_key, actor_user_id, created_at
		) VALUES ($1, $2, $3, $4, $5, NULLIF($6, 0), $7)
	`, orderID, action, recipientID, amount, key, actorUserID, now); err != nil {
		return SettlementResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE redstone_market_orders
		SET status = $2, settled_at = CASE WHEN $3 = 'settled' THEN $4 ELSE settled_at END,
			refunded_at = CASE WHEN $3 = 'refunded' THEN $4 ELSE refunded_at END,
			updated_at = $4
		WHERE id = $1
	`, orderID, newStatus, string(newStatus), now); err != nil {
		return SettlementResult{}, err
	}
	if action == FinancialRefund {
		// A refunded credential/file/account reference must never become
		// deliverable again. Inventory remains reserved because delivered
		// secrets are not safe to put back on sale.
		if _, err := tx.ExecContext(ctx, `
			UPDATE redstone_market_delivery_items
			SET status = 'revoked'
			WHERE id = (SELECT delivery_item_id FROM redstone_market_orders WHERE id = $1)
				AND status IN ('reserved', 'delivered')
		`, orderID); err != nil {
			return SettlementResult{}, err
		}
		if err := releaseAccountEscrowForOrder(ctx, tx, orderID, order.SellerUserID, order.BuyerUserID); err != nil {
			return SettlementResult{}, err
		}
	}
	if fromAppeal {
		appealStatus := "resolved_release"
		if action == FinancialRefund {
			appealStatus = "resolved_refund"
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE redstone_market_appeals
			SET status = $2, resolution_note = $3, resolved_by_user_id = NULLIF($4, 0), resolved_at = $5
			WHERE order_id = $1 AND status = 'open'
		`, orderID, appealStatus, resolutionNote, actorUserID, now); err != nil {
			return SettlementResult{}, err
		}
	}
	return SettlementResult{OrderID: orderID, Action: action, RecipientUserID: recipientID, Amount: amount, BalanceAfter: newBalance, Applied: true}, nil
}

func lockSettlementOrder(ctx context.Context, tx *sql.Tx, orderID int64) (settlementOrder, error) {
	var order settlementOrder
	err := tx.QueryRowContext(ctx, `
		SELECT id, buyer_user_id, seller_user_id, status, unit_price, seller_net_amount,
			settlement_due_at, delivered_at
		FROM redstone_market_orders WHERE id = $1 FOR UPDATE
	`, orderID).Scan(&order.ID, &order.BuyerUserID, &order.SellerUserID, &order.Status,
		&order.UnitPrice, &order.SellerNetAmount, &order.SettlementDueAt, &order.DeliveredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return settlementOrder{}, ErrInvalidOrderID
	}
	return order, err
}

type financialEvent struct {
	RecipientUserID int64
	Amount          decimal.Decimal
}

func findFinancialEvent(ctx context.Context, tx *sql.Tx, orderID int64, action FinancialAction) (financialEvent, error) {
	var event financialEvent
	err := tx.QueryRowContext(ctx, `
		SELECT recipient_user_id, amount FROM redstone_market_financial_events
		WHERE order_id = $1 AND action = $2 FOR UPDATE
	`, orderID, action).Scan(&event.RecipientUserID, &event.Amount)
	if errors.Is(err, sql.ErrNoRows) {
		return financialEvent{}, ErrSettlementIncomplete
	}
	return event, err
}

// financialActionFingerprint remains a compact pure identifier for focused
// state-machine tests. Wallet mutations derive their own request fingerprints.
func financialActionFingerprint(action FinancialAction, orderID, recipientID int64, amount decimal.Decimal) string {
	value := string(action) + "\x00" + strconv.FormatInt(orderID, 10) + "\x00" + strconv.FormatInt(recipientID, 10) + "\x00" + amount.StringFixed(wallet.MonetaryScale)
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])
}

func loadOrder(ctx context.Context, db *sql.DB, orderID int64) (Order, error) {
	row := db.QueryRowContext(ctx, orderSelect+` WHERE o.id = $1`, orderID)
	return scanOrder(row)
}
