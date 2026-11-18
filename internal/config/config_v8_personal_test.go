package config

import (
	"strings"
	"testing"
)

func TestV8MigrationPreservesPersonalCapabilities(t *testing.T) {
	legacy := []byte(`port: 8317
api-keys: [client-key]
pricing:
  enabled: true
  refresh-interval-minutes: 180
usage-statistics:
  retention-days: 45
  max-records: 1234
opencode:
  enabled: true
  prefer: go
  go:
    api-key-entries:
      - api-key: opencode-test-key
quota-guard:
  enabled: true
  five-hour-used-percent: 89
account-pools:
  enabled: true
  groups:
    - id: private
      name: Private
      lease: true
      credential-ids: [stable-key]
openai-compatibility:
  - id: stable-provider
    name: Provider
    base-url: https://example.test/v1
    test-model: test-model
    api-key-entries:
      - id: stable-key
        note: key-note
        api-key: upstream-key
`)
	migrated, _, err := NormalizeConfigLayout(legacy, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateV8Config(migrated); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(migrated), "unrecognized") {
		t.Fatalf("personal configuration archived: %s", migrated)
	}
	cfg, err := ParseConfigBytes(migrated)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Pricing.Enabled || cfg.UsageStatistics.RetentionDays != 45 || !cfg.OpenCode.Enabled || cfg.QuotaGuard == nil || !cfg.AccountPools.Enabled || !cfg.AccountPools.Groups[0].Lease {
		t.Fatalf("personal configuration lost: %+v", cfg)
	}
	if len(cfg.OpenAICompatibility) != 1 {
		t.Fatal("provider missing")
	}
	p := cfg.OpenAICompatibility[0]
	if p.ID != "stable-provider" || p.TestModel != "test-model" || len(p.APIKeyEntries) != 1 || p.APIKeyEntries[0].ID != "stable-key" || p.APIKeyEntries[0].Note != "key-note" {
		t.Fatalf("provider identity lost: %+v", p)
	}
	if got := cfg.AccountPools.Groups[0].CredentialIDs[0]; got != p.APIKeyEntries[0].ID {
		t.Fatalf("pool identity changed: %s", got)
	}
	// Explicit migration always reports changed after writing config-version.
	second, _, err := NormalizeConfigLayout(migrated, true)
	if err != nil || string(second) != string(migrated) {
		t.Fatalf("migration not idempotent: err=%v", err)
	}
}
