package market

import (
	"github.com/silent-QAQ/redstoneapi/internal/server/middleware"
	"github.com/silent-QAQ/redstoneapi/internal/service"
	"github.com/gin-gonic/gin"
)

// RegisterSellerRoutes mounts a separate authenticated seller group so it can
// evolve independently of the buyer/delivery routing file. It deliberately
// carries the same protections as /market: JWT identity, user-mode guard, and
// panel rate limiting.
func RegisterSellerRoutes(
	v1 *gin.RouterGroup,
	handler *Handler,
	jwtAuth middleware.JWTAuthMiddleware,
	settingService *service.SettingService,
	panelRateLimiter *middleware.PanelRateLimiter,
) {
	if v1 == nil || handler == nil {
		return
	}
	market := v1.Group("/market")
	market.Use(gin.HandlerFunc(jwtAuth))
	market.Use(middleware.BackendModeUserGuard(settingService))
	market.Use(panelRateLimiter.Global())
	seller := market.Group("/seller")
	seller.GET("", handler.GetSellerDashboard)
	seller.GET("/products", handler.ListCurrentSellerProducts)
	seller.GET("/orders", handler.ListCurrentSellerOrders)
	seller.POST("/products", handler.CreateSellerDraft)
	seller.PUT("/products/:id", handler.UpdateSellerDraft)
	seller.POST("/products/:id/publish", handler.PublishSellerProduct)
	seller.POST("/products/:id/archive", handler.ArchiveSellerProduct)
	seller.POST("/products/:id/delivery-items", handler.UploadSellerDelivery)
}
