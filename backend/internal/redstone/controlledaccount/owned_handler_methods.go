package controlledaccount

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	adminhandler "github.com/silent-QAQ/redstoneapi/internal/handler/admin"
	"github.com/silent-QAQ/redstoneapi/internal/handler/dto"
	"github.com/silent-QAQ/redstoneapi/internal/pkg/openai"
	"github.com/silent-QAQ/redstoneapi/internal/pkg/response"
	"github.com/silent-QAQ/redstoneapi/internal/server/middleware"
	"github.com/silent-QAQ/redstoneapi/internal/service"
)

func (h *Handler) ListOwnedAccounts(c *gin.Context) {
	if h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.List(c)
}

func (h *Handler) GetOwnedUpstreamBillingProbeSettings(c *gin.Context) {
	if h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.GetUpstreamBillingProbeSettings(c)
}

func (h *Handler) UpdateOwnedUpstreamBillingProbeSettings(c *gin.Context) {
	if h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.UpdateUpstreamBillingProbeSettings(c)
}

func (h *Handler) GetOwnedOllamaCloudUsageSettings(c *gin.Context) {
	if h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.GetOllamaCloudUsageSettings(c)
}

func (h *Handler) UpdateOwnedOllamaCloudUsageSettings(c *gin.Context) {
	if h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.UpdateOllamaCloudUsageSettings(c)
}

func (h *Handler) GetOwnedAccount(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.GetByID(c)
}

func (h *Handler) CreateOwnedAccount(c *gin.Context) {
	if h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.Create(c)
}

func (h *Handler) UpdateOwnedAccount(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.Update(c)
}

func (h *Handler) DeleteOwnedAccount(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.Delete(c)
}

func (h *Handler) DuplicateOwnedAccount(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.Duplicate(c)
}

func (h *Handler) BatchCreateOwnedAccounts(c *gin.Context) {
	if h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.BatchCreate(c)
}

func (h *Handler) BatchUpdateOwnedCredentials(c *gin.Context) {
	if h.ensureOwnedBatchRequest(c) == nil || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.BatchUpdateCredentials(c)
}

func (h *Handler) BulkUpdateOwnedAccounts(c *gin.Context) {
	if h.ensureOwnedBatchRequest(c) == nil || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.BulkUpdate(c)
}

func (h *Handler) BatchDeleteOwnedAccounts(c *gin.Context) {
	if h.ensureOwnedBatchRequest(c) == nil || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.BatchDelete(c)
}

func (h *Handler) BatchClearOwnedErrors(c *gin.Context) {
	if h.ensureOwnedBatchRequest(c) == nil || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.BatchClearError(c)
}

func (h *Handler) BatchRefreshOwnedAccounts(c *gin.Context) {
	if h.ensureOwnedBatchRequest(c) == nil || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.BatchRefresh(c)
}

func (h *Handler) ImportOwnedAccounts(c *gin.Context) {
	if h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.ImportData(c)
}

func (h *Handler) ExportOwnedAccounts(c *gin.Context) {
	if !h.ensureOwnedExportRequest(c) || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.ExportData(c)
}

func (h *Handler) ImportOwnedCodexSession(c *gin.Context) {
	if h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.ImportCodexSession(c)
}

type ownedSyncFromCRSRequest struct {
	BaseURL            string   `json:"base_url" binding:"required"`
	Username           string   `json:"username" binding:"required"`
	Password           string   `json:"password" binding:"required"`
	SyncProxies        *bool    `json:"sync_proxies"`
	SelectedAccountIDs []string `json:"selected_account_ids"`
}

func (h *Handler) SyncOwnedFromCRS(c *gin.Context) {
	if h.crsSyncService == nil {
		response.Error(c, 503, "CRS sync is unavailable")
		return
	}
	ownerUserID, err := ownerScopeFromContext(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	var req ownedSyncFromCRSRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	syncProxies := true
	if req.SyncProxies != nil {
		syncProxies = *req.SyncProxies
	}
	result, err := h.crsSyncService.SyncFromCRS(c.Request.Context(), service.SyncFromCRSInput{
		BaseURL:            req.BaseURL,
		Username:           req.Username,
		Password:           req.Password,
		SyncProxies:        syncProxies,
		SelectedAccountIDs: req.SelectedAccountIDs,
		OwnerUserID:        &ownerUserID,
	})
	if err != nil {
		response.InternalError(c, "CRS sync failed: "+err.Error())
		return
	}
	response.Success(c, result)
}

func (h *Handler) PreviewOwnedFromCRS(c *gin.Context) {
	if h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.PreviewFromCRS(c)
}

func (h *Handler) TestOwnedAccount(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.Test(c)
}

func (h *Handler) RecoverOwnedState(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.RecoverState(c)
}

func (h *Handler) RefreshOwnedAccount(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.Refresh(c)
}

func (h *Handler) ApplyOwnedOAuthCredentials(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.ApplyOAuthCredentials(c)
}

func (h *Handler) SetOwnedPrivacy(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.SetPrivacy(c)
}

func (h *Handler) CreateOwnedShadow(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedOpenAIHandler(c) == nil {
		return
	}
	h.ownedOpenAIHandler.CreateShadow(c)
}

func (h *Handler) GetOwnedStats(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.GetStats(c)
}

func (h *Handler) ClearOwnedError(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.ClearError(c)
}

func (h *Handler) RevertOwnedProxyFallback(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.RevertProxyFallback(c)
}

func (h *Handler) GetOwnedUsage(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.GetUsage(c)
}

func (h *Handler) GetOwnedTodayStats(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.GetTodayStats(c)
}

func (h *Handler) GetOwnedBatchUsage(c *gin.Context) {
	if h.ensureOwnedBatchRequest(c) == nil || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.GetBatchUsage(c)
}

func (h *Handler) GetOwnedBatchTodayStats(c *gin.Context) {
	if h.ensureOwnedBatchRequest(c) == nil || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.GetBatchTodayStats(c)
}

func (h *Handler) ClearOwnedRateLimit(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.ClearRateLimit(c)
}

func (h *Handler) ResetOwnedQuota(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.ResetQuota(c)
}

func (h *Handler) GetOwnedTempUnschedulable(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.GetTempUnschedulable(c)
}

func (h *Handler) ClearOwnedTempUnschedulable(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.ClearTempUnschedulable(c)
}

func (h *Handler) SetOwnedAccountSchedulable(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.SetSchedulable(c)
}

func (h *Handler) GetOwnedAvailableModels(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.GetAvailableModels(c)
}

func (h *Handler) SyncOwnedUpstreamModels(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.SyncUpstreamModels(c)
}

func (h *Handler) SyncOwnedUpstreamModelsPreview(c *gin.Context) {
	if h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.SyncUpstreamModelsPreview(c)
}

func (h *Handler) RefreshOwnedTier(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.RefreshTier(c)
}

func (h *Handler) BatchRefreshOwnedTier(c *gin.Context) {
	if h.ensureOwnedBatchRequest(c) == nil || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.BatchRefreshTier(c)
}

func (h *Handler) SetOwnedUpstreamBillingProbeEnabled(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.SetUpstreamBillingProbeEnabled(c)
}

func (h *Handler) ProbeOwnedUpstreamBilling(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.ProbeUpstreamBilling(c)
}

func (h *Handler) ProbeOwnedUpstreamBillingBatch(c *gin.Context) {
	if h.ensureOwnedBatchRequest(c) == nil || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.ProbeUpstreamBillingBatch(c)
}

func (h *Handler) GetOwnedOllamaCloudUsage(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.GetOllamaCloudUsage(c)
}

func (h *Handler) SaveOwnedOllamaCloudUsageSession(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.SaveOllamaCloudUsageSession(c)
}

func (h *Handler) DeleteOwnedOllamaCloudUsageSession(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.DeleteOllamaCloudUsageSession(c)
}

func (h *Handler) SetOwnedOllamaCloudUsageAutoRefresh(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.SetOllamaCloudUsageAutoRefresh(c)
}

func (h *Handler) RefreshOwnedOllamaCloudUsage(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.RefreshOllamaCloudUsage(c)
}

func (h *Handler) CheckOwnedMixedChannel(c *gin.Context) {
	if h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.CheckMixedChannel(c)
}

func (h *Handler) OwnedVerify(c *gin.Context) {
	middleware.SetAuditAction(c, "user.accounts.verify")
	accountID, ok := accountIDFromParam(c)
	if !ok {
		return
	}
	if h.ownedAdminService == nil || h.verifier == nil {
		response.Error(c, 503, "Account verification is unavailable")
		return
	}

	// GetAccount is owner-scoped by ownedAdminService, so the account ID from
	// the path can never be used to probe another user's credential.
	account, err := h.ownedAdminService.GetAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if account == nil || account.Type != service.AccountTypeAPIKey {
		response.BadRequest(c, "This operation is only available for API key accounts")
		return
	}

	apiKey := strings.TrimSpace(account.GetCredential("api_key"))
	if apiKey == "" {
		response.BadRequest(c, "API key is required for model verification")
		return
	}

	result := h.verifier.VerifyWithCredentials(
		c.Request.Context(),
		account.Platform,
		apiKey,
		ownedVerificationBaseURL(account),
	)
	if err := h.ownedAdminService.UpdateAccountExtra(c.Request.Context(), accountID, map[string]any{
		"model_verification_status":   result.Verdict,
		"model_verification_verdict":  result.Verdict,
		"model_verification_score":    result.Score,
		"model_verification_protocol": result.Protocol,
		"model_verification_at":       result.Timestamp.UTC().Format(time.RFC3339),
	}); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{
		"account_id":  accountID,
		"success":     result.Success,
		"score":       result.Score,
		"verdict":     result.Verdict,
		"protocol":    result.Protocol,
		"message":     result.Message,
		"timestamp":   result.Timestamp,
		"duration_ms": result.DurationMs,
	})
}

func ownedVerificationBaseURL(account *service.Account) string {
	if account == nil {
		return ""
	}
	switch account.Platform {
	case service.PlatformAnthropic:
		return account.GetBaseURL()
	case service.PlatformGemini, service.PlatformAntigravity:
		return account.GetGeminiBaseURL("https://generativelanguage.googleapis.com")
	case service.PlatformGrok:
		if baseURL := strings.TrimSpace(account.GetCredential("base_url")); baseURL != "" {
			return baseURL
		}
		return "https://api.x.ai"
	default:
		return account.GetOpenAIBaseURL()
	}
}

func (h *Handler) ListAccountOptionsGroups(c *gin.Context) {
	if h.adminService == nil {
		response.Error(c, 503, "User account management is unavailable")
		return
	}
	groups, err := h.adminService.GetAllGroups(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, accountOptionGroups(groups))
}

func (h *Handler) ListAccountOptionsProxies(c *gin.Context) {
	if h.adminService == nil {
		response.Error(c, 503, "User account management is unavailable")
		return
	}
	proxies, err := h.adminService.GetAllProxies(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, accountOptionProxies(proxies))
}

func (h *Handler) GetAccountOptionWebSearchEmulation(c *gin.Context) {
	if h.settingService == nil {
		response.Error(c, 503, "Account feature options are unavailable")
		return
	}
	cfg, err := h.settingService.GetWebSearchEmulationConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"enabled": cfg != nil && cfg.Enabled && len(cfg.Providers) > 0})
}

func (h *Handler) ListAccountOptionTLSFingerprintProfiles(c *gin.Context) {
	if h.tlsFingerprintHandler == nil {
		response.Error(c, 503, "Account feature options are unavailable")
		return
	}
	h.tlsFingerprintHandler.ListOptions(c)
}

func (h *Handler) TestAccountOptionProxy(c *gin.Context) {
	if h.adminService == nil || h.proxyHandler == nil {
		response.Error(c, 503, "Account feature options are unavailable")
		return
	}
	proxyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || proxyID <= 0 {
		response.BadRequest(c, "Invalid proxy ID")
		return
	}
	proxy, err := h.adminService.GetProxy(c.Request.Context(), proxyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if proxy == nil || !proxy.IsActive() {
		response.NotFound(c, "Proxy not found")
		return
	}
	h.proxyHandler.Test(c)
}

func accountOptionGroups(groups []service.Group) []dto.Group {
	options := make([]dto.Group, 0, len(groups))
	for i := range groups {
		if option := dto.GroupFromService(&groups[i]); option != nil {
			options = append(options, *option)
		}
	}
	return options
}

func accountOptionProxies(proxies []service.Proxy) []dto.Proxy {
	options := make([]dto.Proxy, 0, len(proxies))
	for i := range proxies {
		if option := dto.ProxyFromService(&proxies[i]); option != nil {
			options = append(options, *option)
		}
	}
	return options
}

func (h *Handler) ListOwnedScheduledTestPlans(c *gin.Context) {
	if h.scheduledTestService == nil || h.ownedAdminService == nil {
		response.Error(c, 503, "Scheduled tests are unavailable")
		return
	}
	accountID := h.ensureOwnedAccountAccess(c)
	if accountID == 0 {
		return
	}
	plans, err := h.scheduledTestService.ListPlansByAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, plans)
}

type ownedScheduledTestPlanRequest struct {
	AccountID      int64  `json:"account_id" binding:"required"`
	ModelID        string `json:"model_id"`
	CronExpression string `json:"cron_expression" binding:"required"`
	Enabled        *bool  `json:"enabled"`
	MaxResults     int    `json:"max_results"`
	AutoRecover    *bool  `json:"auto_recover"`
}

type ownedScheduledTestPlanUpdateRequest struct {
	ModelID        string `json:"model_id"`
	CronExpression string `json:"cron_expression"`
	Enabled        *bool  `json:"enabled"`
	MaxResults     int    `json:"max_results"`
	AutoRecover    *bool  `json:"auto_recover"`
}

// CreateOwnedScheduledTestPlan retains the admin plan payload and lifecycle,
// but verifies the referenced account before persisting the plan.
func (h *Handler) CreateOwnedScheduledTestPlan(c *gin.Context) {
	if h.scheduledTestService == nil || h.ownedAdminService == nil {
		response.Error(c, 503, "Scheduled tests are unavailable")
		return
	}
	var req ownedScheduledTestPlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if req.AccountID <= 0 || !h.ensureOwnedAccountID(c, req.AccountID) {
		return
	}
	plan := &service.ScheduledTestPlan{
		AccountID:      req.AccountID,
		ModelID:        req.ModelID,
		CronExpression: req.CronExpression,
		Enabled:        true,
		MaxResults:     req.MaxResults,
	}
	if req.Enabled != nil {
		plan.Enabled = *req.Enabled
	}
	if req.AutoRecover != nil {
		plan.AutoRecover = *req.AutoRecover
	}
	created, err := h.scheduledTestService.CreatePlan(c.Request.Context(), plan)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, created)
}

func (h *Handler) UpdateOwnedScheduledTestPlan(c *gin.Context) {
	plan, ok := h.ownedScheduledTestPlan(c)
	if !ok {
		return
	}
	var req ownedScheduledTestPlanUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if req.ModelID != "" {
		plan.ModelID = req.ModelID
	}
	if req.CronExpression != "" {
		plan.CronExpression = req.CronExpression
	}
	if req.Enabled != nil {
		plan.Enabled = *req.Enabled
	}
	if req.MaxResults > 0 {
		plan.MaxResults = req.MaxResults
	}
	if req.AutoRecover != nil {
		plan.AutoRecover = *req.AutoRecover
	}
	updated, err := h.scheduledTestService.UpdatePlan(c.Request.Context(), plan)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, updated)
}

func (h *Handler) DeleteOwnedScheduledTestPlan(c *gin.Context) {
	plan, ok := h.ownedScheduledTestPlan(c)
	if !ok {
		return
	}
	if err := h.scheduledTestService.DeletePlan(c.Request.Context(), plan.ID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "deleted"})
}

func (h *Handler) ListOwnedScheduledTestResults(c *gin.Context) {
	plan, ok := h.ownedScheduledTestPlan(c)
	if !ok {
		return
	}
	limit := 50
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			response.BadRequest(c, "invalid limit")
			return
		}
		limit = parsed
	}
	results, err := h.scheduledTestService.ListResults(c.Request.Context(), plan.ID, limit)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, results)
}

func (h *Handler) GenerateOwnedAuthURL(c *gin.Context) {
	if h.oauthService == nil {
		response.Error(c, 503, "OAuth is unavailable")
		return
	}
	var req adminhandler.GenerateAuthURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		req = adminhandler.GenerateAuthURLRequest{}
	}
	result, err := h.oauthService.GenerateAuthURL(c.Request.Context(), req.ProxyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	h.recordOAuthBinding(c, "anthropic", result.SessionID, extractStateFromAuthURL(result.AuthURL))
	response.Success(c, result)
}

func (h *Handler) GenerateOwnedSetupTokenURL(c *gin.Context) {
	if h.oauthService == nil {
		response.Error(c, 503, "OAuth is unavailable")
		return
	}
	var req adminhandler.GenerateAuthURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		req = adminhandler.GenerateAuthURLRequest{}
	}
	result, err := h.oauthService.GenerateSetupTokenURL(c.Request.Context(), req.ProxyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	h.recordOAuthBinding(c, "anthropic", result.SessionID, extractStateFromAuthURL(result.AuthURL))
	response.Success(c, result)
}

func (h *Handler) ExchangeOwnedCode(c *gin.Context) {
	if h.oauthService == nil {
		response.Error(c, 503, "OAuth is unavailable")
		return
	}
	var req struct {
		SessionID string `json:"session_id" binding:"required"`
		Code      string `json:"code" binding:"required"`
		State     string `json:"state"`
		ProxyID   *int64 `json:"proxy_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if !h.consumeOAuthBinding(c, "anthropic", req.SessionID, req.State) {
		return
	}
	tokenInfo, err := h.oauthService.ExchangeCode(c.Request.Context(), &service.ExchangeCodeInput{
		SessionID: req.SessionID,
		Code:      req.Code,
		ProxyID:   req.ProxyID,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, tokenInfo)
}

func (h *Handler) ExchangeOwnedSetupTokenCode(c *gin.Context) {
	h.ExchangeOwnedCode(c)
}

func (h *Handler) CookieOwnedAuth(c *gin.Context) {
	if h.ensureOwnedOAuthHandler(c) == nil {
		return
	}
	h.ownedOAuthHandler.CookieAuth(c)
}

func (h *Handler) SetupTokenOwnedCookieAuth(c *gin.Context) {
	if h.ensureOwnedOAuthHandler(c) == nil {
		return
	}
	h.ownedOAuthHandler.SetupTokenCookieAuth(c)
}

func (h *Handler) OpenAIGenerateOwnedAuthURL(c *gin.Context) {
	if h.openaiOAuthService == nil {
		response.Error(c, 503, "OpenAI OAuth is unavailable")
		return
	}
	var req adminhandler.OpenAIGenerateAuthURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		req = adminhandler.OpenAIGenerateAuthURLRequest{}
	}
	result, err := h.openaiOAuthService.GenerateAuthURL(c.Request.Context(), req.ProxyID, req.RedirectURI, service.PlatformOpenAI)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	h.recordOAuthBinding(c, "openai", result.SessionID, extractStateFromAuthURL(result.AuthURL))
	response.Success(c, result)
}

func (h *Handler) OpenAIExchangeOwnedCode(c *gin.Context) {
	if h.openaiOAuthService == nil {
		response.Error(c, 503, "OpenAI OAuth is unavailable")
		return
	}
	var req adminhandler.OpenAIExchangeCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if !h.consumeOAuthBinding(c, "openai", req.SessionID, req.State) {
		return
	}
	tokenInfo, err := h.openaiOAuthService.ExchangeCode(c.Request.Context(), &service.OpenAIExchangeCodeInput{
		SessionID:   req.SessionID,
		Code:        req.Code,
		State:       req.State,
		RedirectURI: req.RedirectURI,
		ProxyID:     req.ProxyID,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, tokenInfo)
}

func (h *Handler) OpenAIRefreshToken(c *gin.Context) {
	if h.openaiOAuthService == nil || h.adminService == nil {
		response.Error(c, 503, "OpenAI OAuth is unavailable")
		return
	}
	var req adminhandler.OpenAIRefreshTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	refreshToken := strings.TrimSpace(req.RefreshToken)
	if refreshToken == "" {
		refreshToken = strings.TrimSpace(req.RT)
	}
	if refreshToken == "" {
		response.BadRequest(c, "refresh_token is required")
		return
	}

	var proxyURL string
	if req.ProxyID != nil {
		proxy, err := h.adminService.GetProxy(c.Request.Context(), *req.ProxyID)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		if proxy != nil {
			proxyURL = proxy.URL()
		}
	}

	clientID := strings.TrimSpace(req.ClientID)
	if clientID == "" {
		clientID, _ = openai.OAuthClientConfigByPlatform(service.PlatformOpenAI)
	}
	tokenInfo, err := h.openaiOAuthService.RefreshTokenWithClientID(c.Request.Context(), refreshToken, proxyURL, clientID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, tokenInfo)
}

func (h *Handler) QueryOwnedOpenAIQuota(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedOpenAIHandler(c) == nil {
		return
	}
	h.ownedOpenAIHandler.QueryQuota(c)
}

func (h *Handler) RefreshOwnedOpenAIQuota(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedOpenAIHandler(c) == nil {
		return
	}
	h.ownedOpenAIHandler.RefreshQuota(c)
}

func (h *Handler) ResetOwnedOpenAIQuota(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedOpenAIHandler(c) == nil {
		return
	}
	h.ownedOpenAIHandler.ResetQuota(c)
}

func (h *Handler) CreateOwnedOpenAICodexPAT(c *gin.Context) {
	if h.ensureOwnedOpenAIHandler(c) == nil {
		return
	}
	h.ownedOpenAIHandler.CreateAccountFromCodexPAT(c)
}

func (h *Handler) GetAntigravityDefaultModelMapping(c *gin.Context) {
	if h.ensureOwnedAccountHandler(c) == nil {
		return
	}
	h.ownedAccountHandler.GetAntigravityDefaultModelMapping(c)
}

func (h *Handler) GeminiCapabilities(c *gin.Context) {
	if h.geminiOAuthService == nil {
		response.Error(c, 503, "Gemini OAuth is unavailable")
		return
	}
	response.Success(c, h.geminiOAuthService.GetOAuthConfig())
}

func (h *Handler) GeminiGenerateOwnedAuthURL(c *gin.Context) {
	if h.geminiOAuthService == nil {
		response.Error(c, 503, "Gemini OAuth is unavailable")
		return
	}
	var req adminhandler.GeminiGenerateAuthURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	oauthType := strings.TrimSpace(req.OAuthType)
	if oauthType == "" {
		oauthType = "code_assist"
	}
	result, err := h.geminiOAuthService.GenerateAuthURL(c.Request.Context(), req.ProxyID, deriveGeminiRedirectURI(c), req.ProjectID, oauthType, req.TierID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	h.recordOAuthBinding(c, "gemini", result.SessionID, strings.TrimSpace(result.State))
	response.Success(c, result)
}

func (h *Handler) GeminiExchangeOwnedCode(c *gin.Context) {
	if h.geminiOAuthService == nil {
		response.Error(c, 503, "Gemini OAuth is unavailable")
		return
	}
	var req adminhandler.GeminiExchangeCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if !h.consumeOAuthBinding(c, "gemini", req.SessionID, req.State) {
		return
	}
	oauthType := strings.TrimSpace(req.OAuthType)
	if oauthType == "" {
		oauthType = "code_assist"
	}
	tokenInfo, err := h.geminiOAuthService.ExchangeCode(c.Request.Context(), &service.GeminiExchangeCodeInput{
		SessionID: req.SessionID,
		State:     req.State,
		Code:      req.Code,
		ProxyID:   req.ProxyID,
		OAuthType: oauthType,
		TierID:    req.TierID,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, tokenInfo)
}

func (h *Handler) AntigravityGenerateOwnedAuthURL(c *gin.Context) {
	if h.antigravityOAuthService == nil {
		response.Error(c, 503, "Antigravity OAuth is unavailable")
		return
	}
	var req adminhandler.AntigravityGenerateAuthURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.antigravityOAuthService.GenerateAuthURL(c.Request.Context(), req.ProxyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	h.recordOAuthBinding(c, "antigravity", result.SessionID, strings.TrimSpace(result.State))
	response.Success(c, result)
}

func (h *Handler) AntigravityExchangeOwnedCode(c *gin.Context) {
	if h.antigravityOAuthService == nil {
		response.Error(c, 503, "Antigravity OAuth is unavailable")
		return
	}
	var req adminhandler.AntigravityExchangeCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if !h.consumeOAuthBinding(c, "antigravity", req.SessionID, req.State) {
		return
	}
	tokenInfo, err := h.antigravityOAuthService.ExchangeCode(c.Request.Context(), &service.AntigravityExchangeCodeInput{
		SessionID: req.SessionID,
		State:     req.State,
		Code:      req.Code,
		ProxyID:   req.ProxyID,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, tokenInfo)
}

func (h *Handler) AntigravityRefreshToken(c *gin.Context) {
	if h.antigravityOAuthService == nil {
		response.Error(c, 503, "Antigravity OAuth is unavailable")
		return
	}
	var req adminhandler.AntigravityRefreshTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	tokenInfo, err := h.antigravityOAuthService.ValidateRefreshToken(c.Request.Context(), req.RefreshToken, req.ProxyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, tokenInfo)
}

func (h *Handler) GrokCapabilities(c *gin.Context) {
	if h.grokOAuthService == nil {
		response.Error(c, 503, "Grok OAuth is unavailable")
		return
	}
	response.Success(c, h.grokOAuthService.GetCapabilities())
}

func (h *Handler) GrokGenerateOwnedAuthURL(c *gin.Context) {
	if h.grokOAuthService == nil {
		response.Error(c, 503, "Grok OAuth is unavailable")
		return
	}
	var req adminhandler.GrokGenerateAuthURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		req = adminhandler.GrokGenerateAuthURLRequest{}
	}
	result, err := h.grokOAuthService.GenerateAuthURL(c.Request.Context(), req.ProxyID, req.RedirectURI)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	h.recordOAuthBinding(c, "grok", result.SessionID, strings.TrimSpace(result.State))
	response.Success(c, result)
}

func (h *Handler) GrokExchangeOwnedCode(c *gin.Context) {
	if h.grokOAuthService == nil {
		response.Error(c, 503, "Grok OAuth is unavailable")
		return
	}
	var req adminhandler.GrokExchangeCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if !h.consumeOAuthBinding(c, "grok", req.SessionID, req.State) {
		return
	}
	tokenInfo, err := h.grokOAuthService.ExchangeCode(c.Request.Context(), &service.GrokExchangeCodeInput{
		SessionID:   req.SessionID,
		Code:        req.Code,
		State:       req.State,
		RedirectURI: req.RedirectURI,
		ProxyID:     req.ProxyID,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, tokenInfo)
}

func (h *Handler) GrokRefreshToken(c *gin.Context) {
	if h.grokOAuthService == nil || h.adminService == nil {
		response.Error(c, 503, "Grok OAuth is unavailable")
		return
	}
	var req adminhandler.GrokRefreshTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	refreshToken := strings.TrimSpace(req.RefreshToken)
	if refreshToken == "" {
		refreshToken = strings.TrimSpace(req.RT)
	}
	if refreshToken == "" {
		response.BadRequest(c, "refresh_token is required")
		return
	}
	var proxyURL string
	if req.ProxyID != nil {
		proxy, err := h.adminService.GetProxy(c.Request.Context(), *req.ProxyID)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		if proxy != nil {
			proxyURL = proxy.URL()
		}
	}
	tokenInfo, err := h.grokOAuthService.RefreshToken(c.Request.Context(), refreshToken, proxyURL, req.ClientID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, tokenInfo)
}

func (h *Handler) GrokValidateSSOToken(c *gin.Context) {
	if h.ensureOwnedGrokHandler(c) == nil {
		return
	}
	h.ownedGrokHandler.ValidateSSOToken(c)
}

func (h *Handler) GrokAuthorizePassword(c *gin.Context) {
	if h.ensureOwnedGrokHandler(c) == nil {
		return
	}
	h.ownedGrokHandler.AuthorizePassword(c)
}

func (h *Handler) CreateOwnedGrokFromSSO(c *gin.Context) {
	if h.ensureOwnedGrokHandler(c) == nil {
		return
	}
	h.ownedGrokHandler.CreateAccountsFromSSO(c)
}

func (h *Handler) QueryOwnedGrokQuota(c *gin.Context) {
	if h.ensureOwnedAccountAccess(c) == 0 || h.ensureOwnedGrokHandler(c) == nil {
		return
	}
	h.ownedGrokHandler.QueryQuota(c)
}

func (h *Handler) ensureOwnedAccountHandler(c *gin.Context) *adminhandler.AccountHandler {
	if h.ownedAccountHandler == nil {
		response.Error(c, 503, "User account management is unavailable")
		return nil
	}
	return h.ownedAccountHandler
}

func (h *Handler) ensureOwnedOAuthHandler(c *gin.Context) *adminhandler.OAuthHandler {
	if h.ownedOAuthHandler == nil {
		response.Error(c, 503, "OAuth is unavailable")
		return nil
	}
	return h.ownedOAuthHandler
}

func (h *Handler) ensureOwnedOpenAIHandler(c *gin.Context) *adminhandler.OpenAIOAuthHandler {
	if h.ownedOpenAIHandler == nil {
		response.Error(c, 503, "OpenAI OAuth is unavailable")
		return nil
	}
	return h.ownedOpenAIHandler
}

func (h *Handler) ensureOwnedGrokHandler(c *gin.Context) *adminhandler.GrokOAuthHandler {
	if h.ownedGrokHandler == nil {
		response.Error(c, 503, "Grok OAuth is unavailable")
		return nil
	}
	return h.ownedGrokHandler
}

func (h *Handler) ensureOwnedAccountAccess(c *gin.Context) int64 {
	if h.ownedAdminService == nil {
		response.Error(c, 503, "User account management is unavailable")
		return 0
	}
	accountID, ok := accountIDFromParam(c)
	if !ok {
		return 0
	}
	if _, err := h.ownedAdminService.GetAccount(c.Request.Context(), accountID); err != nil {
		response.ErrorFrom(c, err)
		return 0
	}
	return accountID
}

func (h *Handler) ensureOwnedAccountID(c *gin.Context, accountID int64) bool {
	if h.ownedAdminService == nil {
		response.Error(c, 503, "User account management is unavailable")
		return false
	}
	if accountID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return false
	}
	if _, err := h.ownedAdminService.GetAccount(c.Request.Context(), accountID); err != nil {
		response.ErrorFrom(c, err)
		return false
	}
	return true
}

func (h *Handler) ensureOwnedBatchRequest(c *gin.Context) []int64 {
	if h.ownedAdminService == nil {
		response.Error(c, 503, "User account management is unavailable")
		return nil
	}
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		response.BadRequest(c, "Invalid request body")
		return nil
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	var req struct {
		AccountIDs []int64 `json:"account_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return nil
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	if len(req.AccountIDs) == 0 {
		return []int64{}
	}
	if _, err := h.ownedAdminService.GetAccountsByIDs(c.Request.Context(), req.AccountIDs); err != nil {
		response.ErrorFrom(c, err)
		return nil
	}
	return req.AccountIDs
}

func (h *Handler) ensureOwnedExportRequest(c *gin.Context) bool {
	raw := strings.TrimSpace(c.Query("ids"))
	if raw == "" {
		return true
	}
	parts := strings.Split(raw, ",")
	ids := make([]int64, 0, len(parts))
	for _, part := range parts {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "Invalid account ID")
			return false
		}
		ids = append(ids, id)
	}
	if h.ownedAdminService == nil {
		response.Error(c, 503, "User account management is unavailable")
		return false
	}
	if _, err := h.ownedAdminService.GetAccountsByIDs(c.Request.Context(), ids); err != nil {
		response.ErrorFrom(c, err)
		return false
	}
	return true
}

func (h *Handler) ownedScheduledTestPlan(c *gin.Context) (*service.ScheduledTestPlan, bool) {
	if h.scheduledTestService == nil || h.ownedAdminService == nil {
		response.Error(c, 503, "Scheduled tests are unavailable")
		return nil, false
	}
	planID, ok := accountIDFromParam(c)
	if !ok {
		return nil, false
	}
	plan, err := h.scheduledTestService.GetPlan(c.Request.Context(), planID)
	if err != nil || plan == nil {
		response.NotFound(c, "Scheduled test plan not found")
		return nil, false
	}
	if !h.ensureOwnedAccountID(c, plan.AccountID) {
		return nil, false
	}
	return plan, true
}

func (h *Handler) recordOAuthBinding(c *gin.Context, provider, sessionID, state string) {
	if h.oauthBindingStore == nil || sessionID == "" {
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		return
	}
	h.oauthBindingStore.BindProvider(provider, sessionID, state, subject.UserID)
}

func (h *Handler) consumeOAuthBinding(c *gin.Context, provider, sessionID, state string) bool {
	if h.oauthBindingStore == nil {
		return true
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return false
	}
	if err := h.oauthBindingStore.ConsumeProvider(provider, sessionID, state, subject.UserID); err != nil {
		response.ErrorFrom(c, err)
		return false
	}
	return true
}

func extractStateFromAuthURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Query().Get("state"))
}

func deriveGeminiRedirectURI(c *gin.Context) string {
	origin := strings.TrimSpace(c.GetHeader("Origin"))
	if origin != "" {
		return strings.TrimRight(origin, "/") + "/auth/callback"
	}

	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	if xfProto := strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")); xfProto != "" {
		scheme = strings.TrimSpace(strings.Split(xfProto, ",")[0])
	}

	host := strings.TrimSpace(c.Request.Host)
	if xfHost := strings.TrimSpace(c.GetHeader("X-Forwarded-Host")); xfHost != "" {
		host = strings.TrimSpace(strings.Split(xfHost, ",")[0])
	}

	return fmt.Sprintf("%s://%s/auth/callback", scheme, host)
}
