package market

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	infraerrors "github.com/silent-QAQ/redstoneapi/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

const DefaultSellerCNYPerBalance = "1.00000000"

var (
	ErrInvalidSellerUserID             = errors.New("market seller user id must be positive")
	ErrSellerRepositoryUnavailable     = errors.New("market seller repository is unavailable")
	ErrSellerProductNotFound           = errors.New("market seller product was not found")
	ErrSellerProductNotEditable        = errors.New("market seller product is not editable")
	ErrSellerProductNotPublishable     = errors.New("market seller product is not publishable")
	ErrSellerFrozen                    = errors.New("market seller is frozen by governance")
	ErrSellerNotEligible               = errors.New("market seller normal balance is below the listing threshold")
	ErrSellerListingLimitReached       = errors.New("market seller listing limit has been reached")
	ErrSellerProductNoDeliveryStock    = errors.New("market seller product has no delivery stock")
	ErrUnsupportedSellerProductType    = errors.New("market seller product type is unsupported")
	ErrAccountReferenceUnavailable     = errors.New("market account reference is unavailable or not owned by the seller")
	ErrInvalidSellerProductTitle       = errors.New("market seller product title is invalid")
	ErrInvalidSellerProductDescription = errors.New("market seller product description is invalid")
	ErrInvalidSellerProductPrice       = errors.New("market seller product price is invalid")
	ErrInvalidSellerProductAccount     = errors.New("market seller product account reference is invalid")
	ErrInvalidSellerScanResult         = errors.New("market seller scan result is invalid")
	ErrSellerProductNotAwaitingScan    = errors.New("market seller product is not awaiting a scan")
	ErrOfficialProductUnavailable      = errors.New("market official product is unavailable")
)

// SellerProduct is the seller-only product projection. It intentionally
// excludes encrypted delivery object keys, wrapped keys, hashes, and account
// credentials. Delivery inventory is reported as counts only.
type SellerProduct struct {
	Product
	AvailableDeliveryItems int        `json:"available_delivery_items"`
	ReservedDeliveryItems  int        `json:"reserved_delivery_items"`
	DeliveredDeliveryItems int        `json:"delivered_delivery_items"`
	ScanRequestedAt        *time.Time `json:"scan_requested_at,omitempty"`
	ScanCompletedAt        *time.Time `json:"scan_completed_at,omitempty"`
	ScanFailureReason      string     `json:"scan_failure_reason,omitempty"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
	PublishedAt            *time.Time `json:"published_at,omitempty"`
}

// SellerDashboard is the read model used by the seller center. ListingLimit
// is calculated from normal balance only. Bound balance is intentionally not
// read, because it can never unlock or pay for marketplace activity.
type SellerDashboard struct {
	SellerUserID   int64           `json:"seller_user_id"`
	NormalBalance  decimal.Decimal `json:"normal_balance"`
	CNYPerBalance  decimal.Decimal `json:"cny_per_balance"`
	ListingLimit   int             `json:"listing_limit"`
	ActiveListings int             `json:"active_listings"`
	CanPublish     bool            `json:"can_publish"`
}

// SellerRechargeMultiplierResolver returns the active amount of ordinary
// balance credited by one CNY. Seller eligibility uses its reciprocal as the
// CNY value of a balance unit and never applies to checkout or token billing.
type SellerRechargeMultiplierResolver func(context.Context) (float64, error)

// SellerRechargeMultiplierProvider is implemented by the existing payment
// configuration service. It keeps marketplace policy independent from the
// payment service's concrete implementation.
type SellerRechargeMultiplierProvider interface {
	GetBalanceRechargeMultiplier(context.Context) (float64, error)
}

// CreateSellerDraftRequest has no delivery payload by design. Text, card, and
// file contents must be encrypted by the separate upload pipeline before they
// can produce delivery items. This prevents plaintext from reaching this API.
type CreateSellerDraftRequest struct {
	SellerUserID int64
	ProductType  string
	Title        string
	Description  string
	UnitPrice    decimal.Decimal
	AccountID    *int64

	contentDecision ContentModerationDecision
}

func (r CreateSellerDraftRequest) Validate() error {
	if r.SellerUserID <= 0 {
		return ErrInvalidSellerUserID
	}
	if !validSellerProductType(r.ProductType) {
		return ErrUnsupportedSellerProductType
	}
	if strings.TrimSpace(r.Title) == "" || len([]rune(strings.TrimSpace(r.Title))) > 160 {
		return ErrInvalidSellerProductTitle
	}
	if len([]rune(r.Description)) > 20000 {
		return ErrInvalidSellerProductDescription
	}
	if !r.UnitPrice.IsPositive() || !r.UnitPrice.Equal(r.UnitPrice.Round(8)) {
		return ErrInvalidSellerProductPrice
	}
	if r.ProductType == "account_reference" {
		// The repository verifies ownership and account health under a row lock
		// before creating the listing and again before escrow begins.
		if r.AccountID == nil || *r.AccountID <= 0 {
			return ErrInvalidSellerProductAccount
		}
		return nil
	}
	if r.AccountID != nil {
		return ErrInvalidSellerProductAccount
	}
	return nil
}

// UpdateSellerDraftRequest is limited to mutable product metadata. Inventory
// is deliberately fed only by the encrypted upload pipeline, which inserts
// actual delivery items and never accepts plaintext here.
type UpdateSellerDraftRequest struct {
	SellerUserID int64
	ProductID    int64
	ProductType  string
	Title        string
	Description  string
	UnitPrice    decimal.Decimal
	AccountID    *int64

	contentDecision ContentModerationDecision
}

func (r UpdateSellerDraftRequest) Validate() error {
	return CreateSellerDraftRequest{
		SellerUserID: r.SellerUserID,
		ProductType:  r.ProductType,
		Title:        r.Title,
		Description:  r.Description,
		UnitPrice:    r.UnitPrice,
		AccountID:    r.AccountID,
	}.ValidateWithProductID(r.ProductID)
}

func (r CreateSellerDraftRequest) ValidateWithProductID(productID int64) error {
	if productID <= 0 {
		return ErrInvalidProductID
	}
	return r.Validate()
}

type PublishSellerProductRequest struct {
	SellerUserID int64
	ProductID    int64
}

type ArchiveSellerProductRequest struct {
	SellerUserID int64
	ProductID    int64
}

func (r ArchiveSellerProductRequest) Validate() error {
	if r.SellerUserID <= 0 {
		return ErrInvalidSellerUserID
	}
	if r.ProductID <= 0 {
		return ErrInvalidProductID
	}
	return nil
}

func (r PublishSellerProductRequest) Validate() error {
	if r.SellerUserID <= 0 {
		return ErrInvalidSellerUserID
	}
	if r.ProductID <= 0 {
		return ErrInvalidProductID
	}
	return nil
}

// SellerScanResult is intentionally internal. There is no public route that
// lets a seller mark their own item as scanned.
type SellerScanResult string

const (
	SellerScanPassed   SellerScanResult = "passed"
	SellerScanRejected SellerScanResult = "rejected"
	SellerScanFlagged  SellerScanResult = "flagged"
)

type ApplySellerScanResultRequest struct {
	ProductID int64
	Result    SellerScanResult
}

// CreateOfficialProductRequest is intentionally available only through the
// existing protected admin route. Official listings are not subject to the
// user seller balance threshold, but still use the same encrypted inventory
// and scanner requirements as user listings.
type CreateOfficialProductRequest struct {
	SellerUserID int64
	ProductType  string
	Title        string
	Description  string
	UnitPrice    decimal.Decimal

	contentDecision ContentModerationDecision
}

func (r CreateOfficialProductRequest) Validate() error {
	if r.SellerUserID <= 0 {
		return ErrInvalidSellerUserID
	}
	if r.ProductType != "text_key" && r.ProductType != "card_key" && r.ProductType != "file" {
		return ErrUnsupportedSellerProductType
	}
	if strings.TrimSpace(r.Title) == "" || len([]rune(strings.TrimSpace(r.Title))) > 160 {
		return ErrInvalidSellerProductTitle
	}
	if len([]rune(r.Description)) > 20000 {
		return ErrInvalidSellerProductDescription
	}
	if !r.UnitPrice.IsPositive() || !r.UnitPrice.Equal(r.UnitPrice.Round(8)) {
		return ErrInvalidSellerProductPrice
	}
	return nil
}

func (r ApplySellerScanResultRequest) Validate() error {
	if r.ProductID <= 0 {
		return ErrInvalidProductID
	}
	if r.Result != SellerScanPassed && r.Result != SellerScanRejected && r.Result != SellerScanFlagged {
		return ErrInvalidSellerScanResult
	}
	return nil
}

func validSellerProductType(productType string) bool {
	switch productType {
	case "text_key", "card_key", "file", "account_reference":
		return true
	default:
		return false
	}
}

// SellerRepository is a separate optional capability so existing marketplace
// test doubles remain source compatible while seller publishing is introduced.
type SellerRepository interface {
	GetSellerDashboard(context.Context, int64, decimal.Decimal) (SellerDashboard, error)
	ListSellerProducts(context.Context, int64, int, int) ([]SellerProduct, int, error)
	CreateSellerDraft(context.Context, CreateSellerDraftRequest, decimal.Decimal) (SellerProduct, error)
	UpdateSellerDraft(context.Context, UpdateSellerDraftRequest) (SellerProduct, error)
	PublishSellerProduct(context.Context, PublishSellerProductRequest, decimal.Decimal) (SellerProduct, error)
	ArchiveSellerProduct(context.Context, ArchiveSellerProductRequest) (SellerProduct, error)
	ApplySellerScanResult(context.Context, ApplySellerScanResultRequest, decimal.Decimal) (SellerProduct, error)
}

type OfficialRepository interface {
	ListOfficialProducts(context.Context, int64, int, int) ([]Product, int, error)
	CreateOfficialProduct(context.Context, CreateOfficialProductRequest) (Product, error)
	GetOfficialDeliveryProductType(context.Context, int64, int64) (string, error)
	InsertOfficialDeliveryItem(context.Context, EncryptedSellerDeliveryItem) (Product, error)
	ApplyOfficialScanResult(context.Context, int64, SellerScanResult) (Product, error)
}

func (s *Service) sellerRepository() (SellerRepository, error) {
	if s == nil || s.repository == nil {
		return nil, ErrSellerRepositoryUnavailable
	}
	repository, ok := s.repository.(SellerRepository)
	if !ok {
		return nil, ErrSellerRepositoryUnavailable
	}
	return repository, nil
}

func sellerCNYPerBalance() decimal.Decimal {
	return decimal.RequireFromString(DefaultSellerCNYPerBalance)
}

func (s *Service) sellerCNYPerBalance(ctx context.Context) (decimal.Decimal, error) {
	if s == nil || s.sellerRechargeMultiplierResolver == nil {
		return sellerCNYPerBalance(), nil
	}
	multiplier, err := s.sellerRechargeMultiplierResolver(ctx)
	if err != nil {
		return decimal.Zero, err
	}
	if multiplier <= 0 {
		return decimal.Zero, ErrInvalidRechargeRate
	}
	return decimal.NewFromInt(1).Div(decimal.NewFromFloat(multiplier)), nil
}

func (s *Service) GetSellerDashboard(ctx context.Context, sellerUserID int64) (SellerDashboard, error) {
	if sellerUserID <= 0 {
		return SellerDashboard{}, sellerApplicationError(ErrInvalidSellerUserID)
	}
	repository, err := s.sellerRepository()
	if err != nil {
		return SellerDashboard{}, sellerApplicationError(err)
	}
	cnyPerBalance, err := s.sellerCNYPerBalance(ctx)
	if err != nil {
		return SellerDashboard{}, sellerApplicationError(err)
	}
	dashboard, err := repository.GetSellerDashboard(ctx, sellerUserID, cnyPerBalance)
	if err != nil {
		return SellerDashboard{}, sellerApplicationError(err)
	}
	return dashboard, nil
}

func (s *Service) ListSellerProducts(ctx context.Context, sellerUserID int64, limit, offset int) ([]SellerProduct, int, error) {
	if sellerUserID <= 0 {
		return nil, 0, sellerApplicationError(ErrInvalidSellerUserID)
	}
	if limit <= 0 || limit > 100 || offset < 0 {
		return nil, 0, sellerApplicationError(ErrInvalidPagination)
	}
	repository, err := s.sellerRepository()
	if err != nil {
		return nil, 0, sellerApplicationError(err)
	}
	products, total, err := repository.ListSellerProducts(ctx, sellerUserID, limit, offset)
	if err != nil {
		return nil, 0, sellerApplicationError(err)
	}
	return products, total, nil
}

func (s *Service) CreateSellerDraft(ctx context.Context, request CreateSellerDraftRequest) (SellerProduct, error) {
	if err := request.Validate(); err != nil {
		return SellerProduct{}, sellerApplicationError(err)
	}
	decision, err := s.scanProductMetadata(ctx, request.ProductType, request.Title, request.Description)
	if err != nil {
		return SellerProduct{}, sellerApplicationError(err)
	}
	if decision.Verdict == ContentModerationRejected {
		if err := s.recordRejectedContent(ctx, RecordContentReviewRequest{
			SellerUserID: request.SellerUserID, Scope: ContentScopeProductMetadata, Decision: decision,
		}); err != nil {
			return SellerProduct{}, sellerApplicationError(err)
		}
		return SellerProduct{}, sellerApplicationError(ErrContentModerationRejected)
	}
	request.contentDecision = decision
	repository, err := s.sellerRepository()
	if err != nil {
		return SellerProduct{}, sellerApplicationError(err)
	}
	cnyPerBalance, err := s.sellerCNYPerBalance(ctx)
	if err != nil {
		return SellerProduct{}, sellerApplicationError(err)
	}
	product, err := repository.CreateSellerDraft(ctx, normalizeCreateSellerDraftRequest(request), cnyPerBalance)
	if err != nil {
		return SellerProduct{}, sellerApplicationError(err)
	}
	return product, nil
}

func (s *Service) UpdateSellerDraft(ctx context.Context, request UpdateSellerDraftRequest) (SellerProduct, error) {
	if err := request.Validate(); err != nil {
		return SellerProduct{}, sellerApplicationError(err)
	}
	decision, err := s.scanProductMetadata(ctx, request.ProductType, request.Title, request.Description)
	if err != nil {
		return SellerProduct{}, sellerApplicationError(err)
	}
	if decision.Verdict == ContentModerationRejected {
		productID := request.ProductID
		if err := s.recordRejectedContent(ctx, RecordContentReviewRequest{
			SellerUserID: request.SellerUserID, ProductID: &productID, Scope: ContentScopeProductMetadata, Decision: decision,
		}); err != nil {
			return SellerProduct{}, sellerApplicationError(err)
		}
		return SellerProduct{}, sellerApplicationError(ErrContentModerationRejected)
	}
	request.contentDecision = decision
	repository, err := s.sellerRepository()
	if err != nil {
		return SellerProduct{}, sellerApplicationError(err)
	}
	request.Title = strings.TrimSpace(request.Title)
	request.Description = strings.TrimSpace(request.Description)
	product, err := repository.UpdateSellerDraft(ctx, request)
	if err != nil {
		return SellerProduct{}, sellerApplicationError(err)
	}
	return product, nil
}

func (s *Service) PublishSellerProduct(ctx context.Context, request PublishSellerProductRequest) (SellerProduct, error) {
	if err := request.Validate(); err != nil {
		return SellerProduct{}, sellerApplicationError(err)
	}
	repository, err := s.sellerRepository()
	if err != nil {
		return SellerProduct{}, sellerApplicationError(err)
	}
	cnyPerBalance, err := s.sellerCNYPerBalance(ctx)
	if err != nil {
		return SellerProduct{}, sellerApplicationError(err)
	}
	product, err := repository.PublishSellerProduct(ctx, request, cnyPerBalance)
	if err != nil {
		return SellerProduct{}, sellerApplicationError(err)
	}
	return product, nil
}

// ArchiveSellerProduct withdraws only the seller's own active listing. It
// never deletes a product or its order history. Restoring uses the ordinary
// PublishSellerProduct flow, including current-balance quota enforcement.
func (s *Service) ArchiveSellerProduct(ctx context.Context, request ArchiveSellerProductRequest) (SellerProduct, error) {
	if err := request.Validate(); err != nil {
		return SellerProduct{}, sellerApplicationError(err)
	}
	repository, err := s.sellerRepository()
	if err != nil {
		return SellerProduct{}, sellerApplicationError(err)
	}
	product, err := repository.ArchiveSellerProduct(ctx, request)
	if err != nil {
		return SellerProduct{}, sellerApplicationError(err)
	}
	return product, nil
}

// ApplySellerScanResult is called only by the trusted scanner pipeline, never
// by a browser. A passing result moves an eligible, stocked product directly
// to active, satisfying the automatic-listing requirement without trusting
// the seller to approve their own content.
func (s *Service) ApplySellerScanResult(ctx context.Context, request ApplySellerScanResultRequest) (SellerProduct, error) {
	if err := request.Validate(); err != nil {
		return SellerProduct{}, sellerApplicationError(err)
	}
	repository, err := s.sellerRepository()
	if err != nil {
		return SellerProduct{}, sellerApplicationError(err)
	}
	cnyPerBalance, err := s.sellerCNYPerBalance(ctx)
	if err != nil {
		return SellerProduct{}, sellerApplicationError(err)
	}
	product, err := repository.ApplySellerScanResult(ctx, request, cnyPerBalance)
	if err != nil {
		return SellerProduct{}, sellerApplicationError(err)
	}
	return product, nil
}

func (s *Service) officialRepository() (OfficialRepository, error) {
	if s == nil || s.repository == nil {
		return nil, ErrSellerRepositoryUnavailable
	}
	r, ok := s.repository.(OfficialRepository)
	if !ok {
		return nil, ErrSellerRepositoryUnavailable
	}
	return r, nil
}

func (s *Service) CreateOfficialProduct(ctx context.Context, request CreateOfficialProductRequest) (Product, error) {
	if err := request.Validate(); err != nil {
		return Product{}, sellerApplicationError(err)
	}
	decision, err := s.scanProductMetadata(ctx, request.ProductType, request.Title, request.Description)
	if err != nil {
		return Product{}, sellerApplicationError(err)
	}
	if decision.Verdict == ContentModerationRejected {
		if err := s.recordRejectedContent(ctx, RecordContentReviewRequest{
			SellerUserID: request.SellerUserID, Scope: ContentScopeProductMetadata, Decision: decision,
		}); err != nil {
			return Product{}, sellerApplicationError(err)
		}
		return Product{}, sellerApplicationError(ErrContentModerationRejected)
	}
	request.contentDecision = decision
	r, err := s.officialRepository()
	if err != nil {
		return Product{}, sellerApplicationError(err)
	}
	product, err := r.CreateOfficialProduct(ctx, request)
	return product, sellerApplicationError(err)
}

func (s *Service) ListOfficialProducts(ctx context.Context, sellerUserID int64, limit, offset int) ([]Product, int, error) {
	if sellerUserID <= 0 {
		return nil, 0, sellerApplicationError(ErrInvalidSellerUserID)
	}
	if limit <= 0 || limit > 100 || offset < 0 {
		return nil, 0, sellerApplicationError(ErrInvalidPagination)
	}
	repository, err := s.officialRepository()
	if err != nil {
		return nil, 0, sellerApplicationError(err)
	}
	products, total, err := repository.ListOfficialProducts(ctx, sellerUserID, limit, offset)
	return products, total, sellerApplicationError(err)
}

func sellerApplicationError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*infraerrors.ApplicationError); ok {
		return err
	}
	switch {
	case errors.Is(err, ErrInvalidSellerUserID),
		errors.Is(err, ErrInvalidSellerProductTitle),
		errors.Is(err, ErrInvalidSellerProductDescription),
		errors.Is(err, ErrInvalidSellerProductPrice),
		errors.Is(err, ErrInvalidSellerProductAccount),
		errors.Is(err, ErrUnsupportedSellerProductType),
		errors.Is(err, ErrInvalidSellerScanResult),
		errors.Is(err, ErrInvalidProductID),
		errors.Is(err, ErrInvalidPagination):
		return infraerrors.BadRequest("MARKET_SELLER_INVALID_REQUEST", "Invalid marketplace seller request").WithCause(err)
	case errors.Is(err, ErrAccountReferenceUnavailable):
		return infraerrors.Conflict("MARKET_ACCOUNT_REFERENCE_UNAVAILABLE", "Account references are not available until account ownership is verified").WithCause(err)
	case errors.Is(err, ErrSellerProductNotFound):
		return infraerrors.NotFound("MARKET_SELLER_PRODUCT_NOT_FOUND", "Marketplace product was not found").WithCause(err)
	case errors.Is(err, ErrDeliveryUploadUnavailable):
		return infraerrors.ServiceUnavailable("MARKET_DELIVERY_UPLOAD_UNAVAILABLE", "Delivery upload is not available").WithCause(err)
	case errors.Is(err, ErrDeliveryUploadContent):
		return infraerrors.BadRequest("MARKET_DELIVERY_UPLOAD_INVALID", "Invalid delivery upload").WithCause(err)
	case errors.Is(err, ErrContentModerationUnavailable):
		return infraerrors.ServiceUnavailable("MARKET_CONTENT_MODERATION_UNAVAILABLE", "Marketplace content moderation is not available").WithCause(err)
	case errors.Is(err, ErrContentModerationRejected):
		return infraerrors.Conflict("MARKET_CONTENT_REJECTED", "Marketplace content was rejected").WithCause(err)
	case errors.Is(err, ErrDeliveryUploadProduct), errors.Is(err, ErrDeliveryScanRejected):
		return infraerrors.Conflict("MARKET_DELIVERY_UPLOAD_REJECTED", "Delivery upload was rejected").WithCause(err)
	case errors.Is(err, ErrSellerNotEligible),
		errors.Is(err, ErrSellerListingLimitReached),
		errors.Is(err, ErrSellerProductNotEditable),
		errors.Is(err, ErrSellerProductNotPublishable),
		errors.Is(err, ErrSellerProductNoDeliveryStock),
		errors.Is(err, ErrSellerProductNotAwaitingScan):
		return infraerrors.Conflict("MARKET_SELLER_STATE_CONFLICT", "Marketplace seller product is not in a compatible state").WithCause(err)
	case errors.Is(err, ErrSellerFrozen):
		return infraerrors.Forbidden("MARKET_SELLER_FROZEN", "Marketplace seller is frozen").WithCause(err)
	case errors.Is(err, ErrSellerRepositoryUnavailable):
		return infraerrors.ServiceUnavailable("MARKET_SELLER_UNAVAILABLE", "Marketplace seller service is unavailable").WithCause(err)
	default:
		return infraerrors.InternalServer("MARKET_SELLER_OPERATION_FAILED", "Marketplace seller operation failed").WithCause(err)
	}
}

func normalizeCreateSellerDraftRequest(request CreateSellerDraftRequest) CreateSellerDraftRequest {
	request.Title = strings.TrimSpace(request.Title)
	request.Description = strings.TrimSpace(request.Description)
	return request
}

func sellerListingLimitForBalance(balance, cnyPerBalance decimal.Decimal) (int, error) {
	if balance.IsNegative() {
		return 0, nil
	}
	return SellerListingLimit(balance, cnyPerBalance)
}

func buildSellerDashboard(sellerUserID int64, normalBalance, cnyPerBalance decimal.Decimal, activeListings int) (SellerDashboard, error) {
	if sellerUserID <= 0 {
		return SellerDashboard{}, ErrInvalidSellerUserID
	}
	if activeListings < 0 {
		return SellerDashboard{}, fmt.Errorf("market active listing count is invalid")
	}
	limit, err := sellerListingLimitForBalance(normalBalance, cnyPerBalance)
	if err != nil {
		return SellerDashboard{}, err
	}
	return SellerDashboard{
		SellerUserID:   sellerUserID,
		NormalBalance:  normalBalance,
		CNYPerBalance:  cnyPerBalance,
		ListingLimit:   limit,
		ActiveListings: activeListings,
		CanPublish:     limit > activeListings,
	}, nil
}

func (r *sqlRepository) GetSellerDashboard(ctx context.Context, sellerUserID int64, cnyPerBalance decimal.Decimal) (SellerDashboard, error) {
	var normalBalance decimal.Decimal
	err := r.db.QueryRowContext(ctx, `
		SELECT balance FROM users WHERE id = $1 AND deleted_at IS NULL
	`, sellerUserID).Scan(&normalBalance)
	if errors.Is(err, sql.ErrNoRows) {
		return SellerDashboard{}, ErrInvalidSellerUserID
	}
	if err != nil {
		return SellerDashboard{}, err
	}
	var activeListings int
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM redstone_market_products
		WHERE seller_user_id = $1 AND seller_kind = 'user' AND status = 'active'
	`, sellerUserID).Scan(&activeListings); err != nil {
		return SellerDashboard{}, err
	}
	return buildSellerDashboard(sellerUserID, normalBalance, cnyPerBalance, activeListings)
}

func (r *sqlRepository) ListSellerProducts(ctx context.Context, sellerUserID int64, limit, offset int) ([]SellerProduct, int, error) {
	if sellerUserID <= 0 {
		return nil, 0, ErrInvalidSellerUserID
	}
	var total int
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM redstone_market_products
		WHERE seller_user_id = $1 AND seller_kind = 'user'
	`, sellerUserID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, sellerProductSelect+`
		WHERE p.seller_user_id = $1 AND p.seller_kind = 'user'
		ORDER BY p.updated_at DESC, p.id DESC
		LIMIT $2 OFFSET $3
	`, sellerUserID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	products := make([]SellerProduct, 0)
	for rows.Next() {
		product, scanErr := scanSellerProduct(rows)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		products = append(products, product)
	}
	return products, total, rows.Err()
}

func (r *sqlRepository) CreateSellerDraft(ctx context.Context, request CreateSellerDraftRequest, cnyPerBalance decimal.Decimal) (SellerProduct, error) {
	if err := request.Validate(); err != nil {
		return SellerProduct{}, err
	}
	decision, err := productMetadataDecision(ctx, request.ProductType, request.Title, request.Description, request.contentDecision)
	if err != nil {
		return SellerProduct{}, err
	}
	if decision.Verdict == ContentModerationRejected {
		return SellerProduct{}, ErrContentModerationRejected
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return SellerProduct{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := r.lockSellerBalance(ctx, tx, request.SellerUserID, cnyPerBalance); err != nil {
		return SellerProduct{}, err
	}
	if request.ProductType == "account_reference" {
		if err := r.lockOwnedMarketAccount(ctx, tx, request.SellerUserID, *request.AccountID); err != nil {
			return SellerProduct{}, err
		}
	}
	status, riskStatus, failureReason := "draft", "pending", ""
	if decision.Verdict == ContentModerationManualReview {
		status, riskStatus, failureReason = "suspended", "flagged", "content_review_pending"
	}
	var productID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO redstone_market_products (
			seller_user_id, seller_kind, product_type, title, description, unit_price,
			inventory_total, inventory_reserved, status, risk_status, account_id, scan_failure_reason
		)
		SELECT $1, 'user', $2, $3, $4, $5, 0, 0, $6, $7, $8, $9
		WHERE EXISTS (SELECT 1 FROM users WHERE id = $1 AND deleted_at IS NULL)
		RETURNING id
	`, request.SellerUserID, request.ProductType, request.Title, request.Description, request.UnitPrice,
		status, riskStatus, request.AccountID, failureReason).Scan(&productID)
	if errors.Is(err, sql.ErrNoRows) {
		return SellerProduct{}, ErrInvalidSellerUserID
	}
	if err != nil {
		return SellerProduct{}, err
	}
	if _, err := r.recordContentReviewTx(ctx, tx, RecordContentReviewRequest{
		SellerUserID: request.SellerUserID, ProductID: &productID, Scope: contentScopeForProductType(request.ProductType), Decision: decision,
	}, time.Now().UTC()); err != nil {
		return SellerProduct{}, err
	}
	product, err := r.loadSellerProduct(ctx, tx, request.SellerUserID, productID)
	if err != nil {
		return SellerProduct{}, err
	}
	if err := tx.Commit(); err != nil {
		return SellerProduct{}, err
	}
	return product, nil
}

func (r *sqlRepository) CreateOfficialProduct(ctx context.Context, request CreateOfficialProductRequest) (_ Product, err error) {
	if err := request.Validate(); err != nil {
		return Product{}, err
	}
	decision, err := productMetadataDecision(ctx, request.ProductType, request.Title, request.Description, request.contentDecision)
	if err != nil {
		return Product{}, err
	}
	if decision.Verdict == ContentModerationRejected {
		return Product{}, ErrContentModerationRejected
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Product{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var sellerID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, request.SellerUserID).Scan(&sellerID); errors.Is(err, sql.ErrNoRows) {
		return Product{}, ErrInvalidSellerUserID
	} else if err != nil {
		return Product{}, err
	}
	status, riskStatus, failureReason := "draft", "pending", ""
	if decision.Verdict == ContentModerationManualReview {
		status, riskStatus, failureReason = "suspended", "flagged", "content_review_pending"
	}
	var productID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO redstone_market_products (
			seller_user_id, seller_kind, product_type, title, description, unit_price,
			inventory_total, inventory_reserved, status, risk_status, account_id, scan_failure_reason
		) VALUES ($1, 'official', $2, $3, $4, $5, 0, 0, $6, $7, NULL, $8)
		RETURNING id
	`, request.SellerUserID, request.ProductType, strings.TrimSpace(request.Title), strings.TrimSpace(request.Description), request.UnitPrice,
		status, riskStatus, failureReason).Scan(&productID)
	if err != nil {
		return Product{}, err
	}
	if _, err := r.recordContentReviewTx(ctx, tx, RecordContentReviewRequest{
		SellerUserID: request.SellerUserID, ProductID: &productID, Scope: ContentScopeProductMetadata, Decision: decision,
	}, time.Now().UTC()); err != nil {
		return Product{}, err
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

func (r *sqlRepository) ListOfficialProducts(ctx context.Context, sellerUserID int64, limit, offset int) ([]Product, int, error) {
	if sellerUserID <= 0 {
		return nil, 0, ErrInvalidSellerUserID
	}
	var total int
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM redstone_market_products
		WHERE seller_user_id = $1 AND seller_kind = 'official'
	`, sellerUserID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, seller_user_id, seller_kind, product_type, title, description,
			unit_price, inventory_total, inventory_reserved, status, risk_status, account_id
		FROM redstone_market_products
		WHERE seller_user_id = $1 AND seller_kind = 'official'
		ORDER BY updated_at DESC, id DESC
		LIMIT $2 OFFSET $3
	`, sellerUserID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	products := make([]Product, 0)
	for rows.Next() {
		var product Product
		if err := rows.Scan(
			&product.ID, &product.SellerUserID, &product.SellerKind, &product.ProductType, &product.Title, &product.Description,
			&product.UnitPrice, &product.InventoryTotal, &product.InventoryReserved, &product.Status, &product.RiskStatus, &product.AccountID,
		); err != nil {
			return nil, 0, err
		}
		products = append(products, product)
	}
	return products, total, rows.Err()
}

type marketProductLoader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadMarketProduct(ctx context.Context, queryer marketProductLoader, productID int64) (Product, error) {
	var product Product
	err := queryer.QueryRowContext(ctx, `
		SELECT id, seller_user_id, seller_kind, product_type, title, description,
			unit_price, inventory_total, inventory_reserved, status, risk_status, account_id
		FROM redstone_market_products WHERE id = $1
	`, productID).Scan(
		&product.ID, &product.SellerUserID, &product.SellerKind, &product.ProductType, &product.Title, &product.Description,
		&product.UnitPrice, &product.InventoryTotal, &product.InventoryReserved, &product.Status, &product.RiskStatus, &product.AccountID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Product{}, ErrOfficialProductUnavailable
	}
	return product, err
}

func (r *sqlRepository) UpdateSellerDraft(ctx context.Context, request UpdateSellerDraftRequest) (SellerProduct, error) {
	if err := request.Validate(); err != nil {
		return SellerProduct{}, err
	}
	decision, err := productMetadataDecision(ctx, request.ProductType, request.Title, request.Description, request.contentDecision)
	if err != nil {
		return SellerProduct{}, err
	}
	if decision.Verdict == ContentModerationRejected {
		return SellerProduct{}, ErrContentModerationRejected
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return SellerProduct{}, err
	}
	defer func() { _ = tx.Rollback() }()
	product, err := r.lockSellerProduct(ctx, tx, request.SellerUserID, request.ProductID)
	if err != nil {
		return SellerProduct{}, err
	}
	if product.Status != "draft" {
		return SellerProduct{}, ErrSellerProductNotEditable
	}
	// Product, seller, then account is the marketplace-wide writer order. It
	// serializes draft updates with publishing and seller-freeze governance.
	if _, err := r.lockSellerNormalBalance(ctx, tx, request.SellerUserID); err != nil {
		return SellerProduct{}, err
	}
	if request.ProductType == "account_reference" {
		if err := r.lockOwnedMarketAccount(ctx, tx, request.SellerUserID, *request.AccountID); err != nil {
			return SellerProduct{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE redstone_market_products
		SET product_type = $1, title = $2, description = $3, unit_price = $4, account_id = $5,
			status = CASE WHEN $6 = 'manual_review' THEN 'suspended' ELSE status END,
			risk_status = CASE WHEN $6 = 'manual_review' THEN 'flagged' ELSE risk_status END,
			scan_failure_reason = CASE WHEN $6 = 'manual_review' THEN 'content_review_pending' ELSE scan_failure_reason END,
			updated_at = NOW()
		WHERE id = $7
	`, request.ProductType, request.Title, request.Description, request.UnitPrice, request.AccountID, decision.Verdict, product.ID); err != nil {
		return SellerProduct{}, err
	}
	review, err := r.recordContentReviewTx(ctx, tx, RecordContentReviewRequest{
		SellerUserID: request.SellerUserID, ProductID: &product.ID, Scope: contentScopeForProductType(request.ProductType), Decision: decision,
	}, time.Now().UTC())
	if err != nil {
		return SellerProduct{}, err
	}
	if decision.Verdict == ContentModerationManualReview {
		if err := suspendProductForContentReview(ctx, tx, product.ID, decision, review.ID, request.SellerUserID, "content_review_pending"); err != nil {
			return SellerProduct{}, err
		}
	}
	result, err := r.loadSellerProduct(ctx, tx, request.SellerUserID, request.ProductID)
	if err != nil {
		return SellerProduct{}, err
	}
	if err := tx.Commit(); err != nil {
		return SellerProduct{}, err
	}
	return result, nil
}

func (r *sqlRepository) PublishSellerProduct(ctx context.Context, request PublishSellerProductRequest, cnyPerBalance decimal.Decimal) (_ SellerProduct, err error) {
	if err := request.Validate(); err != nil {
		return SellerProduct{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return SellerProduct{}, err
	}
	defer func() { _ = tx.Rollback() }()

	product, err := r.lockSellerProduct(ctx, tx, request.SellerUserID, request.ProductID)
	if err != nil {
		return SellerProduct{}, err
	}
	if _, err := r.lockSellerNormalBalance(ctx, tx, request.SellerUserID); err != nil {
		return SellerProduct{}, err
	}
	if product.Status == "active" {
		if err := tx.Commit(); err != nil {
			return SellerProduct{}, err
		}
		return product, nil
	}
	if product.Status != "draft" && product.Status != "pending_scan" && product.Status != "archived" {
		return SellerProduct{}, ErrSellerProductNotPublishable
	}
	if product.ProductType == "account_reference" {
		if product.AccountID == nil {
			return SellerProduct{}, ErrAccountReferenceUnavailable
		}
		if err := r.lockOwnedMarketAccount(ctx, tx, request.SellerUserID, *product.AccountID); err != nil {
			return SellerProduct{}, err
		}
		if err := r.ensureAccountDeliveryItem(ctx, tx, product); err != nil {
			return SellerProduct{}, err
		}
		if err := r.beginAccountEscrow(ctx, tx, product); err != nil {
			return SellerProduct{}, err
		}
		product, err = r.loadSellerProduct(ctx, tx, request.SellerUserID, request.ProductID)
		if err != nil {
			return SellerProduct{}, err
		}
	} else if product.AvailableDeliveryItems <= 0 || product.InventoryTotal <= product.InventoryReserved {
		return SellerProduct{}, ErrSellerProductNoDeliveryStock
	}
	dashboard, err := r.lockSellerDashboard(ctx, tx, request.SellerUserID, cnyPerBalance, product.ID)
	if err != nil {
		return SellerProduct{}, err
	}
	if dashboard.ListingLimit == 0 {
		return SellerProduct{}, ErrSellerNotEligible
	}
	if product.ProductType == "account_reference" {
		if dashboard.ActiveListings >= dashboard.ListingLimit {
			return SellerProduct{}, ErrSellerListingLimitReached
		}
		if err := r.updateSellerProductState(ctx, tx, product.ID, "active", "passed", false); err != nil {
			return SellerProduct{}, err
		}
	} else if product.RiskStatus == "passed" {
		if dashboard.ActiveListings >= dashboard.ListingLimit {
			return SellerProduct{}, ErrSellerListingLimitReached
		}
		if err := r.updateSellerProductState(ctx, tx, product.ID, "active", "passed", false); err != nil {
			return SellerProduct{}, err
		}
	} else if product.RiskStatus == "pending" {
		if err := r.updateSellerProductState(ctx, tx, product.ID, "pending_scan", "pending", true); err != nil {
			return SellerProduct{}, err
		}
	} else {
		return SellerProduct{}, ErrSellerProductNotPublishable
	}
	result, err := r.loadSellerProduct(ctx, tx, request.SellerUserID, product.ID)
	if err != nil {
		return SellerProduct{}, err
	}
	if err := tx.Commit(); err != nil {
		return SellerProduct{}, err
	}
	return result, nil
}

func (r *sqlRepository) ArchiveSellerProduct(ctx context.Context, request ArchiveSellerProductRequest) (_ SellerProduct, err error) {
	if err := request.Validate(); err != nil {
		return SellerProduct{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return SellerProduct{}, err
	}
	defer func() { _ = tx.Rollback() }()
	product, err := r.lockSellerProduct(ctx, tx, request.SellerUserID, request.ProductID)
	if err != nil {
		return SellerProduct{}, err
	}
	if product.Status != "active" && product.Status != "draft" && product.Status != "pending_scan" {
		return SellerProduct{}, ErrSellerProductNotPublishable
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE redstone_market_products
		SET status = 'archived', updated_at = NOW()
		WHERE id = $1
	`, product.ID); err != nil {
		return SellerProduct{}, err
	}
	if product.ProductType == "account_reference" && product.InventoryReserved == 0 {
		if err := releaseAccountEscrowForProduct(ctx, tx, product.ID); err != nil {
			return SellerProduct{}, err
		}
	}
	result, err := r.loadSellerProduct(ctx, tx, request.SellerUserID, product.ID)
	if err != nil {
		return SellerProduct{}, err
	}
	if err := tx.Commit(); err != nil {
		return SellerProduct{}, err
	}
	return result, nil
}

func (r *sqlRepository) ApplySellerScanResult(ctx context.Context, request ApplySellerScanResultRequest, cnyPerBalance decimal.Decimal) (_ SellerProduct, err error) {
	if err := request.Validate(); err != nil {
		return SellerProduct{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return SellerProduct{}, err
	}
	defer func() { _ = tx.Rollback() }()

	product, err := r.lockProductForScan(ctx, tx, request.ProductID)
	if err != nil {
		return SellerProduct{}, err
	}
	if product.Status != "pending_scan" {
		return SellerProduct{}, ErrSellerProductNotAwaitingScan
	}
	status := "suspended"
	riskStatus := string(request.Result)
	if request.Result == SellerScanPassed {
		status = "draft"
		if product.AvailableDeliveryItems > 0 && product.InventoryTotal > product.InventoryReserved {
			dashboard, dashboardErr := r.lockSellerDashboard(ctx, tx, product.SellerUserID, cnyPerBalance, product.ID)
			if dashboardErr != nil && !errors.Is(dashboardErr, ErrSellerFrozen) {
				return SellerProduct{}, dashboardErr
			}
			if dashboardErr == nil && dashboard.ListingLimit > 0 && dashboard.ActiveListings < dashboard.ListingLimit {
				status = "active"
			} else if errors.Is(dashboardErr, ErrSellerFrozen) {
				status = "suspended"
			}
		}
	}
	if err := r.updateSellerProductScanResult(ctx, tx, product.ID, status, riskStatus); err != nil {
		return SellerProduct{}, err
	}
	result, err := r.loadSellerProduct(ctx, tx, product.SellerUserID, product.ID)
	if err != nil {
		return SellerProduct{}, err
	}
	if err := tx.Commit(); err != nil {
		return SellerProduct{}, err
	}
	return result, nil
}

type sellerProductLoader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (r *sqlRepository) loadSellerProduct(ctx context.Context, queryer sellerProductLoader, sellerUserID, productID int64) (SellerProduct, error) {
	product, err := scanSellerProduct(queryer.QueryRowContext(ctx, sellerProductSelect+`
		WHERE p.id = $1 AND p.seller_user_id = $2 AND p.seller_kind = 'user'
	`, productID, sellerUserID))
	if errors.Is(err, sql.ErrNoRows) {
		return SellerProduct{}, ErrSellerProductNotFound
	}
	if err != nil {
		return SellerProduct{}, err
	}
	return product, nil
}

func (r *sqlRepository) lockSellerProduct(ctx context.Context, tx *sql.Tx, sellerUserID, productID int64) (SellerProduct, error) {
	product, err := scanSellerProduct(tx.QueryRowContext(ctx, sellerProductSelect+`
		WHERE p.id = $1 AND p.seller_user_id = $2 AND p.seller_kind = 'user'
		FOR UPDATE OF p
	`, productID, sellerUserID))
	if errors.Is(err, sql.ErrNoRows) {
		return SellerProduct{}, ErrSellerProductNotFound
	}
	if err != nil {
		return SellerProduct{}, err
	}
	return product, nil
}

func (r *sqlRepository) lockProductForScan(ctx context.Context, tx *sql.Tx, productID int64) (SellerProduct, error) {
	product, err := scanSellerProduct(tx.QueryRowContext(ctx, sellerProductSelect+`
		WHERE p.id = $1 AND p.seller_kind = 'user'
		FOR UPDATE OF p
	`, productID))
	if errors.Is(err, sql.ErrNoRows) {
		return SellerProduct{}, ErrSellerProductNotFound
	}
	if err != nil {
		return SellerProduct{}, err
	}
	return product, nil
}

// lockOwnedMarketAccount verifies the existing sub2 account's ownership and
// health while holding its row lock. Credentials are deliberately never
// selected: a marketplace account delivery transfers the existing account
// ownership instead of copying credential material into this domain.
func (r *sqlRepository) lockOwnedMarketAccount(ctx context.Context, tx *sql.Tx, sellerUserID, accountID int64) error {
	var foundID int64
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM accounts
		WHERE id = $1 AND owner_user_id = $2 AND deleted_at IS NULL
			AND status = 'active'
		FOR UPDATE
	`, accountID, sellerUserID).Scan(&foundID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAccountReferenceUnavailable
	}
	if err != nil {
		return err
	}

	// BindAccount locks this same account row before attaching it to a sharing
	// room, so the account lock above serializes listing against new bindings.
	// Existing active/draining bindings make a transfer unsafe because a prior
	// room member could retain gateway access after the sale.
	var shared bool
	err = tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM redstone_account_share_room_accounts ra
			JOIN redstone_account_share_rooms room ON room.id = ra.room_id
			WHERE ra.account_id = $1
			  AND ra.state IN ('active', 'draining')
			  AND room.deleted_at IS NULL
			  AND room.status IN ('draft', 'pending_review', 'active', 'suspended')
		)
	`, accountID).Scan(&shared)
	if err != nil {
		return err
	}
	if shared {
		return ErrAccountReferenceUnavailable
	}
	return nil
}

// ensureAccountDeliveryItem is idempotent under the product row lock. The
// unique partial index added by the governance migration also prevents one
// account from being listed in two products across concurrent transactions.
func (r *sqlRepository) ensureAccountDeliveryItem(ctx context.Context, tx *sql.Tx, product SellerProduct) error {
	if product.AccountID == nil {
		return ErrAccountReferenceUnavailable
	}
	var itemID int64
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM redstone_market_delivery_items
		WHERE product_id = $1 AND account_id = $2
		FOR UPDATE
	`, product.ID, *product.AccountID).Scan(&itemID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO redstone_market_delivery_items (product_id, ordinal, status, account_id)
		VALUES ($1, 0, 'available', $2)
	`, product.ID, *product.AccountID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE redstone_market_products
		SET inventory_total = 1, inventory_reserved = 0, updated_at = NOW()
		WHERE id = $1 AND inventory_total = 0 AND inventory_reserved = 0
	`, product.ID)
	return err
}

// beginAccountEscrow makes a listed account unavailable to its current owner
// before the escrow row becomes active. The database trigger introduced by
// 9008 then rejects every account mutation until release or transfer.
func (r *sqlRepository) beginAccountEscrow(ctx context.Context, tx *sql.Tx, product SellerProduct) error {
	if product.AccountID == nil {
		return ErrAccountReferenceUnavailable
	}
	var escrowProductID int64
	var state string
	var previousSchedule bool
	err := tx.QueryRowContext(ctx, `
		SELECT product_id, state, prior_schedulable
		FROM redstone_market_account_escrows WHERE account_id = $1 FOR UPDATE
	`, *product.AccountID).Scan(&escrowProductID, &state, &previousSchedule)
	if err == nil {
		if escrowProductID != product.ID || (state != "listed" && state != "released") {
			return ErrAccountReferenceUnavailable
		}
		if state == "listed" {
			return nil
		}
		var schedulable bool
		if err := tx.QueryRowContext(ctx, `
			SELECT schedulable FROM accounts
			WHERE id = $1 AND owner_user_id = $2 AND deleted_at IS NULL AND status = 'active'
			FOR UPDATE
		`, *product.AccountID, product.SellerUserID).Scan(&schedulable); errors.Is(err, sql.ErrNoRows) {
			return ErrAccountReferenceUnavailable
		} else if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE accounts SET schedulable = false, updated_at = NOW() WHERE id = $1`, *product.AccountID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE redstone_market_account_escrows
			SET prior_schedulable = $2, state = 'listed', released_at = NULL, updated_at = NOW()
			WHERE account_id = $1 AND state = 'released'
		`, *product.AccountID, schedulable)
		return err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var schedulable bool
	if err := tx.QueryRowContext(ctx, `
		SELECT schedulable FROM accounts
		WHERE id = $1 AND owner_user_id = $2 AND deleted_at IS NULL AND status = 'active'
		FOR UPDATE
	`, *product.AccountID, product.SellerUserID).Scan(&schedulable); errors.Is(err, sql.ErrNoRows) {
		return ErrAccountReferenceUnavailable
	} else if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET schedulable = false, updated_at = NOW() WHERE id = $1`, *product.AccountID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO redstone_market_account_escrows
			(account_id, product_id, seller_user_id, prior_schedulable, state)
		VALUES ($1, $2, $3, $4, 'listed')
	`, *product.AccountID, product.ID, product.SellerUserID, schedulable)
	return err
}

// releaseAccountEscrowForProduct is used only before a sale. Once a delivery
// item is reserved, the escrow stays locked through delivery/refund/reversal.
func releaseAccountEscrowForProduct(ctx context.Context, tx *sql.Tx, productID int64) error {
	var accountID int64
	var priorSchedulable bool
	err := tx.QueryRowContext(ctx, `
		SELECT account_id, prior_schedulable
		FROM redstone_market_account_escrows
		WHERE product_id = $1 AND state = 'listed'
		FOR UPDATE
	`, productID).Scan(&accountID, &priorSchedulable)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	// Change escrow state first so the account mutation is accepted by the
	// database-level guard even when account APIs are bypassed by a worker.
	if _, err := tx.ExecContext(ctx, `
		UPDATE redstone_market_account_escrows
		SET state = 'released', released_at = NOW(), updated_at = NOW()
		WHERE account_id = $1 AND state = 'listed'
	`, accountID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET schedulable = $2, updated_at = NOW() WHERE id = $1`, accountID, priorSchedulable)
	return err
}

func (r *sqlRepository) lockSellerDashboard(ctx context.Context, tx *sql.Tx, sellerUserID int64, cnyPerBalance decimal.Decimal, excludedProductID int64) (SellerDashboard, error) {
	normalBalance, err := r.lockSellerNormalBalance(ctx, tx, sellerUserID)
	if err != nil {
		return SellerDashboard{}, err
	}
	var activeListings int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM redstone_market_products
		WHERE seller_user_id = $1 AND seller_kind = 'user' AND status = 'active' AND id <> $2
	`, sellerUserID, excludedProductID).Scan(&activeListings); err != nil {
		return SellerDashboard{}, err
	}
	return buildSellerDashboard(sellerUserID, normalBalance, cnyPerBalance, activeListings)
}

func (r *sqlRepository) lockSellerBalance(ctx context.Context, tx *sql.Tx, sellerUserID int64, cnyPerBalance decimal.Decimal) (SellerDashboard, error) {
	normalBalance, err := r.lockSellerNormalBalance(ctx, tx, sellerUserID)
	if err != nil {
		return SellerDashboard{}, err
	}
	dashboard, err := buildSellerDashboard(sellerUserID, normalBalance, cnyPerBalance, 0)
	if err != nil {
		return SellerDashboard{}, err
	}
	if dashboard.ListingLimit == 0 {
		return SellerDashboard{}, ErrSellerNotEligible
	}
	return dashboard, nil
}

// lockSellerNormalBalance is the user-row leg of every seller-side mutation.
// Seller-freeze operations take product rows before this row, so product ->
// seller is the required lock order for listing, scan, and upload flows.
func (r *sqlRepository) lockSellerNormalBalance(ctx context.Context, tx *sql.Tx, sellerUserID int64) (decimal.Decimal, error) {
	var normalBalance decimal.Decimal
	err := tx.QueryRowContext(ctx, `
		SELECT balance FROM users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE
	`, sellerUserID).Scan(&normalBalance)
	if errors.Is(err, sql.ErrNoRows) {
		return decimal.Zero, ErrInvalidSellerUserID
	}
	if err != nil {
		return decimal.Zero, err
	}
	var frozen bool
	err = tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM redstone_market_seller_controls WHERE seller_user_id = $1
		)
	`, sellerUserID).Scan(&frozen)
	if err != nil {
		return decimal.Zero, err
	}
	if frozen {
		return decimal.Zero, ErrSellerFrozen
	}
	return normalBalance, nil
}

func (r *sqlRepository) updateSellerProductState(ctx context.Context, tx *sql.Tx, productID int64, status, riskStatus string, requestedScan bool) error {
	if requestedScan {
		_, err := tx.ExecContext(ctx, `
			UPDATE redstone_market_products
			SET status = $1, risk_status = $2, scan_requested_at = NOW(),
				scan_completed_at = NULL, scan_failure_reason = '', updated_at = NOW()
			WHERE id = $3
		`, status, riskStatus, productID)
		return err
	}
	publishNow := status == "active"
	_, err := tx.ExecContext(ctx, `
		UPDATE redstone_market_products
		SET status = $1, risk_status = $2,
			published_at = CASE WHEN $3 THEN COALESCE(published_at, NOW()) ELSE published_at END,
			scan_failure_reason = '', updated_at = NOW()
		WHERE id = $4
	`, status, riskStatus, publishNow, productID)
	return err
}

func (r *sqlRepository) updateSellerProductScanResult(ctx context.Context, tx *sql.Tx, productID int64, status, riskStatus string) error {
	failureReason := ""
	if riskStatus == string(SellerScanRejected) {
		failureReason = "scanner_rejected"
	} else if riskStatus == string(SellerScanFlagged) {
		failureReason = "scanner_flagged"
	}
	publishNow := status == "active"
	_, err := tx.ExecContext(ctx, `
		UPDATE redstone_market_products
		SET status = $1, risk_status = $2, scan_completed_at = NOW(),
			scan_failure_reason = $3,
			published_at = CASE WHEN $4 THEN COALESCE(published_at, NOW()) ELSE published_at END,
			updated_at = NOW()
		WHERE id = $5
	`, status, riskStatus, failureReason, publishNow, productID)
	return err
}

const sellerProductSelect = `
	SELECT p.id, p.seller_user_id, p.seller_kind, p.product_type, p.title, p.description,
		p.unit_price, p.inventory_total, p.inventory_reserved, p.status, p.risk_status, p.account_id,
		delivery_counts.available_delivery_items,
		delivery_counts.reserved_delivery_items,
		delivery_counts.delivered_delivery_items,
		p.scan_requested_at, p.scan_completed_at, p.scan_failure_reason,
		p.created_at, p.updated_at, p.published_at
	FROM redstone_market_products p
	CROSS JOIN LATERAL (
		SELECT
			COUNT(*) FILTER (WHERE status = 'available')::integer AS available_delivery_items,
			COUNT(*) FILTER (WHERE status = 'reserved')::integer AS reserved_delivery_items,
			COUNT(*) FILTER (WHERE status = 'delivered')::integer AS delivered_delivery_items
		FROM redstone_market_delivery_items
		WHERE product_id = p.id
	) AS delivery_counts
`

func scanSellerProduct(scanner rowScanner) (SellerProduct, error) {
	var product SellerProduct
	var scanRequestedAt, scanCompletedAt, publishedAt sql.NullTime
	err := scanner.Scan(
		&product.ID, &product.SellerUserID, &product.SellerKind, &product.ProductType, &product.Title, &product.Description,
		&product.UnitPrice, &product.InventoryTotal, &product.InventoryReserved, &product.Status, &product.RiskStatus, &product.AccountID,
		&product.AvailableDeliveryItems, &product.ReservedDeliveryItems, &product.DeliveredDeliveryItems,
		&scanRequestedAt, &scanCompletedAt, &product.ScanFailureReason,
		&product.CreatedAt, &product.UpdatedAt, &publishedAt,
	)
	if err != nil {
		return SellerProduct{}, err
	}
	if scanRequestedAt.Valid {
		value := scanRequestedAt.Time.UTC()
		product.ScanRequestedAt = &value
	}
	if scanCompletedAt.Valid {
		value := scanCompletedAt.Time.UTC()
		product.ScanCompletedAt = &value
	}
	if publishedAt.Valid {
		value := publishedAt.Time.UTC()
		product.PublishedAt = &value
	}
	return product, nil
}
