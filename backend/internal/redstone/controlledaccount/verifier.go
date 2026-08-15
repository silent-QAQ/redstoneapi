package controlledaccount

// AccountVerifier runs lightweight API probes against user-controlled accounts
// and returns a VerificationResult, modeled after veridrop's detector pipeline
// (github.com/canarybyte/veridrop).
//
// Probe strategy by protocol:
//
//   Anthropic: POST /v1/messages with a minimal prompt. Pass if the response
//     contains a "content" array with at least one text block, the "model"
//     field contains "claude", and usage.output_tokens > 0.
//
//   OpenAI:    POST /v1/chat/completions with a "Reply with: pong" prompt.
//     Pass if choices[0].message.content contains "pong" (case-insensitive)
//     and finish_reason is "stop".
//
//   Gemini:    POST /v1/chat/completions (OpenAI-compat) with same pong probe.
//     Pass if same conditions hold.
//
// All probes run with a 20-second timeout to avoid blocking the scheduler.
// The verifier does NOT attempt thinking-signature cryptographic validation
// (that requires consuming significant tokens per probe); it only validates
// connectivity and basic protocol compliance.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	verifyTimeout = 20 * time.Second

	// Minimum score to be considered "passed". Scores in [50,70) are "marginal".
	verdictPassedThreshold  = 70
	verdictMarginalThreshold = 50
)

// VerificationResult is the outcome of a single probe run.
type VerificationResult struct {
	Success   bool      `json:"success"`
	Message   string    `json:"message"`
	Score     int       `json:"score"`    // 0-100
	Verdict   string    `json:"verdict"`  // "passed","marginal","failed","error"
	Protocol  string    `json:"protocol"` // "anthropic","openai","gemini","unknown"
	Details   map[string]any `json:"details,omitempty"`
	DurationMs int      `json:"duration_ms"`
	Timestamp time.Time `json:"timestamp"`
}

// AccountVerifier probes an account's API key using a minimal request.
type AccountVerifier struct {
	httpClient *http.Client
}

// NewAccountVerifier returns an AccountVerifier with a shared HTTP client.
func NewAccountVerifier() *AccountVerifier {
	return &AccountVerifier{
		httpClient: &http.Client{
			Timeout: verifyTimeout + 2*time.Second, // slightly longer than probe timeout
		},
	}
}

// accountCredentials holds the decrypted API key and base URL for a probe.
type accountCredentials struct {
	APIKey  string
	BaseURL string // e.g. "https://api.anthropic.com"
}

// VerifyAccount fetches the account's credentials from the database,
// decrypts them, and runs the appropriate protocol probe.
// ownerUserID is used to scope the credential fetch.
func (v *AccountVerifier) VerifyAccount(ctx context.Context, service *Service, ownerUserID, accountID int64) (VerificationResult, error) {
	if service == nil || service.db == nil || service.cipher == nil {
		return VerificationResult{
			Success:   false,
			Verdict:   "error",
			Protocol:  "unknown",
			Score:     0,
			Message:   "service unavailable",
			Timestamp: time.Now(),
		}, fmt.Errorf("service unavailable")
	}

	// 1. 查询账号基本信息
	var provider string
	err := service.db.QueryRowContext(ctx, `
		SELECT r.provider
		FROM redstone_user_controlled_accounts r
		WHERE r.owner_user_id = $1 AND r.account_id = $2
			AND r.lifecycle <> 'revoked'
	`, ownerUserID, accountID).Scan(&provider)
	if err != nil {
		return VerificationResult{
			Success:   false,
			Verdict:   "error",
			Protocol:  "unknown",
			Score:     0,
			Message:   "account not found",
			Timestamp: time.Now(),
		}, fmt.Errorf("account not found: %w", err)
	}

	// 2. 查询加密凭证
	var ciphertext, wrappedDEK []byte
	var keyVersion string
	err = service.db.QueryRowContext(ctx, `
		SELECT ciphertext, key_version, wrapped_dek
		FROM redstone_user_account_secrets
		WHERE account_id = $1
	`, accountID).Scan(&ciphertext, &keyVersion, &wrappedDEK)
	if err != nil {
		return VerificationResult{
			Success:   false,
			Verdict:   "error",
			Protocol:  detectProtocol(provider),
			Score:     0,
			Message:   "credentials not found",
			Timestamp: time.Now(),
		}, fmt.Errorf("credentials not found: %w", err)
	}

	// 3. 解密凭证
	payload := Payload{Ciphertext: ciphertext, WrappedDEK: wrappedDEK}
	plaintext, err := service.cipher.Decrypt(payload, accountAAD(accountID))
	if err != nil {
		return VerificationResult{
			Success:   false,
			Verdict:   "error",
			Protocol:  detectProtocol(provider),
			Score:     0,
			Message:   "decrypt failed",
			Timestamp: time.Now(),
		}, fmt.Errorf("decrypt credentials: %w", err)
	}
	defer zeroBytes(plaintext)

	var credentials map[string]interface{}
	if err := json.Unmarshal(plaintext, &credentials); err != nil {
		return VerificationResult{
			Success:   false,
			Verdict:   "error",
			Protocol:  detectProtocol(provider),
			Score:     0,
			Message:   "invalid credentials format",
			Timestamp: time.Now(),
		}, fmt.Errorf("parse credentials: %w", err)
	}

	// 4. 提取 API Key 和 Base URL
	apiKey, _ := credentials["api_key"].(string)
	baseURL, _ := credentials["base_url"].(string)

	if apiKey == "" {
		return VerificationResult{
			Success:   false,
			Verdict:   "error",
			Protocol:  detectProtocol(provider),
			Score:     0,
			Message:   "api_key missing in credentials",
			Timestamp: time.Now(),
		}, fmt.Errorf("api_key missing")
	}

	// 5. 执行验真
	result := v.VerifyWithCredentials(ctx, provider, apiKey, baseURL)
	return result, nil
}

// VerifyWithCredentials runs the probe directly given raw credentials.
// This is the testable, credential-independent core.
func (v *AccountVerifier) VerifyWithCredentials(
	ctx context.Context,
	provider string,
	apiKey string,
	baseURL string,
) VerificationResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()

	protocol := detectProtocol(provider)
	var result VerificationResult

	switch protocol {
	case "anthropic":
		result = v.probeAnthropic(ctx, apiKey, baseURL)
	case "openai", "gemini":
		result = v.probeOpenAICompat(ctx, protocol, apiKey, baseURL)
	default:
		result = v.probeOpenAICompat(ctx, "openai", apiKey, baseURL)
		result.Protocol = "unknown"
	}

	result.DurationMs = int(time.Since(start).Milliseconds())
	result.Timestamp = time.Now()
	return result
}

// detectProtocol maps a sub2api provider name to a veridrop protocol name.
func detectProtocol(provider string) string {
	switch strings.ToLower(provider) {
	case "anthropic":
		return "anthropic"
	case "gemini":
		return "gemini"
	case "openai", "grok":
		return "openai"
	default:
		return "openai" // OpenAI-compat as default
	}
}

// --- Anthropic probe --------------------------------------------------------

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (v *AccountVerifier) probeAnthropic(ctx context.Context, apiKey, baseURL string) VerificationResult {
	base := normalizeBaseURL(baseURL, "https://api.anthropic.com")

	reqBody := anthropicRequest{
		Model:     "claude-haiku-4-5",
		MaxTokens: 16,
		Messages:  []anthropicMessage{{Role: "user", Content: "Reply with only the word: ok"}},
	}
	body, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return errorResult("anthropic", fmt.Sprintf("build request: %v", err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return errorResult("anthropic", fmt.Sprintf("http: %v", err))
	}
	defer resp.Body.Close()
	rawBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return VerificationResult{
			Success: false, Verdict: "failed", Score: 0, Protocol: "anthropic",
			Message: fmt.Sprintf("authentication rejected (HTTP %d)", resp.StatusCode),
			Details: map[string]any{"http_status": resp.StatusCode, "body_excerpt": excerpt(rawBody, 200)},
		}
	}
	if resp.StatusCode != http.StatusOK {
		return VerificationResult{
			Success: false, Verdict: "error", Score: 0, Protocol: "anthropic",
			Message: fmt.Sprintf("unexpected HTTP %d", resp.StatusCode),
			Details: map[string]any{"http_status": resp.StatusCode, "body_excerpt": excerpt(rawBody, 200)},
		}
	}

	var parsed map[string]any
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		return errorResult("anthropic", "response not valid JSON")
	}

	score, checks := scoreAnthropicResponse(parsed)
	verdict := scoreToVerdict(score)

	return VerificationResult{
		Success:  verdict == "passed" || verdict == "marginal",
		Verdict:  verdict,
		Score:    score,
		Protocol: "anthropic",
		Message:  fmt.Sprintf("score %d/100 (%s)", score, verdict),
		Details: map[string]any{
			"http_status": resp.StatusCode,
			"checks":      checks,
			"model":       parsed["model"],
		},
	}
}

// scoreAnthropicResponse applies veridrop-inspired checks to an Anthropic response.
// Returns a 0-100 score and a map of named check results.
func scoreAnthropicResponse(resp map[string]any) (int, map[string]bool) {
	checks := make(map[string]bool)
	score := 0

	// Check 1 (30 pts): response type == "message"
	checks["type_message"] = resp["type"] == "message"
	if checks["type_message"] {
		score += 30
	}

	// Check 2 (25 pts): role == "assistant"
	checks["role_assistant"] = resp["role"] == "assistant"
	if checks["role_assistant"] {
		score += 25
	}

	// Check 3 (25 pts): content is non-empty array with a text block
	checks["has_text_content"] = false
	if content, ok := resp["content"].([]any); ok && len(content) > 0 {
		for _, block := range content {
			if b, ok := block.(map[string]any); ok && b["type"] == "text" {
				if text, ok := b["text"].(string); ok && strings.TrimSpace(text) != "" {
					checks["has_text_content"] = true
					break
				}
			}
		}
	}
	if checks["has_text_content"] {
		score += 25
	}

	// Check 4 (20 pts): model field contains "claude"
	if model, ok := resp["model"].(string); ok {
		checks["model_is_claude"] = strings.Contains(strings.ToLower(model), "claude")
	}
	if checks["model_is_claude"] {
		score += 20
	}

	return score, checks
}

// --- OpenAI-compat probe (OpenAI, Gemini, Grok) ----------------------------

type openaiRequest struct {
	Model               string          `json:"model"`
	MaxCompletionTokens int             `json:"max_completion_tokens"`
	Temperature         float64         `json:"temperature"`
	Messages            []openaiMessage `json:"messages"`
}

type openaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (v *AccountVerifier) probeOpenAICompat(ctx context.Context, protocol, apiKey, baseURL string) VerificationResult {
	defaultURL := map[string]string{
		"openai": "https://api.openai.com",
		"gemini": "https://generativelanguage.googleapis.com",
	}[protocol]
	if defaultURL == "" {
		defaultURL = "https://api.openai.com"
	}
	base := normalizeBaseURL(baseURL, defaultURL)

	// Pick a cheap model for the probe.
	model := "gpt-4o-mini"
	if protocol == "gemini" {
		model = "gemini-2.0-flash"
	}

	reqBody := openaiRequest{
		Model:               model,
		MaxCompletionTokens: 16,
		Temperature:         0,
		Messages:            []openaiMessage{{Role: "user", Content: "Reply with only the single word: pong"}},
	}
	body, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return errorResult(protocol, fmt.Sprintf("build request: %v", err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return errorResult(protocol, fmt.Sprintf("http: %v", err))
	}
	defer resp.Body.Close()
	rawBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return VerificationResult{
			Success: false, Verdict: "failed", Score: 0, Protocol: protocol,
			Message: fmt.Sprintf("authentication rejected (HTTP %d)", resp.StatusCode),
			Details: map[string]any{"http_status": resp.StatusCode, "body_excerpt": excerpt(rawBody, 200)},
		}
	}
	if resp.StatusCode != http.StatusOK {
		return VerificationResult{
			Success: false, Verdict: "error", Score: 0, Protocol: protocol,
			Message: fmt.Sprintf("unexpected HTTP %d", resp.StatusCode),
			Details: map[string]any{"http_status": resp.StatusCode, "body_excerpt": excerpt(rawBody, 200)},
		}
	}

	var parsed map[string]any
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		return errorResult(protocol, "response not valid JSON")
	}

	score, checks := scoreOpenAIResponse(parsed)
	verdict := scoreToVerdict(score)

	return VerificationResult{
		Success:  verdict == "passed" || verdict == "marginal",
		Verdict:  verdict,
		Score:    score,
		Protocol: protocol,
		Message:  fmt.Sprintf("score %d/100 (%s)", score, verdict),
		Details: map[string]any{
			"http_status": resp.StatusCode,
			"checks":      checks,
			"model":       parsed["model"],
		},
	}
}

// scoreOpenAIResponse applies veridrop-inspired checks to an OpenAI-compat response.
func scoreOpenAIResponse(resp map[string]any) (int, map[string]bool) {
	checks := make(map[string]bool)
	score := 0

	// Check 1 (30 pts): object == "chat.completion"
	checks["object_chat_completion"] = resp["object"] == "chat.completion"
	if checks["object_chat_completion"] {
		score += 30
	}

	// Check 2 (30 pts): choices[0].message.content contains "pong"
	checks["content_pong"] = false
	if choices, ok := resp["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if msg, ok := choice["message"].(map[string]any); ok {
				if content, ok := msg["content"].(string); ok {
					checks["content_pong"] = strings.Contains(strings.ToLower(content), "pong")
				}
			}
		}
	}
	if checks["content_pong"] {
		score += 30
	}

	// Check 3 (20 pts): finish_reason == "stop"
	checks["finish_reason_stop"] = false
	if choices, ok := resp["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			checks["finish_reason_stop"] = choice["finish_reason"] == "stop"
		}
	}
	if checks["finish_reason_stop"] {
		score += 20
	}

	// Check 4 (20 pts): usage present
	checks["has_usage"] = false
	if usage, ok := resp["usage"].(map[string]any); ok && usage != nil {
		checks["has_usage"] = true
	}
	if checks["has_usage"] {
		score += 20
	}

	return score, checks
}

// --- helpers ----------------------------------------------------------------

func scoreToVerdict(score int) string {
	switch {
	case score >= verdictPassedThreshold:
		return "passed"
	case score >= verdictMarginalThreshold:
		return "marginal"
	default:
		return "failed"
	}
}

func errorResult(protocol, msg string) VerificationResult {
	return VerificationResult{
		Success:  false,
		Verdict:  "error",
		Score:    0,
		Protocol: protocol,
		Message:  msg,
	}
}

func normalizeBaseURL(userURL, fallback string) string {
	u := strings.TrimRight(strings.TrimSpace(userURL), "/")
	if u == "" {
		return strings.TrimRight(fallback, "/")
	}
	// Strip trailing /v1 so we can always append /v1/... ourselves.
	if strings.HasSuffix(u, "/v1") {
		u = u[:len(u)-3]
	}
	return strings.TrimRight(u, "/")
}

func excerpt(b []byte, maxLen int) string {
	s := string(b)
	if len(s) > maxLen {
		return s[:maxLen] + "…"
	}
	return s
}
