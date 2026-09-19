package thinking

import (
	"strings"

	"github.com/tidwall/gjson"
)

// usageReasoningEffort observes the final payload without changing the thinking
// conversion pipeline. "default" means no explicit setting, not a guessed level.
func usageReasoningEffort(body []byte, provider string) string {
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return ""
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	config := extractThinkingConfig(body, provider)
	if !hasThinkingConfig(config) {
		switch provider {
		case "openai", "openai-response", "codex", "xai":
			config = extractCodexConfig(body)
			if !hasThinkingConfig(config) {
				config = extractOpenAIConfig(body)
			}
		case "kimi":
			if !gjson.GetBytes(body, "thinking").Exists() && !gjson.GetBytes(body, "reasoning_effort").Exists() {
				config = extractCodexConfig(body)
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
		effort := gjson.GetBytes(body, "output_config.effort")
		if effort.Type == gjson.String && strings.TrimSpace(effort.String()) != "" {
			return strings.ToLower(strings.TrimSpace(effort.String()))
		}
		mode := gjson.GetBytes(body, "thinking.type").String()
		if (mode == "adaptive" || mode == "auto") && !effort.Exists() {
			return "auto"
		}
	case "kimi":
		paths = []string{"thinking", "reasoning", "reasoning_effort"}
		if gjson.GetBytes(body, "thinking.type").String() == "enabled" && !gjson.GetBytes(body, "thinking.effort").Exists() {
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
		if gjson.GetBytes(body, path).Exists() {
			return ""
		}
	}
	return "default"
}
