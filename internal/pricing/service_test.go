package pricing

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

const testCatalog = `{
  "test-model": {
    "litellm_provider": "openai",
    "input_cost_per_token": 0.000001,
    "output_cost_per_token": 0.000002
  }
}`

func TestOptionsFromConfigResolvesDefaultsAndRelativeCache(t *testing.T) {
	options := OptionsFromConfig(configForTest(true, "", 5, "cache/pricing.json"), filepath.Join(t.TempDir(), "config.yaml"))
	if options.SourceURL != "" {
		t.Fatalf("raw source URL = %q, want empty before normalization", options.SourceURL)
	}
	if options.RefreshInterval != 5*time.Minute {
		t.Fatalf("refresh interval = %s, want 5m", options.RefreshInterval)
	}
	if filepath.Base(options.CachePath) != "pricing.json" || filepath.Base(filepath.Dir(options.CachePath)) != "cache" {
		t.Fatalf("cache path = %q", options.CachePath)
	}

	normalized := normalizeOptions(options)
	if normalized.SourceURL != DefaultCatalogURL {
		t.Fatalf("normalized source URL = %q", normalized.SourceURL)
	}
	if normalized.RefreshInterval != minimumRefresh {
		t.Fatalf("normalized refresh interval = %s, want %s", normalized.RefreshInterval, minimumRefresh)
	}
}

func TestConfigureLoadsValidCache(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "pricing.json")
	if errWrite := os.WriteFile(cachePath, []byte(testCatalog), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	service := NewService()
	service.Configure(Options{Enabled: true, CachePath: cachePath})
	status := service.Status()
	if status.ActiveSource != "cache" || status.ModelCount != 1 || status.Version == "" {
		t.Fatalf("status = %+v", status)
	}
	models := service.ListModels("test-model", 10)
	if len(models) != 1 || models[0].Model != "test-model" || models[0].InputUSDPerMillion != 1 {
		t.Fatalf("models = %+v", models)
	}
}

func TestRefreshActivatesAndPersistsRemoteCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" || r.Header.Get("User-Agent") != "CLIProxyAPI-pricing" {
			http.Error(w, "missing headers", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(testCatalog))
	}))
	t.Cleanup(server.Close)

	cachePath := filepath.Join(t.TempDir(), "pricing.json")
	service := NewService()
	service.client = server.Client()
	service.Configure(Options{Enabled: true, SourceURL: server.URL, CachePath: cachePath})
	if errRefresh := service.Refresh(context.Background()); errRefresh != nil {
		t.Fatalf("Refresh() error = %v", errRefresh)
	}

	status := service.Status()
	if status.ActiveSource != server.URL || status.ModelCount != 1 || status.LastError != "" || status.LastRefreshAttempt.IsZero() {
		t.Fatalf("status = %+v", status)
	}
	cached, errRead := os.ReadFile(cachePath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	if string(cached) != testCatalog {
		t.Fatalf("cached catalog = %q", cached)
	}
}

func TestRefreshDiscardsResponseAfterConfigurationChange(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(requestStarted)
		<-releaseResponse
		_, _ = w.Write([]byte(testCatalog))
	}))
	t.Cleanup(server.Close)

	service := NewService()
	service.client = server.Client()
	service.Configure(Options{Enabled: true, SourceURL: server.URL})
	previousVersion := service.Status().Version

	refreshDone := make(chan error, 1)
	go func() {
		refreshDone <- service.Refresh(context.Background())
	}()
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for refresh request")
	}

	service.Configure(Options{Enabled: false, SourceURL: "https://example.invalid/pricing.json"})
	close(releaseResponse)
	select {
	case errRefresh := <-refreshDone:
		if !errors.Is(errRefresh, errConfigurationChanged) {
			t.Fatalf("Refresh() error = %v, want configuration changed", errRefresh)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for refresh completion")
	}
	if status := service.Status(); status.Version != previousVersion || status.ActiveSource != "embedded" {
		t.Fatalf("stale refresh changed status: %+v", status)
	}
}

func configForTest(enabled bool, source string, refreshMinutes int, cacheFile string) config.PricingConfig {
	return config.PricingConfig{
		Enabled:                enabled,
		SourceURL:              source,
		RefreshIntervalMinutes: refreshMinutes,
		CacheFile:              cacheFile,
	}
}
