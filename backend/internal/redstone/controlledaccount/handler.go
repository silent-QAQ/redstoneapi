package controlledaccount

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	adminhandler "github.com/silent-QAQ/redstoneapi/internal/handler/admin"
	"github.com/silent-QAQ/redstoneapi/internal/pkg/response"
	"github.com/silent-QAQ/redstoneapi/internal/server/middleware"
	"github.com/silent-QAQ/redstoneapi/internal/service"
	"github.com/gin-gonic/gin"
)

// Handler exposes the administrator account-management surface under a
// user-owned scope. The account handlers are shared; ownedAdminService injects
// owner_user_id into creation and rejects cross-owner reads and mutations.
type Handler struct {
	service                 *Service
	verifier                *AccountVerifier
	scheduler               *VerificationScheduler
	db                      interface{ QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error); QueryRowContext(context.Context, string, ...interface{}) *sql.Row }
	ownedAdminService       *ownedAdminService
	ownedAccountHandler     *adminhandler.AccountHandler
	ownedOAuthHandler       *adminhandler.OAuthHandler
	ownedOpenAIHandler      *adminhandler.OpenAIOAuthHandler
	ownedGeminiHandler      *adminhandler.GeminiOAuthHandler
	ownedAntigravityHandler *adminhandler.AntigravityOAuthHandler
	ownedGrokHandler        *adminhandler.GrokOAuthHandler
	proxyHandler            *adminhandler.ProxyHandler
	tlsFingerprintHandler   *adminhandler.TLSFingerprintProfileHandler
	settingService          *service.SettingService
	oauthService            *service.OAuthService
	openaiOAuthService      *service.OpenAIOAuthService
	geminiOAuthService      *service.GeminiOAuthService
	antigravityOAuthService *service.AntigravityOAuthService
	grokOAuthService        *service.GrokOAuthService
	adminService            service.AdminService
	oauthBindingStore       *oauthBindingStore
	scheduledTestService    *service.ScheduledTestService
	crsSyncService          *service.CRSSyncService

}

func NewHandler(service *Service) *Handler {
	return &Handler{
		service:           service,
		oauthBindingStore: newOAuthBindingStore(),
	}
}

func (h *Handler) List(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if !h.available(c) {
		return
	}
	accounts, err := h.service.ListOwned(c.Request.Context(), subject.UserID)
	if err != nil {
		writeAccountError(c, err)
		return
	}
	response.Success(c, accounts)
}

type createAccountPayload struct {
	Name     string `json:"name" binding:"required,max=100"`
	Provider string `json:"provider" binding:"required,max=50"`
	APIKey   string `json:"api_key" binding:"required,max=20000"`
}

func (h *Handler) Create(c *gin.Context) {
	middleware.SetAuditAction(c, "user.controlled_accounts.create")
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if !h.available(c) {
		return
	}
	var payload createAccountPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid account request")
		return
	}
	credentials, err := json.Marshal(map[string]string{"api_key": strings.TrimSpace(payload.APIKey)})
	// The JSON byte buffer is wiped by Service.Create. Drop the request copy as
	// soon as it has been encoded, and never write it to an audit or application log.
	payload.APIKey = ""
	if err != nil {
		response.BadRequest(c, "Invalid account credentials")
		return
	}
	account, err := h.service.Create(c.Request.Context(), CreateRequest{
		OwnerUserID:    subject.UserID,
		Name:           strings.TrimSpace(payload.Name),
		Provider:       strings.ToLower(strings.TrimSpace(payload.Provider)),
		Authentication: "api_key",
		Credentials:    credentials,
	})
	if err != nil {
		writeAccountError(c, err)
		return
	}
	response.Created(c, account)
}

type renameAccountPayload struct {
	Name string `json:"name" binding:"required,max=100"`
}

func (h *Handler) Rename(c *gin.Context) {
	middleware.SetAuditAction(c, "user.controlled_accounts.rename")
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if !h.available(c) {
		return
	}
	accountID, ok := accountIDFromParam(c)
	if !ok {
		return
	}
	var payload renameAccountPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		response.BadRequest(c, "Invalid account request")
		return
	}
	if err := h.service.Rename(c.Request.Context(), subject.UserID, accountID, strings.TrimSpace(payload.Name)); err != nil {
		writeAccountError(c, err)
		return
	}
	response.Success(c, gin.H{"message": "Account renamed"})
}

func (h *Handler) Disable(c *gin.Context) {
	h.setDisabled(c, true)
}

func (h *Handler) Enable(c *gin.Context) {
	h.setDisabled(c, false)
}

// Verify manually triggers account verification
// POST /api/v1/user/controlled-accounts/:id/verify
func (h *Handler) Verify(c *gin.Context) {
	middleware.SetAuditAction(c, "user.controlled_accounts.verify")

	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if !h.available(c) {
		return
	}
	accountID, ok := accountIDFromParam(c)
	if !ok {
		return
	}

	// 检查 scheduler 是否可用
	if h.scheduler == nil {
		response.Error(c, 501, "Account verification is not enabled")
		return
	}

	// 执行验真
	result, err := h.scheduler.VerifyAccountManually(c.Request.Context(), subject.UserID, accountID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			response.NotFound(c, "Account not found")
			return
		}
		response.Error(c, 500, "Verification failed: "+err.Error())
		return
	}

	// 返回详细结果
	response.Success(c, gin.H{
		"account_id": result.AccountID,
		"success":    result.Success,
		"score":      result.Score,
		"verdict":    result.Verdict,
		"protocol":   result.Protocol,
		"message":    result.Message,
		"timestamp":  result.Timestamp,
		"duration":   result.Duration.Milliseconds(),
	})
}

// VerifyHistory returns verification history for an account
// GET /api/v1/user/controlled-accounts/:id/verify-history
func (h *Handler) VerifyHistory(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if !h.available(c) {
		return
	}
	accountID, ok := accountIDFromParam(c)
	if !ok {
		return
	}

	// 验证账号归属
	exists, err := h.service.OwnsAccount(c.Request.Context(), subject.UserID, accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !exists {
		response.NotFound(c, "Account not found")
		return
	}

	// 查询验真历史（最近 20 条）
	var history []verifyHistoryItem
	rows, err := h.service.db.QueryContext(c.Request.Context(), `
		SELECT id, protocol, verdict, score, duration_ms,
		       triggered_by, created_at
		FROM redstone_account_verify_runs
		WHERE account_id = $1
		ORDER BY created_at DESC
		LIMIT 20
	`, accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var item verifyHistoryItem
		if err := rows.Scan(&item.ID, &item.Protocol, &item.Verdict, &item.Score,
			&item.DurationMs, &item.TriggeredBy, &item.CreatedAt); err != nil {
			continue
		}
		history = append(history, item)
	}

	if len(history) == 0 {
		history = []verifyHistoryItem{}
	}

	response.Success(c, gin.H{
		"account_id": accountID,
		"history":    history,
	})
}

type verifyHistoryItem struct {
	ID          int64  `json:"id"`
	Protocol    string `json:"protocol"`
	Verdict     string `json:"verdict"`
	Score       int    `json:"score"`
	DurationMs  int    `json:"duration_ms"`
	TriggeredBy string `json:"triggered_by"`
	CreatedAt   string `json:"created_at"`
}

func (h *Handler) setDisabled(c *gin.Context, disabled bool) {
	if disabled {
		middleware.SetAuditAction(c, "user.controlled_accounts.disable")
	} else {
		middleware.SetAuditAction(c, "user.controlled_accounts.enable")
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if !h.available(c) {
		return
	}
	accountID, ok := accountIDFromParam(c)
	if !ok {
		return
	}
	if err := h.service.SetAPIKeyDisabled(c.Request.Context(), subject.UserID, accountID, disabled); err != nil {
		writeAccountError(c, err)
		return
	}
	response.Success(c, gin.H{"message": "Account lifecycle updated"})
}

func (h *Handler) Delete(c *gin.Context) {
	middleware.SetAuditAction(c, "user.controlled_accounts.delete")
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if !h.available(c) {
		return
	}
	accountID, ok := accountIDFromParam(c)
	if !ok {
		return
	}
	if err := h.service.Revoke(c.Request.Context(), subject.UserID, accountID); err != nil {
		writeAccountError(c, err)
		return
	}
	response.Success(c, gin.H{"message": "Account deleted"})
}

func (h *Handler) available(c *gin.Context) bool {
	if h == nil || h.service == nil {
		response.Error(c, 503, "User account management is unavailable")
		return false
	}
	return true
}

func accountIDFromParam(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return 0, false
	}
	return id, true
}

func writeAccountError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		response.NotFound(c, "Account not found")
	case errors.Is(err, ErrServiceUnavailable):
		response.Error(c, 503, "User account management is unavailable")
	case errors.Is(err, ErrAPIKeyOnly):
		response.BadRequest(c, "This operation is only available for API key accounts")
	case errors.Is(err, ErrInvalidOwner), errors.Is(err, ErrInvalidName), errors.Is(err, ErrInvalidProvider),
		errors.Is(err, ErrInvalidAuthentication), errors.Is(err, ErrInvalidCredentials), errors.Is(err, ErrUnsupportedProvider):
		response.BadRequest(c, "Invalid account request")
	default:
		response.ErrorFrom(c, err)
	}
}


// Start 启动验真调度器
func (h *Handler) Start(ctx context.Context) {
	if h.scheduler != nil {
		h.scheduler.Start(ctx)
	}
}

// Stop 停止验真调度器
func (h *Handler) Stop() {
	if h.scheduler != nil {
		h.scheduler.Stop()
	}
}


