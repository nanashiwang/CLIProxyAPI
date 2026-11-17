package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenAIIdentityRecoveryOnlyForUniqueLegacyBinding(t *testing.T) {
	legacy, _ := NewStableIDGenerator().Next("openai-compatibility:old", "key", "https://example.test/v1", "")
	for _, mode := range []string{"unique", "duplicate-key", "multiple-bindings", "wrong-key", "already-identified"} {
		t.Run(mode, func(t *testing.T) {
			cfg := &Config{OpenAICompatibility: []OpenAICompatibility{{Name: "new", BaseURL: "https://example.test/v1", APIKeyEntries: []OpenAICompatibilityAPIKey{{APIKey: "key"}}}}, SDKConfig: SDKConfig{AccountPools: AccountPoolsConfig{Groups: []AccountPoolGroup{{ID: "private", CredentialIDs: []string{legacy}}}}}}
			switch mode {
			case "duplicate-key":
				cfg.OpenAICompatibility[0].APIKeyEntries = append(cfg.OpenAICompatibility[0].APIKeyEntries, OpenAICompatibilityAPIKey{APIKey: "key"})
			case "multiple-bindings":
				other, _ := NewStableIDGenerator().Next("openai-compatibility:other", "key", "https://example.test/v1", "")
				cfg.AccountPools.Groups[0].CredentialIDs = append(cfg.AccountPools.Groups[0].CredentialIDs, other)
			case "wrong-key":
				cfg.OpenAICompatibility[0].APIKeyEntries[0].APIKey = "different"
			case "already-identified":
				cfg.OpenAICompatibility[0].APIKeyEntries[0].ID = "fixed"
			}
			cfg.EnsureOpenAICompatibilityIDs()
			got := cfg.OpenAICompatibility[0].APIKeyEntries[0].ID
			if (got == legacy) != (mode == "unique") {
				t.Fatalf("unsafe recovery %s: %s", mode, got)
			}
			if cfg.EnsureOpenAICompatibilityIDs() {
				t.Fatal("identity normalization not idempotent")
			}
			if cfg.AccountPools.Groups[0].CredentialIDs[0] != legacy {
				t.Fatal("migration rewrote membership")
			}
		})
	}
}

func TestOpenAIIdentityPersistsBeforeDirectYAMLRename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("# retained comment\nopenai-compatibility:\n  - name: original\n    base-url: https://example.test/v1\n    api-key-entries:\n      - api-key: test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "# retained comment") || !strings.Contains(string(raw), "id:") {
		t.Fatal("migration not persisted with comments")
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(raw), "name: original", "name: renamed", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if first.OpenAICompatibility[0].ID != second.OpenAICompatibility[0].ID || first.OpenAICompatibility[0].APIKeyEntries[0].ID != second.OpenAICompatibility[0].APIKeyEntries[0].ID {
		t.Fatal("YAML rename changed IDs")
	}
}

func TestOpenAIIdentityKeylessAndDisabledDuplicates(t *testing.T) {
	cfg := &Config{OpenAICompatibility: []OpenAICompatibility{
		{Name: "same", BaseURL: "https://example.test", Disabled: true, APIKeyEntries: []OpenAICompatibilityAPIKey{{APIKey: "key"}}},
		{Name: "same", BaseURL: "https://example.test", APIKeyEntries: []OpenAICompatibilityAPIKey{{APIKey: "key"}}},
		{Name: "same", BaseURL: "https://example.test"},
		{Name: "same", BaseURL: "https://example.test"},
	}}
	cfg.EnsureOpenAICompatibilityIDs()
	if err := cfg.ValidateOpenAICompatibilityIDs(); err != nil {
		t.Fatal(err)
	}
	expected, _ := NewStableIDGenerator().Next("openai-compatibility:same", "key", "https://example.test", "")
	if cfg.OpenAICompatibility[1].APIKeyEntries[0].ID != expected {
		t.Fatal("disabled entries shifted active legacy identity")
	}
	if cfg.EnsureOpenAICompatibilityIDs() {
		t.Fatal("keyless identity is not stable")
	}
}
