package market

import (
	"context"
	"database/sql"
)

// AdminOrder combines an immutable order snapshot with only the operational
// state necessary to resolve it. It never contains delivery plaintext.
type AdminOrder struct {
	Order
	Appeal       *Appeal `json:"appeal,omitempty"`
	HoldCount    int     `json:"hold_count"`
	SellerFrozen bool    `json:"seller_frozen"`
}

// AdminAppeal is the queue projection for operators. The order snapshot is
// intentionally limited to IDs, title, and state to avoid leaking delivery.
type AdminAppeal struct {
	Appeal
	ProductTitle string `json:"product_title"`
	OrderStatus  string `json:"order_status"`
	SellerUserID int64  `json:"seller_user_id"`
}

// HistoryRepository is intentionally optional to preserve source
// compatibility for focused marketplace fakes.
type HistoryRepository interface {
	ListOrdersBySeller(context.Context, int64, int, int) ([]Order, int, error)
	ListAdminOrders(context.Context, int, int) ([]AdminOrder, int, error)
	ListOpenAppeals(context.Context, int, int) ([]AdminAppeal, int, error)
}

func (s *Service) historyRepository() (HistoryRepository, error) {
	if s == nil || s.repository == nil {
		return nil, ErrSettlementUnavailable
	}
	repository, ok := s.repository.(HistoryRepository)
	if !ok {
		return nil, ErrSettlementUnavailable
	}
	return repository, nil
}

func (s *Service) ListOrdersBySeller(ctx context.Context, sellerUserID int64, limit, offset int) ([]Order, int, error) {
	if sellerUserID <= 0 {
		return nil, 0, marketApplicationError(ErrInvalidSellerUserID)
	}
	if limit <= 0 || limit > 100 || offset < 0 {
		return nil, 0, marketApplicationError(ErrInvalidPagination)
	}
	repository, err := s.historyRepository()
	if err != nil {
		return nil, 0, marketApplicationError(err)
	}
	orders, total, err := repository.ListOrdersBySeller(ctx, sellerUserID, limit, offset)
	return orders, total, marketApplicationError(err)
}

func (s *Service) ListAdminOrders(ctx context.Context, limit, offset int) ([]AdminOrder, int, error) {
	if limit <= 0 || limit > 100 || offset < 0 {
		return nil, 0, marketApplicationError(ErrInvalidPagination)
	}
	repository, err := s.historyRepository()
	if err != nil {
		return nil, 0, marketApplicationError(err)
	}
	orders, total, err := repository.ListAdminOrders(ctx, limit, offset)
	return orders, total, marketApplicationError(err)
}

func (s *Service) ListOpenAppeals(ctx context.Context, limit, offset int) ([]AdminAppeal, int, error) {
	if limit <= 0 || limit > 100 || offset < 0 {
		return nil, 0, marketApplicationError(ErrInvalidPagination)
	}
	repository, err := s.historyRepository()
	if err != nil {
		return nil, 0, marketApplicationError(err)
	}
	appeals, total, err := repository.ListOpenAppeals(ctx, limit, offset)
	return appeals, total, marketApplicationError(err)
}

func (r *sqlRepository) ListOrdersBySeller(ctx context.Context, sellerUserID int64, limit, offset int) ([]Order, int, error) {
	if sellerUserID <= 0 {
		return nil, 0, ErrInvalidSellerUserID
	}
	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM redstone_market_orders WHERE seller_user_id = $1`, sellerUserID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, orderSelect+`
		WHERE o.seller_user_id = $1
		ORDER BY o.created_at DESC, o.id DESC
		LIMIT $2 OFFSET $3
	`, sellerUserID, limit, offset)
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

func (r *sqlRepository) ListAdminOrders(ctx context.Context, limit, offset int) ([]AdminOrder, int, error) {
	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM redstone_market_orders`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, adminOrderSelect+`
		ORDER BY o.created_at DESC, o.id DESC
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	orders := make([]AdminOrder, 0)
	for rows.Next() {
		order, err := scanAdminOrder(rows)
		if err != nil {
			return nil, 0, err
		}
		orders = append(orders, order)
	}
	return orders, total, rows.Err()
}

func (r *sqlRepository) ListOpenAppeals(ctx context.Context, limit, offset int) ([]AdminAppeal, int, error) {
	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM redstone_market_appeals WHERE status = 'open'`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT a.id, a.order_id, a.buyer_user_id, a.status, a.reason, a.resolution_note,
			a.resolved_by_user_id, a.created_at, a.resolved_at,
			p.title, o.status, o.seller_user_id
		FROM redstone_market_appeals a
		JOIN redstone_market_orders o ON o.id = a.order_id
		JOIN redstone_market_products p ON p.id = o.product_id
		WHERE a.status = 'open'
		ORDER BY a.created_at, a.id
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	appeals := make([]AdminAppeal, 0)
	for rows.Next() {
		appeal, err := scanAdminAppeal(rows)
		if err != nil {
			return nil, 0, err
		}
		appeals = append(appeals, appeal)
	}
	return appeals, total, rows.Err()
}

const adminOrderSelect = `
	SELECT o.id, o.order_no, o.buyer_user_id, o.seller_user_id, o.product_id,
		p.title, o.delivery_item_id, o.status, o.unit_price, o.service_fee_rate,
		o.service_fee_amount, o.seller_net_amount, o.settlement_due_at,
		o.delivered_at, o.settled_at, o.refunded_at, o.created_at,
		a.id, a.order_id, a.buyer_user_id, a.status, a.reason, a.resolution_note,
		a.resolved_by_user_id, a.created_at, a.resolved_at,
		(SELECT COUNT(*) FROM redstone_market_order_holds h WHERE h.order_id = o.id),
		EXISTS (SELECT 1 FROM redstone_market_seller_controls sc WHERE sc.seller_user_id = o.seller_user_id)
	FROM redstone_market_orders o
	JOIN redstone_market_products p ON p.id = o.product_id
	LEFT JOIN redstone_market_appeals a ON a.order_id = o.id
`

func scanAdminOrder(scanner rowScanner) (AdminOrder, error) {
	var result AdminOrder
	var deliveredAt, settledAt, refundedAt sql.NullTime
	var appealID, appealOrderID, appealBuyerID sql.NullInt64
	var appealStatus, appealReason, appealNote sql.NullString
	var appealResolvedBy sql.NullInt64
	var appealCreatedAt, appealResolvedAt sql.NullTime
	err := scanner.Scan(
		&result.ID, &result.OrderNo, &result.BuyerUserID, &result.SellerUserID, &result.ProductID,
		&result.ProductTitle, &result.DeliveryItemID, &result.Status, &result.UnitPrice, &result.ServiceFeeRate,
		&result.ServiceFeeAmount, &result.SellerNetAmount, &result.SettlementDueAt,
		&deliveredAt, &settledAt, &refundedAt, &result.CreatedAt,
		&appealID, &appealOrderID, &appealBuyerID, &appealStatus, &appealReason, &appealNote,
		&appealResolvedBy, &appealCreatedAt, &appealResolvedAt,
		&result.HoldCount, &result.SellerFrozen,
	)
	if err != nil {
		return AdminOrder{}, err
	}
	if deliveredAt.Valid {
		value := deliveredAt.Time.UTC()
		result.DeliveredAt = &value
	}
	if settledAt.Valid {
		value := settledAt.Time.UTC()
		result.SettledAt = &value
	}
	if refundedAt.Valid {
		value := refundedAt.Time.UTC()
		result.RefundedAt = &value
	}
	if appealID.Valid {
		appeal := Appeal{ID: appealID.Int64, OrderID: appealOrderID.Int64, BuyerUserID: appealBuyerID.Int64, Status: appealStatus.String, Reason: appealReason.String, ResolutionNote: appealNote.String, CreatedAt: appealCreatedAt.Time.UTC()}
		if appealResolvedBy.Valid {
			value := appealResolvedBy.Int64
			appeal.ResolvedByUserID = &value
		}
		if appealResolvedAt.Valid {
			value := appealResolvedAt.Time.UTC()
			appeal.ResolvedAt = &value
		}
		result.Appeal = &appeal
	}
	return result, nil
}

func scanAdminAppeal(scanner rowScanner) (AdminAppeal, error) {
	var result AdminAppeal
	var resolvedBy sql.NullInt64
	var resolvedAt sql.NullTime
	err := scanner.Scan(&result.ID, &result.OrderID, &result.BuyerUserID, &result.Status, &result.Reason, &result.ResolutionNote,
		&resolvedBy, &result.CreatedAt, &resolvedAt, &result.ProductTitle, &result.OrderStatus, &result.SellerUserID)
	if err != nil {
		return AdminAppeal{}, err
	}
	if resolvedBy.Valid {
		value := resolvedBy.Int64
		result.ResolvedByUserID = &value
	}
	if resolvedAt.Valid {
		value := resolvedAt.Time.UTC()
		result.ResolvedAt = &value
	}
	return result, nil
}
