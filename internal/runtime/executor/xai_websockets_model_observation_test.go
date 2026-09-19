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

func TestXAIWebsocketsModelObservationReusedConnection(t *testing.T) {
	authID := uuid.NewString()
	plugin := &captureCodexModelObservationUsage{authID: authID, records: make(chan usage.Record, 3)}
	usage.RegisterPlugin(plugin)
	var connections atomic.Int32
	wireModels := make(chan string, 3)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, http.Header{"Openai-Model": []string{"connection-model"}})
		if err != nil {
			t.Error(err)
			return
		}
		connections.Add(1)
		defer func() { _ = conn.Close() }()
		for index := 0; index < 3; index++ {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				t.Error(err)
				return
			}
			wireModels <- gjson.GetBytes(payload, "model").String()
			response := map[string]any{"id": "response-model", "output": []any{}, "usage": map[string]int{"input_tokens": 2, "output_tokens": 1, "total_tokens": 3}}
			if index == 0 {
				response["model"] = "grok-reported-build"
			}
			if index == 2 {
				if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.metadata","headers":{"openai-model":"grok-metadata-build"}}`)); err != nil {
					t.Error(err)
					return
				}
				response["model"] = "body-fallback"
			}
			completed, _ := json.Marshal(map[string]any{"type": "response.completed", "response": response})
			if err := conn.WriteMessage(websocket.TextMessage, completed); err != nil {
				t.Error(err)
				return
			}
		}
	}))
	defer server.Close()
	executor := NewXAIWebsocketsExecutor(&config.Config{})
	sessionID := uuid.NewString()
	defer executor.CloseExecutionSession(sessionID)
	auth := &cliproxyauth.Auth{ID: authID, Provider: "xai", Attributes: map[string]string{"base_url": server.URL, "websockets": "true"}, Metadata: map[string]any{"access_token": "test-token"}}
	ctx, cancel := context.WithTimeout(usage.WithRequestedModel(context.Background(), "client-grok"), 5*time.Second)
	defer cancel()
	for index := 0; index < 3; index++ {
		req := cliproxyexecutor.Request{Model: "grok-4.3", Payload: []byte(`{"model":"client-grok","input":"hello"}`)}
		opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: sessionID}}
		result, err := executor.ExecuteStream(ctx, auth, req, opts)
		if err != nil {
			t.Fatal(err)
		}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				t.Fatal(chunk.Err)
			}
		}
		select {
		case record := <-plugin.records:
			wire := <-wireModels
			want, source := "", ""
			if index == 0 {
				want, source = "grok-reported-build", "body"
			}
			if index == 2 {
				want, source = "grok-metadata-build", "metadata"
			}
			if wire != "grok-4.3" || record.RequestedModel != "client-grok" || record.UpstreamModel != wire || record.UpstreamResponseModel != want || record.UpstreamResponseModelSource != source || record.Model != req.Model {
				t.Fatalf("request %d wire=%q usage=%+v", index, wire, record)
			}
			if record.Detail.TotalTokens != 3 {
				t.Fatalf("usage changed: %+v", record.Detail)
			}
		case <-ctx.Done():
			t.Fatal("usage record was not emitted")
		}
	}
	if connections.Load() != 1 {
		t.Fatalf("connections=%d, want 1", connections.Load())
	}
}
