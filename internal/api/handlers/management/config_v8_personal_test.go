package management

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestConfigV8PreservesPoolBindingsAndProviderIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := fmt.Sprintf(`port: 8317
api-keys: [client-old]
account-pools:
  enabled: true
  groups:
    - {id: default, name: Default}
    - {id: private, name: Private, credential-ids: [stable-key]}
  key-rules:
    - {key-hash: %s, scope: selected, group-ids: [private]}
openai-compatibility:
  - id: stable-provider
    name: Provider
    base-url: https://example.test/v1
    api-key-entries:
      - {id: stable-key, api-key: provider-key, note: private-note}
`, config.ClientKeyFingerprint("client-old"))
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	h := &Handler{cfg: cfg, configFilePath: path, authManager: manager}
	router := gin.New()
	router.PUT("/v8/management/config/*path", h.ConfigV8)
	request := func(path, body string) {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/v8/management/config/"+path, strings.NewReader(body)))
		if recorder.Code != http.StatusOK {
			t.Fatalf("PUT %s: %d %s", path, recorder.Code, recorder.Body.String())
		}
	}
	request("access/api-keys", `["client-new"]`)
	oldKey := sdkaccess.WithClientKeyIdentity(context.Background(), "client-old")
	newKey := sdkaccess.WithClientKeyIdentity(context.Background(), "client-new")
	if _, err := manager.AccountPoolScope(oldKey); err == nil {
		t.Fatal("rotated key still has account-pool access")
	}
	scope, err := manager.AccountPoolScope(newKey)
	if err != nil || scope == nil || !scope.Allows("stable-key") || scope.Allows("other-key") {
		t.Fatal("new key lost its account-pool restriction")
	}
	request("api-keys/openai-compatibility", `[{"name":"Provider","base-url":"https://example.test/v1","test-model":"chosen-model","keys":[{"api-key":"provider-key","note":"private-note"}]}]`)
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = config.ValidateV8Config(saved); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.ParseConfigBytes(saved)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.AccountPools.KeyRules) != 1 || loaded.AccountPools.KeyRules[0].KeyHash != config.ClientKeyFingerprint("client-new") || loaded.AccountPools.KeyRules[0].Scope != "selected" {
		t.Fatalf("rotated key permission lost: %+v", loaded.AccountPools.KeyRules)
	}
	provider := loaded.OpenAICompatibility[0]
	if provider.ID != "stable-provider" || provider.APIKeyEntries[0].ID != "stable-key" || provider.APIKeyEntries[0].Note != "private-note" || provider.TestModel != "chosen-model" {
		t.Fatalf("provider identity or settings changed: %+v", provider)
	}
	if !strings.Contains(string(saved), "id: stable-key") || !strings.Contains(string(saved), "id: stable-provider") {
		t.Fatal("identities were not persisted to the canonical YAML")
	}
}
