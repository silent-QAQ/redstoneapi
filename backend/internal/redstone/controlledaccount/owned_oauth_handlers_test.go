package controlledaccount

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/silent-QAQ/redstoneapi/internal/server/middleware"
	"github.com/silent-QAQ/redstoneapi/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type ownedOAuthExchangeRouteCase struct {
	name     string
	provider string
	route    string
	bodyFor  func(sessionID, state string) string
	install  func(h *Handler)
	invoke   func(h *Handler, c *gin.Context)
}

func ownedOAuthExchangeRouteCases() []ownedOAuthExchangeRouteCase {
	return []ownedOAuthExchangeRouteCase{
		{
			name:     "generic",
			provider: "anthropic",
			route:    "/exchange",
			bodyFor: func(sessionID, state string) string {
				return `{"session_id":"` + sessionID + `","code":"auth-code","state":"` + state + `"}`
			},
			install: func(h *Handler) { h.oauthService = &service.OAuthService{} },
			invoke:  func(h *Handler, c *gin.Context) { h.ExchangeOwnedCode(c) },
		},
		{
			name:     "openai",
			provider: "openai",
			route:    "/openai",
			bodyFor: func(sessionID, state string) string {
				return `{"session_id":"` + sessionID + `","code":"auth-code","state":"` + state + `"}`
			},
			install: func(h *Handler) { h.openaiOAuthService = &service.OpenAIOAuthService{} },
			invoke:  func(h *Handler, c *gin.Context) { h.OpenAIExchangeOwnedCode(c) },
		},
		{
			name:     "gemini",
			provider: "gemini",
			route:    "/gemini",
			bodyFor: func(sessionID, state string) string {
				return `{"session_id":"` + sessionID + `","code":"auth-code","state":"` + state + `"}`
			},
			install: func(h *Handler) { h.geminiOAuthService = &service.GeminiOAuthService{} },
			invoke:  func(h *Handler, c *gin.Context) { h.GeminiExchangeOwnedCode(c) },
		},
		{
			name:     "antigravity",
			provider: "antigravity",
			route:    "/antigravity",
			bodyFor: func(sessionID, state string) string {
				return `{"session_id":"` + sessionID + `","code":"auth-code","state":"` + state + `"}`
			},
			install: func(h *Handler) { h.antigravityOAuthService = &service.AntigravityOAuthService{} },
			invoke:  func(h *Handler, c *gin.Context) { h.AntigravityExchangeOwnedCode(c) },
		},
		{
			name:     "grok",
			provider: "grok",
			route:    "/grok",
			bodyFor: func(sessionID, state string) string {
				return `{"session_id":"` + sessionID + `","code":"auth-code","state":"` + state + `"}`
			},
			install: func(h *Handler) { h.grokOAuthService = &service.GrokOAuthService{} },
			invoke:  func(h *Handler, c *gin.Context) { h.GrokExchangeOwnedCode(c) },
		},
	}
}

func performOwnedOAuthExchange(t *testing.T, route string, body string, userID int64, handler *Handler, invoke func(h *Handler, c *gin.Context)) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, route, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	ctx.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: userID})
	invoke(handler, ctx)
	return recorder
}

func TestOwnedOAuthExchangeRoutesRejectCrossUserBinding(t *testing.T) {
	for _, tc := range ownedOAuthExchangeRouteCases() {
		t.Run(tc.name, func(t *testing.T) {
			handler := &Handler{oauthBindingStore: newOAuthBindingStore()}
			tc.install(handler)

			sessionID := "session-" + tc.name
			state := "state-" + tc.name
			handler.oauthBindingStore.BindProvider(tc.provider, sessionID, state, 7)
			recorder := performOwnedOAuthExchange(t, tc.route, tc.bodyFor(sessionID, state), 8, handler, tc.invoke)

			require.Equal(t, http.StatusForbidden, recorder.Code)
			handler.oauthBindingStore.mu.Lock()
			require.Contains(t, handler.oauthBindingStore.entries, oauthBindingKey(tc.provider, sessionID), "cross-user exchange must not consume the owner's binding")
			handler.oauthBindingStore.mu.Unlock()
		})
	}
}
