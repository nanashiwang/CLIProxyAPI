package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexWebsocketsModelObservationUsesRequestFramesOnReusedConnection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		stream    bool
		buffering bool
	}{
		{name: "non-stream"},
		{name: "stream", stream: true},
		{name: "buffered-stream", stream: true, buffering: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authID := "ws-model-observation-" + uuid.NewString()
			plugin := &captureCodexModelObservationUsage{authID: authID, records: make(chan usage.Record, 4)}
			usage.RegisterPlugin(plugin)
			var connections atomic.Int32
			wireModels := make(chan string, 3)
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, http.Header{"X-Openai-Model": {"connection-only-model"}})
				if err != nil {
					t.Errorf("upgrade websocket: %v", err)
					return
				}
				connections.Add(1)
				defer func() { _ = conn.Close() }()
				for index := 0; ; index++ {
					_, payload, errRead := conn.ReadMessage()
					if errRead != nil {
						return
					}
					if index >= 3 {
						t.Error("unexpected extra request on the websocket")
						return
					}
					wireModels <- gjson.GetBytes(payload, "model").String()
					response := map[string]any{
						"id": "resp-model-observation",
						"output": []any{map[string]any{
							"type": "message", "role": "assistant",
							"content": []any{map[string]any{"type": "output_text", "text": "hello"}},
						}},
						"usage": map[string]int{"input_tokens": 5, "output_tokens": 3, "total_tokens": 8},
					}
					if index == 0 {
						response["model"] = "gpt-5.4-2026-03-05"
					} else if index == 2 {
						metadata := []byte(`{"type":"response.metadata","headers":{"x-openai-model":"gpt-5.4-metadata"}}`)
						if errWrite := conn.WriteMessage(websocket.TextMessage, metadata); errWrite != nil {
							t.Errorf("write response metadata: %v", errWrite)
							return
						}
						response["model"] = "body-model-lower-priority"
					}
					completed, errMarshal := json.Marshal(map[string]any{"type": "response.completed", "response": response})
					if errMarshal != nil {
						t.Errorf("marshal response: %v", errMarshal)
						return
					}
					if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
						t.Errorf("write response: %v", errWrite)
						return
					}
				}
			}))
			defer server.Close()

			cfg := &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}}
			cfg.Codex.StreamBootstrapBuffering = tc.buffering
			exec := NewCodexWebsocketsExecutor(cfg)
			sessionID := uuid.NewString()
			defer exec.CloseExecutionSession(sessionID)
			auth := &cliproxyauth.Auth{ID: authID, Provider: "codex", Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
			req := cliproxyexecutor.Request{
				Model:   "gpt-5.4",
				Payload: []byte(`{"model":"client-codex","input":[{"role":"user","content":"hello"}]}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("codex"),
				Metadata:     map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: sessionID},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx = usage.WithRequestedModel(ctx, "client-codex")
			for index := 0; index < 3; index++ {
				if tc.stream {
					result, err := exec.ExecuteStream(ctx, auth, req, opts)
					if err != nil {
						t.Fatalf("ExecuteStream: %v", err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatalf("stream chunk: %v", chunk.Err)
						}
					}
				} else if _, err := exec.Execute(ctx, auth, req, opts); err != nil {
					t.Fatalf("Execute: %v", err)
				}
				select {
				case wireModel := <-wireModels:
					if wireModel != "gpt-5.4" {
						t.Fatalf("sent model = %q, want gpt-5.4", wireModel)
					}
				case <-ctx.Done():
					t.Fatal("timed out waiting for wire model")
				}
				select {
				case record := <-plugin.records:
					if record.RequestedModel != "client-codex" || record.UpstreamModel != "gpt-5.4" {
						t.Fatalf("observed request models = %q / %q", record.RequestedModel, record.UpstreamModel)
					}
					wantModel, wantSource := "", ""
					if index == 0 {
						wantModel, wantSource = "gpt-5.4-2026-03-05", "body"
					} else if index == 2 {
						wantModel, wantSource = "gpt-5.4-metadata", "metadata"
					}
					if record.UpstreamResponseModel != wantModel || record.UpstreamResponseModelSource != wantSource {
						t.Fatalf("response observation = %q (%q), want %q (%q)", record.UpstreamResponseModel, record.UpstreamResponseModelSource, wantModel, wantSource)
					}
					if record.Model != "gpt-5.4" || record.Detail.InputTokens != 5 || record.Detail.OutputTokens != 3 || record.Detail.TotalTokens != 8 {
						t.Fatalf("billing model or token counts changed: model=%q, usage=%+v", record.Model, record.Detail)
					}
				case <-ctx.Done():
					t.Fatal("timed out waiting for usage record")
				}
			}
			if got := connections.Load(); got != 1 {
				t.Fatalf("websocket connections = %d, want a single reused connection", got)
			}
		})
	}
}

type captureCodexModelObservationUsage struct {
	authID  string
	records chan usage.Record
}

func (p *captureCodexModelObservationUsage) HandleUsage(_ context.Context, record usage.Record) {
	if record.AuthID != p.authID {
		return
	}
	select {
	case p.records <- record:
	default:
	}
}
