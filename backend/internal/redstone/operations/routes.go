package operations

import (
	"github.com/silent-QAQ/redstoneapi/internal/server/middleware"
	"github.com/silent-QAQ/redstoneapi/internal/service"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes exposes operating workflows to an authenticated user. It
// intentionally does not add announcement, group, API-key, or account routes:
// those are already provided by the corresponding upstream modules.
func RegisterRoutes(v1 *gin.RouterGroup, handler *Handler, jwtAuth middleware.JWTAuthMiddleware, settingService *service.SettingService, panelRateLimiter *middleware.PanelRateLimiter) {
	if v1 == nil || handler == nil {
		return
	}
	group := v1.Group("/operations")
	group.Use(gin.HandlerFunc(jwtAuth), middleware.BackendModeUserGuard(settingService), panelRateLimiter.Global())
	group.GET("/withdrawals", handler.ListWithdrawals)
	group.POST("/withdrawals", handler.RequestWithdrawal)
	group.GET("/invoice-profiles", handler.ListInvoiceProfiles)
	group.POST("/invoice-profiles", handler.CreateInvoiceProfile)
	group.GET("/invoices", handler.ListInvoices)
	group.POST("/invoices", handler.RequestInvoice)
	group.GET("/tickets", handler.ListTickets)
	group.POST("/tickets", handler.CreateTicket)
	group.GET("/tickets/:id/messages", handler.ListTicketMessages)
	group.POST("/tickets/:id/messages", handler.ReplyTicket)
	group.GET("/campaigns", handler.ListCampaigns)
	group.POST("/campaigns/:id/claims", handler.ClaimCampaign)
	group.POST("/content-cases", handler.ReportContent)
	group.GET("/proxy-options", handler.ListProxyOptions)
}

// RegisterAdminRoutes creates a protected extension beneath the existing
// /admin namespace. Financial commands use step-up authentication.
func RegisterAdminRoutes(v1 *gin.RouterGroup, handler *Handler, adminAuth middleware.AdminAuthMiddleware, auditLog middleware.AuditLogMiddleware, stepUpAuth middleware.StepUpAuthMiddleware, settingService *service.SettingService, panelRateLimiter *middleware.PanelRateLimiter) {
	if v1 == nil || handler == nil {
		return
	}
	admin := v1.Group("/admin/operations")
	admin.Use(gin.HandlerFunc(adminAuth), panelRateLimiter.Global(), gin.HandlerFunc(auditLog), middleware.AdminComplianceGuard(settingService))
	admin.GET("/withdrawals", handler.AdminListWithdrawals)
	admin.POST("/withdrawals/:id/resolve", gin.HandlerFunc(stepUpAuth), handler.AdminResolveWithdrawal)
	admin.POST("/wallet/adjustments", gin.HandlerFunc(stepUpAuth), handler.AdminAdjustBalance)
	admin.POST("/referrals/rewards", gin.HandlerFunc(stepUpAuth), handler.AdminGrantReferral)
	admin.POST("/invoices/:id/resolve", handler.AdminResolveInvoice)
	admin.GET("/tickets", handler.AdminListTickets)
	admin.GET("/tickets/:id/messages", handler.AdminListTicketMessages)
	admin.POST("/tickets/:id/messages", handler.AdminReplyTicket)
	admin.POST("/tickets/:id/resolve", handler.AdminResolveTicket)
	admin.POST("/campaigns", handler.AdminCreateCampaign)
	admin.POST("/campaigns/:id/status", handler.AdminSetCampaignStatus)
	admin.POST("/content-cases/:id/resolve", handler.AdminResolveContent)
	admin.POST("/proxies/:id/assignment", gin.HandlerFunc(stepUpAuth), handler.AdminAssignProxy)
}
