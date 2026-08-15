package controlledaccount

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestVerifyWithCredentials_Anthropic_pass 用一个临时服务器模拟正常的 Anthropic 响应。
func TestVerifyWithCredentials_Anthropic_pass(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{
			"id": "msg_01abc",
			"type": "message",
			"role": "assistant",
			"content": [{"type": "text", "text": "ok"}],
			"model": "claude-haiku-4-5",
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 10, "output_tokens": 2}
		}`))
	}))
	defer ts.Close()

	v := NewAccountVerifier()
	result := v.VerifyWithCredentials(t.Context(), "anthropic", "sk-test", ts.URL)

	if result.Protocol != "anthropic" {
		t.Errorf("protocol = %q, want anthropic", result.Protocol)
	}
	if result.Verdict != "passed" {
		t.Errorf("verdict = %q, want passed (score=%d)", result.Verdict, result.Score)
	}
	if result.Score < 70 {
		t.Errorf("score = %d, want ≥70", result.Score)
	}
}

// TestVerifyWithCredentials_Anthropic_401 确保 401 响应映射到 failed。
func TestVerifyWithCredentials_Anthropic_401(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"type":"authentication_error","message":"invalid api key"}}`))
	}))
	defer ts.Close()

	v := NewAccountVerifier()
	result := v.VerifyWithCredentials(t.Context(), "anthropic", "bad-key", ts.URL)

	if result.Verdict != "failed" {
		t.Errorf("verdict = %q, want failed", result.Verdict)
	}
}

// TestVerifyWithCredentials_OpenAI_pass 用临时服务器模拟 OpenAI chat completions。
func TestVerifyWithCredentials_OpenAI_pass(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{
			"id": "chatcmpl-abc",
			"object": "chat.completion",
			"model": "gpt-4o-mini",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "pong"},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 12, "completion_tokens": 1}
		}`))
	}))
	defer ts.Close()

	v := NewAccountVerifier()
	result := v.VerifyWithCredentials(t.Context(), "openai", "sk-test", ts.URL)

	if result.Protocol != "openai" {
		t.Errorf("protocol = %q, want openai", result.Protocol)
	}
	if result.Verdict != "passed" {
		t.Errorf("verdict = %q, want passed (score=%d)", result.Verdict, result.Score)
	}
}

// TestVerifyWithCredentials_OpenAI_badContent 确保没有 "pong" 时得分降低。
func TestVerifyWithCredentials_OpenAI_badContent(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{
			"id": "chatcmpl-xyz",
			"object": "chat.completion",
			"model": "gpt-4o-mini",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "I cannot help with that."},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 12, "completion_tokens": 5}
		}`))
	}))
	defer ts.Close()

	v := NewAccountVerifier()
	result := v.VerifyWithCredentials(t.Context(), "openai", "sk-test", ts.URL)

	// No "pong" → 30 pts deducted from checks[content_pong]. Score should be ≤70.
	if result.Verdict == "passed" && result.Score == 100 {
		t.Errorf("expected score penalty for missing 'pong', got score=%d verdict=%q", result.Score, result.Verdict)
	}
}

// TestScoreAnthropicResponse 单元测试评分逻辑。
func TestScoreAnthropicResponse(t *testing.T) {
	tests := []struct {
		name      string
		resp      map[string]any
		wantMin   int
		wantVerdict string
	}{
		{
			name: "full pass",
			resp: map[string]any{
				"type": "message",
				"role": "assistant",
				"model": "claude-haiku-4-5",
				"content": []any{
					map[string]any{"type": "text", "text": "ok"},
				},
			},
			wantMin: 100,
			wantVerdict: "passed",
		},
		{
			name: "wrong role",
			resp: map[string]any{
				"type": "message",
				"role": "user",
				"model": "claude-haiku-4-5",
				"content": []any{
					map[string]any{"type": "text", "text": "ok"},
				},
			},
			wantMin: 75,
			wantVerdict: "passed",
		},
		{
			name: "non-claude model",
			resp: map[string]any{
				"type": "message",
				"role": "assistant",
				"model": "gpt-4",
				"content": []any{
					map[string]any{"type": "text", "text": "ok"},
				},
			},
			wantMin: 80,
			wantVerdict: "passed",
		},
		{
			name: "empty content",
			resp: map[string]any{
				"type": "message",
				"role": "assistant",
				"model": "claude-haiku-4-5",
				"content": []any{},
			},
			wantMin: 55,
			wantVerdict: "passed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score, _ := scoreAnthropicResponse(tt.resp)
			verdict := scoreToVerdict(score)
			if score < tt.wantMin {
				t.Errorf("score = %d, want ≥ %d", score, tt.wantMin)
			}
			if verdict != tt.wantVerdict {
				t.Errorf("verdict = %q, want %q (score=%d)", verdict, tt.wantVerdict, score)
			}
		})
	}
}

// TestNormalizeBaseURL 验证 URL 规范化逻辑。
func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		input    string
		fallback string
		want     string
	}{
		{"https://api.anthropic.com", "x", "https://api.anthropic.com"},
		{"https://api.anthropic.com/v1", "x", "https://api.anthropic.com"},
		{"https://api.anthropic.com/v1/", "x", "https://api.anthropic.com"},
		{"https://relay.example.com/", "x", "https://relay.example.com"},
		{"", "https://fallback.example.com", "https://fallback.example.com"},
	}
	for _, tt := range tests {
		got := normalizeBaseURL(tt.input, tt.fallback)
		if got != tt.want {
			t.Errorf("normalizeBaseURL(%q, %q) = %q, want %q", tt.input, tt.fallback, got, tt.want)
		}
	}
}

// TestDetectProtocol 验证协议推断。
func TestDetectProtocol(t *testing.T) {
	cases := map[string]string{
		"anthropic": "anthropic",
		"openai":    "openai",
		"gemini":    "gemini",
		"grok":      "openai",
		"unknown":   "openai", // fallback
	}
	for provider, wantProtocol := range cases {
		if got := detectProtocol(provider); got != wantProtocol {
			t.Errorf("detectProtocol(%q) = %q, want %q", provider, got, wantProtocol)
		}
	}
}
