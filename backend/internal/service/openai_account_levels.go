package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	AccountLevelUnknown = "unknown"
	AccountLevelFree    = "free"
	AccountLevelK12     = "k12"
	AccountLevelPlus    = "plus"
	AccountLevelPro     = "pro"
	AccountLevelTeam    = "team"
)

type OpenAIAccountLevelConfig struct {
	Key       string   `json:"key"`
	Label     string   `json:"label"`
	Aliases   []string `json:"aliases"`
	SortOrder int      `json:"sort_order"`
	Enabled   bool     `json:"enabled"`
}

func DefaultOpenAIAccountLevelConfigs() []OpenAIAccountLevelConfig {
	return []OpenAIAccountLevelConfig{
		{Key: AccountLevelFree, Label: "Free", Aliases: []string{"free", "chatgptfree"}, SortOrder: 10, Enabled: true},
		{Key: AccountLevelPlus, Label: "Plus", Aliases: []string{"plus", "plus*", "chatgptplus"}, SortOrder: 20, Enabled: true},
		{Key: AccountLevelPro, Label: "Pro", Aliases: []string{"pro", "pro*", "chatgptpro", "chatgptpro*"}, SortOrder: 30, Enabled: true},
		{Key: AccountLevelTeam, Label: "Team", Aliases: []string{"team", "team*", "chatgptteam"}, SortOrder: 40, Enabled: true},
		{Key: AccountLevelK12, Label: "K12", Aliases: []string{"k12", "chatgptk12", "chatgpt-k12"}, SortOrder: 50, Enabled: true},
	}
}
func normalizeOpenAILevelKey(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.NewReplacer(" ", "-", "_", "-").Replace(v)
	var b strings.Builder
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "-")
}
func normalizeOpenAILevelAlias(v string) string {
	wildcard := strings.HasSuffix(strings.TrimSpace(v), "*")
	v = strings.TrimSuffix(strings.TrimSpace(v), "*")
	v = strings.ToLower(v)
	v = strings.NewReplacer(" ", "", "_", "", "-", "").Replace(v)
	if v == "" {
		return ""
	}
	if wildcard {
		return v + "*"
	}
	return v
}
func NormalizeOpenAIAccountLevelConfigs(configs []OpenAIAccountLevelConfig) []OpenAIAccountLevelConfig {
	if len(configs) == 0 {
		configs = DefaultOpenAIAccountLevelConfigs()
	}
	out := make([]OpenAIAccountLevelConfig, 0, len(configs))
	seen := map[string]bool{}
	for i, cfg := range configs {
		key := normalizeOpenAILevelKey(cfg.Key)
		if key == "" || key == AccountLevelUnknown || seen[key] {
			continue
		}
		seen[key] = true
		label := strings.TrimSpace(cfg.Label)
		if label == "" {
			label = key
		}
		aliases := []string{key}
		used := map[string]bool{key: true}
		for _, raw := range cfg.Aliases {
			a := normalizeOpenAILevelAlias(raw)
			if a != "" && !used[a] {
				aliases = append(aliases, a)
				used[a] = true
			}
		}
		order := cfg.SortOrder
		if order == 0 {
			order = (i + 1) * 10
		}
		out = append(out, OpenAIAccountLevelConfig{Key: key, Label: label, Aliases: aliases, SortOrder: order, Enabled: cfg.Enabled})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SortOrder == out[j].SortOrder {
			return out[i].Key < out[j].Key
		}
		return out[i].SortOrder < out[j].SortOrder
	})
	return out
}
func ValidateOpenAIAccountLevelConfigs(configs []OpenAIAccountLevelConfig) ([]OpenAIAccountLevelConfig, error) {
	if len(configs) == 0 {
		return nil, fmt.Errorf("openai_account_levels cannot be empty")
	}
	seen := map[string]string{}
	for _, cfg := range configs {
		key := normalizeOpenAILevelKey(cfg.Key)
		if key == "" || key == AccountLevelUnknown {
			return nil, fmt.Errorf("invalid OpenAI account level key")
		}
		for _, raw := range append([]string{key}, cfg.Aliases...) {
			a := normalizeOpenAILevelAlias(raw)
			if a == "" {
				return nil, fmt.Errorf("invalid alias for %s", key)
			}
			if owner, ok := seen[a]; ok && owner != key {
				return nil, fmt.Errorf("openai_account_levels alias %q is used by both %q and %q", a, owner, key)
			}
			seen[a] = key
		}
	}
	normalized := NormalizeOpenAIAccountLevelConfigs(configs)
	enabled := false
	for _, cfg := range normalized {
		enabled = enabled || cfg.Enabled
	}
	if len(normalized) == 0 || !enabled {
		return nil, fmt.Errorf("openai_account_levels must contain at least one enabled level")
	}
	return normalized, nil
}
func InferOpenAIAccountLevelWithConfigs(credentials, extra map[string]any, configs []OpenAIAccountLevelConfig) string {
	for _, values := range []map[string]any{credentials, extra} {
		for _, key := range []string{"plan_type", "chatgpt_plan_type", "subscription_plan"} {
			if raw, ok := values[key].(string); ok {
				token := normalizeOpenAILevelAlias(raw)
				for _, cfg := range NormalizeOpenAIAccountLevelConfigs(configs) {
					if cfg.Enabled {
						for _, alias := range cfg.Aliases {
							if strings.HasSuffix(alias, "*") && strings.HasPrefix(token, strings.TrimSuffix(alias, "*")) {
								return cfg.Key
							}
							if token == alias {
								return cfg.Key
							}
						}
					}
				}
			}
		}
	}
	return AccountLevelUnknown
}
func InferOpenAIAccountLevel(credentials, extra map[string]any) string {
	return InferOpenAIAccountLevelWithConfigs(credentials, extra, DefaultOpenAIAccountLevelConfigs())
}
func NormalizeOpenAIAccountLevelWithConfigs(platform, current string, credentials, extra map[string]any, configs []OpenAIAccountLevelConfig) string {
	if platform != PlatformOpenAI {
		return normalizeOpenAILevelKey(current)
	}
	if inferred := InferOpenAIAccountLevelWithConfigs(credentials, extra, configs); inferred != AccountLevelUnknown {
		return inferred
	}
	current = normalizeOpenAILevelKey(current)
	if current == "" {
		return AccountLevelUnknown
	}
	return current
}
func NormalizeOpenAIAccountLevel(platform, current string, credentials, extra map[string]any) string {
	return NormalizeOpenAIAccountLevelWithConfigs(platform, current, credentials, extra, DefaultOpenAIAccountLevelConfigs())
}

// OpenAIAccountLevelConfigs returns the persisted level definitions with a
// safe default for older installations that have not stored the setting yet.
func (s *SettingService) OpenAIAccountLevelConfigs(ctx context.Context) []OpenAIAccountLevelConfig {
	if s == nil || s.settingRepo == nil {
		return DefaultOpenAIAccountLevelConfigs()
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAIAccountLevels)
	if err != nil || strings.TrimSpace(raw) == "" {
		return DefaultOpenAIAccountLevelConfigs()
	}
	var configs []OpenAIAccountLevelConfig
	if json.Unmarshal([]byte(raw), &configs) != nil {
		return DefaultOpenAIAccountLevelConfigs()
	}
	return NormalizeOpenAIAccountLevelConfigs(configs)
}

func (s *adminServiceImpl) normalizeOpenAIAccountLevel(ctx context.Context, account *Account) string {
	if account == nil {
		return AccountLevelUnknown
	}
	configs := DefaultOpenAIAccountLevelConfigs()
	if s != nil && s.settingService != nil {
		configs = s.settingService.OpenAIAccountLevelConfigs(ctx)
	}
	return NormalizeOpenAIAccountLevelWithConfigs(account.Platform, account.AccountLevel, account.Credentials, account.Extra, configs)
}
