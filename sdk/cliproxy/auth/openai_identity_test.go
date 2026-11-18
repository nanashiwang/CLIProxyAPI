package auth

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"testing"
)

func TestOpenAIIdentityOverridesStaleNameAndArrayIndex(t *testing.T) {
	yes := true
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{
		{ID: "another", Name: "old-name", BaseURL: "https://wrong.test", Models: []config.OpenAICompatibilityModel{{Name: "wrong-upstream", Alias: "alias"}}},
		{ID: "selected", Name: "renamed", BaseURL: "https://right.test", DisableCooling: &yes, Models: []config.OpenAICompatibilityModel{{Name: "right-upstream", Alias: "alias"}}},
	}}
	a := &Auth{ID: "key-id", Provider: "openai-compatible-selected", Attributes: map[string]string{"provider_id": "selected", "config_index": "0", "compat_name": "old-name", "source": "config:old-name[token]", "api_key": "test"}}
	if got := resolveUpstreamModelForOpenAICompatAPIKey(cfg, a, "alias"); got != "right-upstream" {
		t.Fatalf("wrong provider after reorder: %s", got)
	}
	if value, ok := providerCoolingOverrideForAuth(a, cfg); !ok || !value {
		t.Fatal("cooling resolved by stale name")
	}
	cfg.OpenAICompatibility = cfg.OpenAICompatibility[:1]
	if got := resolveOpenAICompatConfigForAuth(cfg, a, "", "old-name"); got != nil {
		t.Fatal("removed provider fell back to unrelated index/name")
	}
}
