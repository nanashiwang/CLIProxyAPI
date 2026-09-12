package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCatalogRefreshRetainsSnapshotOnFailureAndDeduplicates(t *testing.T) {
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "temporarily unavailable", http.StatusBadGateway)
			return
		}
		if r.URL.Path == "/docs" {
			_, _ = w.Write([]byte("docs without protocol rows"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"opencode": map[string]any{
				"id": "opencode", "npm": "openai-compatible", "models": map[string]any{
					"free-model": map[string]any{"id": "free-model", "name": "Free", "cost": map[string]any{"input": 0, "output": 0}},
				},
			},
			"opencode-go": map[string]any{
				"id": "opencode-go", "npm": "@ai-sdk/openai", "models": map[string]any{
					"paid-model": map[string]any{"id": "paid-model", "name": "Paid", "cost": map[string]any{"input": 1, "output": 1}},
				},
			},
		})
	}))
	defer server.Close()

	catalog := NewCatalogWithHTTPClient(server.Client(), server.URL)
	catalog.docs = map[Tier]string{TierZen: server.URL + "/docs", TierGo: server.URL + "/docs"}
	if err := catalog.Refresh(context.Background()); err != nil {
		t.Fatalf("initial Refresh() = %v", err)
	}
	if got := catalog.Version(); got != 1 {
		t.Fatalf("Version() = %d, want 1", got)
	}
	if got := catalog.Protocol(TierZen, "free-model"); got != ProtocolChat {
		t.Fatalf("Zen protocol = %q, want chat", got)
	}
	if got := catalog.Protocol(TierGo, "paid-model"); got != ProtocolResponses {
		t.Fatalf("Go protocol = %q, want responses", got)
	}
	before := catalog.Snapshot()
	fail.Store(true)
	if err := catalog.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh() unexpectedly succeeded during upstream failure")
	}
	after := catalog.Snapshot()
	if after.Total != before.Total || catalog.Version() != 1 {
		t.Fatalf("failed refresh replaced snapshot: before=%+v after=%+v version=%d", before, after, catalog.Version())
	}
	fail.Store(false)
	if err := catalog.Refresh(context.Background()); err != nil {
		t.Fatalf("same-content Refresh() = %v", err)
	}
	if got := catalog.Version(); got != 1 {
		t.Fatalf("same-content Version() = %d, want 1", got)
	}
}

func TestCatalogStartStopWaitsForLoop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/docs" {
			_, _ = w.Write([]byte("docs without protocol rows"))
			return
		}
		_, _ = w.Write([]byte(`{"opencode":{"id":"opencode","npm":"openai-compatible","models":{"m":{"id":"m","cost":{"input":0,"output":0}}}}}`))
	}))
	defer server.Close()
	catalog := NewCatalogWithHTTPClient(server.Client(), server.URL)
	catalog.docs = map[Tier]string{TierZen: server.URL + "/docs", TierGo: server.URL + "/docs"}
	changed := make(chan struct{}, 1)
	stop := catalog.Start(context.Background(), time.Hour, func() { changed <- struct{}{} })
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("catalog did not perform initial refresh")
	}
	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("catalog stop did not wait for loop exit")
	}
}

func TestCatalogStartCallbackCanStopCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/docs" {
			_, _ = w.Write([]byte("| m | `https://opencode.ai/v1/chat/completions` |"))
			return
		}
		_, _ = w.Write([]byte(`{"opencode":{"id":"opencode","npm":"@ai-sdk/openai-compatible","models":{"m":{"id":"m"}}}}`))
	}))
	defer server.Close()
	catalog := NewCatalogWithHTTPClient(server.Client(), server.URL)
	catalog.docs = map[Tier]string{TierZen: server.URL + "/docs", TierGo: server.URL + "/docs"}
	callbackDone := make(chan struct{})
	callbackReady := make(chan struct{})
	var stop func()
	stop = catalog.Start(context.Background(), time.Hour, func() {
		<-callbackReady
		stop()
		close(callbackDone)
	})
	close(callbackReady)
	select {
	case <-callbackDone:
	case <-time.After(2 * time.Second):
		t.Fatal("callback could not stop the catalog")
	}
	stop()
}

func TestCatalogParsesCurrentOpenCodeSDKIdentifiers(t *testing.T) {
	providers := map[string]provider{
		"opencode": {
			ID:  "opencode",
			API: "https://opencode.ai/zen/v1",
			NPM: "@ai-sdk/openai-compatible",
			Models: map[string]providerModel{
				"chat-model": {ID: "chat-model", Cost: &modelCost{Input: 1, Output: 1}},
			},
		},
		"opencode-go": {
			ID:  "opencode-go",
			API: "https://opencode.ai/zen/go/v1",
			NPM: "@ai-sdk/openai",
			Models: map[string]providerModel{
				"responses-model": {ID: "responses-model", Cost: &modelCost{Input: 1, Output: 1}},
			},
		},
		"not-opencode": {
			ID:  "not-opencode",
			API: "https://example.invalid/v1",
			NPM: "@ai-sdk/openai-compatible",
			Models: map[string]providerModel{
				"untrusted-model": {ID: "untrusted-model"},
			},
		},
	}
	parsed, err := parseProviders(context.Background(), http.DefaultClient, providers, map[Tier]string{TierZen: "", TierGo: ""})
	if err != nil {
		t.Fatalf("parseProviders() = %v", err)
	}
	if got := parsed[TierZen]["chat-model"].Protocol; got != ProtocolChat {
		t.Fatalf("Zen protocol = %q, want %q", got, ProtocolChat)
	}
	if got := parsed[TierGo]["responses-model"].Protocol; got != ProtocolResponses {
		t.Fatalf("Go protocol = %q, want %q", got, ProtocolResponses)
	}
	if _, exists := parsed[TierZen]["untrusted-model"]; exists {
		t.Fatal("provider with a similar name was accepted")
	}
}

func TestParseProvidersUsesDeterministicProviderPrecedence(t *testing.T) {
	providers := map[string]provider{
		"opencode-z": {
			ID:  "opencode",
			API: "https://opencode.ai/zen/v1",
			NPM: "@ai-sdk/openai-compatible",
			Models: map[string]providerModel{
				"shared": {ID: "shared", Name: "z"},
			},
		},
		"opencode-a": {
			ID:  "opencode",
			API: "https://opencode.ai/zen/v1",
			NPM: "@ai-sdk/openai-compatible",
			Models: map[string]providerModel{
				"shared": {ID: "shared", Name: "a"},
			},
		},
	}
	docs := map[Tier]string{TierZen: "", TierGo: ""}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("no protocol rows"))
	}))
	defer server.Close()
	docs[TierZen], docs[TierGo] = server.URL, server.URL
	parsed, err := parseProviders(context.Background(), server.Client(), providers, docs)
	if err != nil {
		t.Fatalf("parseProviders() = %v", err)
	}
	if got := parsed[TierZen]["shared"].Name; got != "a" {
		t.Fatalf("shared model name = %q, want deterministic first provider %q", got, "a")
	}
}

func TestParseProvidersBoundsCatalogFields(t *testing.T) {
	providers := map[string]provider{
		"opencode": {
			ID:  "opencode",
			API: "https://opencode.ai/zen/v1",
			NPM: "@ai-sdk/openai-compatible",
			Models: map[string]providerModel{
				"model": {
					ID:          "model",
					Name:        strings.Repeat("n", maxModelNameBytes+100),
					Description: strings.Repeat("d", maxModelDescriptionBytes+100),
					Limit:       modelLimit{Context: maxModelLimit + 100, Output: -1},
					Modalities:  modalities{Input: []string{strings.Repeat("i", maxModalityBytes+100), "text", "text"}},
				},
			},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("no protocol rows"))
	}))
	defer server.Close()
	parsed, err := parseProviders(context.Background(), server.Client(), providers, map[Tier]string{TierZen: server.URL, TierGo: server.URL})
	if err != nil {
		t.Fatalf("parseProviders() = %v", err)
	}
	model := parsed[TierZen]["model"]
	if len(model.Name) > maxModelNameBytes || len(model.Description) > maxModelDescriptionBytes {
		t.Fatalf("catalog text was not bounded: name=%d description=%d", len(model.Name), len(model.Description))
	}
	if model.ContextLength != maxModelLimit || model.MaxCompletionTokens != 0 {
		t.Fatalf("model limits = (%d, %d), want (%d, 0)", model.ContextLength, model.MaxCompletionTokens, maxModelLimit)
	}
	if len(model.InputModalities) != 2 || len(model.InputModalities[0]) > maxModalityBytes {
		t.Fatalf("modalities were not normalized: %#v", model.InputModalities)
	}
}
