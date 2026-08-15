package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/silent-QAQ/redstoneapi/internal/pkg/ctxkey"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type sharingAccessGuardStub struct {
	allowed map[int64]struct{}
	err     error
	userID  int64
	groupID *int64
}

func (s *sharingAccessGuardStub) AllowedAccountIDs(_ context.Context, userID int64, groupID *int64, _ []int64) (map[int64]struct{}, error) {
	s.userID = userID
	s.groupID = groupID
	return s.allowed, s.err
}

func sharingAccessTestContext(userID, groupID int64) context.Context {
	ctx := context.WithValue(context.Background(), ctxkey.UserID, userID)
	return context.WithValue(ctx, ctxkey.Group, &Group{ID: groupID})
}

func TestFilterAccountsForSharingAccessLeavesOrdinaryAccountsUnchanged(t *testing.T) {
	guard := &sharingAccessGuardStub{allowed: map[int64]struct{}{2: {}}}
	accounts, err := filterAccountsForSharingAccess(context.Background(), guard, []Account{{ID: 1}, {ID: 2}})
	require.NoError(t, err)
	require.Equal(t, []Account{{ID: 1}, {ID: 2}}, accounts)
	require.Zero(t, guard.userID)
}

func TestFilterAccountsForSharingAccessUsesAuthenticatedUserAndPrivateGroup(t *testing.T) {
	guard := &sharingAccessGuardStub{allowed: map[int64]struct{}{2: {}}}
	accounts, err := filterAccountsForSharingAccess(sharingAccessTestContext(9, 31), guard, []Account{{ID: 1}, {ID: 2}})
	require.NoError(t, err)
	require.Equal(t, []Account{{ID: 2}}, accounts)
	require.Equal(t, int64(9), guard.userID)
	require.NotNil(t, guard.groupID)
	require.Equal(t, int64(31), *guard.groupID)
}

func TestEnsureAccountSharingAccessRejectsRevokedOrExpiredCandidate(t *testing.T) {
	guard := &sharingAccessGuardStub{allowed: map[int64]struct{}{}}
	err := ensureAccountSharingAccess(sharingAccessTestContext(9, 31), guard, &Account{ID: 2})
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
}

func TestEnsureAccountSharingAccessFailsClosedOnGuardError(t *testing.T) {
	guard := &sharingAccessGuardStub{err: errors.New("database unavailable")}
	err := ensureAccountSharingAccess(sharingAccessTestContext(9, 31), guard, &Account{ID: 2})
	require.ErrorContains(t, err, "validate selected shared account lease")
}

func sharingAccessGuardTestWebSocket(t *testing.T) *coderws.Conn {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func TestGatewayForwardEntrypointsFailClosedForRevokedSharedAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := sharingAccessTestContext(9, 31)
	guard := &sharingAccessGuardStub{allowed: map[int64]struct{}{}}
	account := &Account{ID: 17}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	wsClient := sharingAccessGuardTestWebSocket(t)

	gateway := &GatewayService{sharingAccessGuard: guard}
	openai := &OpenAIGatewayService{sharingAccessGuard: guard}
	gemini := &GeminiMessagesCompatService{sharingAccessGuard: guard}
	antigravity := &AntigravityGatewayService{sharingAccessGuard: guard}

	tests := []struct {
		name string
		call func() error
	}{
		{"gateway forward", func() error { _, err := gateway.Forward(ctx, c, account, nil); return err }},
		{"gateway count tokens", func() error { return gateway.ForwardCountTokens(ctx, c, account, nil) }},
		{"gateway chat completions", func() error { _, err := gateway.ForwardAsChatCompletions(ctx, c, account, nil, nil); return err }},
		{"gateway responses", func() error { _, err := gateway.ForwardAsResponses(ctx, c, account, nil, nil); return err }},
		{"gateway grok native", func() error { _, err := gateway.DoGrokNativeResponsesJSON(ctx, account, nil); return err }},
		{"openai forward", func() error { _, err := openai.Forward(ctx, c, account, nil); return err }},
		{"openai chat completions", func() error { _, err := openai.ForwardAsChatCompletions(ctx, c, account, nil, "", ""); return err }},
		{"openai anthropic", func() error { _, err := openai.ForwardAsAnthropic(ctx, c, account, nil, "", ""); return err }},
		{"openai embeddings", func() error { _, err := openai.ForwardEmbeddings(ctx, c, account, nil, ""); return err }},
		{"openai images", func() error { _, err := openai.ForwardImages(ctx, c, account, nil, nil, ""); return err }},
		{"openai grok media", func() error { _, err := openai.ForwardGrokMedia(ctx, c, account, "", "", nil, ""); return err }},
		{"openai grok voice", func() error { _, err := openai.ForwardGrokVoice(ctx, c, account, "", nil, ""); return err }},
		{"openai grok realtime", func() error { return openai.ProxyGrokRealtime(ctx, c, wsClient, account, "", "") }},
		{"openai alpha search", func() error { _, err := openai.ForwardAlphaSearch(ctx, c, account, nil); return err }},
		{"openai count tokens", func() error { return openai.ForwardCountTokensAsAnthropic(ctx, c, account, nil, "") }},
		{"openai responses websocket", func() error { return openai.ProxyResponsesWebSocketFromClient(ctx, c, wsClient, account, "", nil, nil) }},
		{"openai live create", func() error { _, err := openai.createUpstreamLiveCall(ctx, account, nil, ""); return err }},
		{"gemini forward", func() error { _, err := gemini.Forward(ctx, c, account, nil); return err }},
		{"gemini native", func() error { _, err := gemini.ForwardNative(ctx, c, account, "", "", false, nil); return err }},
		{"gemini chat completions", func() error { _, err := gemini.ForwardAsChatCompletions(ctx, c, account, nil); return err }},
		{"gemini ai studio get", func() error { _, err := gemini.ForwardAIStudioGET(ctx, account, "/v1beta/models"); return err }},
		{"antigravity claude", func() error { _, err := antigravity.Forward(ctx, c, account, nil, false); return err }},
		{"antigravity chat completions", func() error { _, err := antigravity.ForwardAsChatCompletions(ctx, c, account, nil, nil); return err }},
		{"antigravity responses", func() error { _, err := antigravity.ForwardAsResponses(ctx, c, account, nil, nil); return err }},
		{"antigravity upstream", func() error { _, err := antigravity.ForwardUpstream(ctx, c, account, nil); return err }},
		{"antigravity gemini", func() error {
			_, err := antigravity.ForwardGemini(ctx, c, account, "", "", false, nil, false)
			return err
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.ErrorIs(t, tt.call(), ErrNoAvailableAccounts)
		})
	}
}
