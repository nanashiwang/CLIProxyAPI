package openai

import (
	"context"
	"fmt"
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
	access "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestResponsesInterruptKeepsAccountLeaseAndContinuation(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(fmt.Sprintf("rejected=%t", rejected), func(t *testing.T) { testResponsesInterrupt(t, rejected) })
	}
}

func testResponsesInterrupt(t *testing.T, rejected bool) {
	var connections atomic.Int32
	received := make(chan struct{})
	confirm := make(chan struct{})
	done := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connections.Add(1)
		defer close(done)
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
		read := func() []byte {
			_, p, e := c.ReadMessage()
			if e != nil {
				t.Error(e)
			}
			return p
		}
		write := func(p string) {
			if e := c.WriteMessage(websocket.TextMessage, []byte(p)); e != nil {
				t.Error(e)
			}
		}
		if gjson.GetBytes(read(), "type").String() != "response.create" {
			t.Error("missing create")
		}
		write(`{"type":"response.created","response":{"id":"r1"}}`)
		write(`{"type":"response.output_item.done","output_index":0,"item":{"id":"partial","type":"message","role":"assistant","content":[{"type":"output_text","text":"discard me"}]}}`)
		p := read()
		if string(p) != `{"type":"response.interrupt","response_id":"r1","mode":"discard_partial_items","future":true}` {
			t.Errorf("invalid control forwarded or frame altered: %s", p)
		}
		close(received)
		select {
		case <-confirm:
		case <-time.After(10 * time.Second):
			t.Error("missing interrupt confirmation permission")
			return
		}
		if rejected {
			write(`{"type":"error","status":400,"response_id":"r1","error":{"type":"invalid_request_error","message":"interrupt unsupported"}}`)
			_, _, _ = c.ReadMessage()
			return
		}
		write(`{"type":"response.incomplete","response":{"id":"r1","status":"incomplete","incomplete_details":{"reason":"interrupted"},"output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`)
		p = read()
		if gjson.GetBytes(p, "previous_response_id").String() != "r1" {
			t.Errorf("continuation lost: %s", p)
		}
		write(`{"type":"response.created","response":{"id":"r2"}}`)
		write(`{"type":"response.completed","response":{"id":"r2","output":[],"usage":{"input_tokens":2,"output_tokens":1}}}`)
		_, _, _ = c.ReadMessage()
	}))
	defer upstream.Close()
	cfg := &config.Config{AuthDir: t.TempDir(), Codex: config.CodexConfig{ResponseSteering: true}}
	cfg.CodexResponseSteering = true
	cfg.APIKeys = []string{"interrupt-key"}
	id, model := t.Name()+"-auth", t.Name()+"-model"
	cfg.AccountPools = config.AccountPoolsConfig{Enabled: true, Groups: []config.AccountPoolGroup{{ID: "default", Name: "Default"}, {ID: "exclusive", Name: "Exclusive", Lease: true, CredentialIDs: []string{id}}}, KeyRules: []config.AccountPoolKeyRule{{KeyHash: config.ClientKeyFingerprint("interrupt-key"), Scope: "selected", GroupIDs: []string{"exclusive"}, LeaseInstance: "interrupt-test"}}}
	manager := auth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
	if _, err := manager.Register(context.Background(), &auth.Auth{ID: id, Provider: "codex", Status: auth.StatusActive, Attributes: map[string]string{"api_key": "test", "base_url": upstream.URL, "websockets": "true"}}); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
	defer registry.GetGlobalRegistry().UnregisterClient(id)
	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	router := gin.New()
	router.GET("/v1/responses", func(c *gin.Context) {
		ctx := access.WithClientKeyIdentity(c.Request.Context(), "interrupt-key")
		c.Request = c.Request.WithContext(access.WithGatewayIdentity(ctx, "interrupt-test", "2"))
		h.ResponsesWebsocket(c)
	})
	downstream := httptest.NewServer(router)
	defer downstream.Close()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	send := func(p string) { steeringPoolSend(t, c, p) }
	check := func(active int64) {
		t.Helper()
		if got := manager.ExecutionCapacity(id).Active; got != active {
			t.Fatalf("execution active=%d want %d", got, active)
		}
		leases, e := manager.AccountPoolLeases()
		if e != nil || len(leases) != 1 || leases[0].Active != 1 {
			t.Fatalf("lease released before socket end: %v %v", leases, e)
		}
	}
	send(fmt.Sprintf(`{"type":"response.create","model":%q,"input":[]}`, model))
	steeringPoolRead(t, c, "response.created")
	steeringPoolRead(t, c, "response.output_item.done")
	check(1)
	for _, p := range []string{`{"type":"response.interrupt","response_id":"other-account","mode":"discard_partial_items"}`, `{"type":"response.interrupt","response_id":"r1","mode":"wrong"}`} {
		send(p)
		steeringPoolRead(t, c, "error")
		check(1)
	}
	control := `{"type":"response.interrupt","response_id":"r1","mode":"discard_partial_items","future":true}`
	send(control)
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("interrupt did not reach active upstream")
	}
	check(1)
	send(control)
	steeringPoolRead(t, c, "error")
	check(1)
	close(confirm)
	if rejected {
		steeringPoolRead(t, c, "error")
		if _, _, err := c.ReadMessage(); !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseAbnormalClosure) {
			t.Fatalf("unconfirmed interruption retained continuation: %v", err)
		}
	} else {
		p := steeringPoolRead(t, c, "response.incomplete")
		if gjson.GetBytes(p, "response.incomplete_details.reason").String() != "interrupted" {
			t.Fatalf("terminal changed: %s", p)
		}
		if output := gjson.GetBytes(p, "response.output"); !output.IsArray() || len(output.Array()) != 0 {
			t.Fatalf("discarded partial output restored: %s", p)
		}
		check(0)
		send(fmt.Sprintf(`{"type":"response.create","model":%q,"previous_response_id":"r1","input":[]}`, model))
		steeringPoolRead(t, c, "response.created")
		steeringPoolRead(t, c, "response.completed")
		check(0)
		send(`{"type":"response.interrupt","response_id":"r2","mode":"discard_partial_items"}`)
		steeringPoolRead(t, c, "error")
		check(0)
	}
	_ = c.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream did not close")
	}
	f := &steeringPoolFixture{manager: manager}
	f.waitReleased(t, false)
	if connections.Load() != 1 {
		t.Fatal("interrupt changed upstream connection")
	}
	current, _ := manager.GetByID(id)
	if current.Unavailable || current.Quota.Exceeded {
		t.Fatal("local control failure cooled account")
	}
}
