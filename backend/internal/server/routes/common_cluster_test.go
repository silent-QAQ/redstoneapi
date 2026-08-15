package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/silent-QAQ/redstoneapi/internal/config"
	"github.com/silent-QAQ/redstoneapi/internal/redstone/cluster"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCommonHealthRejectsUnreadyClusterNode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	manager := cluster.NewManager(config.RedstoneClusterConfig{Enabled: true, NodeID: "node-a"}, nil, nil)
	RegisterCommonRoutes(router, manager)

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.JSONEq(t, `{"status":"unavailable","node_id":"node-a"}`, response.Body.String())
}
