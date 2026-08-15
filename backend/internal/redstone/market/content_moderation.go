package market

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

var (
	ErrContentModerationUnavailable = errors.New("market content moderation is unavailable")
	ErrContentModerationRejected    = errors.New("market content was rejected by moderation")
	ErrInvalidContentReviewID       = errors.New("market content review id must be positive")
	ErrContentReviewNotFound        = errors.New("market content review was not found")
	ErrContentReviewNotOpen         = errors.New("market content review is not open")
	ErrInvalidContentReviewAction   = errors.New("market content review action is invalid")
)

const (
	contentReviewActionApprove = "approve"
	contentReviewActionReject  = "reject"

	contentReviewReasonLimit = 500
)

// ContentModerationScope describes the source material inspected by the
// marketplace-local rules. The scanner produces only stable finding codes and
// a one-way digest; it never retains source text or credential material.
type ContentModerationScope string

const (
	ContentScopeProductMetadata  ContentModerationScope = "product_metadata"
	ContentScopeDeliveryContent  ContentModerationScope = "delivery_content"
	ContentScopeAccountReference ContentModerationScope = "account_reference"
)

// ContentModerationVerdict separates a definitive denial from material that
// needs a human decision. A flagged product is never purchasable.
type ContentModerationVerdict string

const (
	ContentModerationPassed       ContentModerationVerdict = "passed"
	ContentModerationManualReview ContentModerationVerdict = "manual_review"
	ContentModerationRejected     ContentModerationVerdict = "rejected"
)

type ContentModerationInput struct {
	Scope       ContentModerationScope
	ProductType string
	Title       string
	Description string
	Content     []byte
}

// ContentModerationDecision intentionally contains no source content. The
// digest supports audit correlation without making a credential recoverable
// from marketplace tables or logs.
type ContentModerationDecision struct {
	Verdict       ContentModerationVerdict
	FindingCodes  []string
	ContentSHA256 string
}

func (d ContentModerationDecision) valid() bool {
	if d.Verdict != ContentModerationPassed && d.Verdict != ContentModerationManualReview && d.Verdict != ContentModerationRejected {
		return false
	}
	if len(d.ContentSHA256) != sha256.Size*2 {
		return false
	}
	for _, code := range d.FindingCodes {
		if strings.TrimSpace(code) == "" || len(code) > 80 {
			return false
		}
	}
	return true
}

// ContentModerationScanner is deliberately local. It must not send plaintext
// delivery content to a third party and implementations must not log input.
type ContentModerationScanner interface {
	Scan(context.Context, ContentModerationInput) (ContentModerationDecision, error)
}

// DeterministicContentModerationScanner provides a fail-closed, auditable
// baseline before optional malware scanning. It recognizes exposed secrets in
// public metadata, unlawful transaction indicators, and material that needs
// manual review such as credential or account-transfer listings.
type DeterministicContentModerationScanner struct{}

func NewDeterministicContentModerationScanner() *DeterministicContentModerationScanner {
	return &DeterministicContentModerationScanner{}
}

var (
	marketSecretValuePattern    = regexp.MustCompile(`(?i)(?:-----BEGIN(?: [A-Z0-9]+)* PRIVATE KEY-----|sk-[a-z0-9_-]{16,}|AIza[a-z0-9_-]{20,}|AKIA[0-9A-Z]{16}|gh[pousr]_[a-z0-9]{20,}|xox[baprs]-[a-z0-9-]{16,}|(?:(?:api|client)[ _-]?(?:key|secret)|(?:access|refresh|id)[ _-]?token)\s*[:=]\s*[^\s]{12,})`)
	marketCredentialHintPattern = regexp.MustCompile(`(?i)(?:api[ _-]?key|access[ _-]?token|refresh[ _-]?token|oauth|session[ _-]?cookie|private[ _-]?key|service[ _-]?account|授权令牌|访问令牌|刷新令牌|会话(?:cookie|令牌)|私钥|服务账号)`)
)

var marketHighRiskTransactionIndicators = []string{
	"stolen account", "stolen credential", "credential stuffing", "phishing kit",
	"carding", "money laundering", "bypass kyc", "bypass verification",
	"盗号", "盗取凭证", "撞库", "钓鱼", "黑卡", "洗钱", "绕过实名", "绕过验证", "代过风控", "非法支付",
}

func (s *DeterministicContentModerationScanner) Scan(_ context.Context, input ContentModerationInput) (ContentModerationDecision, error) {
	if s == nil || (input.Scope != ContentScopeProductMetadata && input.Scope != ContentScopeDeliveryContent && input.Scope != ContentScopeAccountReference) {
		return ContentModerationDecision{}, ErrContentModerationUnavailable
	}
	digest := contentDigest(input)
	text := contentText(input)
	findings := make([]string, 0, 2)
	if containsHighRiskTransactionIndicator(text) {
		findings = append(findings, "high_risk_transaction_indicator")
		return contentDecision(ContentModerationRejected, findings, digest), nil
	}
	if marketSecretValuePattern.MatchString(text) {
		findings = append(findings, "credential_material_detected")
		if input.Scope == ContentScopeProductMetadata {
			return contentDecision(ContentModerationRejected, findings, digest), nil
		}
		return contentDecision(ContentModerationManualReview, findings, digest), nil
	}
	if input.Scope == ContentScopeAccountReference || strings.TrimSpace(input.ProductType) == "account_reference" {
		return contentDecision(ContentModerationManualReview, []string{"account_transfer_requires_review"}, digest), nil
	}
	if marketCredentialHintPattern.MatchString(text) {
		return contentDecision(ContentModerationManualReview, []string{"credential_listing_requires_review"}, digest), nil
	}
	return contentDecision(ContentModerationPassed, nil, digest), nil
}

func contentDecision(verdict ContentModerationVerdict, findings []string, digest string) ContentModerationDecision {
	findings = append([]string(nil), findings...)
	sort.Strings(findings)
	return ContentModerationDecision{Verdict: verdict, FindingCodes: findings, ContentSHA256: digest}
}

func contentDigest(input ContentModerationInput) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(input.Scope))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(input.ProductType))
	_, _ = hash.Write([]byte{0})
	if input.Scope == ContentScopeDeliveryContent {
		_, _ = hash.Write(input.Content)
	} else {
		_, _ = hash.Write([]byte(input.Title))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(input.Description))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func contentText(input ContentModerationInput) string {
	if input.Scope == ContentScopeDeliveryContent {
		// Binary files still receive the separate ClamAV inspection. Text-only
		// rules intentionally skip opaque binary payloads to avoid interpreting
		// random bytes as credentials or transaction instructions.
		if !utf8.Valid(input.Content) {
			return ""
		}
		return strings.ToLower(string(input.Content))
	}
	return strings.ToLower(input.Title + "\n" + input.Description)
}

func containsHighRiskTransactionIndicator(value string) bool {
	for _, indicator := range marketHighRiskTransactionIndicators {
		if strings.Contains(value, indicator) {
			return true
		}
	}
	return false
}

// ContentReview is an operator-safe projection. It contains category codes
// and a digest, but never product delivery plaintext or account credentials.
type ContentReview struct {
	ID               int64                    `json:"id"`
	SellerUserID     int64                    `json:"seller_user_id"`
	ProductID        *int64                   `json:"product_id,omitempty"`
	DeliveryItemID   *int64                   `json:"delivery_item_id,omitempty"`
	Scope            ContentModerationScope   `json:"scope"`
	Verdict          ContentModerationVerdict `json:"verdict"`
	ReviewState      string                   `json:"review_state"`
	FindingCodes     []string                 `json:"finding_codes"`
	Resolution       string                   `json:"resolution"`
	ResolvedByUserID *int64                   `json:"resolved_by_user_id,omitempty"`
	ResolutionNote   string                   `json:"resolution_note"`
	CreatedAt        time.Time                `json:"created_at"`
	ResolvedAt       *time.Time               `json:"resolved_at,omitempty"`
}

type RecordContentReviewRequest struct {
	SellerUserID   int64
	ProductID      *int64
	DeliveryItemID *int64
	Scope          ContentModerationScope
	Decision       ContentModerationDecision
}

func (r RecordContentReviewRequest) Validate() error {
	if r.SellerUserID <= 0 || !r.Decision.valid() {
		return ErrContentModerationUnavailable
	}
	if r.Scope != ContentScopeProductMetadata && r.Scope != ContentScopeDeliveryContent && r.Scope != ContentScopeAccountReference {
		return ErrContentModerationUnavailable
	}
	if r.ProductID != nil && *r.ProductID <= 0 {
		return ErrInvalidProductID
	}
	if r.DeliveryItemID != nil && *r.DeliveryItemID <= 0 {
		return ErrContentModerationUnavailable
	}
	return nil
}

type ResolveContentReviewRequest struct {
	ActorUserID int64
	ReviewID    int64
	Action      string
	Note        string
}

func (r ResolveContentReviewRequest) Validate() error {
	if r.ActorUserID <= 0 {
		return ErrInvalidActor
	}
	if r.ReviewID <= 0 {
		return ErrInvalidContentReviewID
	}
	if r.Action != contentReviewActionApprove && r.Action != contentReviewActionReject {
		return ErrInvalidContentReviewAction
	}
	if !validContentReviewNote(r.Note) {
		return ErrReportReasonRequired
	}
	return nil
}

func validContentReviewNote(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len([]rune(value)) <= contentReviewReasonLimit
}

type ContentModerationRepository interface {
	RecordContentReview(context.Context, RecordContentReviewRequest) (ContentReview, error)
	ListOpenContentReviews(context.Context, int, int) ([]ContentReview, int, error)
	ResolveContentReview(context.Context, ResolveContentReviewRequest, decimal.Decimal) (ContentReview, error)
}

func (s *Service) SetContentModerationScanner(scanner ContentModerationScanner) {
	if s != nil {
		s.contentScanner = scanner
	}
}

func (s *Service) scanContent(ctx context.Context, input ContentModerationInput) (ContentModerationDecision, error) {
	if s == nil || s.contentScanner == nil {
		return ContentModerationDecision{}, ErrContentModerationUnavailable
	}
	decision, err := s.contentScanner.Scan(ctx, input)
	if err != nil || !decision.valid() {
		return ContentModerationDecision{}, ErrContentModerationUnavailable
	}
	return decision, nil
}

func (s *Service) scanProductMetadata(ctx context.Context, productType, title, description string) (ContentModerationDecision, error) {
	scope := ContentScopeProductMetadata
	if productType == "account_reference" {
		scope = ContentScopeAccountReference
	}
	return s.scanContent(ctx, ContentModerationInput{Scope: scope, ProductType: productType, Title: title, Description: description})
}

func (s *Service) scanDeliveryContent(ctx context.Context, productType string, content []byte) (ContentModerationDecision, error) {
	return s.scanContent(ctx, ContentModerationInput{Scope: ContentScopeDeliveryContent, ProductType: productType, Content: content})
}

func productMetadataDecision(ctx context.Context, productType, title, description string, decision ContentModerationDecision) (ContentModerationDecision, error) {
	if decision.valid() {
		return decision, nil
	}
	scope := ContentScopeProductMetadata
	if productType == "account_reference" {
		scope = ContentScopeAccountReference
	}
	return NewDeterministicContentModerationScanner().Scan(ctx, ContentModerationInput{
		Scope: scope, ProductType: productType, Title: title, Description: description,
	})
}

func contentScopeForProductType(productType string) ContentModerationScope {
	if productType == "account_reference" {
		return ContentScopeAccountReference
	}
	return ContentScopeProductMetadata
}

func (s *Service) contentModerationRepository() (ContentModerationRepository, error) {
	if s == nil || s.repository == nil {
		return nil, ErrContentModerationUnavailable
	}
	repository, ok := s.repository.(ContentModerationRepository)
	if !ok {
		return nil, ErrContentModerationUnavailable
	}
	return repository, nil
}

func (s *Service) recordRejectedContent(ctx context.Context, request RecordContentReviewRequest) error {
	repository, err := s.contentModerationRepository()
	if err != nil {
		return err
	}
	_, err = repository.RecordContentReview(ctx, request)
	return err
}

func (s *Service) ListOpenContentReviews(ctx context.Context, limit, offset int) ([]ContentReview, int, error) {
	if limit <= 0 || limit > 100 || offset < 0 {
		return nil, 0, marketApplicationError(ErrInvalidPagination)
	}
	repository, err := s.contentModerationRepository()
	if err != nil {
		return nil, 0, marketApplicationError(err)
	}
	reviews, total, err := repository.ListOpenContentReviews(ctx, limit, offset)
	return reviews, total, marketApplicationError(err)
}

func (s *Service) ResolveContentReview(ctx context.Context, request ResolveContentReviewRequest) (ContentReview, error) {
	if err := request.Validate(); err != nil {
		return ContentReview{}, marketApplicationError(err)
	}
	repository, err := s.contentModerationRepository()
	if err != nil {
		return ContentReview{}, marketApplicationError(err)
	}
	cnyPerBalance, err := s.sellerCNYPerBalance(ctx)
	if err != nil {
		return ContentReview{}, marketApplicationError(err)
	}
	review, err := repository.ResolveContentReview(ctx, request, cnyPerBalance)
	return review, marketApplicationError(err)
}

func (r *sqlRepository) RecordContentReview(ctx context.Context, request RecordContentReviewRequest) (_ ContentReview, err error) {
	if err := request.Validate(); err != nil {
		return ContentReview{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ContentReview{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if request.ProductID != nil {
		var sellerUserID int64
		if err := tx.QueryRowContext(ctx, `
			SELECT seller_user_id FROM redstone_market_products WHERE id = $1 FOR UPDATE
		`, *request.ProductID).Scan(&sellerUserID); errors.Is(err, sql.ErrNoRows) {
			return ContentReview{}, ErrSellerProductNotFound
		} else if err != nil {
			return ContentReview{}, err
		} else if sellerUserID != request.SellerUserID {
			return ContentReview{}, ErrSellerProductNotFound
		}
	}
	review, err := r.recordContentReviewTx(ctx, tx, request, time.Now().UTC())
	if err != nil {
		return ContentReview{}, err
	}
	if request.ProductID != nil && request.Decision.Verdict != ContentModerationPassed {
		failure := "content_review_pending"
		if request.Decision.Verdict == ContentModerationRejected {
			failure = "content_review_rejected"
		}
		if err := suspendProductForContentReview(ctx, tx, *request.ProductID, request.Decision, review.ID, request.SellerUserID, failure); err != nil {
			return ContentReview{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ContentReview{}, err
	}
	return review, nil
}

func (r *sqlRepository) ListOpenContentReviews(ctx context.Context, limit, offset int) ([]ContentReview, int, error) {
	var total int
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM redstone_market_content_reviews WHERE review_state = 'open'
	`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, contentReviewSelect+`
		WHERE review_state = 'open'
		ORDER BY created_at, id LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	reviews := make([]ContentReview, 0)
	for rows.Next() {
		review, scanErr := scanContentReview(rows)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		reviews = append(reviews, review)
	}
	return reviews, total, rows.Err()
}

func (r *sqlRepository) ResolveContentReview(ctx context.Context, request ResolveContentReviewRequest, cnyPerBalance decimal.Decimal) (_ ContentReview, err error) {
	if err := request.Validate(); err != nil {
		return ContentReview{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ContentReview{}, err
	}
	defer func() { _ = tx.Rollback() }()
	review, err := scanContentReview(tx.QueryRowContext(ctx, contentReviewSelect+` WHERE id = $1 FOR UPDATE`, request.ReviewID))
	if errors.Is(err, sql.ErrNoRows) {
		return ContentReview{}, ErrContentReviewNotFound
	}
	if err != nil {
		return ContentReview{}, err
	}
	if review.ReviewState != "open" {
		return ContentReview{}, ErrContentReviewNotOpen
	}
	now := time.Now().UTC()
	if review.ProductID != nil {
		product, err := r.lockProductForContentReview(ctx, tx, *review.ProductID)
		if err != nil {
			return ContentReview{}, err
		}
		if request.Action == contentReviewActionReject {
			decision := ContentModerationDecision{Verdict: ContentModerationRejected, ContentSHA256: strings.Repeat("0", sha256.Size*2)}
			if err := suspendProductForContentReview(ctx, tx, product.ID, decision, review.ID, request.ActorUserID, "content_review_rejected"); err != nil {
				return ContentReview{}, err
			}
		} else if err := r.approveContentReviewProduct(ctx, tx, review, product, cnyPerBalance); err != nil {
			return ContentReview{}, err
		}
	}
	resolution := "admin_approved"
	if request.Action == contentReviewActionReject {
		resolution = "admin_rejected"
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE redstone_market_content_reviews
		SET review_state = 'resolved', resolution = $2, resolution_note = $3,
			resolved_by_user_id = $4, resolved_at = $5
		WHERE id = $1 AND review_state = 'open'
	`, review.ID, resolution, strings.TrimSpace(request.Note), request.ActorUserID, now); err != nil {
		return ContentReview{}, err
	}
	if err := insertGovernanceAudit(ctx, tx, "content_review", review.ID, resolution, request.ActorUserID, strings.TrimSpace(request.Note), now); err != nil {
		return ContentReview{}, err
	}
	review.ReviewState = "resolved"
	review.Resolution = resolution
	review.ResolutionNote = strings.TrimSpace(request.Note)
	review.ResolvedByUserID = &request.ActorUserID
	review.ResolvedAt = &now
	if err := tx.Commit(); err != nil {
		return ContentReview{}, err
	}
	return review, nil
}

func (r *sqlRepository) lockProductForContentReview(ctx context.Context, tx *sql.Tx, productID int64) (SellerProduct, error) {
	product, err := scanSellerProduct(tx.QueryRowContext(ctx, sellerProductSelect+`
		WHERE p.id = $1 FOR UPDATE OF p
	`, productID))
	if errors.Is(err, sql.ErrNoRows) {
		return SellerProduct{}, ErrSellerProductNotFound
	}
	return product, err
}

func (r *sqlRepository) approveContentReviewProduct(ctx context.Context, tx *sql.Tx, review ContentReview, product SellerProduct, cnyPerBalance decimal.Decimal) error {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM redstone_market_order_holds
		WHERE source = 'content_review' AND source_id = $1
	`, review.ID); err != nil {
		return err
	}
	if review.Scope == ContentScopeProductMetadata || review.Scope == ContentScopeAccountReference {
		_, err := tx.ExecContext(ctx, `
			UPDATE redstone_market_products
			SET status = 'draft', risk_status = 'pending', scan_failure_reason = '', updated_at = NOW()
			WHERE id = $1
		`, product.ID)
		return err
	}
	var hasRejected, hasPending bool
	if err := tx.QueryRowContext(ctx, `
		SELECT
			EXISTS (SELECT 1 FROM redstone_market_delivery_scan_jobs WHERE product_id = $1 AND state = 'rejected'),
			EXISTS (SELECT 1 FROM redstone_market_delivery_scan_jobs WHERE product_id = $1 AND state IN ('pending', 'processing'))
	`, product.ID).Scan(&hasRejected, &hasPending); err != nil {
		return err
	}
	if hasRejected {
		_, err := tx.ExecContext(ctx, `
			UPDATE redstone_market_products
			SET status = 'suspended', risk_status = 'rejected', scan_failure_reason = 'scanner_rejected', updated_at = NOW()
			WHERE id = $1
		`, product.ID)
		return err
	}
	if hasPending {
		_, err := tx.ExecContext(ctx, `
			UPDATE redstone_market_products
			SET status = 'pending_scan', risk_status = 'pending', scan_failure_reason = '', updated_at = NOW()
			WHERE id = $1
		`, product.ID)
		return err
	}
	status := "draft"
	if product.AvailableDeliveryItems > 0 && product.InventoryTotal > product.InventoryReserved {
		if product.SellerKind == SellerOfficial {
			status = "active"
		} else {
			dashboard, err := r.lockSellerDashboard(ctx, tx, product.SellerUserID, cnyPerBalance, product.ID)
			if err != nil && !errors.Is(err, ErrSellerFrozen) {
				return err
			}
			if err == nil && dashboard.ListingLimit > 0 && dashboard.ActiveListings < dashboard.ListingLimit {
				status = "active"
			} else if errors.Is(err, ErrSellerFrozen) {
				status = "suspended"
			}
		}
	}
	publishNow := status == "active"
	_, err := tx.ExecContext(ctx, `
		UPDATE redstone_market_products
		SET status = $2, risk_status = 'passed', scan_failure_reason = '',
			published_at = CASE WHEN $3 THEN COALESCE(published_at, NOW()) ELSE published_at END,
			updated_at = NOW()
		WHERE id = $1
	`, product.ID, status, publishNow)
	return err
}

func (r *sqlRepository) recordContentReviewTx(ctx context.Context, tx *sql.Tx, request RecordContentReviewRequest, now time.Time) (ContentReview, error) {
	findingCodeList := request.Decision.FindingCodes
	if findingCodeList == nil {
		findingCodeList = []string{}
	}
	findingCodes, err := json.Marshal(findingCodeList)
	if err != nil {
		return ContentReview{}, err
	}
	reviewState, resolution := "closed", "auto_passed"
	if request.Decision.Verdict == ContentModerationManualReview {
		reviewState, resolution = "open", ""
	} else if request.Decision.Verdict == ContentModerationRejected {
		resolution = "auto_rejected"
	}
	var resolvedAt *time.Time
	if reviewState != "open" {
		resolvedAt = &now
	}
	var review ContentReview
	err = tx.QueryRowContext(ctx, `
		INSERT INTO redstone_market_content_reviews (
			seller_user_id, product_id, delivery_item_id, scope, verdict, review_state,
			finding_codes, content_sha256, resolution, created_at, resolved_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10, $11)
		RETURNING id, seller_user_id, product_id, delivery_item_id, scope, verdict, review_state,
			finding_codes, resolution, resolved_by_user_id, resolution_note, created_at, resolved_at
	`, request.SellerUserID, request.ProductID, request.DeliveryItemID, request.Scope, request.Decision.Verdict,
		reviewState, string(findingCodes), request.Decision.ContentSHA256, resolution, now, resolvedAt).Scan(
		&review.ID, &review.SellerUserID, &review.ProductID, &review.DeliveryItemID, &review.Scope, &review.Verdict,
		&review.ReviewState, &findingCodes, &review.Resolution, &review.ResolvedByUserID, &review.ResolutionNote,
		&review.CreatedAt, &review.ResolvedAt,
	)
	if err != nil {
		return ContentReview{}, err
	}
	if err := json.Unmarshal(findingCodes, &review.FindingCodes); err != nil {
		return ContentReview{}, err
	}
	action := "content_auto_passed"
	if request.Decision.Verdict == ContentModerationManualReview {
		action = "content_manual_review_opened"
	} else if request.Decision.Verdict == ContentModerationRejected {
		action = "content_auto_rejected"
	}
	if err := insertGovernanceAudit(ctx, tx, "content_review", review.ID, action, request.SellerUserID, strings.Join(review.FindingCodes, ","), now); err != nil {
		return ContentReview{}, err
	}
	return review, nil
}

func suspendProductForContentReview(ctx context.Context, tx *sql.Tx, productID int64, decision ContentModerationDecision, reviewID, actorUserID int64, failure string) error {
	riskStatus := "flagged"
	if decision.Verdict == ContentModerationRejected {
		riskStatus = "rejected"
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE redstone_market_products
		SET status = 'suspended', risk_status = $2, scan_completed_at = NOW(), scan_failure_reason = $3, updated_at = NOW()
		WHERE id = $1
	`, productID, riskStatus, failure); err != nil {
		return err
	}
	if err := lockProductUnsettledOrders(ctx, tx, productID); err != nil {
		return err
	}
	if err := holdProductOrders(ctx, tx, productID, "content_review", reviewID, actorUserID, failure, time.Now().UTC()); err != nil {
		return err
	}
	return insertGovernanceAudit(ctx, tx, "product", productID, "product_content_suspended", actorUserID, failure, time.Now().UTC())
}

const contentReviewSelect = `
	SELECT id, seller_user_id, product_id, delivery_item_id, scope, verdict, review_state,
		finding_codes, resolution, resolved_by_user_id, resolution_note, created_at, resolved_at
	FROM redstone_market_content_reviews`

type contentReviewRow interface {
	Scan(dest ...any) error
}

func scanContentReview(row contentReviewRow) (ContentReview, error) {
	var review ContentReview
	var findingCodes []byte
	if err := row.Scan(
		&review.ID, &review.SellerUserID, &review.ProductID, &review.DeliveryItemID, &review.Scope, &review.Verdict,
		&review.ReviewState, &findingCodes, &review.Resolution, &review.ResolvedByUserID, &review.ResolutionNote,
		&review.CreatedAt, &review.ResolvedAt,
	); err != nil {
		return ContentReview{}, err
	}
	if err := json.Unmarshal(findingCodes, &review.FindingCodes); err != nil {
		return ContentReview{}, err
	}
	return review, nil
}
