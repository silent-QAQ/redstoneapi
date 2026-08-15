package market

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/silent-QAQ/redstoneapi/internal/redstone/wallet"
	"github.com/shopspring/decimal"
)

var (
	ErrInvalidReportID                = errors.New("market report id must be positive")
	ErrReportReasonRequired           = errors.New("market report reason is required")
	ErrReportExists                   = errors.New("market report already exists for this product")
	ErrReportNotFound                 = errors.New("market report was not found")
	ErrReportNotOpen                  = errors.New("market report is not open")
	ErrReportForbidden                = errors.New("market report is not allowed for this product")
	ErrInvalidReportResolution        = errors.New("market report resolution is invalid")
	ErrSettlementFrozen               = errors.New("market order settlement is frozen by governance")
	ErrReversalNotAllowed             = errors.New("market order cannot be reversed in its current state")
	ErrReversalInsufficientSellerFund = errors.New("market seller balance is insufficient for reversal")
)

const (
	reportResolutionDismiss  = "dismiss"
	reportResolutionSuspend  = "suspend"
	reportResolutionRelease  = "release"
	maxGovernanceReasonRunes = 500
)

type Report struct {
	ID               int64      `json:"id"`
	ProductID        int64      `json:"product_id"`
	ReporterUserID   int64      `json:"reporter_user_id"`
	Status           string     `json:"status"`
	Reason           string     `json:"reason"`
	CreatedAt        time.Time  `json:"created_at"`
	ResolvedAt       *time.Time `json:"resolved_at,omitempty"`
	ResolvedByUserID *int64     `json:"resolved_by_user_id,omitempty"`
}

type CreateReportRequest struct {
	ReporterUserID int64
	ProductID      int64
	Reason         string
}

func (r CreateReportRequest) Validate() error {
	if r.ReporterUserID <= 0 {
		return ErrInvalidBuyer
	}
	if r.ProductID <= 0 {
		return ErrInvalidProductID
	}
	if !validGovernanceReason(r.Reason) {
		return ErrReportReasonRequired
	}
	return nil
}

type ResolveReportRequest struct {
	ActorUserID int64
	ReportID    int64
	Action      string
	Note        string
}

func (r ResolveReportRequest) Validate() error {
	if r.ActorUserID <= 0 {
		return ErrInvalidActor
	}
	if r.ReportID <= 0 {
		return ErrInvalidReportID
	}
	if r.Action != reportResolutionDismiss && r.Action != reportResolutionSuspend && r.Action != reportResolutionRelease {
		return ErrInvalidReportResolution
	}
	if !validGovernanceReason(r.Note) {
		return ErrReportReasonRequired
	}
	return nil
}

type SellerFreezeRequest struct {
	ActorUserID  int64
	SellerUserID int64
	Reason       string
}

func (r SellerFreezeRequest) Validate() error {
	if r.ActorUserID <= 0 {
		return ErrInvalidActor
	}
	if r.SellerUserID <= 0 {
		return ErrInvalidSellerUserID
	}
	if !validGovernanceReason(r.Reason) {
		return ErrReportReasonRequired
	}
	return nil
}

type ReverseOrderRequest struct {
	ActorUserID int64
	OrderID     int64
	Reason      string
}

func (r ReverseOrderRequest) Validate() error {
	if r.ActorUserID <= 0 {
		return ErrInvalidActor
	}
	if r.OrderID <= 0 {
		return ErrInvalidOrderID
	}
	if !validGovernanceReason(r.Reason) {
		return ErrReportReasonRequired
	}
	return nil
}

type Reversal struct {
	OrderID           int64           `json:"order_id"`
	SellerUserID      int64           `json:"seller_user_id"`
	BuyerUserID       int64           `json:"buyer_user_id"`
	SellerDebitAmount decimal.Decimal `json:"seller_debit_amount"`
	BuyerCreditAmount decimal.Decimal `json:"buyer_credit_amount"`
	Applied           bool            `json:"applied"`
	Replayed          bool            `json:"replayed"`
}

type GovernanceRepository interface {
	CreateReport(context.Context, CreateReportRequest) (Report, error)
	ListOpenReports(context.Context, int, int) ([]Report, int, error)
	ResolveReport(context.Context, ResolveReportRequest) (Report, error)
	FreezeSeller(context.Context, SellerFreezeRequest) error
	UnfreezeSeller(context.Context, SellerFreezeRequest) error
	ReverseOrder(context.Context, ReverseOrderRequest) (Reversal, error)
}

func (s *Service) governanceRepository() (GovernanceRepository, error) {
	if s == nil || s.repository == nil {
		return nil, ErrSettlementUnavailable
	}
	r, ok := s.repository.(GovernanceRepository)
	if !ok {
		return nil, ErrSettlementUnavailable
	}
	return r, nil
}

func (s *Service) CreateReport(ctx context.Context, request CreateReportRequest) (Report, error) {
	if err := request.Validate(); err != nil {
		return Report{}, marketApplicationError(err)
	}
	r, err := s.governanceRepository()
	if err != nil {
		return Report{}, marketApplicationError(err)
	}
	report, err := r.CreateReport(ctx, request)
	return report, marketApplicationError(err)
}

func (s *Service) ListOpenReports(ctx context.Context, limit, offset int) ([]Report, int, error) {
	if limit <= 0 || limit > 100 || offset < 0 {
		return nil, 0, marketApplicationError(ErrInvalidPagination)
	}
	r, err := s.governanceRepository()
	if err != nil {
		return nil, 0, marketApplicationError(err)
	}
	reports, total, err := r.ListOpenReports(ctx, limit, offset)
	return reports, total, marketApplicationError(err)
}

func (s *Service) ResolveReport(ctx context.Context, request ResolveReportRequest) (Report, error) {
	if err := request.Validate(); err != nil {
		return Report{}, marketApplicationError(err)
	}
	r, err := s.governanceRepository()
	if err != nil {
		return Report{}, marketApplicationError(err)
	}
	report, err := r.ResolveReport(ctx, request)
	return report, marketApplicationError(err)
}

func (s *Service) FreezeSeller(ctx context.Context, request SellerFreezeRequest) error {
	if err := request.Validate(); err != nil {
		return marketApplicationError(err)
	}
	r, err := s.governanceRepository()
	if err != nil {
		return marketApplicationError(err)
	}
	return marketApplicationError(r.FreezeSeller(ctx, request))
}

func (s *Service) UnfreezeSeller(ctx context.Context, request SellerFreezeRequest) error {
	if err := request.Validate(); err != nil {
		return marketApplicationError(err)
	}
	r, err := s.governanceRepository()
	if err != nil {
		return marketApplicationError(err)
	}
	return marketApplicationError(r.UnfreezeSeller(ctx, request))
}

func (s *Service) ReverseOrder(ctx context.Context, request ReverseOrderRequest) (Reversal, error) {
	if err := request.Validate(); err != nil {
		return Reversal{}, marketApplicationError(err)
	}
	r, err := s.governanceRepository()
	if err != nil {
		return Reversal{}, marketApplicationError(err)
	}
	result, err := r.ReverseOrder(ctx, request)
	return result, marketApplicationError(err)
}

func validGovernanceReason(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len([]rune(value)) <= maxGovernanceReasonRunes
}

func (r *sqlRepository) CreateReport(ctx context.Context, request CreateReportRequest) (_ Report, err error) {
	if err := request.Validate(); err != nil {
		return Report{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Report{}, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	var report Report
	err = tx.QueryRowContext(ctx, `
		INSERT INTO redstone_market_reports (product_id, reporter_user_id, status, reason, created_at)
		SELECT id, $2, 'open', $3, $4
		FROM redstone_market_products
		WHERE id = $1 AND seller_user_id <> $2 AND status = 'active' AND risk_status = 'passed'
		ON CONFLICT (product_id, reporter_user_id) DO NOTHING
		RETURNING id, product_id, reporter_user_id, status, reason, created_at, resolved_at, resolved_by_user_id
	`, request.ProductID, request.ReporterUserID, strings.TrimSpace(request.Reason), now).Scan(
		&report.ID, &report.ProductID, &report.ReporterUserID, &report.Status, &report.Reason,
		&report.CreatedAt, &report.ResolvedAt, &report.ResolvedByUserID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		var exists bool
		if checkErr := tx.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM redstone_market_reports
				WHERE product_id = $1 AND reporter_user_id = $2
			)`, request.ProductID, request.ReporterUserID).Scan(&exists); checkErr != nil {
			return Report{}, checkErr
		} else if exists {
			return Report{}, ErrReportExists
		}
		return Report{}, ErrReportForbidden
	}
	if err != nil {
		return Report{}, err
	}
	if err := insertGovernanceAudit(ctx, tx, "product", request.ProductID, "reported", request.ReporterUserID, strings.TrimSpace(request.Reason), now); err != nil {
		return Report{}, err
	}
	if err := tx.Commit(); err != nil {
		return Report{}, err
	}
	return report, nil
}

func (r *sqlRepository) ListOpenReports(ctx context.Context, limit, offset int) ([]Report, int, error) {
	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM redstone_market_reports WHERE status = 'open'`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, product_id, reporter_user_id, status, reason, created_at, resolved_at, resolved_by_user_id
		FROM redstone_market_reports WHERE status = 'open'
		ORDER BY created_at, id LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	reports := make([]Report, 0)
	for rows.Next() {
		var report Report
		if err := rows.Scan(&report.ID, &report.ProductID, &report.ReporterUserID, &report.Status, &report.Reason,
			&report.CreatedAt, &report.ResolvedAt, &report.ResolvedByUserID); err != nil {
			return nil, 0, err
		}
		reports = append(reports, report)
	}
	return reports, total, rows.Err()
}

func (r *sqlRepository) ResolveReport(ctx context.Context, request ResolveReportRequest) (_ Report, err error) {
	if err := request.Validate(); err != nil {
		return Report{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Report{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var report Report
	err = tx.QueryRowContext(ctx, `
		SELECT id, product_id, reporter_user_id, status, reason, created_at, resolved_at, resolved_by_user_id
		FROM redstone_market_reports WHERE id = $1 FOR UPDATE
	`, request.ReportID).Scan(&report.ID, &report.ProductID, &report.ReporterUserID, &report.Status, &report.Reason,
		&report.CreatedAt, &report.ResolvedAt, &report.ResolvedByUserID)
	if errors.Is(err, sql.ErrNoRows) {
		return Report{}, ErrReportNotFound
	}
	if err != nil {
		return Report{}, err
	}
	if request.Action == reportResolutionRelease {
		if report.Status != "actioned" {
			return Report{}, ErrReportNotOpen
		}
		now := time.Now().UTC()
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM redstone_market_order_holds
			WHERE source = 'report' AND source_id = $1
		`, report.ID); err != nil {
			return Report{}, err
		}
		if err := insertGovernanceAudit(ctx, tx, "report", report.ID, "report_hold_released", request.ActorUserID, strings.TrimSpace(request.Note), now); err != nil {
			return Report{}, err
		}
		if err := tx.Commit(); err != nil {
			return Report{}, err
		}
		return report, nil
	}
	if report.Status != "open" {
		return Report{}, ErrReportNotOpen
	}
	now := time.Now().UTC()
	status := "dismissed"
	auditAction := "report_dismissed"
	if request.Action == reportResolutionSuspend {
		status = "actioned"
		auditAction = "product_suspended"
		if _, err := tx.ExecContext(ctx, `
			UPDATE redstone_market_products SET status = 'suspended', updated_at = $2
			WHERE id = $1 AND status IN ('active', 'pending_scan')
		`, report.ProductID, now); err != nil {
			return Report{}, err
		}
		if err := lockProductUnsettledOrders(ctx, tx, report.ProductID); err != nil {
			return Report{}, err
		}
		if err := holdProductOrders(ctx, tx, report.ProductID, "report", report.ID, request.ActorUserID, strings.TrimSpace(request.Note), now); err != nil {
			return Report{}, err
		}
		if err := insertGovernanceAudit(ctx, tx, "product", report.ProductID, auditAction, request.ActorUserID, strings.TrimSpace(request.Note), now); err != nil {
			return Report{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE redstone_market_reports
		SET status = $2, resolved_at = $3, resolved_by_user_id = $4
		WHERE id = $1 AND status = 'open'
	`, report.ID, status, now, request.ActorUserID); err != nil {
		return Report{}, err
	}
	report.Status = status
	report.ResolvedAt = &now
	report.ResolvedByUserID = &request.ActorUserID
	if err := insertGovernanceAudit(ctx, tx, "report", report.ID, auditAction, request.ActorUserID, strings.TrimSpace(request.Note), now); err != nil {
		return Report{}, err
	}
	if err := tx.Commit(); err != nil {
		return Report{}, err
	}
	return report, nil
}

func (r *sqlRepository) FreezeSeller(ctx context.Context, request SellerFreezeRequest) (err error) {
	if err := request.Validate(); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	// Marketplace writer paths lock a product before the seller. Lock every
	// listing first so freeze cannot race a draft into active state or deadlock
	// against publish/scan/upload.
	if err := lockSellerProductsForGovernance(ctx, tx, request.SellerUserID); err != nil {
		return err
	}
	var userID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, request.SellerUserID).Scan(&userID); errors.Is(err, sql.ErrNoRows) {
		return ErrInvalidSellerUserID
	} else if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO redstone_market_seller_controls (seller_user_id, frozen_at, frozen_by_user_id, reason, updated_at)
		VALUES ($1, $2, $3, $4, $2)
		ON CONFLICT (seller_user_id) DO UPDATE
		SET frozen_at = EXCLUDED.frozen_at, frozen_by_user_id = EXCLUDED.frozen_by_user_id,
			reason = EXCLUDED.reason, updated_at = EXCLUDED.updated_at
	`, request.SellerUserID, now, request.ActorUserID, strings.TrimSpace(request.Reason)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE redstone_market_products SET status = 'suspended', updated_at = $2
		WHERE seller_user_id = $1 AND seller_kind = 'user' AND status IN ('active', 'pending_scan')
	`, request.SellerUserID, now); err != nil {
		return err
	}
	if err := lockSellerUnsettledOrders(ctx, tx, request.SellerUserID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO redstone_market_order_holds (order_id, source, source_id, reason, actor_user_id, created_at)
		SELECT id, 'seller_freeze', 0, $2, $3, $4
		FROM redstone_market_orders
		WHERE seller_user_id = $1 AND status IN ('paid', 'delivered', 'appealed')
		ON CONFLICT (order_id, source, source_id) DO NOTHING
	`, request.SellerUserID, strings.TrimSpace(request.Reason), request.ActorUserID, now); err != nil {
		return err
	}
	if err := insertGovernanceAudit(ctx, tx, "seller", request.SellerUserID, "seller_frozen", request.ActorUserID, strings.TrimSpace(request.Reason), now); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *sqlRepository) UnfreezeSeller(ctx context.Context, request SellerFreezeRequest) (err error) {
	if err := request.Validate(); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	// Freeze and unfreeze serialize on the seller row. Otherwise an unfreeze can
	// commit after observing no uncommitted control row and still be overtaken
	// by the concurrent freeze it was supposed to reverse.
	var sellerID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, request.SellerUserID).Scan(&sellerID); errors.Is(err, sql.ErrNoRows) {
		return ErrInvalidSellerUserID
	} else if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM redstone_market_seller_controls WHERE seller_user_id = $1`, request.SellerUserID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM redstone_market_order_holds h
		USING redstone_market_orders o
		WHERE h.order_id = o.id AND h.source = 'seller_freeze' AND h.source_id = 0
			AND o.seller_user_id = $1
	`, request.SellerUserID); err != nil {
		return err
	}
	if err := insertGovernanceAudit(ctx, tx, "seller", request.SellerUserID, "seller_unfrozen", request.ActorUserID, strings.TrimSpace(request.Reason), now); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *sqlRepository) ReverseOrder(ctx context.Context, request ReverseOrderRequest) (_ Reversal, err error) {
	if err := request.Validate(); err != nil {
		return Reversal{}, err
	}
	if r.wallet == nil {
		return Reversal{}, ErrMarketplaceUnavailable
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Reversal{}, err
	}
	defer func() { _ = tx.Rollback() }()
	order, err := lockSettlementOrder(ctx, tx, request.OrderID)
	if err != nil {
		return Reversal{}, err
	}
	var existing Reversal
	err = tx.QueryRowContext(ctx, `
		SELECT order_id, seller_user_id, buyer_user_id, seller_debit_amount, buyer_credit_amount
		FROM redstone_market_reversals WHERE order_id = $1 FOR UPDATE
	`, request.OrderID).Scan(&existing.OrderID, &existing.SellerUserID, &existing.BuyerUserID,
		&existing.SellerDebitAmount, &existing.BuyerCreditAmount)
	if err == nil {
		existing.Replayed = true
		if err := tx.Commit(); err != nil {
			return Reversal{}, err
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Reversal{}, err
	}
	if order.Status != "settled" {
		return Reversal{}, ErrReversalNotAllowed
	}
	now := time.Now().UTC()
	sellerKey := "market-reversal-seller-" + strconv.FormatInt(order.ID, 10)
	buyerKey := "market-reversal-buyer-" + strconv.FormatInt(order.ID, 10)
	sellerDebit, err := r.wallet.AdjustNormalInExecutor(ctx, tx, wallet.NormalAdjustmentRequest{
		UserID:         order.SellerUserID,
		Delta:          order.SellerNetAmount.Neg(),
		Operation:      wallet.OperationMarketplaceDebit,
		Reference:      wallet.Reference{Type: "marketplace_reversal", ID: strconv.FormatInt(order.ID, 10)},
		IdempotencyKey: sellerKey,
	})
	if errors.Is(err, wallet.ErrInsufficientFunds) {
		return Reversal{}, ErrReversalInsufficientSellerFund
	}
	if err != nil || !sellerDebit.Applied {
		if err != nil {
			return Reversal{}, err
		}
		return Reversal{}, ErrReversalNotAllowed
	}
	buyerCredit, err := r.wallet.CreditInExecutor(ctx, tx, wallet.CreditRequest{
		UserID:         order.BuyerUserID,
		Asset:          wallet.AssetNormal,
		Amount:         order.UnitPrice,
		Reason:         wallet.CreditRefund,
		Reference:      wallet.Reference{Type: "marketplace_reversal", ID: strconv.FormatInt(order.ID, 10)},
		IdempotencyKey: buyerKey,
	})
	if err != nil || !buyerCredit.Applied {
		if err != nil {
			return Reversal{}, err
		}
		return Reversal{}, ErrReversalNotAllowed
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO redstone_market_reversals (
			order_id, seller_user_id, buyer_user_id, seller_debit_amount, buyer_credit_amount,
			seller_wallet_operation_key, buyer_wallet_operation_key, actor_user_id, reason, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, order.ID, order.SellerUserID, order.BuyerUserID, order.SellerNetAmount, order.UnitPrice,
		sellerKey, buyerKey, request.ActorUserID, strings.TrimSpace(request.Reason), now); err != nil {
		return Reversal{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE redstone_market_orders SET status = 'reversed', refunded_at = $2, updated_at = $2 WHERE id = $1
	`, order.ID, now); err != nil {
		return Reversal{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE redstone_market_delivery_items SET status = 'revoked'
		WHERE id = (SELECT delivery_item_id FROM redstone_market_orders WHERE id = $1)
			AND status IN ('reserved', 'delivered')
	`, order.ID); err != nil {
		return Reversal{}, err
	}
	if err := releaseAccountEscrowForOrder(ctx, tx, order.ID, order.SellerUserID, order.BuyerUserID); err != nil {
		return Reversal{}, err
	}
	if err := insertGovernanceAudit(ctx, tx, "order", order.ID, "order_reversed", request.ActorUserID, strings.TrimSpace(request.Reason), now); err != nil {
		return Reversal{}, err
	}
	if err := tx.Commit(); err != nil {
		return Reversal{}, err
	}
	return Reversal{OrderID: order.ID, SellerUserID: order.SellerUserID, BuyerUserID: order.BuyerUserID,
		SellerDebitAmount: order.SellerNetAmount, BuyerCreditAmount: order.UnitPrice, Applied: true}, nil
}

func holdProductOrders(ctx context.Context, tx *sql.Tx, productID int64, source string, sourceID, actorUserID int64, reason string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO redstone_market_order_holds (order_id, source, source_id, reason, actor_user_id, created_at)
		SELECT id, $2, $3, $4, $5, $6 FROM redstone_market_orders
		WHERE product_id = $1 AND status IN ('paid', 'delivered', 'appealed')
		ON CONFLICT (order_id, source, source_id) DO NOTHING
	`, productID, source, sourceID, reason, actorUserID, now)
	return err
}

// Governance holds and automatic settlement contend on the same order row.
// Taking these locks before writing holds makes their outcome deterministic:
// a committed hold is always visible before a later settlement can proceed.
func lockProductUnsettledOrders(ctx context.Context, tx *sql.Tx, productID int64) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id FROM redstone_market_orders
		WHERE product_id = $1 AND status IN ('paid', 'delivered', 'appealed')
		FOR UPDATE
	`, productID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ignored int64
		if err := rows.Scan(&ignored); err != nil {
			return err
		}
	}
	return rows.Err()
}

func lockSellerUnsettledOrders(ctx context.Context, tx *sql.Tx, sellerUserID int64) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id FROM redstone_market_orders
		WHERE seller_user_id = $1 AND status IN ('paid', 'delivered', 'appealed')
		FOR UPDATE
	`, sellerUserID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ignored int64
		if err := rows.Scan(&ignored); err != nil {
			return err
		}
	}
	return rows.Err()
}

func lockSellerProductsForGovernance(ctx context.Context, tx *sql.Tx, sellerUserID int64) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id FROM redstone_market_products
		WHERE seller_user_id = $1 AND seller_kind = 'user'
		FOR UPDATE
	`, sellerUserID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ignored int64
		if err := rows.Scan(&ignored); err != nil {
			return err
		}
	}
	return rows.Err()
}

func isOrderHeldTx(ctx context.Context, tx *sql.Tx, orderID int64) (bool, error) {
	var held bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM redstone_market_order_holds WHERE order_id = $1)`, orderID).Scan(&held)
	return held, err
}

func insertGovernanceAudit(ctx context.Context, tx *sql.Tx, entityType string, entityID int64, action string, actorUserID int64, reason string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO redstone_market_governance_audit (entity_type, entity_id, action, actor_user_id, reason, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, entityType, entityID, action, actorUserID, reason, now)
	return err
}
