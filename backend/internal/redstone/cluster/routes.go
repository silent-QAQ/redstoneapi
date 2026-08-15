package cluster

import (
	"errors"
	"net/http"
	"strings"

	"github.com/silent-QAQ/redstoneapi/internal/server/middleware"
	"github.com/silent-QAQ/redstoneapi/internal/service"
	"github.com/gin-gonic/gin"
)

// RegisterAdminRoutes mounts cluster controls under the same protection chain
// as the rest of the administrator control plane. State-changing operations
// additionally require a recent step-up verification because they can alter
// traffic admission and invalidate process-wide caches.
func RegisterAdminRoutes(
	v1 *gin.RouterGroup,
	adminAuth middleware.AdminAuthMiddleware,
	auditLog middleware.AuditLogMiddleware,
	stepUpAuth middleware.StepUpAuthMiddleware,
	settingService *service.SettingService,
	panelRateLimiter *middleware.PanelRateLimiter,
	manager *Manager,
) {
	if v1 == nil || manager == nil {
		return
	}

	group := v1.Group("/admin/redstone/cluster")
	group.Use(
		gin.HandlerFunc(adminAuth),
		panelRateLimiter.Global(),
	)
	group.GET("/nodes", func(c *gin.Context) {
		nodes, err := manager.ListNodes(c.Request.Context())
		if err != nil {
			writeClusterError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{"nodes": nodes}})
	})
	group.GET("/task-leases", func(c *gin.Context) {
		leases, err := manager.ListTaskLeases(c.Request.Context())
		if err != nil {
			writeClusterError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{"leases": leases}})
	})

	mutations := group.Group("")
	mutations.Use(
		gin.HandlerFunc(auditLog),
		middleware.AdminComplianceGuard(settingService),
		gin.HandlerFunc(stepUpAuth),
	)
	mutations.POST("/nodes/:node_id/drain", func(c *gin.Context) {
		if err := manager.SetNodeDraining(c.Request.Context(), c.Param("node_id")); err != nil {
			writeClusterError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{"node_id": c.Param("node_id"), "state": StateDraining}})
	})
	mutations.POST("/nodes/:node_id/resume", func(c *gin.Context) {
		if err := manager.ResumeNode(c.Request.Context(), c.Param("node_id")); err != nil {
			writeClusterError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{"node_id": c.Param("node_id"), "state": StateActive}})
	})
	mutations.POST("/cache-epochs/:cache_name/invalidate", func(c *gin.Context) {
		epoch, err := manager.BumpCacheEpoch(c.Request.Context(), c.Param("cache_name"))
		if err != nil {
			writeClusterError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{"cache_name": c.Param("cache_name"), "epoch": epoch}})
	})
}

func writeClusterError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	code := "REDSTONE_CLUSTER_INTERNAL"
	switch {
	case errors.Is(err, ErrClusterDisabled):
		status = http.StatusNotFound
		code = "REDSTONE_CLUSTER_DISABLED"
	case errors.Is(err, ErrNodeNotFound):
		status = http.StatusNotFound
		code = "REDSTONE_CLUSTER_NODE_NOT_FOUND"
	case errors.Is(err, ErrNodeDraining), errors.Is(err, ErrLeaseNotHeld):
		status = http.StatusConflict
		code = "REDSTONE_CLUSTER_CONFLICT"
	case strings.Contains(err.Error(), "is required"):
		status = http.StatusBadRequest
		code = "REDSTONE_CLUSTER_INVALID_REQUEST"
	}
	c.JSON(status, gin.H{"code": code, "message": err.Error()})
}
