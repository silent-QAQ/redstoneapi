package controlledaccount

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/silent-QAQ/redstoneapi/internal/server/middleware"
	"github.com/silent-QAQ/redstoneapi/internal/service"
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

func TestProvideHandlerInjectsOwnedAccountSettingsServices(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstreamBillingProbe := service.NewUpstreamBillingProbeService(nil, nil, nil)
	ollamaCloudUsage := service.NewOllamaCloudUsageService(nil, nil, nil, nil, false)
	handler := ProvideHandler(
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		upstreamBillingProbe, ollamaCloudUsage,
	)

	router := gin.New()
	router.GET("/upstream-billing-probe/settings", handler.GetOwnedUpstreamBillingProbeSettings)
	router.GET("/ollama-cloud-usage/settings", handler.GetOwnedOllamaCloudUsageSettings)

	for _, path := range []string{
		"/upstream-billing-probe/settings",
		"/ollama-cloud-usage/settings",
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equalf(t, http.StatusOK, recorder.Code, "unexpected status for %s: %s", path, recorder.Body.String())
	}
}

type ownedVerifyAdminService struct {
	service.AdminService
	account      *service.Account
	extraUpdates map[string]any
}

func (s *ownedVerifyAdminService) GetAccount(_ context.Context, id int64) (*service.Account, error) {
	if s.account == nil || s.account.ID != id {
		return nil, service.ErrAccountNotFound
	}
	return s.account, nil
}

func (s *ownedVerifyAdminService) UpdateAccountExtra(_ context.Context, _ int64, updates map[string]any) error {
	s.extraUpdates = make(map[string]any, len(updates))
	for key, value := range updates {
		s.extraUpdates[key] = value
	}
	return nil
}

func TestOwnedVerifyHandlerRunsModelVerificationForOwnedAPIKeyAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		require.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","model":"gpt-4o-mini","choices":[{"message":{"content":"pong"},"finish_reason":"stop"}],"usage":{"total_tokens":2}}`))
	}))
	t.Cleanup(upstream.Close)

	base := &ownedVerifyAdminService{account: &service.Account{
		ID: 17, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "secret", "base_url": upstream.URL},
	}}
	handler := &Handler{
		verifier: &AccountVerifier{httpClient: upstream.Client()},
		ownedAdminService: &ownedAdminService{
			AdminService: base,
			base:         base,
			db:           db,
		},
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 88})
		c.Request = c.Request.WithContext(withOwnerScope(c.Request.Context(), 88))
		c.Next()
	})
	router.POST("/:id/verify", handler.OwnedVerify)
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM accounts`).
		WithArgs(int64(17), int64(88)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM accounts`).
		WithArgs(int64(17), int64(88)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/17/verify", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "passed", base.extraUpdates["model_verification_verdict"])
	require.Equal(t, "passed", base.extraUpdates["model_verification_status"])
	require.Equal(t, "openai", base.extraUpdates["model_verification_protocol"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOwnedVerifyHandlerRejectsNonAPIKeyAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	base := &ownedVerifyAdminService{account: &service.Account{ID: 17, Type: service.AccountTypeOAuth}}
	handler := &Handler{
		verifier: NewAccountVerifier(),
		ownedAdminService: &ownedAdminService{
			AdminService: base,
			base:         base,
			db:           db,
		},
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(withOwnerScope(c.Request.Context(), 88))
		c.Next()
	})
	router.POST("/:id/verify", handler.OwnedVerify)
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM accounts`).
		WithArgs(int64(17), int64(88)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/17/verify", nil))

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Nil(t, base.extraUpdates)
	require.NoError(t, mock.ExpectationsWereMet())
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
