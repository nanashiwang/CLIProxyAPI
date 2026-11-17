package openai

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestResponsesOversizedOAuthRecordsHTTPTransport(t *testing.T) {
	var requests, upgrades, messages atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			t.Error("missing complete history")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"http-r\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"http-r\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
	}))
	defer upstream.Close()
	cfg := &config.Config{AuthDir: t.TempDir(), Codex: config.CodexConfig{ResponseSteering: true}}
	cfg.CodexResponseSteering = true
	id, model := t.Name()+"-auth", t.Name()+"-model"
	manager := auth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
	if _, err := manager.Register(context.Background(), &auth.Auth{ID: id, Provider: "codex", Status: auth.StatusActive, Metadata: map[string]any{"access_token": "test"}, Attributes: map[string]string{"base_url": upstream.URL, "websockets": "true"}}); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
	defer registry.GetGlobalRegistry().UnregisterClient(id)
	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	router := gin.New()
	router.GET("/v1/responses", h.ResponsesWebsocket)
	downstream := httptest.NewServer(router)
	defer downstream.Close()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
	steeringPoolSend(t, c, fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"role":"user","content":%q}]}`, model, strings.Repeat("x", 2048)))
	steeringPoolRead(t, c, "response.created")
	steeringPoolRead(t, c, "response.completed")
	// A delta after the HTTP fallback must close for full replay rather than
	// entering the otherwise eligible duplex WebSocket path.
	steeringPoolSend(t, c, fmt.Sprintf(`{"type":"response.create","model":%q,"previous_response_id":"http-r","input":[]}`, model))
	_, _, err = c.ReadMessage()
	if !websocket.IsCloseError(err, websocket.CloseServiceRestart) {
		t.Fatalf("expected replay close, got %v", err)
	}
	if !strings.Contains(err.Error(), wsHTTPReplayRequiredCloseReason) {
		t.Fatalf("missing replay reason: %v", err)
	}
	if requests.Load() != 1 || upgrades.Load() != 1 || messages.Load() != 0 {
		t.Fatalf("delta was sent upstream: %d", requests.Load())
	}
	if manager.ExecutionCapacity(id).Active != 0 {
		t.Fatal("HTTP fallback leaked execution occupancy")
	}
}
