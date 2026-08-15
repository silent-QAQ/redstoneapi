package controlledaccount

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/silent-QAQ/redstoneapi/internal/server/middleware"
	"github.com/silent-QAQ/redstoneapi/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOwnerScopeMiddlewareBindsOwnerToRequestContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 88})
		c.Next()
	})
	router.Use(ownerScopeMiddleware())
	router.GET("/", func(c *gin.Context) {
		ownerUserID, err := ownerScopeFromContext(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"owner_user_id": ownerUserID})
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	var body struct {
		OwnerUserID float64 `json:"owner_user_id"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.EqualValues(t, 88, body.OwnerUserID)
}

func TestOwnedVerifyHandlerIsReservedButInactive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := &Handler{}
	router.POST("/:id/verify", handler.OwnedVerify)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/17/verify", nil))

	require.Equal(t, http.StatusNotImplemented, recorder.Code)
}

func TestOwnedAccountRoutesExposeCompleteCredentialAndDataModes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerOwnedAccountRoutes(router.Group("/user/accounts"), &Handler{})

	routes := make(map[string]struct{}, len(router.Routes()))
	for _, route := range router.Routes() {
		routes[route.Method+" "+route.Path] = struct{}{}
	}

	expected := []string{
		"GET /user/accounts",
		"POST /user/accounts",
		"POST /user/accounts/batch",
		"GET /user/accounts/data",
		"POST /user/accounts/data",
		"POST /user/accounts/import/codex-session",
		"POST /user/accounts/generate-auth-url",
		"POST /user/accounts/generate-setup-token-url",
		"POST /user/accounts/exchange-code",
		"POST /user/accounts/exchange-setup-token-code",
		"POST /user/accounts/cookie-auth",
		"POST /user/accounts/setup-token-cookie-auth",
		"POST /user/accounts/openai/create-from-codex-pat",
		"POST /user/accounts/openai/generate-auth-url",
		"POST /user/accounts/openai/exchange-code",
		"POST /user/accounts/openai/refresh-token",
		"GET /user/accounts/gemini/oauth/capabilities",
		"POST /user/accounts/gemini/oauth/auth-url",
		"POST /user/accounts/gemini/oauth/exchange-code",
		"POST /user/accounts/antigravity/oauth/auth-url",
		"POST /user/accounts/antigravity/oauth/exchange-code",
		"POST /user/accounts/antigravity/oauth/refresh-token",
		"GET /user/accounts/grok/oauth/capabilities",
		"POST /user/accounts/grok/oauth/auth-url",
		"POST /user/accounts/grok/oauth/exchange-code",
		"POST /user/accounts/grok/oauth/refresh-token",
		"POST /user/accounts/grok/oauth/sso-token",
		"POST /user/accounts/grok/oauth/password",
		"POST /user/accounts/grok/sso-to-oauth",
		"GET /user/accounts/upstream-billing-probe/settings",
		"PUT /user/accounts/upstream-billing-probe/settings",
		"GET /user/accounts/ollama-cloud-usage/settings",
		"PUT /user/accounts/ollama-cloud-usage/settings",
		"POST /user/accounts/options/proxies/:id/test",
		"GET /user/accounts/options/web-search-emulation",
		"GET /user/accounts/options/tls-fingerprint-profiles",
		"POST /user/accounts/:id/verify",
	}
	for _, route := range expected {
		_, ok := routes[route]
		require.Truef(t, ok, "missing owned account route %s", route)
	}
}

func TestOwnedAccountExportRouteRunsStepUpGuard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	stepUp := middleware.StepUpAuthMiddleware(func(c *gin.Context) {
		c.Header("X-Step-Up-Checked", "true")
		c.Next()
	})
	registerOwnedAccountRoutes(router.Group("/user/accounts"), &Handler{}, stepUp)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/user/accounts/data", nil))

	require.Equal(t, "true", recorder.Header().Get("X-Step-Up-Checked"))
}

func TestAccountOptionsUseUserSafeDTOs(t *testing.T) {
	groups := accountOptionGroups([]service.Group{{
		ID:                   7,
		Name:                 "OpenAI",
		Platform:             service.PlatformOpenAI,
		Status:               service.StatusActive,
		RateMultiplier:       1.25,
		ProfitControlEnabled: true,
		ProfitMinMargin:      0.4,
		DuplicateOperationID: "internal-operation",
		ModelRoutingEnabled:  true,
		ModelRouting:         map[string][]int64{"secret-route": {99}},
	}})
	proxies := accountOptionProxies([]service.Proxy{{
		ID:       9,
		Name:     "Shared proxy",
		Protocol: "https",
		Host:     "proxy.example.com",
		Port:     443,
		Username: "visible-user",
		Password: "secret-password",
		Status:   service.StatusActive,
	}})

	groupJSON, err := json.Marshal(groups)
	require.NoError(t, err)
	require.Contains(t, string(groupJSON), `"id":7`)
	require.Contains(t, string(groupJSON), `"rate_multiplier":1.25`)
	require.NotContains(t, string(groupJSON), "secret-route")
	require.NotContains(t, string(groupJSON), "internal-operation")
	require.NotContains(t, string(groupJSON), "profit_min_margin")

	proxyJSON, err := json.Marshal(proxies)
	require.NoError(t, err)
	require.Contains(t, string(proxyJSON), `"host":"proxy.example.com"`)
	require.NotContains(t, string(proxyJSON), "secret-password")
	require.NotContains(t, string(proxyJSON), `"Password"`)
}
