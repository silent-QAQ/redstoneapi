package wallet

import (
	"github.com/silent-QAQ/redstoneapi/internal/server/middleware"
	"github.com/silent-QAQ/redstoneapi/internal/service"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes mounts the current-user wallet read APIs. Each endpoint is
// JWT-protected; the handler derives the user ID exclusively from the subject
// set by that middleware.
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
	group := v1.Group("/wallet")
	group.Use(gin.HandlerFunc(jwtAuth))
	group.Use(middleware.BackendModeUserGuard(settingService))
	group.Use(panelRateLimiter.Global())
	group.GET("", handler.GetSnapshot)
	group.GET("/ledger", handler.ListLedger)
}
