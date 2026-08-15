package cluster

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/silent-QAQ/redstoneapi/internal/config"
	servermiddleware "github.com/silent-QAQ/redstoneapi/internal/server/middleware"
	"github.com/silent-QAQ/redstoneapi/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type clusterAuditCaptureRepository struct {
	mu   sync.Mutex
	logs []*service.AuditLog
}

func (r *clusterAuditCaptureRepository) BatchInsert(_ context.Context, logs []*service.AuditLog) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, logs...)
	return int64(len(logs)), nil
}

func (r *clusterAuditCaptureRepository) Insert(_ context.Context, log *service.AuditLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, log)
	return nil
}

func (r *clusterAuditCaptureRepository) List(context.Context, *service.AuditLogFilter) (*service.AuditLogList, error) {
	return &service.AuditLogList{}, nil
}

func (r *clusterAuditCaptureRepository) GetByID(context.Context, int64) (*service.AuditLog, error) {
	return nil, service.ErrAuditLogNotFound
}

func (r *clusterAuditCaptureRepository) Count(context.Context) (int64, error) { return 0, nil }
func (r *clusterAuditCaptureRepository) TruncateAll(context.Context) error    { return nil }
func (r *clusterAuditCaptureRepository) DeleteBefore(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

type clusterComplianceRepository struct {
	acknowledged bool
}

func (r *clusterComplianceRepository) Get(ctx context.Context, key string) (*service.Setting, error) {
	value, err := r.GetValue(ctx, key)
	if err != nil {
		return nil, err
	}
	return &service.Setting{Key: key, Value: value}, nil
}

func (r *clusterComplianceRepository) GetValue(_ context.Context, _ string) (string, error) {
	if !r.acknowledged {
		return "", service.ErrSettingNotFound
	}
	return `{"version":"` + service.AdminComplianceVersion + `"}`, nil
}

func (r *clusterComplianceRepository) Set(context.Context, string, string) error { return nil }
func (r *clusterComplianceRepository) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *clusterComplianceRepository) SetMultiple(context.Context, map[string]string) error {
	return nil
}
func (r *clusterComplianceRepository) GetAll(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *clusterComplianceRepository) Delete(context.Context, string) error { return nil }

func clusterAdminAuth(c *gin.Context) {
	c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 7})
	c.Set(string(servermiddleware.ContextKeyUserRole), service.RoleAdmin)
	c.Set(servermiddleware.ContextKeyAuthEmail, "admin@example.test")
	c.Set("auth_method", service.AuditAuthMethodJWT)
	c.Next()
}

func passStepUp(c *gin.Context) { c.Next() }

func rejectStepUp(c *gin.Context) {
	servermiddleware.AbortWithError(c, http.StatusForbidden, "STEP_UP_REQUIRED", "Recent two-factor verification is required")
}

func newClusterRouter(
	manager *Manager,
	auditLog servermiddleware.AuditLogMiddleware,
	stepUp servermiddleware.StepUpAuthMiddleware,
	settingService *service.SettingService,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterAdminRoutes(
		router.Group("/api/v1"),
		servermiddleware.AdminAuthMiddleware(clusterAdminAuth),
		auditLog,
		stepUp,
		settingService,
		nil,
		manager,
	)
	return router
}

func TestClusterMutationEndpointsRequireStepUp(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	manager := NewManager(config.RedstoneClusterConfig{Enabled: true, NodeID: "node-a"}, db, nil)
	router := newClusterRouter(
		manager,
		servermiddleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() }),
		servermiddleware.StepUpAuthMiddleware(rejectStepUp),
		nil,
	)

	for _, path := range []string{
		"/api/v1/admin/redstone/cluster/nodes/node-b/drain",
		"/api/v1/admin/redstone/cluster/nodes/node-b/resume",
		"/api/v1/admin/redstone/cluster/cache-epochs/gateway:models/invalidate",
	} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, nil))
			require.Equal(t, http.StatusForbidden, response.Code)
			require.Contains(t, response.Body.String(), "STEP_UP_REQUIRED")
		})
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestClusterMutationsWithStepUpAreAudited(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	manager := NewManager(config.RedstoneClusterConfig{Enabled: true, NodeID: "node-a"}, db, nil)
	mock.ExpectExec("UPDATE redstone_cluster_nodes").
		WithArgs("node-b", StateDraining).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("INSERT INTO redstone_cluster_cache_epochs").
		WithArgs("cluster:nodes").
		WillReturnRows(sqlmock.NewRows([]string{"epoch"}).AddRow(int64(2)))
	mock.ExpectExec("UPDATE redstone_cluster_nodes").
		WithArgs("node-b", StateActive).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("INSERT INTO redstone_cluster_cache_epochs").
		WithArgs("cluster:nodes").
		WillReturnRows(sqlmock.NewRows([]string{"epoch"}).AddRow(int64(3)))
	mock.ExpectQuery("INSERT INTO redstone_cluster_cache_epochs").
		WithArgs("gateway:models").
		WillReturnRows(sqlmock.NewRows([]string{"epoch"}).AddRow(int64(4)))

	auditRepository := &clusterAuditCaptureRepository{}
	auditService := service.NewAuditLogService(auditRepository, nil)
	auditService.Start()
	t.Cleanup(auditService.Stop)
	settings := service.NewSettingService(&clusterComplianceRepository{acknowledged: true}, &config.Config{})
	router := newClusterRouter(
		manager,
		servermiddleware.NewAuditLogMiddleware(auditService),
		servermiddleware.StepUpAuthMiddleware(passStepUp),
		settings,
	)

	for _, tc := range []struct {
		path string
		body string
	}{
		{
			path: "/api/v1/admin/redstone/cluster/nodes/node-b/drain",
			body: `{"code":0,"data":{"node_id":"node-b","state":"draining"}}`,
		},
		{
			path: "/api/v1/admin/redstone/cluster/nodes/node-b/resume",
			body: `{"code":0,"data":{"node_id":"node-b","state":"active"}}`,
		},
		{
			path: "/api/v1/admin/redstone/cluster/cache-epochs/gateway:models/invalidate",
			body: `{"code":0,"data":{"cache_name":"gateway:models","epoch":4}}`,
		},
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, tc.path, nil))
		require.Equal(t, http.StatusOK, response.Code)
		require.JSONEq(t, tc.body, response.Body.String())
	}

	auditService.Stop()
	auditRepository.mu.Lock()
	logs := append([]*service.AuditLog(nil), auditRepository.logs...)
	auditRepository.mu.Unlock()
	require.Len(t, logs, 3)
	byAction := make(map[string]*service.AuditLog, len(logs))
	for _, entry := range logs {
		byAction[entry.Action] = entry
		require.Equal(t, http.StatusOK, entry.StatusCode)
		require.Equal(t, service.AuditAuthMethodJWT, entry.AuthMethod)
		require.EqualValues(t, 7, *entry.ActorUserID)
	}
	require.Equal(t, "node-b", byAction["admin.redstone.cluster.node.drain"].Extra["params"].(map[string]string)["node_id"])
	require.Equal(t, "node-b", byAction["admin.redstone.cluster.node.resume"].Extra["params"].(map[string]string)["node_id"])
	require.Equal(t, "gateway:models", byAction["admin.redstone.cluster.cache.invalidate"].Extra["params"].(map[string]string)["cache_name"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestClusterMutationsRequireComplianceAcknowledgement(t *testing.T) {
	manager := NewManager(config.RedstoneClusterConfig{Enabled: true, NodeID: "node-a"}, nil, nil)
	stepUpCalls := 0
	router := newClusterRouter(
		manager,
		servermiddleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() }),
		servermiddleware.StepUpAuthMiddleware(func(c *gin.Context) { stepUpCalls++; c.Next() }),
		service.NewSettingService(&clusterComplianceRepository{}, &config.Config{}),
	)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/admin/redstone/cluster/nodes/node-b/drain", nil))
	require.Equal(t, http.StatusLocked, response.Code)
	require.Contains(t, response.Body.String(), "ADMIN_COMPLIANCE_ACK_REQUIRED")
	require.Zero(t, stepUpCalls)
}

func TestClusterReadEndpointsDoNotRequireStepUp(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	manager := NewManager(config.RedstoneClusterConfig{Enabled: true, NodeID: "node-a"}, db, nil)
	mock.ExpectQuery("SELECT node_id, state, advertise_url, version, capabilities, last_heartbeat_at").
		WillReturnRows(sqlmock.NewRows([]string{
			"node_id", "state", "advertise_url", "version", "capabilities", "last_heartbeat_at", "drain_requested_at", "created_at", "updated_at",
		}).AddRow("node-a", StateActive, "", "", []byte(`{}`), time.Now().UTC(), nil, time.Now().UTC(), time.Now().UTC()))
	stepUpCalls := 0
	router := newClusterRouter(
		manager,
		servermiddleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() }),
		servermiddleware.StepUpAuthMiddleware(func(c *gin.Context) { stepUpCalls++; rejectStepUp(c) }),
		service.NewSettingService(&clusterComplianceRepository{}, &config.Config{}),
	)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/admin/redstone/cluster/nodes", nil))
	require.Equal(t, http.StatusOK, response.Code)
	require.Zero(t, stepUpCalls)
	require.NoError(t, mock.ExpectationsWereMet())
}
