package controlledaccount

import (
	adminhandler "github.com/silent-QAQ/redstoneapi/internal/handler/admin"
	"github.com/silent-QAQ/redstoneapi/internal/pkg/response"
	"github.com/silent-QAQ/redstoneapi/internal/server/middleware"
	"github.com/silent-QAQ/redstoneapi/internal/service"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes mounts both the legacy Redstone upload shell and the new
// owner-scoped account management surface that directly reuses admin handlers.
func RegisterRoutes(
	v1 *gin.RouterGroup,
	handler *Handler,
	jwtAuth middleware.JWTAuthMiddleware,
	auditLog middleware.AuditLogMiddleware,
	stepUpAuth middleware.StepUpAuthMiddleware,
	settingService *service.SettingService,
	panelRateLimiter *middleware.PanelRateLimiter,
	proxyHandler *adminhandler.ProxyHandler,
	tlsFingerprintHandler *adminhandler.TLSFingerprintProfileHandler,
) {
	if v1 == nil || handler == nil {
		return
	}

	userMiddleware := []gin.HandlerFunc{
		gin.HandlerFunc(jwtAuth),
		middleware.BackendModeUserGuard(settingService),
		panelRateLimiter.Global(),
		gin.HandlerFunc(auditLog),
		ownerScopeMiddleware(),
	}

	if handler.service != nil {
		legacy := v1.Group("/user/controlled-accounts")
		legacy.Use(userMiddleware...)
		legacy.GET("", handler.List)
		legacy.POST("", handler.Create)
		legacy.PATCH("/:id", handler.Rename)
		legacy.POST("/:id/disable", handler.Disable)
		legacy.POST("/:id/enable", handler.Enable)
		legacy.POST("/:id/verify", handler.Verify)
		legacy.DELETE("/:id", handler.Delete)
	}

	accounts := v1.Group("/user/accounts")
	accounts.Use(userMiddleware...)
	handler.proxyHandler = proxyHandler
	handler.tlsFingerprintHandler = tlsFingerprintHandler
	handler.settingService = settingService
	registerOwnedAccountRoutes(accounts, handler, stepUpAuth)
}

func registerOwnedAccountRoutes(accounts *gin.RouterGroup, handler *Handler, stepUpAuth ...middleware.StepUpAuthMiddleware) {
	if accounts == nil || handler == nil {
		return
	}
	var exportGuards []gin.HandlerFunc
	if len(stepUpAuth) > 0 && stepUpAuth[0] != nil {
		exportGuards = append(exportGuards, gin.HandlerFunc(stepUpAuth[0]))
	}
	{
		accounts.GET("", handler.ListOwnedAccounts)
		accounts.GET("/upstream-billing-probe/settings", handler.GetOwnedUpstreamBillingProbeSettings)
		accounts.PUT("/upstream-billing-probe/settings", handler.UpdateOwnedUpstreamBillingProbeSettings)
		accounts.GET("/ollama-cloud-usage/settings", handler.GetOwnedOllamaCloudUsageSettings)
		accounts.PUT("/ollama-cloud-usage/settings", handler.UpdateOwnedOllamaCloudUsageSettings)
		accounts.POST("", handler.CreateOwnedAccount)
		accounts.POST("/batch", handler.BatchCreateOwnedAccounts)
		accounts.POST("/batch-update-credentials", handler.BatchUpdateOwnedCredentials)
		accounts.POST("/bulk-update", handler.BulkUpdateOwnedAccounts)
		accounts.POST("/batch-delete", handler.BatchDeleteOwnedAccounts)
		accounts.POST("/batch-clear-error", handler.BatchClearOwnedErrors)
		accounts.POST("/batch-refresh", handler.BatchRefreshOwnedAccounts)
		accounts.POST("/check-mixed-channel", handler.CheckOwnedMixedChannel)
		accounts.GET("/data", append(exportGuards, handler.ExportOwnedAccounts)...)
		accounts.POST("/data", handler.ImportOwnedAccounts)
		accounts.POST("/usage/batch", handler.GetOwnedBatchUsage)
		accounts.POST("/today-stats/batch", handler.GetOwnedBatchTodayStats)
		accounts.POST("/models/sync-upstream-preview", handler.SyncOwnedUpstreamModelsPreview)
		accounts.POST("/import/codex-session", handler.ImportOwnedCodexSession)
		accounts.POST("/sync/crs", handler.SyncOwnedFromCRS)
		accounts.POST("/sync/crs/preview", handler.PreviewOwnedFromCRS)
		accounts.POST("/batch-refresh-tier", handler.BatchRefreshOwnedTier)
		accounts.POST("/upstream-billing-probe/batch", handler.ProbeOwnedUpstreamBillingBatch)
		accounts.POST("/scheduled-test-plans", handler.CreateOwnedScheduledTestPlan)
		accounts.PUT("/scheduled-test-plans/:id", handler.UpdateOwnedScheduledTestPlan)
		accounts.DELETE("/scheduled-test-plans/:id", handler.DeleteOwnedScheduledTestPlan)
		accounts.GET("/scheduled-test-plans/:id/results", handler.ListOwnedScheduledTestResults)
		accounts.GET("/options/proxies", handler.ListAccountOptionsProxies)
		accounts.POST("/options/proxies/:id/test", handler.TestAccountOptionProxy)
		accounts.GET("/options/groups", handler.ListAccountOptionsGroups)
		accounts.GET("/options/web-search-emulation", handler.GetAccountOptionWebSearchEmulation)
		accounts.GET("/options/tls-fingerprint-profiles", handler.ListAccountOptionTLSFingerprintProfiles)
		accounts.GET("/antigravity/default-model-mapping", handler.GetAntigravityDefaultModelMapping)

		accounts.POST("/generate-auth-url", handler.GenerateOwnedAuthURL)
		accounts.POST("/generate-setup-token-url", handler.GenerateOwnedSetupTokenURL)
		accounts.POST("/exchange-code", handler.ExchangeOwnedCode)
		accounts.POST("/exchange-setup-token-code", handler.ExchangeOwnedSetupTokenCode)
		accounts.POST("/cookie-auth", handler.CookieOwnedAuth)
		accounts.POST("/setup-token-cookie-auth", handler.SetupTokenOwnedCookieAuth)

		accounts.POST("/openai/create-from-codex-pat", handler.CreateOwnedOpenAICodexPAT)
		accounts.POST("/openai/generate-auth-url", handler.OpenAIGenerateOwnedAuthURL)
		accounts.POST("/openai/exchange-code", handler.OpenAIExchangeOwnedCode)
		accounts.POST("/openai/refresh-token", handler.OpenAIRefreshToken)

		accounts.GET("/gemini/oauth/capabilities", handler.GeminiCapabilities)
		accounts.POST("/gemini/oauth/auth-url", handler.GeminiGenerateOwnedAuthURL)
		accounts.POST("/gemini/oauth/exchange-code", handler.GeminiExchangeOwnedCode)

		accounts.POST("/antigravity/oauth/auth-url", handler.AntigravityGenerateOwnedAuthURL)
		accounts.POST("/antigravity/oauth/exchange-code", handler.AntigravityExchangeOwnedCode)
		accounts.POST("/antigravity/oauth/refresh-token", handler.AntigravityRefreshToken)

		accounts.GET("/grok/oauth/capabilities", handler.GrokCapabilities)
		accounts.POST("/grok/oauth/auth-url", handler.GrokGenerateOwnedAuthURL)
		accounts.POST("/grok/oauth/exchange-code", handler.GrokExchangeOwnedCode)
		accounts.POST("/grok/oauth/refresh-token", handler.GrokRefreshToken)
		accounts.POST("/grok/oauth/sso-token", handler.GrokValidateSSOToken)
		accounts.POST("/grok/oauth/password", handler.GrokAuthorizePassword)
		accounts.POST("/grok/sso-to-oauth", handler.CreateOwnedGrokFromSSO)
		accounts.GET("/grok/accounts/:id/quota", handler.QueryOwnedGrokQuota)

		accounts.GET("/:id", handler.GetOwnedAccount)
		accounts.PUT("/:id", handler.UpdateOwnedAccount)
		accounts.DELETE("/:id", handler.DeleteOwnedAccount)
		accounts.POST("/:id/duplicate", handler.DuplicateOwnedAccount)
		accounts.POST("/:id/test", handler.TestOwnedAccount)
		accounts.POST("/:id/recover-state", handler.RecoverOwnedState)
		accounts.POST("/:id/refresh", handler.RefreshOwnedAccount)
		accounts.POST("/:id/apply-oauth-credentials", handler.ApplyOwnedOAuthCredentials)
		accounts.POST("/:id/set-privacy", handler.SetOwnedPrivacy)
		accounts.POST("/:id/refresh-tier", handler.RefreshOwnedTier)
		accounts.GET("/:id/stats", handler.GetOwnedStats)
		accounts.POST("/:id/clear-error", handler.ClearOwnedError)
		accounts.POST("/:id/revert-proxy-fallback", handler.RevertOwnedProxyFallback)
		accounts.GET("/:id/usage", handler.GetOwnedUsage)
		accounts.GET("/:id/today-stats", handler.GetOwnedTodayStats)
		accounts.POST("/:id/clear-rate-limit", handler.ClearOwnedRateLimit)
		accounts.POST("/:id/reset-quota", handler.ResetOwnedQuota)
		accounts.GET("/:id/temp-unschedulable", handler.GetOwnedTempUnschedulable)
		accounts.DELETE("/:id/temp-unschedulable", handler.ClearOwnedTempUnschedulable)
		accounts.POST("/:id/schedulable", handler.SetOwnedAccountSchedulable)
		accounts.GET("/:id/models", handler.GetOwnedAvailableModels)
		accounts.POST("/:id/models/sync-upstream", handler.SyncOwnedUpstreamModels)
		accounts.PUT("/:id/upstream-billing-probe", handler.SetOwnedUpstreamBillingProbeEnabled)
		accounts.POST("/:id/upstream-billing-probe", handler.ProbeOwnedUpstreamBilling)
		accounts.GET("/:id/ollama-cloud-usage", handler.GetOwnedOllamaCloudUsage)
		accounts.PUT("/:id/ollama-cloud-usage/session", handler.SaveOwnedOllamaCloudUsageSession)
		accounts.DELETE("/:id/ollama-cloud-usage/session", handler.DeleteOwnedOllamaCloudUsageSession)
		accounts.PUT("/:id/ollama-cloud-usage/auto-refresh", handler.SetOwnedOllamaCloudUsageAutoRefresh)
		accounts.POST("/:id/ollama-cloud-usage/refresh", handler.RefreshOwnedOllamaCloudUsage)
		accounts.GET("/:id/scheduled-test-plans", handler.ListOwnedScheduledTestPlans)
		accounts.POST("/:id/verify", handler.OwnedVerify)
		accounts.POST("/:id/shadow", handler.CreateOwnedShadow)
		accounts.GET("/:id/openai-quota", handler.QueryOwnedOpenAIQuota)
		accounts.POST("/:id/openai-quota/refresh", handler.RefreshOwnedOpenAIQuota)
		accounts.POST("/:id/openai-quota/reset", handler.ResetOwnedOpenAIQuota)
	}
}

func ownerScopeMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		subject, ok := middleware.GetAuthSubjectFromContext(c)
		if !ok || subject.UserID <= 0 {
			response.Unauthorized(c, "User not authenticated")
			c.Abort()
			return
		}
		c.Request = c.Request.WithContext(withOwnerScope(c.Request.Context(), subject.UserID))
		c.Next()
	}
}
