package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestGetOpenCodeMasksCredentials(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{
		OpenCode: config.OpenCodeConfig{
			Enabled: true,
			Prefer:  "zen",
			Zen: config.OpenCodeTierConfig{
				BaseURL:       config.DefaultOpenCodeZenURL,
				APIKeyEntries: []config.OpenCodeAPIKey{{APIKey: "zen-secret-key", Note: "  张三账号  "}},
				Headers: map[string]string{
					"Authorization": "Bearer header-secret",
					"X-Region":      "cn",
				},
			},
			Go: config.OpenCodeTierConfig{BaseURL: config.DefaultOpenCodeGoURL},
		},
	}, nil)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/opencode", nil)
	h.GetOpenCode(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var body struct {
		OpenCode config.OpenCodeConfig `json:"opencode"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	entry := body.OpenCode.Zen.APIKeyEntries[0]
	if entry.APIKey != "" || !entry.APIKeyConfigured || entry.APIKeyPreview == "" || entry.SourceIndex == nil {
		t.Fatalf("unexpected masked OpenCode response: %#v", entry)
	}
	if entry.Note != "张三账号" {
		t.Fatalf("unexpected OpenCode note: %q", entry.Note)
	}
	if strings.Contains(rec.Body.String(), "zen-secret-key") || strings.Contains(rec.Body.String(), "header-secret") {
		t.Fatalf("response leaked OpenCode credential: %s", rec.Body.String())
	}
	if got := body.OpenCode.Zen.Headers["Authorization"]; got != "Bearer head...cret" {
		t.Fatalf("unexpected masked authorization header: %q", got)
	}
	if got := body.OpenCode.Zen.Headers["X-Region"]; got != "cn" {
		t.Fatalf("unexpected non-sensitive header: %q", got)
	}
}

func TestGetConfigMasksOpenCodeCredentials(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{OpenCode: config.OpenCodeConfig{
		Zen: config.OpenCodeTierConfig{
			BaseURL:       config.DefaultOpenCodeZenURL,
			APIKeyEntries: []config.OpenCodeAPIKey{{APIKey: "config-secret-key"}},
			Headers:       map[string]string{"X-Api-Key": "config-header-secret"},
		},
		Go: config.OpenCodeTierConfig{BaseURL: config.DefaultOpenCodeGoURL},
	}}, nil)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/config", nil)
	h.GetConfig(ctx)

	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "config-secret-key") || strings.Contains(rec.Body.String(), "config-header-secret") {
		t.Fatalf("config response leaked OpenCode credential: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPutOpenCodeRetainsMaskedCredentialsBySourceIndex(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("create config: %v", err)
	}
	h := NewHandler(&config.Config{OpenCode: config.OpenCodeConfig{
		Enabled: true,
		Prefer:  "zen",
		Zen: config.OpenCodeTierConfig{
			BaseURL: config.DefaultOpenCodeZenURL,
			APIKeyEntries: []config.OpenCodeAPIKey{
				{APIKey: "first-key"},
				{APIKey: "second-key"},
			},
		},
		Go: config.OpenCodeTierConfig{BaseURL: config.DefaultOpenCodeGoURL},
	}}, path, nil)
	payload := []byte(`{"opencode":{"enabled":true,"prefer":"zen","zen":{"base-url":"https://opencode.ai/zen","api-key-entries":[{"api-key":"","api-key-configured":true,"source-index":1,"note":"第二个账号"}]},"go":{"base-url":"https://opencode.ai/zen/go","api-key-entries":[]}}}`)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/opencode", bytes.NewReader(payload))
	h.PutOpenCode(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	entries := h.cfg.OpenCode.Zen.APIKeyEntries
	if len(entries) != 1 || entries[0].APIKey != "second-key" {
		t.Fatalf("masked credential was not retained by source index: %#v", entries)
	}
	if entries[0].Note != "第二个账号" {
		t.Fatalf("note was not updated while retaining masked credential: %q", entries[0].Note)
	}
}

func TestPutOpenCodeSanitizesAndPersists(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("create config: %v", err)
	}
	h := NewHandler(&config.Config{}, path, nil)
	payload := []byte(`{"opencode":{"enabled":true,"prefer":"ZEN","anonymous":false,"zen":{"base-url":"","api-key-entries":[{"api-key":" zen-key ","note":"  备用账号  ","weight":2}]},"go":{"base-url":""}}}`)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/opencode", bytes.NewReader(payload))
	h.PutOpenCode(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if !h.cfg.OpenCode.Enabled || h.cfg.OpenCode.Prefer != "zen" {
		t.Fatalf("unexpected normalized config: %#v", h.cfg.OpenCode)
	}
	if h.cfg.OpenCode.Zen.BaseURL != config.DefaultOpenCodeZenURL || h.cfg.OpenCode.Go.BaseURL != config.DefaultOpenCodeGoURL {
		t.Fatalf("unexpected default URLs: %#v", h.cfg.OpenCode)
	}
	if len(h.cfg.OpenCode.Zen.APIKeyEntries) != 1 || h.cfg.OpenCode.Zen.APIKeyEntries[0].APIKey != "zen-key" {
		t.Fatalf("unexpected keys: %#v", h.cfg.OpenCode.Zen.APIKeyEntries)
	}
	if h.cfg.OpenCode.Zen.APIKeyEntries[0].Note != "备用账号" {
		t.Fatalf("unexpected note: %q", h.cfg.OpenCode.Zen.APIKeyEntries[0].Note)
	}
}

func TestPutOpenCodeRejectsInvalidEndpoint(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	payload := []byte(`{"opencode":{"enabled":false,"zen":{"base-url":"https://example.com/zen"},"go":{"base-url":"https://opencode.ai/zen/go"}}}`)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/opencode", bytes.NewReader(payload))
	h.PutOpenCode(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestPutOpenCodeRequiresCredentialWhenEnabled(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	payload := []byte(`{"opencode":{"enabled":true,"zen":{"base-url":"https://opencode.ai/zen"},"go":{"base-url":"https://opencode.ai/zen/go"}}}`)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/opencode", bytes.NewReader(payload))
	h.PutOpenCode(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestPutOpenCodeRetainsMaskedSensitiveHeaders(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("create config: %v", err)
	}
	h := NewHandler(&config.Config{OpenCode: config.OpenCodeConfig{
		Enabled: true,
		Prefer:  "zen",
		Zen: config.OpenCodeTierConfig{
			BaseURL: config.DefaultOpenCodeZenURL,
			Headers: map[string]string{"Authorization": "Bearer header-secret"},
		},
		Go: config.OpenCodeTierConfig{BaseURL: config.DefaultOpenCodeGoURL},
	}}, path, nil)
	payload := []byte(`{"opencode":{"enabled":true,"prefer":"zen","zen":{"base-url":"https://opencode.ai/zen","headers":{"Authorization":"Bearer head...cret"},"api-key-entries":[{"api-key":"new-key"}]},"go":{"base-url":"https://opencode.ai/zen/go","api-key-entries":[]}}}`)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/opencode", bytes.NewReader(payload))
	h.PutOpenCode(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if got := h.cfg.OpenCode.Zen.Headers["Authorization"]; got != "Bearer header-secret" {
		t.Fatalf("masked sensitive header was not retained: %q", got)
	}
}

func TestPutOpenCodeRejectsStaleMaskedCredential(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{OpenCode: config.OpenCodeConfig{
		Enabled: true,
		Prefer:  "zen",
		Zen: config.OpenCodeTierConfig{
			BaseURL:       config.DefaultOpenCodeZenURL,
			APIKeyEntries: []config.OpenCodeAPIKey{{APIKey: "current-key"}},
		},
		Go: config.OpenCodeTierConfig{BaseURL: config.DefaultOpenCodeGoURL},
	}}, nil)
	payload := []byte(`{"opencode":{"enabled":true,"prefer":"zen","zen":{"base-url":"https://opencode.ai/zen","api-key-entries":[{"api-key":"","api-key-configured":true,"api-key-preview":"old...key","source-index":0}]},"go":{"base-url":"https://opencode.ai/zen/go","api-key-entries":[]}}}`)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/opencode", bytes.NewReader(payload))
	h.PutOpenCode(ctx)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusConflict, rec.Code, rec.Body.String())
	}
}

func TestPutOpenCodeRestoresConfigWhenPersistenceFails(t *testing.T) {
	h := NewHandler(&config.Config{OpenCode: config.OpenCodeConfig{
		Enabled: true,
		Prefer:  "go",
		Zen:     config.OpenCodeTierConfig{BaseURL: config.DefaultOpenCodeZenURL},
		Go:      config.OpenCodeTierConfig{BaseURL: config.DefaultOpenCodeGoURL, APIKeyEntries: []config.OpenCodeAPIKey{{APIKey: "old-key"}}},
	}}, t.TempDir()+"/missing/config.yaml", nil)
	payload := []byte(`{"opencode":{"enabled":true,"prefer":"zen","zen":{"base-url":"https://opencode.ai/zen","api-key-entries":[{"api-key":"new-key"}]},"go":{"base-url":"https://opencode.ai/zen/go"}}}`)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/opencode", bytes.NewReader(payload))
	h.PutOpenCode(ctx)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, rec.Code)
	}
	if h.cfg.OpenCode.Prefer != "go" || h.cfg.OpenCode.Go.APIKeyEntries[0].APIKey != "old-key" {
		t.Fatalf("config changed after failed persistence: %#v", h.cfg.OpenCode)
	}
}
