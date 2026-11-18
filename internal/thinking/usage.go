package thinking

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// usageReasoningEffort observes the final payload without changing the thinking
// conversion pipeline. "default" means no explicit setting, not a guessed level.
func usageReasoningEffort(body []byte, provider string) string {
	if !ojson.ValidBytes(body) || !ojson.ParseBytes(body).IsObject() {
		return ""
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	config := extractThinkingConfigForUsage(body, provider)
	if !hasThinkingConfig(config) {
		switch provider {
		case "openai", "openai-response", "codex", "xai":
			config = extractCodexUsageConfig(body)
			if !hasThinkingConfig(config) {
				config = extractOpenAIConfig(body)
			}
		case "kimi":
			if !ojson.GetBytes(body, "thinking").Exists() && !ojson.GetBytes(body, "reasoning_effort").Exists() {
				config = extractCodexUsageConfig(body)
			}
		}
	}
	if effort := reasoningEffortFromConfig(config); effort != "" {
		return effort
	}

	var paths []string
	switch provider {
	case "claude":
		paths = []string{"thinking", "output_config.effort"}
		effort := ojson.GetBytes(body, "output_config.effort")
		if effort.Type == ojson.String && strings.TrimSpace(effort.String()) != "" {
			return strings.ToLower(strings.TrimSpace(effort.String()))
		}
		mode := ojson.GetBytes(body, "thinking.type").String()
		if (mode == "adaptive" || mode == "auto") && !effort.Exists() {
			return "auto"
		}
	case "kimi":
		paths = []string{"thinking", "reasoning", "reasoning_effort"}
		if ojson.GetBytes(body, "thinking.type").String() == "enabled" && !ojson.GetBytes(body, "thinking.effort").Exists() {
			return "enabled"
		}
	case "openai", "openai-response", "codex", "xai":
		paths = []string{"reasoning.effort", "reasoning_effort"}
	case "gemini":
		paths = []string{"generationConfig.thinkingConfig", "generation_config.thinking_config"}
	case "antigravity":
		paths = []string{"request.generationConfig.thinkingConfig"}
	case "interactions":
		paths = []string{"generation_config.thinking_level", "generation_config.thinkingLevel", "generation_config.thinking_budget", "generation_config.thinkingBudget", "generation_config.thinking_config", "generation_config.thinkingConfig"}
	default:
		return ""
	}
	for _, path := range paths {
		if ojson.GetBytes(body, path).Exists() {
			return ""
		}
	}
	return "default"
}
