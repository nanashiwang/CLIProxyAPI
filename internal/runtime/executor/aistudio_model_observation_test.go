package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/wsrelay"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestAIStudioModelObservationRelayRequestAndResponses(t *testing.T) {
	for _, tc := range []struct {
		name             string
		stream, fallback bool
	}{
		{name: "non-stream"}, {name: "fragmented SSE", stream: true}, {name: "stream HTTP response", stream: true, fallback: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authID := uuid.NewString()
			connected := make(chan struct{})
			var connectedOnce sync.Once
			relay := wsrelay.NewManager(wsrelay.Options{ProviderFactory: func(*http.Request) (string, error) { return authID, nil }, OnConnected: func(string) { connectedOnce.Do(func() { close(connected) }) }})
			server := httptest.NewServer(relay.Handler())
			defer server.Close()
			defer func() { _ = relay.Stop(context.Background()) }()
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+relay.Path(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			select {
			case <-connected:
			case <-time.After(time.Second):
				t.Fatal("relay did not connect")
			}
			clientDone := make(chan error, 1)
			go func() {
				for index := 0; index < 3; index++ {
					var request wsrelay.Message
					if err := conn.ReadJSON(&request); err != nil {
						clientDone <- err
						return
					}
					endpoint, _ := request.Payload["url"].(string)
					if !strings.Contains(endpoint, "/models/gemini-3.1-pro-preview:") {
						clientDone <- fmt.Errorf("unexpected actual endpoint %q", endpoint)
						return
					}
					headers := map[string]any{"Content-Type": "application/json"}
					if index == 2 {
						headers["OpenAI-Model"] = "gemini-header-build"
					}
					response := map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": "ok"}}}, "finishReason": "STOP"}}, "usageMetadata": map[string]int{"promptTokenCount": 2, "candidatesTokenCount": 1, "totalTokenCount": 3}}
					if index != 1 {
						response["modelVersion"] = "gemini-reported-build"
					}
					payload, _ := json.Marshal(response)
					send := func(kind string, body map[string]any) error {
						return conn.WriteJSON(wsrelay.Message{ID: request.ID, Type: kind, Payload: body})
					}
					if !tc.stream || tc.fallback {
						if err := send(wsrelay.MessageTypeHTTPResp, map[string]any{"status": 200, "headers": headers, "body": string(payload)}); err != nil {
							clientDone <- err
							return
						}
						continue
					}
					headers["Content-Type"] = "text/event-stream"
					if err := send(wsrelay.MessageTypeStreamStart, map[string]any{"status": 200, "headers": headers}); err != nil {
						clientDone <- err
						return
					}
					if index != 1 {
						for _, part := range []string{`data: {"modelVer`, "sion\":\"gemini-reported-build\"}\n\n"} {
							if err := send(wsrelay.MessageTypeStreamChunk, map[string]any{"data": part}); err != nil {
								clientDone <- err
								return
							}
						}
					}
					delete(response, "modelVersion")
					payload, _ = json.Marshal(response)
					if err := send(wsrelay.MessageTypeStreamChunk, map[string]any{"data": "data: " + string(payload) + "\n\n"}); err != nil {
						clientDone <- err
						return
					}
					if err := send(wsrelay.MessageTypeStreamEnd, nil); err != nil {
						clientDone <- err
						return
					}
				}
				clientDone <- nil
			}()
			plugin := &captureCodexModelObservationUsage{authID: authID, records: make(chan usage.Record, 3)}
			usage.RegisterPlugin(plugin)
			executor := NewAIStudioExecutor(&config.Config{}, "aistudio", relay)
			ctx, cancel := context.WithTimeout(usage.WithRequestedModel(context.Background(), "client-gemini"), 5*time.Second)
			defer cancel()
			for index := 0; index < 3; index++ {
				req := cliproxyexecutor.Request{Model: "gemini-3.1-pro-preview", Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}
				auth := &cliproxyauth.Auth{ID: authID, Provider: "aistudio"}
				if tc.stream {
					result, err := executor.ExecuteStream(ctx, auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, err := executor.Execute(ctx, auth, req, opts); err != nil {
					t.Fatal(err)
				}
				select {
				case record := <-plugin.records:
					want, source := "gemini-reported-build", "body"
					if index == 1 {
						want, source = "", ""
					}
					if index == 2 {
						want, source = "gemini-header-build", "header"
					}
					wantTransport := "http"
					if tc.stream && !tc.fallback {
						wantTransport = "sse"
					}
					if record.UpstreamTransport != wantTransport || record.ClientTransport != "" {
						t.Fatalf("relayed API transport=%q client=%q, want %q/unknown", record.UpstreamTransport, record.ClientTransport, wantTransport)
					}
					if record.RequestedModel != "client-gemini" || record.UpstreamModel != req.Model || record.UpstreamResponseModel != want || record.UpstreamResponseModelSource != source || record.Model != req.Model || record.Detail.TotalTokens != 3 {
						t.Fatalf("request %d usage=%+v", index, record)
					}
				case <-ctx.Done():
					t.Fatal("usage record was not emitted")
				}
			}
			if err := <-clientDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}
