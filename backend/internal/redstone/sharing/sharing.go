// Package sharing implements Redstone's account sharing domain. It only
// references sub2 accounts by ID; credentials and runtime account management
// stay in their existing owners.
package sharing

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	infraerrors "github.com/silent-QAQ/redstoneapi/internal/pkg/errors"
	"github.com/silent-QAQ/redstoneapi/internal/redstone/wallet"
)

var (
	ErrInvalidRoom               = errors.New("account share room is invalid")
	ErrInvalidAccount            = errors.New("account share account is invalid")
	ErrInvalidMembership         = errors.New("account share membership is invalid")
	ErrInvalidLease              = errors.New("account share lease is invalid")
	ErrInvalidReview             = errors.New("account share review is invalid")
	ErrInvalidIdempotencyKey     = errors.New("account share idempotency key is invalid")
	ErrRoomNotFound              = errors.New("account share room was not found")
	ErrMembershipNotFound        = errors.New("account share membership was not found")
	ErrLeaseNotFound             = errors.New("account share lease was not found")
	ErrRoomForbidden             = errors.New("account share room access is forbidden")
	ErrRoomUnavailable           = errors.New("account share room is unavailable")
	ErrRoomHasNoAvailableAccount = errors.New("account share room has no available account")
	ErrPrivateInviteRequired     = errors.New("account share private invitation is required")
	ErrRoomReviewRequired        = errors.New("account share room review is required")
	ErrQuotaExceeded             = errors.New("account share quota exceeded")
	ErrAccountNotOwned           = errors.New("account share account is not owned by room owner")
	ErrAccountAlreadyBound       = errors.New("account share account is already bound")
	ErrRoomAccountNotFound       = errors.New("account share room account was not found")
	ErrRoomAccountState          = errors.New("account share room account state transition is invalid")
	ErrRoomAccountHasActiveLease = errors.New("account share room account still has an active lease")
	ErrRoomHasActiveLeases       = errors.New("account share room still has active leases")
	ErrRoomMustBeClosed          = errors.New("account share room must be closed before deletion")
	ErrReviewNotEligible         = errors.New("account share review requires a completed membership")
	ErrSettlementNotFound        = errors.New("account share settlement intent was not found")
	ErrSettlementState           = errors.New("account share settlement intent is not chargeable")
	ErrWalletRequired            = errors.New("account share wallet service is required")
	ErrRepositoryRequired        = errors.New("account share repository is required")
	ErrAtomicSettlementRequired  = errors.New("account share repository does not support atomic settlement")
	ErrInvalidPagination         = errors.New("account share pagination is invalid")
)

type RoomVisibility string

const (
	VisibilityPrivate RoomVisibility = "private"
	VisibilityPublic  RoomVisibility = "public"
)

type RoomStatus string

const (
	RoomDraft         RoomStatus = "draft"
	RoomPendingReview RoomStatus = "pending_review"
	RoomActive        RoomStatus = "active"
	RoomSuspended     RoomStatus = "suspended"
	RoomClosed        RoomStatus = "closed"
)

type MembershipStatus string

const (
	MembershipQueued  MembershipStatus = "queued"
	MembershipActive  MembershipStatus = "active"
	MembershipEnding  MembershipStatus = "ending"
	MembershipEnded   MembershipStatus = "ended"
	MembershipRevoked MembershipStatus = "revoked"
)

type MembershipDecision string

const (
	MembershipApprove MembershipDecision = "approve"
	MembershipReject  MembershipDecision = "reject"
)

type LeaseState string

const (
	LeaseActive   LeaseState = "active"
	LeaseReleased LeaseState = "released"
	LeaseExpired  LeaseState = "expired"
)

// RoomAccountState is the lifecycle state of an account bound to a sharing
// room. Draining prevents future lease allocation while preserving access for
// a lease already assigned to that account.
type RoomAccountState string

const (
	RoomAccountActive   RoomAccountState = "active"
	RoomAccountDraining RoomAccountState = "draining"
	RoomAccountRemoved  RoomAccountState = "removed"
)

type PaymentSource string

const (
	PaymentPending         PaymentSource = "pending"
	PaymentSubscription    PaymentSource = "subscription"
	PaymentBound           PaymentSource = "bound"
	PaymentNormal          PaymentSource = "normal"
	PaymentBoundThenNormal PaymentSource = "bound_then_normal"
)

type SettlementStatus string

const (
	SettlementPending  SettlementStatus = "pending"
	SettlementCharging SettlementStatus = "charging"
	SettlementSettled  SettlementStatus = "settled"
	SettlementFailed   SettlementStatus = "failed"
	SettlementReversed SettlementStatus = "reversed"
)

type Room struct {
	ID                     int64           `json:"id"`
	OwnerUserID            int64           `json:"owner_user_id"`
	Name                   string          `json:"name"`
	Description            string          `json:"description"`
	Platform               string          `json:"platform"`
	Visibility             RoomVisibility  `json:"visibility"`
	Status                 RoomStatus      `json:"status"`
	RequiresApproval       bool            `json:"requires_approval"`
	SeatLimit              int             `json:"seat_limit"`
	LeaseSeconds           int             `json:"lease_seconds"`
	IdleTimeoutSeconds     int             `json:"idle_timeout_seconds"`
	LeasePrice             decimal.Decimal `json:"lease_price"`
	RoomRateMultiplier     decimal.Decimal `json:"room_rate_multiplier"`
	HourlyFee              decimal.Decimal `json:"hourly_fee"`
	HourlyFeeFreeThreshold decimal.Decimal `json:"hourly_fee_free_threshold"`
	PlatformFeeRate        decimal.Decimal `json:"platform_fee_rate"`
	AccountCount           int             `json:"account_count"`
	ActiveSeats            int             `json:"active_seats"`
	AverageRating          decimal.Decimal `json:"average_rating"`
	ReviewCount            int             `json:"review_count"`
	LowestAccountLevel     string          `json:"lowest_account_level"`
	// IsVerified is true only when the room has bound accounts and every
	// still-bound API-key account has passed model verification. Other account
	// types are verified when they are added.
	IsVerified bool `json:"is_verified"`
	// IsAvailable means an active account can currently be scheduled and the
	// room still has a seat.
	IsAvailable bool `json:"is_available"`
	// HasSchedulableAccount distinguishes a full room from a room whose owner
	// has not bound a usable account yet.
	HasSchedulableAccount bool      `json:"has_schedulable_account"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// PublicRoomFilter describes the account-square filters. AccountGrade is
// matched against the lowest level among the room's currently bound accounts.
type PublicRoomFilter struct {
	Search        string
	Platform      string
	AccountGrade  string
	VerifiedOnly  bool
	AvailableOnly bool
	SortBy        string
	SortOrder     string
}

func (f *PublicRoomFilter) NormalizeAndValidate() error {
	f.Search = strings.TrimSpace(f.Search)
	f.Platform = strings.ToLower(strings.TrimSpace(f.Platform))
	if f.Platform == "claude" {
		f.Platform = "anthropic"
	}
	f.AccountGrade = strings.ToLower(strings.TrimSpace(f.AccountGrade))
	f.SortBy = strings.ToLower(strings.TrimSpace(f.SortBy))
	f.SortOrder = strings.ToLower(strings.TrimSpace(f.SortOrder))
	if len(f.Search) > 120 || len(f.AccountGrade) > 50 {
		return ErrInvalidRoom
	}
	if f.Platform != "" && f.Platform != "openai" && f.Platform != "anthropic" && f.Platform != "grok" && f.Platform != "gemini" && f.Platform != "antigravity" {
		return ErrInvalidRoom
	}
	if f.SortBy == "" {
		f.SortBy = "updated_at"
	}
	if f.SortBy != "updated_at" && f.SortBy != "rate_multiplier" && f.SortBy != "hourly_fee" && f.SortBy != "hourly_fee_free_threshold" {
		return ErrInvalidRoom
	}
	if f.SortOrder == "" {
		f.SortOrder = "desc"
	}
	if f.SortOrder != "asc" && f.SortOrder != "desc" {
		return ErrInvalidRoom
	}
	return nil
}

type Membership struct {
	ID        int64            `json:"id"`
	RoomID    int64            `json:"room_id"`
	UserID    int64            `json:"user_id"`
	Status    MembershipStatus `json:"status"`
	QueuedAt  time.Time        `json:"queued_at"`
	JoinedAt  *time.Time       `json:"joined_at,omitempty"`
	EndedAt   *time.Time       `json:"ended_at,omitempty"`
	EndReason string           `json:"end_reason"`
	// Lease is included for the current user only when the membership still
	// has a live lease. The account square uses it to resume its heartbeat
	// after a browser refresh.
	Lease *Lease `json:"lease,omitempty"`
}

type Lease struct {
	ID           int64      `json:"id"`
	RoomID       int64      `json:"room_id"`
	MembershipID int64      `json:"membership_id"`
	AccountID    int64      `json:"account_id"`
	UserID       int64      `json:"user_id"`
	State        LeaseState `json:"state"`
	GrantedAt    time.Time  `json:"granted_at"`
	HeartbeatAt  time.Time  `json:"heartbeat_at"`
	ExpiresAt    time.Time  `json:"expires_at"`
	ReleasedAt   *time.Time `json:"released_at,omitempty"`
	Reason       string     `json:"release_reason"`
}

// RoomAccount exposes binding lifecycle metadata only. Account credentials
// and account-domain fields remain owned by the existing account service.
type RoomAccount struct {
	RoomID    int64            `json:"room_id"`
	AccountID int64            `json:"account_id"`
	State     RoomAccountState `json:"state"`
	BoundAt   time.Time        `json:"bound_at"`
	UnboundAt *time.Time       `json:"unbound_at,omitempty"`
}

type SettlementIntent struct {
	ID             int64            `json:"id"`
	LeaseID        int64            `json:"lease_id"`
	MembershipID   int64            `json:"membership_id"`
	RoomID         int64            `json:"room_id"`
	PayerUserID    int64            `json:"payer_user_id"`
	OwnerUserID    int64            `json:"owner_user_id"`
	SubscriptionID *int64           `json:"subscription_id,omitempty"`
	GrossAmount    decimal.Decimal  `json:"gross_amount"`
	PlatformFee    decimal.Decimal  `json:"platform_fee_amount"`
	OwnerAmount    decimal.Decimal  `json:"owner_amount"`
	PaymentSource  PaymentSource    `json:"payment_source"`
	Status         SettlementStatus `json:"status"`
	IdempotencyKey string           `json:"idempotency_key"`
	FailureReason  string           `json:"failure_reason,omitempty"`
	SettledAt      *time.Time       `json:"settled_at,omitempty"`
}

type PayoutReceipt struct {
	ID                 int64           `json:"id"`
	SettlementIntentID int64           `json:"settlement_intent_id"`
	ReceiptNumber      string          `json:"receipt_number"`
	Amount             decimal.Decimal `json:"amount"`
	CreatedAt          time.Time       `json:"created_at"`
}

type CreateRoomRequest struct {
	OwnerUserID            int64
	Name                   string
	Description            string
	Platform               string
	Visibility             RoomVisibility
	RequiresApproval       bool
	SeatLimit              int
	LeaseSeconds           int
	IdleTimeoutSeconds     int
	LeasePrice             decimal.Decimal
	RoomRateMultiplier     decimal.Decimal
	HourlyFee              decimal.Decimal
	HourlyFeeFreeThreshold decimal.Decimal
}

type UpdateRoomRequest struct {
	OwnerUserID            int64
	RoomID                 int64
	Name                   string
	Description            string
	Visibility             RoomVisibility
	RequiresApproval       bool
	SeatLimit              int
	LeaseSeconds           int
	IdleTimeoutSeconds     int
	LeasePrice             decimal.Decimal
	RoomRateMultiplier     decimal.Decimal
	HourlyFee              decimal.Decimal
	HourlyFeeFreeThreshold decimal.Decimal
}

// roomStatusAfterVisibilityChange keeps historical public visibility
// moderation behavior for updates. New room creation bypasses this helper
// and is immediately active.
func roomStatusAfterVisibilityChange(currentStatus RoomStatus, currentVisibility, requestedVisibility RoomVisibility) RoomStatus {
	if currentVisibility == requestedVisibility || currentStatus == RoomSuspended {
		return currentStatus
	}
	if requestedVisibility == VisibilityPublic {
		return RoomPendingReview
	}
	if currentStatus == RoomPendingReview {
		return RoomActive
	}
	return currentStatus
}

func (r UpdateRoomRequest) Validate() error {
	if r.OwnerUserID <= 0 || r.RoomID <= 0 || !validText(r.Name, 120) || len(r.Description) > 2000 ||
		(r.Visibility != VisibilityPrivate && r.Visibility != VisibilityPublic) || r.SeatLimit < 1 || r.SeatLimit > 30 ||
		r.LeaseSeconds < 60 || r.LeaseSeconds > 86400 || r.IdleTimeoutSeconds < 60 || r.IdleTimeoutSeconds > 86400 ||
		r.LeasePrice.IsNegative() || !r.LeasePrice.Equal(r.LeasePrice.Round(wallet.MonetaryScale)) ||
		(r.RoomRateMultiplier.IsNegative() || (r.RoomRateMultiplier.IsPositive() && !r.RoomRateMultiplier.Equal(r.RoomRateMultiplier.Round(wallet.MonetaryScale))) ||
			r.HourlyFee.IsNegative() || !r.HourlyFee.Equal(r.HourlyFee.Round(wallet.MonetaryScale)) ||
			r.HourlyFeeFreeThreshold.IsNegative() || !r.HourlyFeeFreeThreshold.Equal(r.HourlyFeeFreeThreshold.Round(wallet.MonetaryScale))) {
		return ErrInvalidRoom
	}
	return nil
}

func (r CreateRoomRequest) Validate() error {
	if r.OwnerUserID <= 0 || !validText(r.Name, 120) || !validText(r.Platform, 50) || len(r.Description) > 2000 ||
		(r.Visibility != VisibilityPrivate && r.Visibility != VisibilityPublic) || r.SeatLimit < 1 || r.SeatLimit > 30 ||
		r.LeaseSeconds < 60 || r.LeaseSeconds > 86400 || r.IdleTimeoutSeconds < 60 || r.IdleTimeoutSeconds > 86400 ||
		r.LeasePrice.IsNegative() || !r.LeasePrice.Equal(r.LeasePrice.Round(wallet.MonetaryScale)) ||
		(r.RoomRateMultiplier.IsNegative() || (r.RoomRateMultiplier.IsPositive() && !r.RoomRateMultiplier.Equal(r.RoomRateMultiplier.Round(wallet.MonetaryScale))) ||
			r.HourlyFee.IsNegative() || !r.HourlyFee.Equal(r.HourlyFee.Round(wallet.MonetaryScale)) ||
			r.HourlyFeeFreeThreshold.IsNegative() || !r.HourlyFeeFreeThreshold.Equal(r.HourlyFeeFreeThreshold.Round(wallet.MonetaryScale))) {
		return ErrInvalidRoom
	}
	return nil
}

type BindAccountRequest struct{ OwnerUserID, RoomID, AccountID, PrivateGroupID int64 }

func (r BindAccountRequest) Validate() error {
	if r.OwnerUserID <= 0 || r.RoomID <= 0 || r.AccountID <= 0 || r.PrivateGroupID < 0 {
		return ErrInvalidAccount
	}
	return nil
}

// RoomAccountLifecycleRequest is owner-scoped. A bound account has a strict
// one-way lifecycle: active -> draining -> removed.
type RoomAccountLifecycleRequest struct{ OwnerUserID, RoomID, AccountID int64 }

func (r RoomAccountLifecycleRequest) Validate() error {
	if r.OwnerUserID <= 0 || r.RoomID <= 0 || r.AccountID <= 0 {
		return ErrInvalidAccount
	}
	return nil
}

type InviteRequest struct {
	OwnerUserID, RoomID, InvitedUserID int64
	ExpiresAt                          *time.Time
}

func (r InviteRequest) Validate() error {
	if r.OwnerUserID <= 0 || r.RoomID <= 0 || r.InvitedUserID <= 0 || r.OwnerUserID == r.InvitedUserID || (r.ExpiresAt != nil && !r.ExpiresAt.After(time.Now().UTC())) {
		return ErrInvalidMembership
	}
	return nil
}

type JoinRoomRequest struct {
	UserID, RoomID int64
	IdempotencyKey string
}

func (r JoinRoomRequest) Validate() error {
	if r.UserID <= 0 || r.RoomID <= 0 {
		return ErrInvalidMembership
	}
	if !validKey(r.IdempotencyKey) {
		return ErrInvalidIdempotencyKey
	}
	return nil
}

// MembershipDecisionRequest is made by the room owner after a member has
// requested admission to a room that requires approval. Approval is a paid
// admission: membership activation, lease creation, wallet debit, owner
// payout, and private-group access must commit as one transaction.
type MembershipDecisionRequest struct {
	OwnerUserID  int64
	RoomID       int64
	MembershipID int64
	Decision     MembershipDecision
}

func (r MembershipDecisionRequest) Validate() error {
	if r.OwnerUserID <= 0 || r.RoomID <= 0 || r.MembershipID <= 0 ||
		(r.Decision != MembershipApprove && r.Decision != MembershipReject) {
		return ErrInvalidMembership
	}
	return nil
}

type ReviewRequest struct {
	UserID, RoomID, MembershipID int64
	Rating                       int
	Body                         string
}

func (r ReviewRequest) Validate() error {
	if r.UserID <= 0 || r.RoomID <= 0 || r.MembershipID <= 0 || r.Rating < 1 || r.Rating > 5 || len(r.Body) > 1000 {
		return ErrInvalidReview
	}
	return nil
}

// Review is the public, moderation-filtered representation of a room review.
// Internal moderation notes are intentionally excluded from this contract.
type Review struct {
	ID             int64     `json:"id"`
	RoomID         int64     `json:"room_id"`
	MembershipID   int64     `json:"membership_id"`
	ReviewerUserID int64     `json:"reviewer_user_id"`
	Rating         int       `json:"rating"`
	Body           string    `json:"body"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type ReviewModerationRequest struct {
	AdminUserID, ReviewID int64
	Status, Note          string
}

func (r ReviewModerationRequest) Validate() error {
	if r.AdminUserID <= 0 || r.ReviewID <= 0 || (r.Status != "visible" && r.Status != "hidden" && r.Status != "removed") || len(r.Note) > 1000 {
		return ErrInvalidReview
	}
	return nil
}

type RoomModerationRequest struct {
	AdminUserID, RoomID int64
	Status, Note        string
}

func (r RoomModerationRequest) Validate() error {
	if r.AdminUserID <= 0 || r.RoomID <= 0 || (r.Status != string(RoomActive) && r.Status != string(RoomSuspended) && r.Status != string(RoomClosed)) || len(r.Note) > 1000 {
		return ErrInvalidRoom
	}
	return nil
}

type JoinResult struct {
	Membership Membership        `json:"membership"`
	Lease      *Lease            `json:"lease,omitempty"`
	Settlement *SettlementIntent `json:"settlement,omitempty"`
}

// Repository makes each state transition atomic. Implementations must derive
// ownership from the request subject and use room row locks for seat decisions.
type Repository interface {
	ListPublicRooms(context.Context, int, int) ([]Room, int, error)
	ListOwnerRooms(context.Context, int64, int, int) ([]Room, int, error)
	ListUserMemberships(context.Context, int64, int, int) ([]Membership, int, error)
	CreateRoom(context.Context, CreateRoomRequest) (Room, error)
	BindAccount(context.Context, BindAccountRequest) error
	CreateInvite(context.Context, InviteRequest) error
	JoinRoom(context.Context, JoinRoomRequest) (JoinResult, error)
	AcquireLease(context.Context, int64, int64, string) (JoinResult, error)
	HeartbeatLease(context.Context, int64, int64) (Lease, error)
	ReleaseLease(context.Context, int64, int64, string) error
	CreateReview(context.Context, ReviewRequest) error
	ListRoomReviews(context.Context, int64, int64, int, int) ([]Review, int, error)
	ModerateRoom(context.Context, RoomModerationRequest) error
	ModerateReview(context.Context, ReviewModerationRequest) error
	MarkSettlementCharging(context.Context, int64) (SettlementIntent, error)
	FinalizeSettlement(context.Context, int64, PaymentSource, string) (SettlementIntent, error)
	FailSettlement(context.Context, int64, string) error
	ListPayoutReceipts(context.Context, int64, int, int) ([]PayoutReceipt, int, error)
}

// PublicRoomFilterRepository is optional to preserve compatibility with
// integrations that only implement the original room-listing interface.
type PublicRoomFilterRepository interface {
	ListPublicRoomsFiltered(context.Context, PublicRoomFilter, int, int) ([]Room, int, error)
}

// AtomicSettlementRepository keeps the payer debit, owner payout, immutable
// receipt and settlement transition within one SQL transaction.
type AtomicSettlementRepository interface {
	Settle(context.Context, int64, *wallet.Service) (SettlementIntent, error)
}

// AtomicAdmissionRepository keeps a paid room admission in one transaction.
// A membership, lease, wallet debit, owner credit, and private-group grant
// must either commit together or not exist at all.
type AtomicAdmissionRepository interface {
	JoinAndSettle(context.Context, JoinRoomRequest, *wallet.Service) (JoinResult, error)
	AcquireAndSettle(context.Context, int64, int64, string, *wallet.Service) (JoinResult, error)
}

// OwnerMembershipRepository is separate from Repository so existing sharing
// integrations only opt into owner approval when they can preserve the same
// atomic payment boundary as ordinary admission.
type OwnerMembershipRepository interface {
	ListOwnerRoomMemberships(context.Context, int64, int64, MembershipStatus, int, int) ([]Membership, int, error)
	DecideMembershipAndSettle(context.Context, MembershipDecisionRequest, *wallet.Service) (JoinResult, error)
}

// MembershipLifecycleRepository owns a member's explicit exit. It must release
// any active lease, revoke temporary group access, and free a seat atomically.
type MembershipLifecycleRepository interface {
	LeaveMembership(context.Context, int64, int64) error
}

// OwnerRoomAccountRepository is intentionally separate so integrations that
// only implement admission can opt into account lifecycle management without
// weakening the owner-scoped transaction boundary.
type OwnerRoomAccountRepository interface {
	ListOwnerRoomAccounts(context.Context, int64, int64, int, int) ([]RoomAccount, int, error)
	DrainRoomAccount(context.Context, RoomAccountLifecycleRequest) (RoomAccount, error)
	RemoveRoomAccount(context.Context, RoomAccountLifecycleRequest) error
}

type OwnerRoomLifecycleRepository interface {
	UpdateRoom(context.Context, UpdateRoomRequest) (Room, error)
	CloseRoom(context.Context, int64, int64) error
	DeleteRoom(context.Context, int64, int64) error
}

type Service struct {
	repository Repository
	wallet     *wallet.Service
}

func NewService(repository Repository, walletService *wallet.Service) (*Service, error) {
	if repository == nil {
		return nil, ErrRepositoryRequired
	}
	if walletService == nil {
		return nil, ErrWalletRequired
	}
	return &Service{repository: repository, wallet: walletService}, nil
}

func (s *Service) ListPublicRooms(ctx context.Context, limit, offset int) ([]Room, int, error) {
	if !validPage(limit, offset) {
		return nil, 0, applicationError(ErrInvalidPagination)
	}
	rooms, total, err := s.repository.ListPublicRooms(ctx, limit, offset)
	return rooms, total, applicationError(err)
}

func (s *Service) ListPublicRoomsFiltered(ctx context.Context, filter PublicRoomFilter, limit, offset int) ([]Room, int, error) {
	if !validPage(limit, offset) {
		return nil, 0, applicationError(ErrInvalidPagination)
	}
	repository, ok := s.repository.(PublicRoomFilterRepository)
	if !ok {
		return nil, 0, applicationError(ErrRepositoryRequired)
	}
	rooms, total, err := repository.ListPublicRoomsFiltered(ctx, filter, limit, offset)
	return rooms, total, applicationError(err)
}
func (s *Service) ListOwnerRooms(ctx context.Context, ownerUserID int64, limit, offset int) ([]Room, int, error) {
	if ownerUserID <= 0 || !validPage(limit, offset) {
		return nil, 0, applicationError(ErrInvalidPagination)
	}
	rooms, total, err := s.repository.ListOwnerRooms(ctx, ownerUserID, limit, offset)
	return rooms, total, applicationError(err)
}
func (s *Service) ListUserMemberships(ctx context.Context, userID int64, limit, offset int) ([]Membership, int, error) {
	if userID <= 0 || !validPage(limit, offset) {
		return nil, 0, applicationError(ErrInvalidPagination)
	}
	items, total, err := s.repository.ListUserMemberships(ctx, userID, limit, offset)
	return items, total, applicationError(err)
}

func (s *Service) LeaveMembership(ctx context.Context, userID, membershipID int64) error {
	if userID <= 0 || membershipID <= 0 {
		return applicationError(ErrInvalidMembership)
	}
	repository, ok := s.repository.(MembershipLifecycleRepository)
	if !ok {
		return applicationError(ErrRepositoryRequired)
	}
	return applicationError(repository.LeaveMembership(ctx, userID, membershipID))
}
func (s *Service) CreateRoom(ctx context.Context, request CreateRoomRequest) (Room, error) {
	if err := request.Validate(); err != nil {
		return Room{}, applicationError(err)
	}
	room, err := s.repository.CreateRoom(ctx, request)
	return room, applicationError(err)
}
func (s *Service) BindAccount(ctx context.Context, request BindAccountRequest) error {
	if err := request.Validate(); err != nil {
		return applicationError(err)
	}
	return applicationError(s.repository.BindAccount(ctx, request))
}

func (s *Service) ListOwnerRoomAccounts(ctx context.Context, ownerUserID, roomID int64, limit, offset int) ([]RoomAccount, int, error) {
	if ownerUserID <= 0 || roomID <= 0 || !validPage(limit, offset) {
		return nil, 0, applicationError(ErrInvalidAccount)
	}
	repository, ok := s.repository.(OwnerRoomAccountRepository)
	if !ok {
		return nil, 0, applicationError(ErrRepositoryRequired)
	}
	items, total, err := repository.ListOwnerRoomAccounts(ctx, ownerUserID, roomID, limit, offset)
	return items, total, applicationError(err)
}

func (s *Service) DrainRoomAccount(ctx context.Context, request RoomAccountLifecycleRequest) (RoomAccount, error) {
	if err := request.Validate(); err != nil {
		return RoomAccount{}, applicationError(err)
	}
	repository, ok := s.repository.(OwnerRoomAccountRepository)
	if !ok {
		return RoomAccount{}, applicationError(ErrRepositoryRequired)
	}
	account, err := repository.DrainRoomAccount(ctx, request)
	return account, applicationError(err)
}

func (s *Service) RemoveRoomAccount(ctx context.Context, request RoomAccountLifecycleRequest) error {
	if err := request.Validate(); err != nil {
		return applicationError(err)
	}
	repository, ok := s.repository.(OwnerRoomAccountRepository)
	if !ok {
		return applicationError(ErrRepositoryRequired)
	}
	return applicationError(repository.RemoveRoomAccount(ctx, request))
}

func (s *Service) UpdateRoom(ctx context.Context, request UpdateRoomRequest) (Room, error) {
	if err := request.Validate(); err != nil {
		return Room{}, applicationError(err)
	}
	repository, ok := s.repository.(OwnerRoomLifecycleRepository)
	if !ok {
		return Room{}, applicationError(ErrRepositoryRequired)
	}
	room, err := repository.UpdateRoom(ctx, request)
	return room, applicationError(err)
}

func (s *Service) CloseRoom(ctx context.Context, ownerUserID, roomID int64) error {
	if ownerUserID <= 0 || roomID <= 0 {
		return applicationError(ErrInvalidRoom)
	}
	repository, ok := s.repository.(OwnerRoomLifecycleRepository)
	if !ok {
		return applicationError(ErrRepositoryRequired)
	}
	return applicationError(repository.CloseRoom(ctx, ownerUserID, roomID))
}

// DeleteRoom archives a room only after it has been explicitly closed. The
// history remains intact for audit and settlement reconciliation.
func (s *Service) DeleteRoom(ctx context.Context, ownerUserID, roomID int64) error {
	if ownerUserID <= 0 || roomID <= 0 {
		return applicationError(ErrInvalidRoom)
	}
	repository, ok := s.repository.(OwnerRoomLifecycleRepository)
	if !ok {
		return applicationError(ErrRepositoryRequired)
	}
	return applicationError(repository.DeleteRoom(ctx, ownerUserID, roomID))
}

func (s *Service) CreateInvite(ctx context.Context, request InviteRequest) error {
	if err := request.Validate(); err != nil {
		return applicationError(err)
	}
	return applicationError(s.repository.CreateInvite(ctx, request))
}

func (s *Service) JoinRoom(ctx context.Context, request JoinRoomRequest) (JoinResult, error) {
	if err := request.Validate(); err != nil {
		return JoinResult{}, applicationError(err)
	}
	repository, ok := s.repository.(AtomicAdmissionRepository)
	if !ok {
		return JoinResult{}, applicationError(ErrAtomicSettlementRequired)
	}
	result, err := repository.JoinAndSettle(ctx, request, s.wallet)
	return result, applicationError(err)
}

func (s *Service) ListOwnerRoomMemberships(ctx context.Context, ownerUserID, roomID int64, status MembershipStatus, limit, offset int) ([]Membership, int, error) {
	if ownerUserID <= 0 || roomID <= 0 || status != MembershipQueued || !validPage(limit, offset) {
		return nil, 0, applicationError(ErrInvalidMembership)
	}
	repository, ok := s.repository.(OwnerMembershipRepository)
	if !ok {
		return nil, 0, applicationError(ErrAtomicSettlementRequired)
	}
	items, total, err := repository.ListOwnerRoomMemberships(ctx, ownerUserID, roomID, status, limit, offset)
	return items, total, applicationError(err)
}

func (s *Service) DecideMembership(ctx context.Context, request MembershipDecisionRequest) (JoinResult, error) {
	if err := request.Validate(); err != nil {
		return JoinResult{}, applicationError(err)
	}
	repository, ok := s.repository.(OwnerMembershipRepository)
	if !ok {
		return JoinResult{}, applicationError(ErrAtomicSettlementRequired)
	}
	result, err := repository.DecideMembershipAndSettle(ctx, request, s.wallet)
	return result, applicationError(err)
}

// AcquireLease lets an already-active member claim a lease after it was
// promoted from the queue. Joining an active room creates the initial lease
// directly; this endpoint only covers later queue promotion and is idempotent
// at the membership level.
func (s *Service) AcquireLease(ctx context.Context, userID, membershipID int64, idempotencyKey string) (JoinResult, error) {
	if userID <= 0 || membershipID <= 0 || !validKey(idempotencyKey) {
		return JoinResult{}, applicationError(ErrInvalidLease)
	}
	repository, ok := s.repository.(AtomicAdmissionRepository)
	if !ok {
		return JoinResult{}, applicationError(ErrAtomicSettlementRequired)
	}
	result, err := repository.AcquireAndSettle(ctx, userID, membershipID, idempotencyKey, s.wallet)
	return result, applicationError(err)
}

func (s *Service) settle(ctx context.Context, intent SettlementIntent) (SettlementIntent, error) {
	settler, ok := s.repository.(AtomicSettlementRepository)
	if !ok {
		return SettlementIntent{}, applicationError(ErrAtomicSettlementRequired)
	}
	settled, err := settler.Settle(ctx, intent.ID, s.wallet)
	return settled, applicationError(err)
}

func (s *Service) HeartbeatLease(ctx context.Context, userID, leaseID int64) (Lease, error) {
	if userID <= 0 || leaseID <= 0 {
		return Lease{}, applicationError(ErrInvalidLease)
	}
	lease, err := s.repository.HeartbeatLease(ctx, userID, leaseID)
	return lease, applicationError(err)
}
func (s *Service) ReleaseLease(ctx context.Context, userID, leaseID int64) error {
	if userID <= 0 || leaseID <= 0 {
		return applicationError(ErrInvalidLease)
	}
	return applicationError(s.repository.ReleaseLease(ctx, userID, leaseID, "user_released"))
}
func (s *Service) CreateReview(ctx context.Context, request ReviewRequest) error {
	if err := request.Validate(); err != nil {
		return applicationError(err)
	}
	return applicationError(s.repository.CreateReview(ctx, request))
}
func (s *Service) ListRoomReviews(ctx context.Context, viewerUserID, roomID int64, limit, offset int) ([]Review, int, error) {
	if viewerUserID <= 0 || roomID <= 0 || !validPage(limit, offset) {
		return nil, 0, applicationError(ErrInvalidReview)
	}
	items, total, err := s.repository.ListRoomReviews(ctx, viewerUserID, roomID, limit, offset)
	return items, total, applicationError(err)
}
func (s *Service) ModerateRoom(ctx context.Context, request RoomModerationRequest) error {
	if err := request.Validate(); err != nil {
		return applicationError(err)
	}
	return applicationError(s.repository.ModerateRoom(ctx, request))
}
func (s *Service) ModerateReview(ctx context.Context, request ReviewModerationRequest) error {
	if err := request.Validate(); err != nil {
		return applicationError(err)
	}
	return applicationError(s.repository.ModerateReview(ctx, request))
}
func (s *Service) ListPayoutReceipts(ctx context.Context, ownerUserID int64, limit, offset int) ([]PayoutReceipt, int, error) {
	if ownerUserID <= 0 || !validPage(limit, offset) {
		return nil, 0, applicationError(ErrInvalidPagination)
	}
	items, total, err := s.repository.ListPayoutReceipts(ctx, ownerUserID, limit, offset)
	return items, total, applicationError(err)
}

func sourceFromAllocation(allocation wallet.DebitAllocation) PaymentSource {
	if allocation.Bound.IsPositive() && allocation.Normal.IsPositive() {
		return PaymentBoundThenNormal
	}
	if allocation.Bound.IsPositive() {
		return PaymentBound
	}
	return PaymentNormal
}
func validText(value string, max int) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= max
}
func validKey(value string) bool       { return validText(value, 128) }
func validPage(limit, offset int) bool { return limit > 0 && limit <= 100 && offset >= 0 }

func applicationError(err error) error {
	if err == nil {
		return nil
	}
	if infraerrors.FromError(err).Code != http.StatusInternalServerError {
		return err
	}
	switch {
	case errors.Is(err, ErrInvalidRoom), errors.Is(err, ErrInvalidAccount), errors.Is(err, ErrInvalidMembership), errors.Is(err, ErrInvalidLease), errors.Is(err, ErrInvalidReview), errors.Is(err, ErrInvalidIdempotencyKey), errors.Is(err, ErrInvalidPagination), errors.Is(err, ErrInvalidPrivateGroup), errors.Is(err, ErrInvalidSharingPolicy), errors.Is(err, ErrInvalidQuotaPolicy):
		return infraerrors.BadRequest("ACCOUNT_SHARE_INVALID", "Invalid account sharing request").WithCause(err)
	case errors.Is(err, ErrRoomNotFound), errors.Is(err, ErrMembershipNotFound), errors.Is(err, ErrLeaseNotFound), errors.Is(err, ErrSettlementNotFound), errors.Is(err, ErrRoomAccountNotFound), errors.Is(err, ErrPrivateGroupNotFound), errors.Is(err, ErrPolicyNotFound), errors.Is(err, ErrQuotaPolicyNotFound):
		return infraerrors.NotFound("ACCOUNT_SHARE_NOT_FOUND", "Account sharing resource was not found").WithCause(err)
	case errors.Is(err, ErrRoomForbidden), errors.Is(err, ErrPrivateInviteRequired), errors.Is(err, ErrPrivateGroupForbidden):
		return infraerrors.Forbidden("ACCOUNT_SHARE_FORBIDDEN", "You are not allowed to access this sharing room").WithCause(err)
	case errors.Is(err, ErrRoomHasNoAvailableAccount):
		return infraerrors.Conflict("ACCOUNT_SHARE_NO_AVAILABLE_ACCOUNT", "This room has no available account").WithCause(err)
	case errors.Is(err, ErrRoomUnavailable), errors.Is(err, ErrRoomReviewRequired), errors.Is(err, ErrQuotaExceeded), errors.Is(err, ErrAccountAlreadyBound), errors.Is(err, ErrRoomAccountState), errors.Is(err, ErrRoomAccountHasActiveLease), errors.Is(err, ErrRoomHasActiveLeases), errors.Is(err, ErrRoomMustBeClosed), errors.Is(err, ErrReviewNotEligible), errors.Is(err, ErrSettlementState), errors.Is(err, ErrPrivateGroupConflict), errors.Is(err, ErrPrivateGroupOwnerRequired), errors.Is(err, ErrPrivateGroupRequired):
		return infraerrors.Conflict("ACCOUNT_SHARE_CONFLICT", "Account sharing request conflicts with current state").WithCause(err)
	case errors.Is(err, ErrAccountNotOwned):
		return infraerrors.Forbidden("ACCOUNT_SHARE_ACCOUNT_OWNERSHIP", "Only an account owner can share this account").WithCause(err)
	case errors.Is(err, ErrAtomicSettlementRequired), errors.Is(err, ErrPrivateGroupUnavailable), errors.Is(err, ErrGovernanceUnavailable):
		return infraerrors.InternalServer("ACCOUNT_SHARE_SETTLEMENT_UNAVAILABLE", "Account sharing settlement is unavailable").WithCause(err)
	default:
		return err
	}
}
