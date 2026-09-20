package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexDuplexBoundsUnacknowledgedSteering(t *testing.T) {
	var steers atomic.Int32
	drained, closed := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(closed)
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(8 * time.Second))
		if _, _, err = c.ReadMessage(); err != nil {
			t.Error(err)
			return
		}
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r1"}}`))
		for {
			_, p, err := c.ReadMessage()
			if err != nil {
				return
			}
			if gjson.GetBytes(p, "type").String() != "response.steer" {
				t.Errorf("unexpected frame: %s", p)
				return
			}
			if steers.Add(1) == 16 {
				close(drained)
			}
		}
	}))
	defer upstream.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	input := make(chan core.WebsocketInput, 17)
	ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)
	exec := NewCodexWebsocketsExecutor(&config.Config{Codex: config.CodexConfig{ResponseSteering: true}})
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	stream, err := exec.ExecuteStream(ctx, &auth.Auth{ID: t.Name(), Provider: "codex", Attributes: map[string]string{"api_key": "test", "base_url": upstream.URL}}, core.Request{Model: "test-model", Payload: []byte(`{"model":"test-model","input":[]}`)}, core.Options{SourceFormat: translator.FromString("codex"), Metadata: map[string]any{core.ExecutionSessionMetadataKey: t.Name()}})
	if err != nil {
		t.Fatal(err)
	}
	sawLimit := false
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("queue limit terminated the socket: %v", chunk.Err)
		}
		switch gjson.GetBytes(chunk.Payload, "type").String() {
		case "response.created":
			for range 17 {
				input <- core.WebsocketInput{Payload: []byte(`{"type":"response.steer","previous_response_id":"r1","input":"pending"}`)}
			}
		case "error":
			if !strings.Contains(gjson.GetBytes(chunk.Payload, "error.message").String(), "too many outstanding response.steer") {
				t.Fatalf("unexpected error: %s", chunk.Payload)
			}
			select {
			case <-drained:
			case <-ctx.Done():
				t.Fatal("accepted steering did not reach upstream")
			}
			sawLimit = true
			cancel()
		}
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation failed to close upstream")
	}
	if !sawLimit || steers.Load() != 16 {
		t.Fatalf("steering limit missing: rejected=%v forwarded=%d", sawLimit, steers.Load())
	}
}
