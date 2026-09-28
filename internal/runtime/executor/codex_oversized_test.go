package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCodexOversizedOAuthChoosesHTTPBeforeSend(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Upgrade") != "" {
			t.Error("oversized request attempted WebSocket upgrade")
			w.WriteHeader(400)
			return
		}
		n, _ := io.Copy(io.Discard, r.Body)
		if n < helps.CodexWebsocketHTTPThreshold {
			t.Error("request truncated")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"http-r\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"http-r\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
	}))
	defer server.Close()
	e := NewCodexWebsocketsExecutor(&config.Config{})
	a := &auth.Auth{ID: "large-oauth", Provider: "codex", Metadata: map[string]any{"access_token": "test"}, Attributes: map[string]string{"base_url": server.URL}}
	payload := []byte(`{"model":"test-model","input":[{"role":"user","content":"` + strings.Repeat("x", helps.CodexWebsocketHTTPThreshold) + `"}]}`)
	req := core.Request{Model: "test-model", Payload: payload}
	opts := core.Options{SourceFormat: translator.FromString("codex")}
	ctx := core.WithDownstreamWebsocket(context.Background())
	transport := ""
	ctx = core.WithUpstreamTransportObserver(ctx, func(s string) { transport = s })
	result, err := e.ExecuteStream(ctx, a, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
	}
	if requests.Load() != 1 || transport != "sse" {
		t.Fatalf("requests=%d transport=%s", requests.Load(), transport)
	}
	if _, err = e.Execute(ctx, a, req, opts); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatal("non-streaming fallback did not execute exactly once")
	}
	// A required connection must fail before any network operation, even for a full-looking body.
	_, err = e.ExecuteStream(core.WithRequiredUpstreamWebsocket(ctx), a, req, opts)
	if !core.IsUpstreamWebsocketReplayRequired(err) || requests.Load() != 2 {
		t.Fatalf("continuation sent or wrong error: %v", err)
	}
}
