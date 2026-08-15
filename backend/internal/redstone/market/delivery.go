package market

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrDeliveryForbidden        = errors.New("market delivery does not belong to the buyer")
	ErrDeliveryUnavailable      = errors.New("market delivery is unavailable")
	ErrDeliveryAlreadyViewed    = errors.New("market delivery has already been viewed")
	ErrDeliveryNotAllowed       = errors.New("market order cannot be delivered in its current state")
	ErrDeliveryItemUnavailable  = errors.New("market delivery item is unavailable")
	ErrDeliveryResolverMissing  = errors.New("market text delivery resolver is not configured")
	ErrDeliveryContentIntegrity = errors.New("market delivery content integrity check failed")
)

// DeliveryContentResolver is the encrypted object-store boundary for text and
// card content. It must never log or persist plaintext.
type DeliveryContentResolver interface {
	ResolveText(context.Context, DeliveryItem) (string, error)
}

type DeliveryItem struct {
	ID                 int64
	ProductType        string
	Status             string
	AccountID          *int64
	EncryptedObjectKey string
	KeyVersion         string
	WrappedDEK         []byte
	ContentSHA256      string
	ContentType        string
	ByteSize           *int64
}

type Delivery struct {
	OrderID         int64         `json:"order_id"`
	ProductID       int64         `json:"product_id"`
	ProductType     string        `json:"product_type"`
	DeliveryItemID  int64         `json:"delivery_item_id"`
	AccountID       *int64        `json:"account_id,omitempty"`
	AccountName     string        `json:"account_name,omitempty"`
	AccountPlatform string        `json:"account_platform,omitempty"`
	DeliveredAt     time.Time     `json:"delivered_at"`
	Text            string        `json:"text,omitempty"`
	File            *DeliveryFile `json:"file,omitempty"`
}

type DeliveryFile struct {
	ContentType string `json:"content_type,omitempty"`
	ByteSize    *int64 `json:"byte_size,omitempty"`
	Available   bool   `json:"available"`
}

type DeliveryRepository interface {
	DeliverOrder(context.Context, int64, int64, string, DeliveryContentResolver) (Delivery, error)
}

// FileDeliveryContentResolver is intentionally separate from text delivery.
// File data must be written directly to the authenticated response and is
// never embedded in a JSON DTO or exchanged for an object-storage URL.
type FileDeliveryContentResolver interface {
	ResolveFile(context.Context, DeliveryItem) ([]byte, error)
}

type FileDelivery struct {
	OrderID        int64
	DeliveryItemID int64
	DeliveredAt    time.Time
	content        []byte
}

func (d *FileDelivery) clear() {
	if d != nil {
		zeroBytes(d.content)
		d.content = nil
	}
}

type FileDeliveryRepository interface {
	PrepareFileDelivery(context.Context, int64, int64) (DeliveryItem, error)
	ClaimFileDelivery(context.Context, int64, int64, string) (FileDelivery, error)
}

func (s *Service) DeliverOrder(ctx context.Context, buyerUserID, orderID int64, requestID string) (Delivery, error) {
	if buyerUserID <= 0 || orderID <= 0 {
		return Delivery{}, marketApplicationError(ErrInvalidOrderID)
	}
	r, ok := s.repository.(DeliveryRepository)
	if !ok {
		return Delivery{}, marketApplicationError(ErrDeliveryUnavailable)
	}
	d, err := r.DeliverOrder(ctx, buyerUserID, orderID, requestID, s.deliveryResolver)
	if err != nil {
		return Delivery{}, marketApplicationError(err)
	}
	return d, nil
}

// DownloadFileDelivery reads and authenticates the private ciphertext before
// taking the one-time claim. This prevents an object-store outage from
// consuming the buyer's entitlement, while the subsequent short SQL
// transaction ensures concurrent requests can claim it only once.
func (s *Service) DownloadFileDelivery(ctx context.Context, buyerUserID, orderID int64, requestID string) (FileDelivery, error) {
	if buyerUserID <= 0 || orderID <= 0 {
		return FileDelivery{}, marketApplicationError(ErrInvalidOrderID)
	}
	repository, ok := s.repository.(FileDeliveryRepository)
	if !ok {
		return FileDelivery{}, marketApplicationError(ErrDeliveryUnavailable)
	}
	resolver, ok := s.deliveryResolver.(FileDeliveryContentResolver)
	if !ok || resolver == nil {
		return FileDelivery{}, marketApplicationError(ErrDeliveryResolverMissing)
	}
	item, err := repository.PrepareFileDelivery(ctx, buyerUserID, orderID)
	if err != nil {
		return FileDelivery{}, marketApplicationError(err)
	}
	content, err := resolver.ResolveFile(ctx, item)
	if err != nil {
		return FileDelivery{}, marketApplicationError(err)
	}
	delivery, err := repository.ClaimFileDelivery(ctx, buyerUserID, orderID, requestID)
	if err != nil {
		zeroBytes(content)
		return FileDelivery{}, marketApplicationError(err)
	}
	delivery.content = content
	return delivery, nil
}

func (r *sqlRepository) DeliverOrder(ctx context.Context, buyerUserID, orderID int64, requestID string, resolver DeliveryContentResolver) (_ Delivery, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Delivery{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var d Delivery
	var status string
	var sellerUserID int64
	var deliveredAt sql.NullTime
	err = tx.QueryRowContext(ctx, `
		SELECT o.product_id, o.delivery_item_id, o.status, o.delivered_at,
		       o.seller_user_id,
		       p.product_type, di.account_id, COALESCE(a.name, ''), COALESCE(a.platform, '')
		FROM redstone_market_orders o
		JOIN redstone_market_products p ON p.id = o.product_id
		JOIN redstone_market_delivery_items di ON di.id = o.delivery_item_id
		LEFT JOIN accounts a ON a.id = di.account_id
		WHERE o.id = $1 AND o.buyer_user_id = $2
		FOR UPDATE OF o, di`, orderID, buyerUserID).Scan(&d.ProductID, &d.DeliveryItemID, &status, &deliveredAt,
		&sellerUserID,
		&d.ProductType, &d.AccountID, &d.AccountName, &d.AccountPlatform)
	if errors.Is(err, sql.ErrNoRows) {
		return Delivery{}, ErrDeliveryForbidden
	}
	if err != nil {
		return Delivery{}, err
	}
	if status != "paid" && status != "delivered" {
		return Delivery{}, ErrDeliveryNotAllowed
	}
	d.OrderID = orderID
	var viewed bool
	err = tx.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM redstone_market_delivery_audit WHERE order_id = $1 AND event_type IN ('viewed','downloaded'))`, orderID).Scan(&viewed)
	if err != nil {
		return Delivery{}, err
	}
	if viewed {
		return Delivery{}, ErrDeliveryAlreadyViewed
	}
	var item DeliveryItem
	err = tx.QueryRowContext(ctx, `
		SELECT id, product_type, status, account_id, encrypted_object_key, key_version,
			wrapped_dek, COALESCE(content_sha256, ''), content_type, byte_size
		FROM redstone_market_delivery_items di
		JOIN redstone_market_products p ON p.id = di.product_id
		WHERE di.id = $1 FOR UPDATE
	`, d.DeliveryItemID).Scan(&item.ID, &item.ProductType, &item.Status, &item.AccountID,
		&item.EncryptedObjectKey, &item.KeyVersion, &item.WrappedDEK, &item.ContentSHA256, &item.ContentType, &item.ByteSize)
	if errors.Is(err, sql.ErrNoRows) {
		return Delivery{}, ErrDeliveryItemUnavailable
	}
	if err != nil {
		return Delivery{}, err
	}
	if item.Status != "reserved" && item.Status != "delivered" {
		return Delivery{}, ErrDeliveryItemUnavailable
	}
	// A file has a separate binary endpoint. Returning its availability here
	// must not write a view audit or advance the order, otherwise merely opening
	// the dialog would consume the buyer's one-time download entitlement.
	if item.ProductType == "file" {
		d.File = &DeliveryFile{ContentType: item.ContentType, ByteSize: item.ByteSize, Available: true}
		if deliveredAt.Valid {
			d.DeliveredAt = deliveredAt.Time.UTC()
		}
		if err = tx.Commit(); err != nil {
			return Delivery{}, err
		}
		return d, nil
	}
	switch item.ProductType {
	case "account_reference":
		if item.AccountID == nil || d.AccountName == "" {
			return Delivery{}, ErrDeliveryUnavailable
		}
		if err := transferAccountDelivery(ctx, tx, *item.AccountID, sellerUserID, buyerUserID); err != nil {
			return Delivery{}, err
		}
	case "text_key", "card_key":
		if resolver == nil {
			return Delivery{}, ErrDeliveryResolverMissing
		}
		text, resolveErr := resolver.ResolveText(ctx, item)
		if resolveErr != nil {
			return Delivery{}, fmt.Errorf("resolve market delivery: %w", resolveErr)
		}
		if strings.TrimSpace(text) == "" {
			return Delivery{}, ErrDeliveryUnavailable
		}
		d.Text = text
	default:
		return Delivery{}, ErrDeliveryUnavailable
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO redstone_market_delivery_audit (order_id, event_type, actor_user_id, request_id)
		VALUES ($1, 'viewed', $2, NULLIF($3, ''))`, orderID, buyerUserID, requestID); err != nil {
		return Delivery{}, err
	}
	if status == "paid" {
		now := time.Now().UTC()
		if err = markDeliveredTx(ctx, tx, orderID, now); err != nil {
			return Delivery{}, err
		}
		deliveredAt = sql.NullTime{Time: now, Valid: true}
	}
	if deliveredAt.Valid {
		d.DeliveredAt = deliveredAt.Time.UTC()
	} else {
		d.DeliveredAt = time.Now().UTC()
	}
	if err = tx.Commit(); err != nil {
		return Delivery{}, err
	}
	return d, nil
}

// transferAccountDelivery makes the buyer the sole owner of the existing
// sub2 account in the same transaction as the immutable delivery audit. No
// account credential is selected or copied by marketplace code.
func transferAccountDelivery(ctx context.Context, tx *sql.Tx, accountID, sellerUserID, buyerUserID int64) error {
	var priorSchedulable bool
	err := tx.QueryRowContext(ctx, `
		SELECT prior_schedulable FROM redstone_market_account_escrows
		WHERE account_id = $1 AND seller_user_id = $2 AND state = 'reserved'
		FOR UPDATE
	`, accountID, sellerUserID).Scan(&priorSchedulable)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDeliveryUnavailable
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE redstone_market_account_escrows
		SET state = 'transferring', updated_at = NOW()
		WHERE account_id = $1 AND state = 'reserved'
	`, accountID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE accounts
		SET owner_user_id = $1, schedulable = $2, updated_at = NOW()
		WHERE id = $3 AND owner_user_id = $4 AND deleted_at IS NULL AND status = 'active'
	`, buyerUserID, priorSchedulable, accountID, sellerUserID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrDeliveryUnavailable
	}
	// Pre-9006 user uploads retain a compatibility ownership row. Keep it in
	// sync when present so both the legacy shell and the fully reused account
	// surface agree on the buyer after a one-time account delivery.
	if _, err := tx.ExecContext(ctx, `
		UPDATE redstone_user_controlled_accounts
		SET owner_user_id = $1, updated_at = NOW()
		WHERE account_id = $2 AND owner_user_id = $3
	`, buyerUserID, accountID, sellerUserID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE redstone_market_account_escrows
		SET state = 'transferred', updated_at = NOW()
		WHERE account_id = $1 AND state = 'transferring'
	`, accountID)
	return err
}

// releaseAccountEscrowForOrder compensates an account delivery when a refund
// or an administrative reversal voids the order. It always flips escrow state
// before writing accounts so the database guard remains authoritative.
func releaseAccountEscrowForOrder(ctx context.Context, tx *sql.Tx, orderID, sellerUserID, buyerUserID int64) error {
	var accountID int64
	var priorSchedulable bool
	var state string
	err := tx.QueryRowContext(ctx, `
		SELECT e.account_id, e.prior_schedulable, e.state
		FROM redstone_market_account_escrows e
		JOIN redstone_market_delivery_items di ON di.account_id = e.account_id
		JOIN redstone_market_orders o ON o.delivery_item_id = di.id
		WHERE o.id = $1 AND e.seller_user_id = $2
		FOR UPDATE OF e
	`, orderID, sellerUserID).Scan(&accountID, &priorSchedulable, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if state != "reserved" && state != "transferred" {
		return ErrDeliveryUnavailable
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE redstone_market_account_escrows
		SET state = 'released', released_at = NOW(), updated_at = NOW()
		WHERE account_id = $1 AND state IN ('reserved', 'transferred')
	`, accountID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE accounts SET owner_user_id = $1, schedulable = $2, updated_at = NOW()
		WHERE id = $3 AND owner_user_id IN ($1, $4) AND deleted_at IS NULL
	`, sellerUserID, priorSchedulable, accountID, buyerUserID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrDeliveryUnavailable
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE redstone_user_controlled_accounts
		SET owner_user_id = $1, updated_at = NOW()
		WHERE account_id = $2 AND owner_user_id = $3
	`, sellerUserID, accountID, buyerUserID); err != nil {
		return err
	}
	return nil
}

func (r *sqlRepository) PrepareFileDelivery(ctx context.Context, buyerUserID, orderID int64) (DeliveryItem, error) {
	var orderStatus string
	var item DeliveryItem
	err := r.db.QueryRowContext(ctx, `
		SELECT o.status, di.id, p.product_type, di.status, di.account_id,
			di.encrypted_object_key, di.key_version, di.wrapped_dek,
			COALESCE(di.content_sha256, ''), di.content_type, di.byte_size
		FROM redstone_market_orders o
		JOIN redstone_market_products p ON p.id = o.product_id
		JOIN redstone_market_delivery_items di ON di.id = o.delivery_item_id
		WHERE o.id = $1 AND o.buyer_user_id = $2
	`, orderID, buyerUserID).Scan(
		&orderStatus, &item.ID, &item.ProductType, &item.Status, &item.AccountID,
		&item.EncryptedObjectKey, &item.KeyVersion, &item.WrappedDEK,
		&item.ContentSHA256, &item.ContentType, &item.ByteSize,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return DeliveryItem{}, ErrDeliveryForbidden
	}
	if err != nil {
		return DeliveryItem{}, err
	}
	if orderStatus != "paid" && orderStatus != "delivered" {
		return DeliveryItem{}, ErrDeliveryNotAllowed
	}
	if item.ProductType != "file" || (item.Status != "reserved" && item.Status != "delivered") {
		return DeliveryItem{}, ErrDeliveryItemUnavailable
	}
	var claimed bool
	if err := r.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM redstone_market_delivery_audit
			WHERE order_id = $1 AND event_type IN ('viewed', 'downloaded')
		)
	`, orderID).Scan(&claimed); err != nil {
		return DeliveryItem{}, err
	}
	if claimed {
		return DeliveryItem{}, ErrDeliveryAlreadyViewed
	}
	return item, nil
}

func (r *sqlRepository) ClaimFileDelivery(ctx context.Context, buyerUserID, orderID int64, requestID string) (_ FileDelivery, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return FileDelivery{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var itemID int64
	var orderStatus string
	var itemStatus string
	err = tx.QueryRowContext(ctx, `
		SELECT o.delivery_item_id, o.status, di.status
		FROM redstone_market_orders o
		JOIN redstone_market_delivery_items di ON di.id = o.delivery_item_id
		WHERE o.id = $1 AND o.buyer_user_id = $2
		FOR UPDATE OF o, di
	`, orderID, buyerUserID).Scan(&itemID, &orderStatus, &itemStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return FileDelivery{}, ErrDeliveryForbidden
	}
	if err != nil {
		return FileDelivery{}, err
	}
	if orderStatus != "paid" && orderStatus != "delivered" {
		return FileDelivery{}, ErrDeliveryNotAllowed
	}
	if itemStatus != "reserved" && itemStatus != "delivered" {
		return FileDelivery{}, ErrDeliveryItemUnavailable
	}
	var claimed bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM redstone_market_delivery_audit
			WHERE order_id = $1 AND event_type IN ('viewed', 'downloaded')
		)
	`, orderID).Scan(&claimed); err != nil {
		return FileDelivery{}, err
	}
	if claimed {
		return FileDelivery{}, ErrDeliveryAlreadyViewed
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO redstone_market_delivery_audit (order_id, event_type, actor_user_id, request_id)
		VALUES ($1, 'downloaded', $2, NULLIF($3, ''))
	`, orderID, buyerUserID, requestID); err != nil {
		return FileDelivery{}, err
	}

	deliveredAt := time.Now().UTC()
	if orderStatus == "paid" {
		if err := markDeliveredTx(ctx, tx, orderID, deliveredAt); err != nil {
			return FileDelivery{}, err
		}
	} else {
		var existing sql.NullTime
		if err := tx.QueryRowContext(ctx, `SELECT delivered_at FROM redstone_market_orders WHERE id = $1`, orderID).Scan(&existing); err != nil {
			return FileDelivery{}, err
		}
		if existing.Valid {
			deliveredAt = existing.Time.UTC()
		}
	}
	if err := tx.Commit(); err != nil {
		return FileDelivery{}, err
	}
	return FileDelivery{OrderID: orderID, DeliveryItemID: itemID, DeliveredAt: deliveredAt}, nil
}
