package service

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestInferOpenAIAccountLevel(t *testing.T) {
	require.Equal(t, AccountLevelPlus, InferOpenAIAccountLevel(map[string]any{"plan_type": " ChatGPT_Plus "}, nil))
	require.Equal(t, AccountLevelK12, InferOpenAIAccountLevel(nil, map[string]any{"subscription_plan": "chatgpt-k12"}))
	require.Equal(t, AccountLevelPro, InferOpenAIAccountLevel(map[string]any{"chatgpt_plan_type": "pro-2026"}, nil))
	require.Equal(t, AccountLevelUnknown, InferOpenAIAccountLevel(map[string]any{"plan_type": "enterprise"}, nil))
}

func TestValidateOpenAIAccountLevelConfigs(t *testing.T) {
	_, err := ValidateOpenAIAccountLevelConfigs([]OpenAIAccountLevelConfig{{Key: "student", Aliases: []string{"edu"}, Enabled: true}, {Key: "other", Aliases: []string{"edu"}, Enabled: true}})
	require.Error(t, err)
	normalized, err := ValidateOpenAIAccountLevelConfigs([]OpenAIAccountLevelConfig{{Key: "student", Label: "Student", Aliases: []string{"edu*"}, Enabled: true}})
	require.NoError(t, err)
	require.Equal(t, "student", InferOpenAIAccountLevelWithConfigs(map[string]any{"plan_type": "education-v2"}, nil, normalized))
}

func TestGroupAllowsOpenAIAccountLevel(t *testing.T) {
	group := &Group{Platform: PlatformOpenAI, AllowedOpenAIAccountLevels: []string{AccountLevelPlus, AccountLevelPro}}
	require.True(t, group.AllowsOpenAIAccountLevel("PLUS"))
	require.False(t, group.AllowsOpenAIAccountLevel(AccountLevelFree))
	group.AllowedOpenAIAccountLevels = nil
	require.True(t, group.AllowsOpenAIAccountLevel(AccountLevelFree))
	group.Platform = PlatformAnthropic
	group.AllowedOpenAIAccountLevels = []string{AccountLevelPro}
	require.True(t, group.AllowsOpenAIAccountLevel(AccountLevelFree))
}
