package market

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

const maxDeliveryUploadBytes int = 32 << 20

var (
	ErrDeliveryUploadUnavailable = errors.New("market delivery upload is unavailable")
	ErrDeliveryUploadContent     = errors.New("market delivery upload content is invalid")
	ErrDeliveryUploadProduct     = errors.New("market delivery upload product is not editable")
	ErrDeliveryScanRejected      = errors.New("market delivery content was rejected by the scanner")
)

// DeliveryScanner is a server-side malware/content scanner boundary. Scanner
// implementations must not log, cache, or submit delivery plaintext to an
// external moderation service. An unavailable scanner is fail-closed.
type DeliveryScanner interface {
	Scan(context.Context, DeliveryScanInput) (SellerScanResult, error)
}

type DeliveryScanInput struct {
	ProductType string
	ContentType string
	Content     []byte
}

type UploadSellerDeliveryRequest struct {
	SellerUserID int64
	ProductID    int64
	ContentType  string
	Content      []byte
}

func (r UploadSellerDeliveryRequest) Validate() error {
	if r.SellerUserID <= 0 || r.ProductID <= 0 || len(r.Content) == 0 || len(r.Content) > maxDeliveryUploadBytes {
		return ErrDeliveryUploadContent
	}
	if strings.TrimSpace(r.ContentType) == "" || len(r.ContentType) > 120 {
		return ErrDeliveryUploadContent
	}
	return nil
}

// DeliveryInventoryRepository is deliberately separate from SellerRepository
// so existing seller tests and consumers are not forced to implement upload
// internals.
type DeliveryInventoryRepository interface {
	GetSellerDeliveryProductType(context.Context, int64, int64) (string, error)
	InsertSellerDeliveryItem(context.Context, EncryptedSellerDeliveryItem) (SellerProduct, error)
}

type EncryptedSellerDeliveryItem struct {
	SellerUserID       int64
	ProductID          int64
	ProductType        string
	EncryptedObjectKey string
	KeyVersion         string
	WrappedDEK         []byte
	ContentSHA256      string
	ContentType        string
	ByteSize           int64

	contentDecision ContentModerationDecision
}

func (s *Service) SetDeliveryScanner(scanner DeliveryScanner) {
	if s != nil {
		s.deliveryScanner = scanner
	}
}

func (s *Service) UploadSellerDelivery(ctx context.Context, request UploadSellerDeliveryRequest) (SellerProduct, error) {
	if err := request.Validate(); err != nil {
		return SellerProduct{}, sellerApplicationError(err)
	}
	defer zeroBytes(request.Content)
	if s == nil || s.deliveryResolver == nil || s.deliveryScanner == nil {
		return SellerProduct{}, sellerApplicationError(ErrDeliveryUploadUnavailable)
	}
	resolver, ok := s.deliveryResolver.(*EncryptedDeliveryResolver)
	if !ok {
		return SellerProduct{}, sellerApplicationError(ErrDeliveryUploadUnavailable)
	}
	repository, ok := s.repository.(DeliveryInventoryRepository)
	if !ok {
		return SellerProduct{}, sellerApplicationError(ErrDeliveryUploadUnavailable)
	}
	productType, err := repository.GetSellerDeliveryProductType(ctx, request.SellerUserID, request.ProductID)
	if err != nil {
		return SellerProduct{}, sellerApplicationError(err)
	}
	if productType != "text_key" && productType != "card_key" && productType != "file" {
		return SellerProduct{}, sellerApplicationError(ErrDeliveryUploadProduct)
	}
	decision, err := s.scanDeliveryContent(ctx, productType, request.Content)
	if err != nil {
		return SellerProduct{}, sellerApplicationError(err)
	}
	if decision.Verdict == ContentModerationRejected {
		productID := request.ProductID
		if err := s.recordRejectedContent(ctx, RecordContentReviewRequest{
			SellerUserID: request.SellerUserID, ProductID: &productID, Scope: ContentScopeDeliveryContent, Decision: decision,
		}); err != nil {
			return SellerProduct{}, sellerApplicationError(err)
		}
		return SellerProduct{}, sellerApplicationError(ErrContentModerationRejected)
	}
	objectKey := "redstone-market/delivery/" + uuid.NewString()
	payload, err := resolver.Store(ctx, objectKey, request.Content)
	if err != nil {
		return SellerProduct{}, sellerApplicationError(err)
	}
	stored := true
	defer func() {
		if stored {
			_ = resolver.store.Delete(context.Background(), objectKey)
		}
	}()
	product, err := repository.InsertSellerDeliveryItem(ctx, EncryptedSellerDeliveryItem{
		SellerUserID: request.SellerUserID, ProductID: request.ProductID, ProductType: productType,
		EncryptedObjectKey: objectKey, KeyVersion: resolver.cipher.KeyVersion(), WrappedDEK: payload.WrappedDEK,
		ContentType: request.ContentType, ByteSize: int64(len(request.Content)), contentDecision: decision,
	})
	if err != nil {
		return SellerProduct{}, sellerApplicationError(err)
	}
	stored = false
	return product, nil
}

// UploadOfficialDelivery uses the same envelope-encryption and private-object
// queue as a user listing. It is exposed only through an administrator route
// and intentionally returns no plaintext.
func (s *Service) UploadOfficialDelivery(ctx context.Context, request UploadSellerDeliveryRequest) (Product, error) {
	if err := request.Validate(); err != nil {
		return Product{}, sellerApplicationError(err)
	}
	defer zeroBytes(request.Content)
	if s == nil || s.deliveryResolver == nil || s.deliveryScanner == nil {
		return Product{}, sellerApplicationError(ErrDeliveryUploadUnavailable)
	}
	resolver, ok := s.deliveryResolver.(*EncryptedDeliveryResolver)
	if !ok {
		return Product{}, sellerApplicationError(ErrDeliveryUploadUnavailable)
	}
	repository, err := s.officialRepository()
	if err != nil {
		return Product{}, sellerApplicationError(err)
	}
	productType, err := repository.GetOfficialDeliveryProductType(ctx, request.SellerUserID, request.ProductID)
	if err != nil {
		return Product{}, sellerApplicationError(err)
	}
	decision, err := s.scanDeliveryContent(ctx, productType, request.Content)
	if err != nil {
		return Product{}, sellerApplicationError(err)
	}
	if decision.Verdict == ContentModerationRejected {
		productID := request.ProductID
		if err := s.recordRejectedContent(ctx, RecordContentReviewRequest{
			SellerUserID: request.SellerUserID, ProductID: &productID, Scope: ContentScopeDeliveryContent, Decision: decision,
		}); err != nil {
			return Product{}, sellerApplicationError(err)
		}
		return Product{}, sellerApplicationError(ErrContentModerationRejected)
	}
	objectKey := "redstone-market/delivery/" + uuid.NewString()
	payload, err := resolver.Store(ctx, objectKey, request.Content)
	if err != nil {
		return Product{}, sellerApplicationError(err)
	}
	stored := true
	defer func() {
		if stored {
			_ = resolver.store.Delete(context.Background(), objectKey)
		}
	}()
	product, err := repository.InsertOfficialDeliveryItem(ctx, EncryptedSellerDeliveryItem{
		SellerUserID: request.SellerUserID, ProductID: request.ProductID, ProductType: productType,
		EncryptedObjectKey: objectKey, KeyVersion: resolver.cipher.KeyVersion(), WrappedDEK: payload.WrappedDEK,
		ContentType: request.ContentType, ByteSize: int64(len(request.Content)), contentDecision: decision,
	})
	if err != nil {
		return Product{}, sellerApplicationError(err)
	}
	stored = false
	return product, nil
}

func (r *sqlRepository) GetSellerDeliveryProductType(ctx context.Context, sellerUserID, productID int64) (string, error) {
	var productType string
	err := r.db.QueryRowContext(ctx, `
		SELECT product_type
		FROM redstone_market_products
		WHERE id = $1 AND seller_user_id = $2 AND seller_kind = 'user'
			AND status IN ('draft', 'pending_scan', 'active', 'sold_out')
	`, productID, sellerUserID).Scan(&productType)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrDeliveryUploadProduct
	}
	return productType, err
}

func (r *sqlRepository) InsertSellerDeliveryItem(ctx context.Context, item EncryptedSellerDeliveryItem) (_ SellerProduct, err error) {
	decision := item.contentDecision
	if !decision.valid() {
		decision = contentDecision(ContentModerationPassed, nil, deliveryItemFallbackDigest(item))
	}
	if decision.Verdict == ContentModerationRejected {
		return SellerProduct{}, ErrContentModerationRejected
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return SellerProduct{}, err
	}
	defer func() { _ = tx.Rollback() }()
	product, err := r.lockSellerProduct(ctx, tx, item.SellerUserID, item.ProductID)
	if err != nil {
		return SellerProduct{}, err
	}
	if product.ProductType != item.ProductType || !deliveryUploadAllowedStatus(product.Status) {
		return SellerProduct{}, ErrDeliveryUploadProduct
	}
	if _, err := r.lockSellerNormalBalance(ctx, tx, item.SellerUserID); err != nil {
		return SellerProduct{}, err
	}
	var ordinal int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal) + 1, 0) FROM redstone_market_delivery_items WHERE product_id = $1`, item.ProductID).Scan(&ordinal); err != nil {
		return SellerProduct{}, err
	}
	var deliveryItemID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO redstone_market_delivery_items (
			product_id, ordinal, status, encrypted_object_key, key_version, wrapped_dek,
			content_sha256, content_type, byte_size, account_id
		) VALUES ($1, $2, 'available', $3, $4, $5, $6, $7, $8, NULL)
		RETURNING id
	`, item.ProductID, ordinal, item.EncryptedObjectKey, item.KeyVersion, item.WrappedDEK,
		item.ContentSHA256, item.ContentType, item.ByteSize).Scan(&deliveryItemID)
	if err != nil {
		return SellerProduct{}, err
	}
	if err := enqueueDeliveryScan(ctx, tx, deliveryItemID, item.ProductID); err != nil {
		return SellerProduct{}, err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE redstone_market_products
		SET inventory_total = inventory_total + 1, status = 'pending_scan', risk_status = 'pending',
			scan_requested_at = NOW(), scan_completed_at = NULL, scan_failure_reason = '', updated_at = NOW()
		WHERE id = $1
	`, item.ProductID); err != nil {
		return SellerProduct{}, err
	}
	deliveryItemIDCopy := deliveryItemID
	review, err := r.recordContentReviewTx(ctx, tx, RecordContentReviewRequest{
		SellerUserID: item.SellerUserID, ProductID: &item.ProductID, DeliveryItemID: &deliveryItemIDCopy,
		Scope: ContentScopeDeliveryContent, Decision: decision,
	}, time.Now().UTC())
	if err != nil {
		return SellerProduct{}, err
	}
	if decision.Verdict == ContentModerationManualReview {
		if err := suspendProductForContentReview(ctx, tx, item.ProductID, decision, review.ID, item.SellerUserID, "content_review_pending"); err != nil {
			return SellerProduct{}, err
		}
	}
	result, err := r.loadSellerProduct(ctx, tx, item.SellerUserID, item.ProductID)
	if err != nil {
		return SellerProduct{}, err
	}
	if err := tx.Commit(); err != nil {
		return SellerProduct{}, err
	}
	return result, nil
}

func (r *sqlRepository) GetOfficialDeliveryProductType(ctx context.Context, sellerUserID, productID int64) (string, error) {
	var productType string
	err := r.db.QueryRowContext(ctx, `
		SELECT product_type FROM redstone_market_products
		WHERE id = $1 AND seller_user_id = $2 AND seller_kind = 'official'
			AND status IN ('draft', 'pending_scan', 'active', 'sold_out')
	`, productID, sellerUserID).Scan(&productType)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrOfficialProductUnavailable
	}
	return productType, err
}

func (r *sqlRepository) InsertOfficialDeliveryItem(ctx context.Context, item EncryptedSellerDeliveryItem) (_ Product, err error) {
	decision := item.contentDecision
	if !decision.valid() {
		decision = contentDecision(ContentModerationPassed, nil, deliveryItemFallbackDigest(item))
	}
	if decision.Verdict == ContentModerationRejected {
		return Product{}, ErrContentModerationRejected
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Product{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var productType, status string
	err = tx.QueryRowContext(ctx, `
		SELECT product_type, status FROM redstone_market_products
		WHERE id = $1 AND seller_user_id = $2 AND seller_kind = 'official'
		FOR UPDATE
	`, item.ProductID, item.SellerUserID).Scan(&productType, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return Product{}, ErrOfficialProductUnavailable
	}
	if err != nil {
		return Product{}, err
	}
	if productType != item.ProductType || !deliveryUploadAllowedStatus(status) {
		return Product{}, ErrDeliveryUploadProduct
	}
	var ordinal int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal) + 1, 0) FROM redstone_market_delivery_items WHERE product_id = $1`, item.ProductID).Scan(&ordinal); err != nil {
		return Product{}, err
	}
	var deliveryItemID int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO redstone_market_delivery_items (
			product_id, ordinal, status, encrypted_object_key, key_version, wrapped_dek,
			content_sha256, content_type, byte_size, account_id
		) VALUES ($1, $2, 'available', $3, $4, $5, $6, $7, $8, NULL)
		RETURNING id
	`, item.ProductID, ordinal, item.EncryptedObjectKey, item.KeyVersion, item.WrappedDEK,
		item.ContentSHA256, item.ContentType, item.ByteSize).Scan(&deliveryItemID); err != nil {
		return Product{}, err
	}
	if err := enqueueDeliveryScan(ctx, tx, deliveryItemID, item.ProductID); err != nil {
		return Product{}, err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE redstone_market_products
		SET inventory_total = inventory_total + 1, status = 'pending_scan', risk_status = 'pending',
			scan_requested_at = NOW(), scan_completed_at = NULL, scan_failure_reason = '', updated_at = NOW()
		WHERE id = $1
	`, item.ProductID)
	if err != nil {
		return Product{}, err
	}
	deliveryItemIDCopy := deliveryItemID
	review, err := r.recordContentReviewTx(ctx, tx, RecordContentReviewRequest{
		SellerUserID: item.SellerUserID, ProductID: &item.ProductID, DeliveryItemID: &deliveryItemIDCopy,
		Scope: ContentScopeDeliveryContent, Decision: decision,
	}, time.Now().UTC())
	if err != nil {
		return Product{}, err
	}
	if decision.Verdict == ContentModerationManualReview {
		if err := suspendProductForContentReview(ctx, tx, item.ProductID, decision, review.ID, item.SellerUserID, "content_review_pending"); err != nil {
			return Product{}, err
		}
	}
	product, err := loadMarketProduct(ctx, tx, item.ProductID)
	if err != nil {
		return Product{}, err
	}
	if err := tx.Commit(); err != nil {
		return Product{}, err
	}
	return product, nil
}

func deliveryUploadAllowedStatus(status string) bool {
	switch status {
	case "draft", "pending_scan", "active", "sold_out":
		return true
	default:
		return false
	}
}

func enqueueDeliveryScan(ctx context.Context, tx *sql.Tx, deliveryItemID, productID int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO redstone_market_delivery_scan_jobs (delivery_item_id, product_id, state, available_at)
		VALUES ($1, $2, 'pending', NOW())
		ON CONFLICT (delivery_item_id) DO NOTHING
	`, deliveryItemID, productID)
	return err
}

func deliveryItemFallbackDigest(item EncryptedSellerDeliveryItem) string {
	return contentDigest(ContentModerationInput{
		Scope: ContentScopeDeliveryContent, ProductType: item.ProductType,
		Content: []byte(item.EncryptedObjectKey),
	})
}

func (r *sqlRepository) ApplyOfficialScanResult(ctx context.Context, productID int64, result SellerScanResult) (_ Product, err error) {
	if productID <= 0 || (result != SellerScanPassed && result != SellerScanRejected && result != SellerScanFlagged) {
		return Product{}, ErrInvalidSellerScanResult
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Product{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	if err := tx.QueryRowContext(ctx, `
		SELECT status FROM redstone_market_products WHERE id = $1 AND seller_kind = 'official' FOR UPDATE
	`, productID).Scan(&status); errors.Is(err, sql.ErrNoRows) {
		return Product{}, ErrOfficialProductUnavailable
	} else if err != nil {
		return Product{}, err
	}
	if status != "pending_scan" {
		return Product{}, ErrSellerProductNotAwaitingScan
	}
	nextStatus, riskStatus, failure := "active", string(SellerScanPassed), ""
	if result == SellerScanRejected {
		nextStatus, riskStatus, failure = "suspended", string(SellerScanRejected), "scanner_rejected"
	} else if result == SellerScanFlagged {
		nextStatus, riskStatus, failure = "suspended", string(SellerScanFlagged), "scanner_flagged"
	}
	publishNow := nextStatus == "active"
	update, err := tx.ExecContext(ctx, `
		UPDATE redstone_market_products
		SET status = $2, risk_status = $3, scan_completed_at = NOW(), scan_failure_reason = $4,
			published_at = CASE WHEN $5 THEN COALESCE(published_at, NOW()) ELSE published_at END,
			updated_at = NOW()
		WHERE id = $1 AND inventory_total > inventory_reserved
	`, productID, nextStatus, riskStatus, failure, publishNow)
	if err != nil {
		return Product{}, err
	}
	if affected, err := update.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return Product{}, err
		}
		return Product{}, ErrOfficialProductUnavailable
	}
	product, err := loadMarketProduct(ctx, tx, productID)
	if err != nil {
		return Product{}, err
	}
	if err := tx.Commit(); err != nil {
		return Product{}, err
	}
	return product, nil
}
