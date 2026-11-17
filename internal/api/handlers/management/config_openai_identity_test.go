package management

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"gopkg.in/yaml.v3"
)

func TestOpenAIRenameNotesPreservePoolIdentityAcrossRestart(t *testing.T) {
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: "old", BaseURL: "https://example.test/v1", APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "first"}, {APIKey: "second"}}, Models: []config.OpenAICompatibilityModel{{Name: "test-model"}}}}}
	synth := func(cfg *config.Config) []*coreauth.Auth {
		t.Helper()
		a, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	cfg.CredentialInFlight = config.DefaultCredentialInFlightConfig()
	cfg.UsageStatistics = config.DefaultUsageStatisticsConfig()
	cfg.Pricing = config.DefaultPricingConfig()
	legacy := synth(cfg)
	cfg.AccountPools = config.AccountPoolsConfig{Enabled: true, Groups: []config.AccountPoolGroup{{ID: "default", Name: "Default"}, {ID: "private", Name: "Private", CredentialIDs: []string{legacy[0].ID}}}}
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw, _ := yaml.Marshal(cfg)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	originalID := loaded.OpenAICompatibility[0].ID
	if originalID == "" {
		t.Fatal("provider ID missing")
	}
	for i, a := range synth(loaded) {
		if a.ID != legacy[i].ID || a.EnsureIndex() != legacy[i].EnsureIndex() {
			t.Fatal("migration changed credential identity")
		}
	}
	manager := coreauth.NewManager(nil, nil, nil)
	for _, a := range synth(loaded) {
		if _, err := manager.Register(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	h := &Handler{cfg: loaded, authManager: manager, configFilePath: path}
	call := func(method, body string, handle func(*gin.Context), want int) {
		t.Helper()
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(method, "/openai-compatibility", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		handle(c)
		if rec.Code != want {
			t.Fatalf("%d: %s", rec.Code, rec.Body.String())
		}
	}
	for _, name := range []string{"renamed", "renamed-again"} {
		body, _ := json.Marshal(map[string]any{"id": originalID, "index": 999, "value": map[string]any{"name": name}})
		call("PATCH", string(body), h.PatchOpenAICompat, 200)
		if h.cfg.OpenAICompatibility[0].ID != originalID {
			t.Fatal("provider ID changed")
		}
	}
	p := h.cfg.OpenAICompatibility[0]
	p.APIKeyEntries[0].Note = "Production"
	p.APIKeyEntries[1].Note = "Testing"
	p.APIKeyEntries[0], p.APIKeyEntries[1] = p.APIKeyEntries[1], p.APIKeyEntries[0]
	raw, _ = json.Marshal([]config.OpenAICompatibility{p})
	call("PUT", string(raw), h.PutOpenAICompat, 200)
	loaded, err = config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h.cfg = loaded
	for _, a := range synth(loaded) {
		if _, err := manager.Update(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	view := getPoolView(t, h)
	var rows []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Group    string `json:"group-id"`
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(view["credentials"], &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rename created duplicate/missing rows: %s", view["credentials"])
	}
	for _, row := range rows {
		if row.Provider == "missing" {
			t.Fatal("stale pool reference")
		}
		if row.ID == legacy[0].ID && (row.Group != "private" || !strings.Contains(row.Name, "Production") || !strings.Contains(row.Name, "renamed-again")) {
			t.Fatalf("wrong membership or label: %+v", row)
		}
	}
	if loaded.OpenAICompatibility[0].APIKeyEntries[0].ID != legacy[1].ID {
		t.Fatal("reorder reassigned identity")
	}
	p = loaded.OpenAICompatibility[0]
	p.APIKeyEntries = p.APIKeyEntries[1:]
	p.APIKeyEntries[0].Note = ""
	p.APIKeyEntries[0].APIKey = "rotated"
	raw, _ = json.Marshal([]config.OpenAICompatibility{p})
	call("PUT", string(raw), h.PutOpenAICompat, 200)
	reloaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	key := reloaded.OpenAICompatibility[0].APIKeyEntries[0]
	if key.ID != legacy[0].ID || key.Note != "" || key.APIKey != "rotated" {
		t.Fatalf("key edit lost identity: %+v", key)
	}
	p.APIKeyEntries = append(p.APIKeyEntries, p.APIKeyEntries[0])
	raw, _ = json.Marshal([]config.OpenAICompatibility{p})
	call("PUT", string(raw), h.PutOpenAICompat, 400)
	call("PATCH", `{"id":"missing","index":0,"value":{"name":"wrong"}}`, h.PatchOpenAICompat, 404)
	if h.cfg.OpenAICompatibility[0].Name != "renamed-again" {
		t.Fatal("missing ID fell back to index")
	}
}
