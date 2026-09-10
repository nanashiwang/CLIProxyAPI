package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestAccountPoolServerModelsAndUnsupportedRoutes(t *testing.T) {
	server := newTestServer(t)
	m := server.handlers.AuthManager
	cfg := server.cfg.CloneForRuntime()
	cfg.AccountPools = config.AccountPoolsConfig{Enabled: true, Groups: []config.AccountPoolGroup{{ID: "default", Name: "Default"}, {ID: "private", Name: "Private", CredentialIDs: []string{"pool-server-private"}}}}
	m.SetConfig(cfg)
	_, err := m.Register(context.Background(), &coreauth.Auth{ID: "pool-server-private", Provider: "codex", Status: coreauth.StatusActive})
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("pool-server-private", "codex", []*registry.ModelInfo{{ID: "gpt-private-model"}})
	defer reg.UnregisterClient("pool-server-private")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	server.engine.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("models request failed: %d %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, model := range body.Data {
		if model.ID == "gpt-private-model" {
			t.Fatal("private model leaked to default-group key")
		}
	}
	for _, path := range []string{"/v1/live", "/v1/realtime/client_secrets"} {
		rr = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Authorization", "Bearer test-key")
		server.engine.ServeHTTP(rr, req)
		if rr.Code != 503 {
			t.Fatalf("unsupported grouped path %s returned %d", path, rr.Code)
		}
	}
}
