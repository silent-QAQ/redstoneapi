package operations

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/silent-QAQ/redstoneapi/internal/pkg/response"
	"github.com/silent-QAQ/redstoneapi/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

type Handler struct{ service *Service }

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

func (h *Handler) subject(c *gin.Context) (int64, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return 0, false
	}
	if h == nil || h.service == nil {
		response.Error(c, http.StatusServiceUnavailable, "Operations service is unavailable")
		return 0, false
	}
	return subject.UserID, true
}

func operationError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrInvalidInput):
		response.BadRequest(c, "Invalid operations request")
	case errors.Is(err, ErrForbidden):
		response.Forbidden(c, "Operation is not permitted")
	case errors.Is(err, ErrNotFound):
		response.NotFound(c, "Operations record not found")
	case errors.Is(err, ErrConflict), errors.Is(err, ErrCampaignUnavailable):
		response.Error(c, http.StatusConflict, "Operations state conflict")
	default:
		response.ErrorFrom(c, err)
	}
}

func page(c *gin.Context) (int, int) {
	p, size := response.ParsePagination(c)
	return size, (p - 1) * size
}
func idParam(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid record ID")
		return 0, false
	}
	return id, true
}

func (h *Handler) RequestWithdrawal(c *gin.Context) {
	userID, ok := h.subject(c)
	if !ok {
		return
	}
	var payload struct {
		Amount          decimal.Decimal `json:"amount"`
		FeeAmount       decimal.Decimal `json:"fee_amount"`
		PayoutMethod    string          `json:"payout_method"`
		PayoutReference string          `json:"payout_reference"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid withdrawal request")
		return
	}
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if key == "" {
		response.BadRequest(c, "Idempotency-Key is required")
		return
	}
	item, created, err := h.service.RequestWithdrawal(c.Request.Context(), WithdrawalRequest{UserID: userID, Amount: payload.Amount, FeeAmount: payload.FeeAmount, PayoutMethod: payload.PayoutMethod, PayoutReference: payload.PayoutReference, IdempotencyKey: key})
	if err != nil {
		operationError(c, err)
		return
	}
	if created {
		response.Created(c, item)
		return
	}
	c.Header("X-Idempotency-Replayed", "true")
	response.Success(c, item)
}

func (h *Handler) ListWithdrawals(c *gin.Context) {
	userID, ok := h.subject(c)
	if !ok {
		return
	}
	limit, offset := page(c)
	items, total, err := h.service.ListWithdrawals(c.Request.Context(), userID, limit, offset)
	if err != nil {
		operationError(c, err)
		return
	}
	response.Paginated(c, items, int64(total), offset/limit+1, limit)
}

func (h *Handler) CreateInvoiceProfile(c *gin.Context) {
	userID, ok := h.subject(c)
	if !ok {
		return
	}
	var payload struct {
		InvoiceType    string `json:"invoice_type"`
		TitleName      string `json:"title_name"`
		TaxID          string `json:"tax_id"`
		RecipientEmail string `json:"recipient_email"`
		IsDefault      bool   `json:"is_default"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid invoice profile")
		return
	}
	profile, err := h.service.CreateInvoiceProfile(c.Request.Context(), InvoiceProfileRequest{UserID: userID, InvoiceType: payload.InvoiceType, TitleName: payload.TitleName, TaxID: payload.TaxID, RecipientEmail: payload.RecipientEmail, IsDefault: payload.IsDefault})
	if err != nil {
		operationError(c, err)
		return
	}
	response.Created(c, profile)
}
func (h *Handler) ListInvoiceProfiles(c *gin.Context) {
	userID, ok := h.subject(c)
	if !ok {
		return
	}
	items, err := h.service.ListInvoiceProfiles(c.Request.Context(), userID)
	if err != nil {
		operationError(c, err)
		return
	}
	response.Success(c, items)
}
func (h *Handler) RequestInvoice(c *gin.Context) {
	userID, ok := h.subject(c)
	if !ok {
		return
	}
	var payload struct {
		ProfileID    int64  `json:"profile_id"`
		PaymentRefID string `json:"payment_ref_id"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid invoice request")
		return
	}
	item, created, err := h.service.RequestInvoice(c.Request.Context(), InvoiceRequestInput{UserID: userID, ProfileID: payload.ProfileID, PaymentRefID: payload.PaymentRefID})
	if err != nil {
		operationError(c, err)
		return
	}
	if created {
		response.Created(c, item)
		return
	}
	response.Success(c, item)
}
func (h *Handler) ListInvoices(c *gin.Context) {
	userID, ok := h.subject(c)
	if !ok {
		return
	}
	limit, offset := page(c)
	items, total, err := h.service.ListInvoices(c.Request.Context(), userID, limit, offset)
	if err != nil {
		operationError(c, err)
		return
	}
	response.Paginated(c, items, int64(total), offset/limit+1, limit)
}

func (h *Handler) CreateTicket(c *gin.Context) {
	userID, ok := h.subject(c)
	if !ok {
		return
	}
	var payload struct {
		Subject  string `json:"subject"`
		Category string `json:"category"`
		Body     string `json:"body"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid support ticket")
		return
	}
	item, err := h.service.CreateTicket(c.Request.Context(), TicketRequest{UserID: userID, Subject: payload.Subject, Category: payload.Category, Body: payload.Body})
	if err != nil {
		operationError(c, err)
		return
	}
	response.Created(c, item)
}
func (h *Handler) ListTickets(c *gin.Context) {
	userID, ok := h.subject(c)
	if !ok {
		return
	}
	limit, offset := page(c)
	items, total, err := h.service.ListTickets(c.Request.Context(), userID, limit, offset)
	if err != nil {
		operationError(c, err)
		return
	}
	response.Paginated(c, items, int64(total), offset/limit+1, limit)
}
func (h *Handler) ListTicketMessages(c *gin.Context) {
	userID, ok := h.subject(c)
	if !ok {
		return
	}
	ticketID, ok := idParam(c, "id")
	if !ok {
		return
	}
	items, err := h.service.ListTicketMessages(c.Request.Context(), userID, ticketID, false)
	if err != nil {
		operationError(c, err)
		return
	}
	response.Success(c, items)
}
func (h *Handler) ReplyTicket(c *gin.Context) {
	userID, ok := h.subject(c)
	if !ok {
		return
	}
	ticketID, ok := idParam(c, "id")
	if !ok {
		return
	}
	var payload struct {
		Body string `json:"body"`
	}
	if c.ShouldBindJSON(&payload) != nil {
		response.BadRequest(c, "Invalid ticket reply")
		return
	}
	item, err := h.service.ReplyTicket(c.Request.Context(), userID, ticketID, false, payload.Body)
	if err != nil {
		operationError(c, err)
		return
	}
	response.Success(c, item)
}

func (h *Handler) ListCampaigns(c *gin.Context) {
	if _, ok := h.subject(c); !ok {
		return
	}
	items, err := h.service.ListActiveCampaigns(c.Request.Context())
	if err != nil {
		operationError(c, err)
		return
	}
	response.Success(c, items)
}
func (h *Handler) ClaimCampaign(c *gin.Context) {
	userID, ok := h.subject(c)
	if !ok {
		return
	}
	campaignID, ok := idParam(c, "id")
	if !ok {
		return
	}
	amount, created, err := h.service.ClaimCampaign(c.Request.Context(), userID, campaignID)
	if err != nil {
		operationError(c, err)
		return
	}
	if !created {
		c.Header("X-Idempotency-Replayed", "true")
	}
	response.Success(c, gin.H{"amount": amount, "granted": true})
}

func (h *Handler) ReportContent(c *gin.Context) {
	userID, ok := h.subject(c)
	if !ok {
		return
	}
	var payload struct {
		SubjectType string `json:"subject_type"`
		SubjectID   string `json:"subject_id"`
		Reason      string `json:"reason"`
	}
	if c.ShouldBindJSON(&payload) != nil {
		response.BadRequest(c, "Invalid content report")
		return
	}
	item, created, err := h.service.ReportContent(c.Request.Context(), userID, payload.SubjectType, payload.SubjectID, payload.Reason)
	if err != nil {
		operationError(c, err)
		return
	}
	if created {
		response.Created(c, item)
		return
	}
	response.Success(c, item)
}
func (h *Handler) ListProxyOptions(c *gin.Context) {
	userID, ok := h.subject(c)
	if !ok {
		return
	}
	items, err := h.service.ListAvailableProxies(c.Request.Context(), userID)
	if err != nil {
		operationError(c, err)
		return
	}
	response.Success(c, items)
}

func (h *Handler) AdminListWithdrawals(c *gin.Context) {
	adminID, ok := h.subject(c)
	_ = adminID
	if !ok {
		return
	}
	limit, offset := page(c)
	items, total, err := h.service.ListWithdrawalQueue(c.Request.Context(), limit, offset)
	if err != nil {
		operationError(c, err)
		return
	}
	response.Paginated(c, items, int64(total), offset/limit+1, limit)
}
func (h *Handler) AdminResolveWithdrawal(c *gin.Context) {
	adminID, ok := h.subject(c)
	if !ok {
		return
	}
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	var payload struct {
		Action string `json:"action"`
		Note   string `json:"note"`
	}
	if c.ShouldBindJSON(&payload) != nil {
		response.BadRequest(c, "Invalid withdrawal decision")
		return
	}
	item, err := h.service.ResolveWithdrawal(c.Request.Context(), adminID, id, payload.Action, payload.Note)
	if err != nil {
		operationError(c, err)
		return
	}
	response.Success(c, item)
}
func (h *Handler) AdminAdjustBalance(c *gin.Context) {
	adminID, ok := h.subject(c)
	if !ok {
		return
	}
	var payload struct {
		UserID      int64           `json:"user_id"`
		Delta       decimal.Decimal `json:"delta"`
		ReferenceID string          `json:"reference_id"`
		Note        string          `json:"note"`
	}
	if c.ShouldBindJSON(&payload) != nil {
		response.BadRequest(c, "Invalid balance adjustment")
		return
	}
	item, err := h.service.AdjustNormal(c.Request.Context(), adminID, payload.UserID, payload.Delta, payload.ReferenceID, payload.Note)
	if err != nil {
		operationError(c, err)
		return
	}
	response.Success(c, item)
}
func (h *Handler) AdminGrantReferral(c *gin.Context) {
	adminID, ok := h.subject(c)
	if !ok {
		return
	}
	var payload struct {
		InviterUserID int64           `json:"inviter_user_id"`
		InvitedUserID int64           `json:"invited_user_id"`
		SourceType    string          `json:"source_type"`
		SourceID      string          `json:"source_id"`
		Amount        decimal.Decimal `json:"amount"`
	}
	if c.ShouldBindJSON(&payload) != nil {
		response.BadRequest(c, "Invalid referral reward")
		return
	}
	if err := h.service.GrantReferralReward(c.Request.Context(), adminID, payload.InviterUserID, payload.InvitedUserID, payload.SourceType, payload.SourceID, payload.Amount); err != nil {
		operationError(c, err)
		return
	}
	response.Success(c, gin.H{"message": "Referral reward granted"})
}
func (h *Handler) AdminResolveInvoice(c *gin.Context) {
	adminID, ok := h.subject(c)
	if !ok {
		return
	}
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	var payload struct {
		Status        string `json:"status"`
		InvoiceNumber string `json:"invoice_number"`
		FileReference string `json:"file_reference"`
		Note          string `json:"note"`
	}
	if c.ShouldBindJSON(&payload) != nil {
		response.BadRequest(c, "Invalid invoice decision")
		return
	}
	item, err := h.service.ResolveInvoice(c.Request.Context(), adminID, id, payload.Status, payload.InvoiceNumber, payload.FileReference, payload.Note)
	if err != nil {
		operationError(c, err)
		return
	}
	response.Success(c, item)
}
func (h *Handler) AdminListTickets(c *gin.Context) {
	if _, ok := h.subject(c); !ok {
		return
	}
	limit, offset := page(c)
	items, total, err := h.service.ListTicketQueue(c.Request.Context(), limit, offset)
	if err != nil {
		operationError(c, err)
		return
	}
	response.Paginated(c, items, int64(total), offset/limit+1, limit)
}
func (h *Handler) AdminListTicketMessages(c *gin.Context) {
	adminID, ok := h.subject(c)
	if !ok {
		return
	}
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	items, err := h.service.ListTicketMessages(c.Request.Context(), adminID, id, true)
	if err != nil {
		operationError(c, err)
		return
	}
	response.Success(c, items)
}
func (h *Handler) AdminReplyTicket(c *gin.Context) {
	adminID, ok := h.subject(c)
	if !ok {
		return
	}
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	var payload struct {
		Body string `json:"body"`
	}
	if c.ShouldBindJSON(&payload) != nil {
		response.BadRequest(c, "Invalid ticket reply")
		return
	}
	item, err := h.service.ReplyTicket(c.Request.Context(), adminID, id, true, payload.Body)
	if err != nil {
		operationError(c, err)
		return
	}
	response.Success(c, item)
}
func (h *Handler) AdminResolveTicket(c *gin.Context) {
	adminID, ok := h.subject(c)
	if !ok {
		return
	}
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	var payload struct {
		Status          string `json:"status"`
		Priority        string `json:"priority"`
		AssignedAdminID *int64 `json:"assigned_admin_id"`
	}
	if c.ShouldBindJSON(&payload) != nil {
		response.BadRequest(c, "Invalid ticket decision")
		return
	}
	item, err := h.service.ResolveTicket(c.Request.Context(), adminID, id, payload.Status, payload.Priority, payload.AssignedAdminID)
	if err != nil {
		operationError(c, err)
		return
	}
	response.Success(c, item)
}
func (h *Handler) AdminCreateCampaign(c *gin.Context) {
	adminID, ok := h.subject(c)
	if !ok {
		return
	}
	var payload struct {
		Name         string          `json:"name"`
		Description  string          `json:"description"`
		StartsAt     time.Time       `json:"starts_at"`
		EndsAt       time.Time       `json:"ends_at"`
		RewardAmount decimal.Decimal `json:"reward_amount"`
		MaxClaims    int             `json:"max_claims"`
	}
	if c.ShouldBindJSON(&payload) != nil {
		response.BadRequest(c, "Invalid campaign")
		return
	}
	item, err := h.service.CreateCampaign(c.Request.Context(), CampaignRequest{AdminUserID: adminID, Name: payload.Name, Description: payload.Description, StartsAt: payload.StartsAt, EndsAt: payload.EndsAt, RewardAmount: payload.RewardAmount, MaxClaims: payload.MaxClaims})
	if err != nil {
		operationError(c, err)
		return
	}
	response.Created(c, item)
}
func (h *Handler) AdminSetCampaignStatus(c *gin.Context) {
	adminID, ok := h.subject(c)
	if !ok {
		return
	}
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	var payload struct {
		Status string `json:"status"`
	}
	if c.ShouldBindJSON(&payload) != nil {
		response.BadRequest(c, "Invalid campaign status")
		return
	}
	item, err := h.service.SetCampaignStatus(c.Request.Context(), adminID, id, payload.Status)
	if err != nil {
		operationError(c, err)
		return
	}
	response.Success(c, item)
}
func (h *Handler) AdminResolveContent(c *gin.Context) {
	adminID, ok := h.subject(c)
	if !ok {
		return
	}
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	var payload struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if c.ShouldBindJSON(&payload) != nil {
		response.BadRequest(c, "Invalid content decision")
		return
	}
	item, err := h.service.ResolveContentCase(c.Request.Context(), adminID, id, payload.Status, payload.Note)
	if err != nil {
		operationError(c, err)
		return
	}
	response.Success(c, item)
}
func (h *Handler) AdminAssignProxy(c *gin.Context) {
	adminID, ok := h.subject(c)
	if !ok {
		return
	}
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	var payload struct {
		OwnerUserID *int64 `json:"owner_user_id"`
		MaxAccounts int    `json:"max_accounts"`
	}
	if c.ShouldBindJSON(&payload) != nil {
		response.BadRequest(c, "Invalid proxy assignment")
		return
	}
	if err := h.service.AssignProxy(c.Request.Context(), adminID, id, payload.OwnerUserID, payload.MaxAccounts); err != nil {
		operationError(c, err)
		return
	}
	response.Success(c, gin.H{"message": "Proxy assignment updated"})
}
