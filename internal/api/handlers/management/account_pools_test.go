package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func poolManagementFixture(t *testing.T) (*Handler, *coreauth.Manager) {
	t.Helper()
	cfg := &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{"client-secret-a", "client-secret-b"}}}
	m := coreauth.NewManager(nil, nil, nil)
	for _, id := range []string{"a", "b"} {
		if _, err := m.Register(context.Background(), &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive}); err != nil {
			t.Fatal(err)
		}
	}
	m.SetConfig(cfg)
	return &Handler{cfg: cfg, authManager: m, configFilePath: writeTestConfigFile(t)}, m
}
func getPoolView(t *testing.T, h *Handler) map[string]json.RawMessage {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/account-pools", nil)
	h.GetAccountPools(c)
	if bytes.Contains(rec.Body.Bytes(), []byte("client-secret-a")) {
		t.Fatal("management group view exposed plaintext key")
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func putPoolView(t *testing.T, h *Handler, cfg config.AccountPoolsConfig, revision string) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"config": cfg, "revision": revision})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/account-pools", bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	h.PutAccountPools(c)
	return rec
}
func TestAccountPoolManagementPersistenceImmediatePolicyAndConflict(t *testing.T) {
	h, m := poolManagementFixture(t)
	view := getPoolView(t, h)
	var pools config.AccountPoolsConfig
	var revision string
	_ = json.Unmarshal(view["config"], &pools)
	_ = json.Unmarshal(view["revision"], &revision)
	pools.Enabled = true
	pools.Groups = append(pools.Groups, config.AccountPoolGroup{ID: "private", Name: "My custom group", CredentialIDs: []string{"a"}})
	pools.KeyRules = []config.AccountPoolKeyRule{{KeyHash: config.ClientKeyFingerprint("client-secret-a"), Scope: "selected", GroupIDs: []string{"private"}}}
	rec := putPoolView(t, h, pools, revision)
	if rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	ctx := sdkaccess.WithClientKeyIdentity(context.Background(), "client-secret-a")
	if m.CheckAccountPoolAccess(ctx, "a") != nil || m.CheckAccountPoolAccess(ctx, "b") == nil {
		t.Fatal("save acknowledged before authorization took effect")
	}
	loaded, err := config.LoadConfig(h.configFilePath)
	if err != nil {
		t.Fatal(err)
	}
	restarted := coreauth.NewManager(nil, nil, nil)
	restarted.SetConfig(loaded)
	scope, err := restarted.AccountPoolScope(ctx)
	if err != nil || !scope.Allows("a") || scope.Allows("b") {
		t.Fatal("permissions changed after reload")
	}
	if rec = putPoolView(t, h, pools, revision); rec.Code != http.StatusConflict {
		t.Fatal("stale edit overwrote policy")
	}
}
func TestAccountPoolManagementRejectsDuplicateUnknownAndMissingReferences(t *testing.T) {
	for _, kind := range []string{"duplicate", "unknown", "missing-group"} {
		t.Run(kind, func(t *testing.T) {
			h, _ := poolManagementFixture(t)
			view := getPoolView(t, h)
			var pools config.AccountPoolsConfig
			var rev string
			_ = json.Unmarshal(view["config"], &pools)
			_ = json.Unmarshal(view["revision"], &rev)
			pools.Enabled = true
			pools.Groups = append(pools.Groups, config.AccountPoolGroup{ID: "g", Name: "G", CredentialIDs: []string{"a"}})
			switch kind {
			case "duplicate":
				pools.Groups = append(pools.Groups, config.AccountPoolGroup{ID: "h", Name: "H", CredentialIDs: []string{"a"}})
			case "unknown":
				pools.Groups[1].CredentialIDs = []string{"unknown"}
			case "missing-group":
				pools.KeyRules = []config.AccountPoolKeyRule{{KeyHash: config.ClientKeyFingerprint("client-secret-a"), Scope: "selected", GroupIDs: []string{"missing"}}}
			}
			if rec := putPoolView(t, h, pools, rev); rec.Code != 400 {
				t.Fatalf("invalid config accepted: %d %s", rec.Code, rec.Body.String())
			}
			if h.cfg.AccountPools.Enabled {
				t.Fatal("failed validation changed configuration")
			}
		})
	}
}
func TestAccountPoolManagementFailedSaveKeepsOldPolicy(t *testing.T) {
	h, m := poolManagementFixture(t)
	view := getPoolView(t, h)
	var pools config.AccountPoolsConfig
	var rev string
	_ = json.Unmarshal(view["config"], &pools)
	_ = json.Unmarshal(view["revision"], &rev)
	pools.Enabled = true
	h.configFilePath = t.TempDir()
	if rec := putPoolView(t, h, pools, rev); rec.Code != 500 {
		t.Fatalf("expected failed file save, got %d", rec.Code)
	}
	if h.cfg.AccountPools.Enabled || m.AccountPoolsEnabled() {
		t.Fatal("failed save activated restrictions")
	}
}
func TestAccountPoolManagementRemovesOrphanRulesFromEditableView(t *testing.T) {
	h, _ := poolManagementFixture(t)
	h.cfg.AccountPools = poolConfigForManagement(h.cfg)
	h.cfg.AccountPools.KeyRules = []config.AccountPoolKeyRule{{KeyHash: config.ClientKeyFingerprint("removed-key"), Scope: "all"}}
	view := getPoolView(t, h)
	var pools config.AccountPoolsConfig
	_ = json.Unmarshal(view["config"], &pools)
	if len(pools.KeyRules) != 0 {
		t.Fatal("deleted key rule prevents future editing")
	}
}
func TestAccountPoolConfigInvalidReferenceDoesNotLoad(t *testing.T) {
	path := writeTestConfigFile(t)
	raw := []byte("api-keys: [test]\naccount-pools:\n  enabled: true\n  groups: [{id: default, name: Default}]\n  key-rules: [{key-hash: '" + config.ClientKeyFingerprint("test") + "', scope: selected, group-ids: [missing]}]\n")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadConfig(path); err == nil {
		t.Fatal("invalid stored restrictions loaded successfully")
	}
}
