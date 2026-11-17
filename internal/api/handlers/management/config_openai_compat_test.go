package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestGetOpenAICompatIncludesDisableCooling(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	requestRetry := 0
	disableCooling := true
	h := NewHandlerWithoutConfigFilePath(&config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "Mimo CN",
				BaseURL: "https://token-plan-cn.xiaomimimo.com/v1",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "test-key"},
				},
				Models: []config.OpenAICompatibilityModel{
					{Name: "mimo-v2.5", Alias: ""},
				},
				SupportPromptCacheKey: true,
				DisableCooling:        &disableCooling,
				RequestRetry:          &requestRetry,
			},
		},
	}, nil)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/openai-compatibility", nil)
	h.GetOpenAICompat(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var body struct {
		OpenAICompatibility []struct {
			SupportPromptCacheKey *bool `json:"support-prompt-cache-key"`
			DisableCooling        *bool `json:"disable-cooling"`
			RequestRetry          *int  `json:"request-retry"`
		} `json:"openai-compatibility"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(body.OpenAICompatibility) != 1 {
		t.Fatalf("expected 1 openai-compatibility entry, got %d", len(body.OpenAICompatibility))
	}
	if body.OpenAICompatibility[0].SupportPromptCacheKey == nil || !*body.OpenAICompatibility[0].SupportPromptCacheKey {
		t.Fatalf("expected support-prompt-cache-key to be present and true, got %#v", body.OpenAICompatibility[0].SupportPromptCacheKey)
	}
	if body.OpenAICompatibility[0].DisableCooling == nil || !*body.OpenAICompatibility[0].DisableCooling {
		t.Fatalf("expected disable-cooling to be present and true, got %#v", body.OpenAICompatibility[0].DisableCooling)
	}
	if body.OpenAICompatibility[0].RequestRetry == nil || *body.OpenAICompatibility[0].RequestRetry != 0 {
		t.Fatalf("expected request-retry to be present and 0, got %#v", body.OpenAICompatibility[0].RequestRetry)
	}
}

func TestOpenAICompatTestModelPersistsAcrossReload(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(&config.Config{}, path, nil)
	call := func(method, body string, handler func(*gin.Context)) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(method, "/v0/management/openai-compatibility", strings.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		handler(ctx)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", method, rec.Code, rec.Body.String())
		}
		return rec
	}
	check := func(want string) {
		t.Helper()
		loaded, err := config.LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		h = NewHandler(loaded, path, nil)
		rec := call(http.MethodGet, "", h.GetOpenAICompat)
		var body struct {
			Items []config.OpenAICompatibility `json:"openai-compatibility"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Items) != 1 || body.Items[0].TestModel != want {
			t.Fatalf("reloaded preference: %s; want %q", rec.Body.String(), want)
		}
		if body.Items[0].Models[0].Name != "gpt-4.1-mini" {
			t.Fatal("test preference reordered routing models")
		}
	}
	call(http.MethodPut, `[{"name":"official","base-url":"https://api.openai.com/v1","test-model":" gpt-6-luna ","models":[{"name":"gpt-4.1-mini"},{"name":"gpt-6-luna"}]}]`, h.PutOpenAICompat)
	check("gpt-6-luna")
	call(http.MethodPatch, `{"name":"official","value":{"disabled":true}}`, h.PatchOpenAICompat)
	check("gpt-6-luna")
	call(http.MethodPatch, `{"name":"official","value":{"test-model":"gpt-4.1-mini"}}`, h.PatchOpenAICompat)
	check("gpt-4.1-mini")
	call(http.MethodPatch, `{"name":"official","value":{"test-model":""}}`, h.PatchOpenAICompat)
	check("")
	call(http.MethodPut, `[{"name":"official","base-url":"https://api.openai.com/v1","models":[{"name":"gpt-4.1-mini"}]}]`, h.PutOpenAICompat)
	check("")
}
