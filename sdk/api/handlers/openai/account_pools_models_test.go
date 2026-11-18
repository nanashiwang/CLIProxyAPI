package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestOpenAIModelsPreservesAccountPoolFilterForCodexClients(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	ids := []string{uuid.NewString(), uuid.NewString()}
	models := []string{"pool-visible-model", "pool-hidden-model"}
	for i, id := range ids {
		registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: models[i]}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{SDKConfig: config.SDKConfig{
		APIKeys: []string{"models-client"},
		AccountPools: config.AccountPoolsConfig{
			Enabled:  true,
			Groups:   []config.AccountPoolGroup{{ID: "default", Name: "Default"}, {ID: "visible", Name: "Visible", CredentialIDs: ids[:1]}},
			KeyRules: []config.AccountPoolKeyRule{{KeyHash: config.ClientKeyFingerprint("models-client"), Scope: "selected", GroupIDs: []string{"visible"}}},
		},
	}}
	manager.SetConfig(cfg)
	h := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	for _, query := range []string{"", "?client_version=0.149.1"} {
		t.Run(query, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/models"+query, nil)
			c.Request = c.Request.WithContext(sdkaccess.WithClientKeyIdentity(c.Request.Context(), "models-client"))
			h.OpenAIModels(c)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d: %s", recorder.Code, recorder.Body.String())
			}
			path, idField := "data", "id"
			if query != "" {
				path, idField = "models", "slug"
			}
			entries := ojson.GetBytesNoCopy(recorder.Body.Bytes(), path).Array()
			if len(entries) != 1 || entries[0].Get(idField).String() != models[0] {
				t.Fatalf("unauthorized model exposed or allowed model lost: %s", recorder.Body.String())
			}
		})
	}
}
