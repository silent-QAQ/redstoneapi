package market

import (
	"github.com/silent-QAQ/redstoneapi/internal/server/middleware"
	"github.com/silent-QAQ/redstoneapi/internal/service"
	"github.com/gin-gonic/gin"
)

func RegisterRoutes(
	v1 *gin.RouterGroup,
	handler *Handler,
	jwtAuth middleware.JWTAuthMiddleware,
	settingService *service.SettingService,
	panelRateLimiter *middleware.PanelRateLimiter,
) {
	if handler == nil {
		return
	}
	group := v1.Group("/market")
	group.Use(gin.HandlerFunc(jwtAuth))
	group.Use(middleware.BackendModeUserGuard(settingService))
	group.Use(panelRateLimiter.Global())
	group.GET("/products", handler.ListProducts)
	group.POST("/products/:id/orders", handler.CreateOrder)
	group.POST("/products/:id/reports", handler.CreateReport)
	group.GET("/orders", handler.ListCurrentUserOrders)
	group.POST("/orders/:id/delivery", handler.DeliverOrder)
	group.POST("/orders/:id/file-download", handler.DownloadFileDelivery)
	group.POST("/orders/:id/appeals", handler.CreateAppeal)
}

// RegisterAdminRoutes mounts the settlement controls beneath the existing
// protected admin group. The caller owns authentication, audit logging,
// compliance acknowledgement, and step-up verification; keeping this package
// free of those dependencies preserves its domain boundary.
func RegisterAdminRoutes(admin *gin.RouterGroup, handler *Handler, stepUpAuth middleware.StepUpAuthMiddleware) {
	if admin == nil || handler == nil {
		return
	}
	runtime := admin.Group("/market/runtime")
	runtime.Use(gin.HandlerFunc(stepUpAuth))
	runtime.GET("", handler.AdminGetRuntimeHealth)
	orders := admin.Group("/market/orders")
	orders.Use(gin.HandlerFunc(stepUpAuth))
	orders.POST("/:id/settle", handler.AdminSettleOrder)
	orders.POST("/:id/refund", handler.AdminRefundOrder)
	orders.POST("/:id/appeal-resolution", handler.AdminResolveAppeal)
	orders.POST("/:id/reverse", handler.AdminReverseOrder)
	orders.GET("", handler.AdminListOrders)
	appeals := admin.Group("/market/appeals")
	appeals.Use(gin.HandlerFunc(stepUpAuth))
	appeals.GET("", handler.AdminListOpenAppeals)
	reports := admin.Group("/market/reports")
	reports.Use(gin.HandlerFunc(stepUpAuth))
	reports.GET("", handler.AdminListOpenReports)
	reports.POST("/:id/dismiss", handler.AdminDismissReport)
	reports.POST("/:id/suspend", handler.AdminSuspendReport)
	reports.POST("/:id/release-hold", handler.AdminReleaseReportHold)
	contentReviews := admin.Group("/market/content-reviews")
	contentReviews.Use(gin.HandlerFunc(stepUpAuth))
	contentReviews.GET("", handler.AdminListOpenContentReviews)
	contentReviews.POST("/:id/approve", handler.AdminApproveContentReview)
	contentReviews.POST("/:id/reject", handler.AdminRejectContentReview)
	sellers := admin.Group("/market/sellers")
	sellers.Use(gin.HandlerFunc(stepUpAuth))
	sellers.POST("/:id/freeze", handler.AdminFreezeSeller)
	sellers.POST("/:id/unfreeze", handler.AdminUnfreezeSeller)
	official := admin.Group("/market/official/products")
	official.Use(gin.HandlerFunc(stepUpAuth))
	official.GET("", handler.AdminListOfficialProducts)
	official.POST("", handler.AdminCreateOfficialProduct)
	official.POST("/:id/delivery-items", handler.AdminUploadOfficialDelivery)
	policy := admin.Group("/market/settings")
	policy.Use(gin.HandlerFunc(stepUpAuth))
	policy.GET("/service-fee", handler.AdminGetFeePolicy)
	policy.PUT("/service-fee", handler.AdminUpdateFeePolicy)
}
