package config

import (
	"path/filepath"
	"testing"
)

func TestParseConfigBytesPricingDefaults(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte("host: 127.0.0.1\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if !cfg.Pricing.Enabled || cfg.Pricing.RefreshIntervalMinutes != 180 {
		t.Fatalf("Pricing = %+v, want enabled with 180 minute refresh", cfg.Pricing)
	}
}

func TestParseConfigBytesPricingOverride(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
pricing:
  enabled: false
  source-url: "https://pricing.example/catalog.json"
  refresh-interval-minutes: 30
  cache-file: "cache/pricing.json"
  overrides:
    gpt-custom:
      provider: openai
      input: 1.25
      output: 4.5
      cache-read: 0.125
      cache-write: 1.5625
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.Pricing.Enabled || cfg.Pricing.SourceURL != "https://pricing.example/catalog.json" ||
		cfg.Pricing.RefreshIntervalMinutes != 30 || cfg.Pricing.CacheFile != "cache/pricing.json" {
		t.Fatalf("Pricing = %+v", cfg.Pricing)
	}
	override, exists := cfg.Pricing.Overrides["gpt-custom"]
	if !exists || override.Provider != "openai" || override.Input == nil || override.Output == nil ||
		override.CacheRead == nil || override.CacheWrite == nil {
		t.Fatalf("override = %+v", override)
	}
	if *override.Input != 1.25 || *override.Output != 4.5 || *override.CacheRead != 0.125 || *override.CacheWrite != 1.5625 {
		t.Fatalf("override values = %+v", override)
	}
}

func TestLoadConfigOptionalMissingPricingDefaults(t *testing.T) {
	cfg, errLoad := LoadConfigOptional(filepath.Join(t.TempDir(), "missing.yaml"), true)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional() error = %v", errLoad)
	}
	if !cfg.Pricing.Enabled || cfg.Pricing.RefreshIntervalMinutes != 180 {
		t.Fatalf("Pricing = %+v", cfg.Pricing)
	}
}
