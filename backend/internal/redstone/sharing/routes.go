package sharing

import (
	"github.com/gin-gonic/gin"
	"github.com/silent-QAQ/redstoneapi/internal/server/middleware"
	"github.com/silent-QAQ/redstoneapi/internal/service"
)

// RegisterRoutes only exposes room lifecycle APIs. It never exposes account
// credentials or direct account mutations; those stay behind the owner-scoped
// account adapter and existing sub2 account services.
func RegisterRoutes(v1 *gin.RouterGroup, handler *Handler, jwtAuth middleware.JWTAuthMiddleware, settingService *service.SettingService, limiter *middleware.PanelRateLimiter) {
	if v1 == nil || handler == nil {
		return
	}
	group := v1.Group("/account-share")
	group.Use(gin.HandlerFunc(jwtAuth), middleware.BackendModeUserGuard(settingService), limiter.Global())
	group.GET("/rooms", handler.ListPublicRooms)
	group.GET("/rooms/mine", handler.ListOwnerRooms)
	group.POST("/rooms", handler.CreateRoom)
	group.PUT("/rooms/:id", handler.UpdateRoom)
	group.DELETE("/rooms/:id", handler.CloseRoom)
	group.DELETE("/rooms/:id/archive", handler.DeleteRoom)
	group.GET("/rooms/:id/accounts", handler.ListOwnerRoomAccounts)
	group.POST("/rooms/:id/accounts", handler.BindAccount)
	group.POST("/rooms/:id/accounts/:account_id/drain", handler.DrainRoomAccount)
	group.DELETE("/rooms/:id/accounts/:account_id", handler.RemoveRoomAccount)
	group.POST("/rooms/:id/invites", handler.CreateInvite)
	group.GET("/rooms/:id/memberships", handler.ListOwnerRoomMemberships)
	group.POST("/rooms/:id/memberships/:membership_id/decision", handler.DecideMembership)
	group.POST("/rooms/:id/join", handler.JoinRoom)
	group.GET("/rooms/:id/reviews", handler.ListRoomReviews)
	group.POST("/rooms/:id/reviews", handler.CreateReview)
	group.GET("/memberships", handler.ListMemberships)
	group.DELETE("/memberships/:id", handler.LeaveMembership)
	group.POST("/memberships/:id/lease", handler.AcquireLease)
	group.POST("/leases/:id/heartbeat", handler.HeartbeatLease)
	group.DELETE("/leases/:id", handler.ReleaseLease)
	group.GET("/payout-receipts", handler.ListPayoutReceipts)
	group.GET("/key-room-options", handler.ListAPIKeyRoomOptions)
	group.GET("/private-groups", handler.ListPrivateGroups)
	group.POST("/private-groups", handler.CreatePrivateGroup)
	group.GET("/private-groups/:id/members", handler.ListPrivateGroupMembers)
	group.POST("/private-groups/:id/members", handler.GrantPrivateGroupMember)
	group.DELETE("/private-groups/:id/members/:user_id", handler.RevokePrivateGroupMember)
	group.DELETE("/private-groups/:id", handler.ArchivePrivateGroup)
}

// RegisterAdminRoutes inherits the caller's existing admin authentication,
// audit and compliance protections. It adds no parallel admin control plane.
func RegisterAdminRoutes(admin *gin.RouterGroup, handler *Handler, stepUpAuth middleware.StepUpAuthMiddleware) {
	if admin == nil || handler == nil {
		return
	}
	group := admin.Group("/account-share")
	group.Use(gin.HandlerFunc(stepUpAuth))
	governance := group.Group("/settings")
	governance.GET("/policy", handler.AdminGetSharingPolicy)
	governance.PUT("/policy", handler.AdminUpdateSharingPolicy)
	governance.GET("/quota", handler.AdminGetQuotaPolicy)
	governance.PUT("/quota", handler.AdminUpdateQuotaPolicy)
	group.PUT("/rooms/:id/moderation", handler.ModerateRoom)
	group.PUT("/reviews/:id/moderation", handler.ModerateReview)
	group.GET("/rooms/pending", handler.AdminListPendingRooms)
	group.GET("/reviews/reported", handler.AdminListReportedReviews)
}
