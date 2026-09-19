package thinking

import "testing"

func TestUsageReasoningEffortObservesPayloadSettings(t *testing.T) {
	for _, tc := range []struct{ name, provider, body, want string }{
		{"openai level", "openai", `{"reasoning_effort":"high"}`, "high"},
		{"responses level", "openai-response", `{"reasoning":{"effort":"xhigh"}}`, "xhigh"},
		{"disabled", "claude", `{"thinking":{"type":"disabled"},"output_config":{"effort":"high"}}`, "none"},
		{"adaptive", "claude", `{"thinking":{"type":"adaptive"}}`, "auto"},
		{"adaptive level", "claude", `{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`, "max"},
		{"effort only", "claude", `{"output_config":{"effort":"high"}}`, "high"},
		{"kimi enabled", "kimi", `{"thinking":{"type":"enabled"},"reasoning_effort":"high"}`, "enabled"},
		{"kimi native level", "kimi", `{"thinking":{"type":"enabled","effort":"low"}}`, "low"},
		{"kimi disabled", "kimi", `{"thinking":{"type":"disabled"},"reasoning":{"effort":"high"}}`, "none"},
		{"kimi responses", "kimi", `{"reasoning":{"effort":"medium"}}`, "medium"},
		{"gemini level", "gemini", `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}}`, "high"},
		{"gemini zero budget", "gemini", `{"generationConfig":{"thinkingConfig":{"thinkingBudget":0}}}`, "none"},
		{"interactions", "interactions", `{"generation_config":{"thinking_level":"medium"}}`, "medium"},
		{"default", "claude", `{"model":"claude-haiku-4-5"}`, "default"},
		{"unspecified responses", "codex", `{"reasoning":{"summary":"auto"}}`, "default"},
		{"invalid effort", "claude", `{"thinking":{"type":"adaptive"},"output_config":{"effort":""}}`, ""},
		{"unknown native mode", "kimi", `{"thinking":{"type":"other"}}`, ""},
		{"unknown format", "plugin-custom", `{}`, ""},
		{"invalid json", "openai", `{`, ""},
		{"non-object", "openai", `[]`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractTranslatedReasoningEffort([]byte(tc.body), tc.provider); got != tc.want {
				t.Fatalf("effort = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUsageReasoningDoesNotChangeThinkingConversionSemantics(t *testing.T) {
	for _, tc := range []struct{ provider, body string }{
		{"claude", `{"thinking":{"type":"adaptive"}}`},
		{"claude", `{"output_config":{"effort":"high"}}`},
		{"kimi", `{"thinking":{"type":"enabled"}}`},
	} {
		body := []byte(tc.body)
		if config := extractThinkingConfig(body, tc.provider); hasThinkingConfig(config) {
			t.Fatalf("conversion config unexpectedly changed: %#v", config)
		}
		_ = ExtractTranslatedReasoningEffort(body, tc.provider)
		if string(body) != tc.body {
			t.Fatal("usage extraction mutated the payload")
		}
	}
	if got := ExtractReasoningEffort([]byte(`{"reasoning_effort":"low"}`), "openai", "model(high)"); got != "high" {
		t.Fatalf("client suffix priority changed: %s", got)
	}
}
