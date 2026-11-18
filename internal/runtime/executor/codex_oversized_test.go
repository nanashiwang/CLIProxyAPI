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

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexAdvertisedLimitChoosesHTTPBeforeSend(t *testing.T) {
	var requests, upgrades, messages atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "" {
			upgrades.Add(1)
			upgrader := websocket.Upgrader{}
			conn, err := upgrader.Upgrade(w, r, http.Header{"X-Codex-Websocket-Max-Message-Bytes": {"1024"}})
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			if _, _, err = conn.ReadMessage(); err == nil {
				messages.Add(1)
			}
			return
		}
		requests.Add(1)
		n, _ := io.Copy(io.Discard, r.Body)
		if n < 2048 {
			t.Error("request truncated")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"http-r\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"http-r\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
	}))
	defer server.Close()
	e := NewCodexWebsocketsExecutor(&config.Config{})
	a := &auth.Auth{ID: "large-oauth", Provider: "codex", Metadata: map[string]any{"access_token": "test"}, Attributes: map[string]string{"base_url": server.URL}}
	payload := []byte(`{"model":"test-model","input":[{"role":"user","content":"` + strings.Repeat("x", 2048) + `"}]}`)
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
	if requests.Load() != 2 || upgrades.Load() != 2 || messages.Load() != 0 {
		t.Fatal("non-streaming fallback did not execute exactly once")
	}
	// A required connection must fail before any network operation, even for a full-looking body.
	_, err = e.ExecuteStream(core.WithRequiredUpstreamWebsocket(ctx), a, req, opts)
	if !core.IsUpstreamWebsocketReplayRequired(err) || requests.Load() != 2 || upgrades.Load() != 2 {
		t.Fatalf("continuation sent or wrong error: %v", err)
	}
}

func TestCodexAdvertisedLimitBoundaries(t *testing.T) {
	payload := []byte("中文")
	for _, tc := range []struct {
		limit int64
		want  bool
	}{{0, false}, {-1, false}, {6, false}, {7, false}, {5, true}} {
		if got := codexMessageExceedsLimit(&websocketConnectionCloser{maxMessageBytes: tc.limit}, payload); got != tc.want {
			t.Fatalf("limit %d: %v", tc.limit, got)
		}
	}
}

func TestCodexNegotiatedLimitDoesNotLeakAcrossConnections(t *testing.T) {
	limits := []string{"6", "bad", "0", "-1", "9223372036854775808", "", "128"}
	var index atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(index.Add(1)) - 1
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, http.Header{"X-Codex-Websocket-Max-Message-Bytes": {limits[i]}})
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		for {
			if _, _, err = conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	e := NewCodexWebsocketsExecutor(&config.Config{})
	for i, want := range []int64{6, 0, 0, 0, 0, 0, 128} {
		conn, closer, _, err := e.dialCodexWebsocket(context.Background(), nil, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if closer.maxMessageBytes != want {
			t.Fatalf("connection %d: %d", i, closer.maxMessageBytes)
		}
		sess := &codexWebsocketSession{conn: conn, connCloser: closer}
		if err := sess.writeMessage(conn, websocket.TextMessage, []byte("中文")); err != nil {
			t.Fatalf("exact limit: %v", err)
		}
		err = sess.writeMessage(conn, websocket.TextMessage, []byte("中文x"))
		if (want == 6) != core.IsUpstreamWebsocketReplayRequired(err) {
			t.Fatalf("boundary: %v", err)
		}
		closer.Close()
	}
}

func TestCodexOversizedContinuationAndWarmupNeverUseHTTP(t *testing.T) {
	var httpRequests, messages atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "" {
			httpRequests.Add(1)
			w.WriteHeader(500)
			return
		}
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, http.Header{"X-Codex-Websocket-Max-Message-Bytes": {"1"}})
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		if _, _, err = conn.ReadMessage(); err == nil {
			messages.Add(1)
		}
	}))
	defer server.Close()
	for _, stream := range []bool{false, true} {
		for _, suffix := range []string{`,"previous_response_id":"resp_old"`, `,"generate":false`} {
			e := NewCodexWebsocketsExecutor(&config.Config{})
			a := &auth.Auth{ID: "limit-guard", Provider: "codex", Attributes: map[string]string{"base_url": server.URL, "api_key": "test"}}
			req := core.Request{Model: "test-model", Payload: []byte(`{"model":"test-model","input":[]` + suffix + `}`)}
			opts := core.Options{SourceFormat: translator.FromString("codex")}
			var err error
			if stream {
				_, err = e.ExecuteStream(context.Background(), a, req, opts)
			} else {
				_, err = e.Execute(context.Background(), a, req, opts)
			}
			if err == nil {
				t.Fatal("oversized stateful request accepted")
			}
			if strings.Contains(suffix, "previous_response_id") && !core.IsUpstreamWebsocketReplayRequired(err) {
				t.Fatal(err)
			}
		}
	}
	if httpRequests.Load() != 0 || messages.Load() != 0 {
		t.Fatal("stateful payload sent")
	}
}
