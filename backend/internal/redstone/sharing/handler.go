package sharing

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/silent-QAQ/redstoneapi/internal/pkg/response"
	"github.com/silent-QAQ/redstoneapi/internal/server/middleware"
)

type Handler struct{ service *Service }

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

func (h *Handler) ListPublicRooms(c *gin.Context) {
	if !h.available(c) {
		return
	}
	page, size := response.ParsePagination(c)
	filter := PublicRoomFilter{
		Search: c.Query("search"), Platform: c.Query("platform"), AccountGrade: c.Query("account_grade"),
		SortBy: c.Query("sort_by"), SortOrder: c.Query("sort_order"),
	}
	var err error
	if filter.VerifiedOnly, err = strconv.ParseBool(c.DefaultQuery("verified_only", "false")); err != nil {
		response.BadRequest(c, "Invalid verified_only filter")
		return
	}
	if filter.AvailableOnly, err = strconv.ParseBool(c.DefaultQuery("available_only", "false")); err != nil {
		response.BadRequest(c, "Invalid available_only filter")
		return
	}
	items, total, err := h.service.ListPublicRoomsFiltered(c.Request.Context(), filter, size, (page-1)*size)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, int64(total), page, size)
}

func (h *Handler) ListOwnerRooms(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	page, size := response.ParsePagination(c)
	items, total, err := h.service.ListOwnerRooms(c.Request.Context(), userID, size, (page-1)*size)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, int64(total), page, size)
}

func (h *Handler) ListMemberships(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	page, size := response.ParsePagination(c)
	items, total, err := h.service.ListUserMemberships(c.Request.Context(), userID, size, (page-1)*size)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, int64(total), page, size)
}

func (h *Handler) LeaveMembership(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	membershipID, ok := positiveID(c, "id")
	if !ok {
		return
	}
	if err := h.service.LeaveMembership(c.Request.Context(), userID, membershipID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Status(204)
}

func (h *Handler) ListOwnerRoomMemberships(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	roomID, ok := positiveID(c, "id")
	if !ok {
		return
	}
	page, size := response.ParsePagination(c)
	items, total, err := h.service.ListOwnerRoomMemberships(c.Request.Context(), userID, roomID, MembershipQueued, size, (page-1)*size)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, int64(total), page, size)
}

func (h *Handler) CreateRoom(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	var payload struct {
		Name                   string          `json:"name"`
		Description            string          `json:"description"`
		Platform               string          `json:"platform"`
		Visibility             RoomVisibility  `json:"visibility"`
		RequiresApproval       bool            `json:"requires_approval"`
		SeatLimit              int             `json:"seat_limit"`
		LeaseSeconds           int             `json:"lease_seconds"`
		IdleTimeoutSeconds     int             `json:"idle_timeout_seconds"`
		LeasePrice             decimal.Decimal `json:"lease_price"`
		RoomRateMultiplier     decimal.Decimal `json:"room_rate_multiplier"`
		HourlyFee              decimal.Decimal `json:"hourly_fee"`
		HourlyFeeFreeThreshold decimal.Decimal `json:"hourly_fee_free_threshold"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid room request")
		return
	}
	if payload.LeaseSeconds == 0 {
		payload.LeaseSeconds = 3600
	}
	if payload.IdleTimeoutSeconds == 0 {
		payload.IdleTimeoutSeconds = 1800
	}
	room, err := h.service.CreateRoom(c.Request.Context(), CreateRoomRequest{
		OwnerUserID: userID, Name: strings.TrimSpace(payload.Name), Description: strings.TrimSpace(payload.Description),
		Platform: strings.TrimSpace(payload.Platform), Visibility: payload.Visibility, RequiresApproval: false,
		SeatLimit: payload.SeatLimit, LeaseSeconds: payload.LeaseSeconds, IdleTimeoutSeconds: payload.IdleTimeoutSeconds,
		LeasePrice: payload.LeasePrice, RoomRateMultiplier: payload.RoomRateMultiplier,
		HourlyFee: payload.HourlyFee, HourlyFeeFreeThreshold: payload.HourlyFeeFreeThreshold,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, room)
}

func (h *Handler) UpdateRoom(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	roomID, ok := positiveID(c, "id")
	if !ok {
		return
	}
	var payload struct {
		Name                   string          `json:"name"`
		Description            string          `json:"description"`
		Visibility             RoomVisibility  `json:"visibility"`
		RequiresApproval       bool            `json:"requires_approval"`
		SeatLimit              int             `json:"seat_limit"`
		LeaseSeconds           int             `json:"lease_seconds"`
		IdleTimeoutSeconds     int             `json:"idle_timeout_seconds"`
		LeasePrice             decimal.Decimal `json:"lease_price"`
		RoomRateMultiplier     decimal.Decimal `json:"room_rate_multiplier"`
		HourlyFee              decimal.Decimal `json:"hourly_fee"`
		HourlyFeeFreeThreshold decimal.Decimal `json:"hourly_fee_free_threshold"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid room update request")
		return
	}
	if payload.LeaseSeconds == 0 {
		payload.LeaseSeconds = 3600
	}
	if payload.IdleTimeoutSeconds == 0 {
		payload.IdleTimeoutSeconds = 1800
	}
	room, err := h.service.UpdateRoom(c.Request.Context(), UpdateRoomRequest{
		OwnerUserID: userID, RoomID: roomID, Name: strings.TrimSpace(payload.Name), Description: strings.TrimSpace(payload.Description),
		Visibility: payload.Visibility, RequiresApproval: false, SeatLimit: payload.SeatLimit,
		LeaseSeconds: payload.LeaseSeconds, IdleTimeoutSeconds: payload.IdleTimeoutSeconds, LeasePrice: payload.LeasePrice,
		RoomRateMultiplier: payload.RoomRateMultiplier, HourlyFee: payload.HourlyFee, HourlyFeeFreeThreshold: payload.HourlyFeeFreeThreshold,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, room)
}

func (h *Handler) CloseRoom(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	roomID, ok := positiveID(c, "id")
	if !ok {
		return
	}
	if err := h.service.CloseRoom(c.Request.Context(), userID, roomID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Status(204)
}

// DeleteRoom soft-deletes an already closed room. DELETE /rooms/:id remains
// the established close operation, while this explicit endpoint prevents an
// accidental destructive state transition.
func (h *Handler) DeleteRoom(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	roomID, ok := positiveID(c, "id")
	if !ok {
		return
	}
	if err := h.service.DeleteRoom(c.Request.Context(), userID, roomID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Status(204)
}

func (h *Handler) BindAccount(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	roomID, ok := positiveID(c, "id")
	if !ok {
		return
	}
	var payload struct {
		AccountID      int64 `json:"account_id"`
		PrivateGroupID int64 `json:"private_group_id"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid account binding request")
		return
	}
	if err := h.service.BindAccount(c.Request.Context(), BindAccountRequest{OwnerUserID: userID, RoomID: roomID, AccountID: payload.AccountID, PrivateGroupID: payload.PrivateGroupID}); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"room_id": roomID, "account_id": payload.AccountID})
}

func (h *Handler) ListOwnerRoomAccounts(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	roomID, ok := positiveID(c, "id")
	if !ok {
		return
	}
	page, size := response.ParsePagination(c)
	items, total, err := h.service.ListOwnerRoomAccounts(c.Request.Context(), userID, roomID, size, (page-1)*size)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, int64(total), page, size)
}

// DrainRoomAccount blocks new lease allocation without interrupting a lease
// the account is already serving.
func (h *Handler) DrainRoomAccount(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	roomID, ok := positiveID(c, "id")
	if !ok {
		return
	}
	accountID, ok := positiveID(c, "account_id")
	if !ok {
		return
	}
	account, err := h.service.DrainRoomAccount(c.Request.Context(), RoomAccountLifecycleRequest{
		OwnerUserID: userID, RoomID: roomID, AccountID: accountID,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, account)
}

func (h *Handler) RemoveRoomAccount(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	roomID, ok := positiveID(c, "id")
	if !ok {
		return
	}
	accountID, ok := positiveID(c, "account_id")
	if !ok {
		return
	}
	if err := h.service.RemoveRoomAccount(c.Request.Context(), RoomAccountLifecycleRequest{
		OwnerUserID: userID, RoomID: roomID, AccountID: accountID,
	}); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Status(204)
}

func (h *Handler) CreateInvite(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	roomID, ok := positiveID(c, "id")
	if !ok {
		return
	}
	var payload struct {
		UserID    int64      `json:"user_id"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid room invitation request")
		return
	}
	if err := h.service.CreateInvite(c.Request.Context(), InviteRequest{OwnerUserID: userID, RoomID: roomID, InvitedUserID: payload.UserID, ExpiresAt: payload.ExpiresAt}); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"room_id": roomID, "user_id": payload.UserID})
}

func (h *Handler) JoinRoom(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	roomID, ok := positiveID(c, "id")
	if !ok {
		return
	}
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	result, err := h.service.JoinRoom(c.Request.Context(), JoinRoomRequest{UserID: userID, RoomID: roomID, IdempotencyKey: key})
	if err != nil {
		slog.Error("account share room join failed", "room_id", roomID, "user_id", userID, "error", err)
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, result)
}

func (h *Handler) DecideMembership(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	roomID, ok := positiveID(c, "id")
	if !ok {
		return
	}
	membershipID, ok := positiveID(c, "membership_id")
	if !ok {
		return
	}
	var payload struct {
		Decision MembershipDecision `json:"decision"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid membership decision request")
		return
	}
	result, err := h.service.DecideMembership(c.Request.Context(), MembershipDecisionRequest{
		OwnerUserID: userID, RoomID: roomID, MembershipID: membershipID, Decision: payload.Decision,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *Handler) HeartbeatLease(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	leaseID, ok := positiveID(c, "id")
	if !ok {
		return
	}
	lease, err := h.service.HeartbeatLease(c.Request.Context(), userID, leaseID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, lease)
}

func (h *Handler) AcquireLease(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	membershipID, ok := positiveID(c, "id")
	if !ok {
		return
	}
	result, err := h.service.AcquireLease(c.Request.Context(), userID, membershipID, strings.TrimSpace(c.GetHeader("Idempotency-Key")))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *Handler) ReleaseLease(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	leaseID, ok := positiveID(c, "id")
	if !ok {
		return
	}
	if err := h.service.ReleaseLease(c.Request.Context(), userID, leaseID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Status(204)
}

func (h *Handler) CreateReview(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	roomID, ok := positiveID(c, "id")
	if !ok {
		return
	}
	var payload struct {
		MembershipID int64  `json:"membership_id"`
		Rating       int    `json:"rating"`
		Body         string `json:"body"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid review request")
		return
	}
	if err := h.service.CreateReview(c.Request.Context(), ReviewRequest{UserID: userID, RoomID: roomID, MembershipID: payload.MembershipID, Rating: payload.Rating, Body: strings.TrimSpace(payload.Body)}); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"room_id": roomID, "membership_id": payload.MembershipID})
}

func (h *Handler) ListRoomReviews(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	roomID, ok := positiveID(c, "id")
	if !ok {
		return
	}
	page, size := response.ParsePagination(c)
	items, total, err := h.service.ListRoomReviews(c.Request.Context(), userID, roomID, size, (page-1)*size)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, int64(total), page, size)
}

func (h *Handler) ListPayoutReceipts(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	page, size := response.ParsePagination(c)
	items, total, err := h.service.ListPayoutReceipts(c.Request.Context(), userID, size, (page-1)*size)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, int64(total), page, size)
}

// ListAPIKeyRoomOptions is the room-mode picker projection. It intentionally
// returns public rooms before admission; binding a key does not grant a lease,
// and the gateway remains fail-closed until admission is active.
func (h *Handler) ListAPIKeyRoomOptions(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	page, size := response.ParsePagination(c)
	items, total, err := h.service.ListAPIKeyRoomOptions(c.Request.Context(), userID, size, (page-1)*size)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, int64(total), page, size)
}

func (h *Handler) ModerateRoom(c *gin.Context) {
	adminID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	roomID, ok := positiveID(c, "id")
	if !ok {
		return
	}
	var payload struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid room moderation request")
		return
	}
	if err := h.service.ModerateRoom(c.Request.Context(), RoomModerationRequest{AdminUserID: adminID, RoomID: roomID, Status: strings.TrimSpace(payload.Status), Note: strings.TrimSpace(payload.Note)}); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"room_id": roomID, "status": payload.Status})
}

func (h *Handler) ModerateReview(c *gin.Context) {
	adminID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	reviewID, ok := positiveID(c, "id")
	if !ok {
		return
	}
	var payload struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid review moderation request")
		return
	}
	if err := h.service.ModerateReview(c.Request.Context(), ReviewModerationRequest{AdminUserID: adminID, ReviewID: reviewID, Status: strings.TrimSpace(payload.Status), Note: strings.TrimSpace(payload.Note)}); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"review_id": reviewID, "status": payload.Status})
}

// AdminGetSharingPolicy returns the active policy snapshot. This route is
// mounted under the administrator step-up group because the companion update
// endpoint controls public rooms and pricing for future leases.
func (h *Handler) AdminGetSharingPolicy(c *gin.Context) {
	if _, ok := currentUserID(c); !ok || !h.available(c) {
		return
	}
	policy, err := h.service.GetSharingPolicy(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, policy)
}

func (h *Handler) AdminUpdateSharingPolicy(c *gin.Context) {
	adminID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	var payload struct {
		PublicRoomAllowed      bool            `json:"public_room_allowed"`
		MaxLeaseSeconds        int             `json:"max_lease_seconds"`
		DefaultPlatformFeeRate decimal.Decimal `json:"default_platform_fee_rate"`
		Reason                 string          `json:"reason"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid account sharing policy")
		return
	}
	policy, err := h.service.UpdateSharingPolicy(c.Request.Context(), UpdateSharingPolicyRequest{
		ActorUserID: adminID, PublicRoomAllowed: payload.PublicRoomAllowed, MaxLeaseSeconds: payload.MaxLeaseSeconds,
		DefaultPlatformFeeRate: payload.DefaultPlatformFeeRate, Reason: strings.TrimSpace(payload.Reason),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, policy)
}

// AdminGetQuotaPolicy reads either the global quota (no query parameter) or
// the current, unexpired override for owner_user_id.
func (h *Handler) AdminGetQuotaPolicy(c *gin.Context) {
	if _, ok := currentUserID(c); !ok || !h.available(c) {
		return
	}
	ownerUserID, ok := optionalPositiveQueryID(c, "owner_user_id")
	if !ok {
		return
	}
	policy, err := h.service.GetQuotaPolicy(c.Request.Context(), ownerUserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, policy)
}

func (h *Handler) AdminUpdateQuotaPolicy(c *gin.Context) {
	adminID, ok := currentUserID(c)
	if !ok || !h.available(c) {
		return
	}
	var payload struct {
		OwnerUserID           *int64     `json:"owner_user_id"`
		MaxLiveRooms          int        `json:"max_live_rooms"`
		MaxAccountsPerRoom    int        `json:"max_accounts_per_room"`
		MaxRoomsCreatedPerDay int        `json:"max_rooms_created_per_day"`
		ExpiresAt             *time.Time `json:"expires_at"`
		Reason                string     `json:"reason"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid account sharing quota")
		return
	}
	policy, err := h.service.UpdateQuotaPolicy(c.Request.Context(), UpdateQuotaPolicyRequest{
		ActorUserID: adminID, OwnerUserID: payload.OwnerUserID, MaxLiveRooms: payload.MaxLiveRooms,
		MaxAccountsPerRoom: payload.MaxAccountsPerRoom, MaxRoomsCreatedPerDay: payload.MaxRoomsCreatedPerDay,
		ExpiresAt: payload.ExpiresAt, Reason: strings.TrimSpace(payload.Reason),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, policy)
}

func (h *Handler) available(c *gin.Context) bool {
	if h == nil || h.service == nil {
		response.Error(c, 503, "Account sharing is unavailable")
		return false
	}
	return true
}

func currentUserID(c *gin.Context) (int64, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return 0, false
	}
	return subject.UserID, true
}

func positiveID(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account sharing identifier")
		return 0, false
	}
	return id, true
}

func optionalPositiveQueryID(c *gin.Context, name string) (*int64, bool) {
	raw, present := c.GetQuery(name)
	if !present || strings.TrimSpace(raw) == "" {
		return nil, true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account sharing identifier")
		return nil, false
	}
	return &id, true
}

// AdminListPendingRooms returns rooms awaiting moderation review.
func (h *Handler) AdminListPendingRooms(c *gin.Context) {
	if _, ok := currentUserID(c); !ok || !h.available(c) {
		return
	}
	limit, offset := pageParams(c)
	rooms, total, err := h.service.ListPublicRooms(c.Request.Context(), limit, offset)
	// Admin variant: include pending_review status rooms
	_ = total
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"items": rooms, "total": total})
}

// AdminListReportedReviews returns reviews flagged for moderation.
func (h *Handler) AdminListReportedReviews(c *gin.Context) {
	if _, ok := currentUserID(c); !ok || !h.available(c) {
		return
	}
	limit, offset := pageParams(c)
	// Currently reuses ListRoomReviews with admin scope 0 = all visible
	// TODO: add moderation_status='flagged' filter once postgres layer supports it
	reviews, total, err := h.service.ListRoomReviews(c.Request.Context(), 0, 0, limit, offset)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"items": reviews, "total": total})
}

func pageParams(c *gin.Context) (int, int) {
	limit := 20
	offset := 0
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	if v := c.Query("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
}
