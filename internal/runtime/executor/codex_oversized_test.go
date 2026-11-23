package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func TestCodexPeerSizeRejection(t *testing.T) {
	for _, mode := range []string{"execute", "stream", "buffered-stream"} {
		for _, scenario := range []string{"fresh", "upload", "metadata", "warmup", "continuation", "bound", "reset"} {
			t.Run(mode+"/"+scenario, func(t *testing.T) {
				var httpCount, wsCount atomic.Int32
				ack := make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer test" || !slices.Equal(r.Header.Values(codexExcludeHeader), []string{"a", "b"}) {
						t.Error("account or exclusions changed")
					}
					if !websocket.IsWebSocketUpgrade(r) {
						httpCount.Add(1)
						data, err := io.ReadAll(r.Body)
						if err != nil || !bytes.Contains(data, []byte("complete-test-input")) {
							t.Error("HTTP fallback lost original input")
						}
						w.Header().Set("X-Test-Transport", "http")
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"http-test\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n")
						return
					}
					wsCount.Add(1)
					conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.Close()
					if scenario == "upload" {
						_, reader, err := conn.NextReader()
						if err != nil {
							t.Error(err)
							return
						}
						if _, err = io.CopyN(io.Discard, reader, 8192); err != nil {
							t.Error(err)
							return
						}
					} else if _, _, err = conn.ReadMessage(); err != nil {
						t.Error(err)
						return
					}
					if scenario == "reset" {
						return
					}
					if scenario == "metadata" {
						if err = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"ws-test"}}`)); err != nil {
							t.Error(err)
							return
						}
					}
					if err = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(1009, "too large"), time.Now().Add(3*time.Second)); err != nil {
						t.Error(err)
						return
					}
					if scenario == "upload" {
						_, _, _ = conn.ReadMessage()
						close(ack)
					}
				}))
				defer server.Close()
				cfg := &config.Config{}
				cfg.Codex.StreamBootstrapBuffering = mode == "buffered-stream"
				exec := NewCodexAutoExecutor(cfg)
				a := &auth.Auth{ID: "account", Provider: "codex", Attributes: map[string]string{"api_key": "test", "base_url": server.URL, "websockets": "true"}}
				input := "complete-test-input"
				if scenario == "upload" {
					input += strings.Repeat("x", 8*codexWebsocketWriteChunkSize)
					old := testWebsocketWriteChunkHook
					defer func() { testWebsocketWriteChunkHook = old }()
					testWebsocketWriteChunkHook = func(i, _ int) {
						if i == 2 {
							select {
							case <-ack:
							case <-time.After(4 * time.Second):
								t.Error("peer did not acknowledge partial upload close")
							}
						}
					}
				}
				suffix := ""
				switch scenario {
				case "warmup":
					suffix = `,"generate":false`
				case "continuation":
					suffix = `,"previous_response_id":"old"`
				}
				req := core.Request{Model: "test-model", Payload: []byte(`{"model":"test-model","input":[{"role":"user","content":"` + input + `"}]` + suffix + `}`)}
				opts := core.Options{SourceFormat: translator.FromString("codex"), Headers: http.Header{"x-codex-exclude": {"a", "b"}}}
				if scenario == "bound" {
					opts.ExecutionLifecycle = &countingWebsocketLifecycle{}
				}
				ctx := core.WithDownstreamWebsocket(context.Background())
				var err error
				var response []byte
				if mode == "execute" {
					var result core.Response
					result, err = exec.Execute(ctx, a, req, opts)
					response = result.Payload
				} else {
					var result *core.StreamResult
					result, err = exec.ExecuteStream(ctx, a, req, opts)
					if err == nil {
						if scenario == "fresh" || scenario == "upload" {
							if result.Headers.Get("X-Test-Transport") != "http" {
								t.Error("stale websocket headers")
							}
						}
						for chunk := range result.Chunks {
							response = append(response, chunk.Payload...)
							if chunk.Err != nil {
								err = chunk.Err
							}
						}
					}
				}
				if wsCount.Load() != 1 {
					t.Fatalf("WS requests=%d", wsCount.Load())
				}
				if scenario == "fresh" || scenario == "upload" {
					if err != nil || httpCount.Load() != 1 || !bytes.Contains(response, []byte("http-test")) {
						t.Fatalf("HTTP requests=%d err=%v response=%q", httpCount.Load(), err, response)
					}
					return
				}
				if err == nil || httpCount.Load() != 0 {
					t.Fatalf("HTTP requests=%d err=%v", httpCount.Load(), err)
				}
				if scenario == "continuation" {
					if !core.IsUpstreamWebsocketReplayRequired(err) {
						t.Fatal(err)
					}
					return
				}
				var sizeErr codexWebsocketMessageTooBigError
				if scenario == "reset" {
					if errors.As(err, &sizeErr) {
						t.Fatal("bare reset classified as size rejection")
					}
					return
				}
				if !errors.As(err, &sizeErr) || sizeErr.StatusCode() != 413 || sizeErr.UsageErrorOrigin() != "upstream_response" {
					t.Fatalf("wrong rejection: %T %v", err, err)
				}
			})
		}
	}
}

func TestCodexResponseExclusionsControlSessionReuse(t *testing.T) {
	var upgrades, creates atomic.Int32
	headers := make(chan []string, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		upgrades.Add(1)
		headers <- r.Header.Values(codexExcludeHeader)
		for {
			if _, _, err = conn.ReadMessage(); err != nil {
				return
			}
			creates.Add(1)
			if err = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"test-id","output":[],"usage":{"input_tokens":1}}}`)); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	e := NewCodexWebsocketsExecutor(&config.Config{})
	e.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	defer e.CloseExecutionSession("exclude-test")
	a := &auth.Auth{ID: "key", Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
	ctx := core.WithDownstreamWebsocket(context.Background())
	call := func(c context.Context, values []string, previous, stream bool) error {
		opts := core.Options{SourceFormat: translator.FromString("codex"), Headers: http.Header{}, Metadata: map[string]any{core.ExecutionSessionMetadataKey: "exclude-test"}}
		if values != nil {
			opts.Headers["x-codex-exclude"] = values
		}
		suffix := ""
		if previous {
			suffix = `,"previous_response_id":"test-id"`
		}
		req := core.Request{Model: "test-model", Payload: []byte(`{"model":"test-model","input":[]` + suffix + `}`)}
		if !stream {
			_, err := e.Execute(c, a, req, opts)
			return err
		}
		res, err := e.ExecuteStream(c, a, req, opts)
		if err != nil {
			return err
		}
		for chunk := range res.Chunks {
			if chunk.Err != nil {
				return chunk.Err
			}
		}
		return nil
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	first := []string{"first", "second"}
	changed := []string{"first", "changed"}
	must(call(ctx, first, false, true))
	must(call(core.WithRequiredUpstreamWebsocket(ctx), first, true, false))
	for _, stream := range []bool{true, false} {
		for _, c := range []context.Context{ctx, core.WithRequiredUpstreamWebsocket(ctx)} {
			if err := call(c, changed, true, stream); !core.IsUpstreamWebsocketReplayRequired(err) {
				t.Fatalf("expected replay, got %v", err)
			}
		}
	}
	must(call(ctx, changed, false, false))
	must(call(ctx, nil, false, true))
	a.Attributes["header:X-Codex-Exclude"] = "configured"
	must(call(ctx, first, false, true))
	must(call(ctx, changed, false, false))
	if upgrades.Load() != 4 || creates.Load() != 6 {
		t.Fatalf("upgrades=%d creates=%d", upgrades.Load(), creates.Load())
	}
	for _, want := range [][]string{first, changed, nil, {"configured"}} {
		if got := <-headers; !slices.Equal(got, want) {
			t.Fatalf("handshake=%q want %q", got, want)
		}
	}
}
