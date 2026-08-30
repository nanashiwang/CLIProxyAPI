package config

import "testing"

func TestIsAllowedOpenCodeBaseURLForTier(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		tier string
		want bool
	}{
		{name: "zen", raw: DefaultOpenCodeZenURL, tier: "zen", want: true},
		{name: "go", raw: DefaultOpenCodeGoURL, tier: "go", want: true},
		{name: "zen cannot use go", raw: DefaultOpenCodeGoURL, tier: "zen"},
		{name: "go cannot use zen", raw: DefaultOpenCodeZenURL, tier: "go"},
		{name: "query", raw: DefaultOpenCodeZenURL + "?x=1", tier: "zen"},
		{name: "userinfo", raw: "https://user:pass@opencode.ai/zen", tier: "zen"},
		{name: "port", raw: "https://opencode.ai:443/zen", tier: "zen"},
		{name: "http", raw: "http://opencode.ai/zen", tier: "zen"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsAllowedOpenCodeBaseURLForTier(tt.raw, tt.tier); got != tt.want {
				t.Fatalf("IsAllowedOpenCodeBaseURLForTier(%q, %q) = %v, want %v", tt.raw, tt.tier, got, tt.want)
			}
		})
	}
}

func TestValidateOpenCodeRequiresCredentialOrAnonymous(t *testing.T) {
	cfg := &Config{OpenCode: OpenCodeConfig{
		Enabled: true,
		Prefer:  "zen",
		Zen:     OpenCodeTierConfig{BaseURL: DefaultOpenCodeZenURL},
		Go:      OpenCodeTierConfig{BaseURL: DefaultOpenCodeGoURL},
	}}
	if err := cfg.ValidateOpenCode(); err == nil {
		t.Fatal("ValidateOpenCode() accepted an enabled provider without credentials")
	}
	cfg.OpenCode.Anonymous = true
	if err := cfg.ValidateOpenCode(); err != nil {
		t.Fatalf("ValidateOpenCode() with anonymous = %v", err)
	}
}

func TestSanitizeOpenCodeNormalizesDuplicateOverridesAndHeaders(t *testing.T) {
	cfg := &Config{OpenCode: OpenCodeConfig{
		ProtocolOverrides: map[string]string{
			" GPT-5 ": "RESPONSES",
			"gpt-5":   "chat",
			"bad":     "xml",
		},
		Zen: OpenCodeTierConfig{Headers: map[string]string{
			"x-trace-id": "lower",
			"X-Trace-Id": "upper",
			"Bad Header": "drop",
			"X-Newline":  "bad\nvalue",
		}},
	}}
	cfg.SanitizeOpenCode()
	if got := cfg.OpenCode.ProtocolOverrides["gpt-5"]; got != "responses" {
		t.Fatalf("normalized protocol override = %q, want deterministic lexical winner %q", got, "responses")
	}
	if len(cfg.OpenCode.ProtocolOverrides) != 1 {
		t.Fatalf("protocol overrides = %#v, want one valid entry", cfg.OpenCode.ProtocolOverrides)
	}
	if got := cfg.OpenCode.Zen.Headers["X-Trace-Id"]; got != "upper" {
		t.Fatalf("normalized header = %q, want deterministic lexical winner %q", got, "upper")
	}
	if len(cfg.OpenCode.Zen.Headers) != 1 {
		t.Fatalf("normalized headers = %#v, want one valid entry", cfg.OpenCode.Zen.Headers)
	}
}
