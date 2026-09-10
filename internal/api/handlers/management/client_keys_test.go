package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

func TestClientKeyRotationPreservesGroupRuleAndRevokesOldKey(t *testing.T) {
	h, m := poolManagementFixture(t)
	h.cfg.AccountPools = poolConfigForManagement(h.cfg)
	h.cfg.AccountPools.Enabled = true
	h.cfg.AccountPools.Groups = append(h.cfg.AccountPools.Groups, config.AccountPoolGroup{ID: "a", Name: "A", CredentialIDs: []string{"a"}})
	h.cfg.AccountPools.KeyRules = []config.AccountPoolKeyRule{{KeyHash: config.ClientKeyFingerprint("client-secret-a"), Name: "User A", Scope: "selected", GroupIDs: []string{"a"}}}
	m.SetConfig(h.cfg)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/api-keys", strings.NewReader(`{"index":0,"value":"new-client-key"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.PatchAPIKeys(c)
	if rec.Code != 200 {
		t.Fatalf("rotation failed: %s", rec.Body.String())
	}
	old := sdkaccess.WithClientKeyIdentity(context.Background(), "client-secret-a")
	current := sdkaccess.WithClientKeyIdentity(context.Background(), "new-client-key")
	if _, err := m.AccountPoolScope(old); err == nil {
		t.Fatal("old key still authorized")
	}
	if m.CheckAccountPoolAccess(current, "a") != nil || m.CheckAccountPoolAccess(current, "b") == nil {
		t.Fatal("rotated key lost its group restriction")
	}
	loaded, err := config.LoadConfig(h.configFilePath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AccountPools.KeyRules[0].KeyHash != config.ClientKeyFingerprint("new-client-key") || loaded.AccountPools.KeyRules[0].Name != "User A" {
		t.Fatal("rotation was not persisted")
	}
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/api-keys?index=0", nil)
	h.DeleteAPIKeys(c)
	if rec.Code != 200 {
		t.Fatal("delete failed")
	}
	if _, err := m.AccountPoolScope(current); err == nil {
		t.Fatal("deleted key remains authorized")
	}
}
func TestAmbiguousGroupedKeyReplacementIsRejected(t *testing.T) {
	rules := []config.AccountPoolKeyRule{{KeyHash: config.ClientKeyFingerprint("a"), Scope: "selected", GroupIDs: []string{"private"}}}
	if _, err := reconcilePoolKeyRules(rules, []string{"a", "b"}, []string{"c", "d"}); err == nil {
		t.Fatal("ambiguous rotation silently removed group policy")
	}
}
func TestClientKeyChangesAndGroupViewsAreRaceSafe(t *testing.T) {
	h, _ := poolManagementFixture(t)
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; i < 8; i++ {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPut, "/api-keys", strings.NewReader(`["client-secret-a","client-secret-b"]`))
			h.PutAPIKeys(c)
		}
	})
	wg.Go(func() {
		for i := 0; i < 20; i++ {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/account-pools", nil)
			h.GetAccountPools(c)
		}
	})
	wg.Wait()
}
