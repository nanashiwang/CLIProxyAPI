package management

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestAuthFileCapacityDoesNotOverrideAvailability(t *testing.T) {
	m := coreauth.NewManager(nil, nil, nil)
	path := filepath.Join(t.TempDir(), "capacity.json")
	if err := os.WriteFile(path, []byte(`{"type":"codex"}`), 0600); err != nil {
		t.Fatal(err)
	}
	a := &coreauth.Auth{ID: "capacity", Provider: "codex", Status: coreauth.StatusDisabled, Disabled: true, Attributes: map[string]string{"path": path}}
	if _, err := m.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, m)
	entry := h.buildAuthFileEntry(a)
	capacity, ok := entry["execution_capacity"].(coreauth.ExecutionCapacity)
	if !ok || capacity.Active != 0 || capacity.Limit != nil || !capacity.Unlimited || capacity.Scope != "local" || time.Since(capacity.ObservedAt) > time.Second {
		t.Fatalf("invalid capacity: %#v", entry["execution_capacity"])
	}
	if entry["disabled"] != true {
		t.Fatal("idle account was enabled")
	}
	h.authManager = nil
	if _, ok := h.buildAuthFileEntry(a)["execution_capacity"]; ok {
		t.Fatal("missing observer reported known capacity")
	}
}

func TestAuthFileQuotaGuardDiagnostics(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	a := &coreauth.Auth{ID: "quota", Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"runtime_only": "true"}, QuotaGuard: &coreauth.QuotaGuardStatus{State: "query_failed", Failure: "http_error", HTTPStatus: 429}, ProviderQuota: &coreauth.CodexQuotaSnapshot{Windows: []coreauth.CodexQuotaWindow{{Name: "claude:five_hour", UsedPercent: 81, WindowSeconds: 18000}}}}
	entry := h.buildAuthFileEntry(a)
	if entry["quota_guard"] != a.QuotaGuard || entry["provider_quota"] != a.ProviderQuota {
		t.Fatalf("missing quota diagnostics: %#v", entry)
	}
	if _, ok := entry["codex_quota"]; ok {
		t.Fatal("Claude quota mislabeled as Codex")
	}
}
