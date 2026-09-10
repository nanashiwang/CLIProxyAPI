package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func sumRecentRequestBuckets(buckets []coreauth.RecentRequestBucket) (int64, int64) {
	var success int64
	var failed int64
	for _, bucket := range buckets {
		success += bucket.Success
		failed += bucket.Failed
	}
	return success, failed
}

func TestGetAPIKeyUsage_GroupsByProviderAndAPIKey(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "codex-key",
			"base_url": "https://codex.example.com",
		},
	}); err != nil {
		t.Fatalf("register codex auth: %v", err)
	}
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "claude-auth",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "claude-key",
			"base_url": "https://claude.example.com",
		},
	}); err != nil {
		t.Fatalf("register claude auth: %v", err)
	}

	manager.MarkResult(context.Background(), coreauth.Result{AuthID: "codex-auth", Provider: "codex", Model: "gpt-5", Success: true})
	manager.MarkResult(context.Background(), coreauth.Result{AuthID: "codex-auth", Provider: "codex", Model: "gpt-5", Success: false})
	manager.MarkResult(context.Background(), coreauth.Result{AuthID: "claude-auth", Provider: "claude", Model: "claude-4", Success: true})

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/v0/management/api-key-usage", nil)
	ginCtx.Request = req
	h.GetAPIKeyUsage(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload map[string]map[string]apiKeyUsageEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	codexEntry := payload["codex"]["https://codex.example.com|codex-key"]
	if codexEntry.Success != 1 || codexEntry.Failed != 1 {
		t.Fatalf("codex totals = %d/%d, want 1/1", codexEntry.Success, codexEntry.Failed)
	}
	if len(codexEntry.RecentRequests) != 20 {
		t.Fatalf("codex buckets len = %d, want 20", len(codexEntry.RecentRequests))
	}
	codexSuccess, codexFailed := sumRecentRequestBuckets(codexEntry.RecentRequests)
	if codexSuccess != 1 || codexFailed != 1 {
		t.Fatalf("codex totals = %d/%d, want 1/1", codexSuccess, codexFailed)
	}

	claudeEntry := payload["claude"]["https://claude.example.com|claude-key"]
	if claudeEntry.Success != 1 || claudeEntry.Failed != 0 {
		t.Fatalf("claude totals = %d/%d, want 1/0", claudeEntry.Success, claudeEntry.Failed)
	}
	if len(claudeEntry.RecentRequests) != 20 {
		t.Fatalf("claude buckets len = %d, want 20", len(claudeEntry.RecentRequests))
	}
	claudeSuccess, claudeFailed := sumRecentRequestBuckets(claudeEntry.RecentRequests)
	if claudeSuccess != 1 || claudeFailed != 0 {
		t.Fatalf("claude totals = %d/%d, want 1/0", claudeSuccess, claudeFailed)
	}
}

func TestGetAPIKeyUsage_GroupsOpenAICompatibleByCompatName(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "vast-auth",
		Provider: "openai-compatible-vast",
		Attributes: map[string]string{
			"api_key":     "vast-key",
			"base_url":    "https://www.vastnum.com/v1",
			"compat_name": "VAST",
		},
	}); err != nil {
		t.Fatalf("register vast auth: %v", err)
	}

	manager.MarkResult(context.Background(), coreauth.Result{AuthID: "vast-auth", Provider: "openai-compatible-vast", Model: "gpt-5", Success: true})

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/v0/management/api-key-usage", nil)
	ginCtx.Request = req
	h.GetAPIKeyUsage(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload map[string]map[string]apiKeyUsageEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	if _, exists := payload["openai-compatible-vast"]; exists {
		t.Fatalf("unexpected namespaced provider bucket in payload: %#v", payload)
	}
	vastBucket, exists := payload["vast"]
	if !exists {
		t.Fatalf("missing compat provider bucket in payload: %#v", payload)
	}
	vastEntry := vastBucket["https://www.vastnum.com/v1|vast-key"]
	if vastEntry.Success != 1 || vastEntry.Failed != 0 {
		t.Fatalf("vast totals = %d/%d, want 1/0", vastEntry.Success, vastEntry.Failed)
	}
}

func TestOpenCodeUsageAndAuthEntriesHideSecrets(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	for _, id := range []string{"zen-auth", "go-auth", "anonymous-auth"} {
		key := "same-prefix-secret-suffix"
		if id == "anonymous-auth" {
			key = "public"
		}
		_, err := manager.Register(context.Background(), &coreauth.Auth{ID: id, Provider: "opencode", Attributes: map[string]string{"api_key": key, "base_url": config.DefaultOpenCodeZenURL, "runtime_only": "true", "note": "account note"}})
		if err != nil {
			t.Fatal(err)
		}
		manager.MarkResult(context.Background(), coreauth.Result{AuthID: id, Provider: "opencode", Model: "test", Success: id != "go-auth"})
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	h.GetAPIKeyUsage(ctx)
	if strings.Contains(rec.Body.String(), "same-prefix-secret-suffix") {
		t.Fatal("usage leaked secret")
	}
	var payload map[string]map[string]apiKeyUsageEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload["opencode"]) != 3 {
		t.Fatalf("lost runtime entries: %s", rec.Body.String())
	}
	var success, failed int64
	for _, entry := range payload["opencode"] {
		success += entry.Success
		failed += entry.Failed
	}
	if success != 2 || failed != 1 {
		t.Fatalf("wrong totals: %d/%d", success, failed)
	}
	for _, auth := range manager.List() {
		auth.Attributes["path"] = "/test/auth.json"
		entry := h.buildAuthFileEntry(auth)
		data, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		if entry == nil || entry["note"] != "account note" || strings.Contains(string(data), "same-prefix-secret-suffix") {
			t.Fatalf("unsafe auth entry: %s", data)
		}
	}
}
