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
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

type steeringPoolFixture struct {
	manager       *coreauth.Manager
	cfg           *config.Config
	downstream    *httptest.Server
	frames        atomic.Int32
	connections   atomic.Int32
	closed        chan struct{}
	authID, model string
}

func newSteeringPoolFixture(t *testing.T) *steeringPoolFixture {
	t.Helper()
	f := &steeringPoolFixture{closed: make(chan struct{}, 4), authID: t.Name() + "-account", model: t.Name() + "-model"}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.connections.Add(1)
		defer func() { f.closed <- struct{}{} }()
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
		for {
			_, p, err := c.ReadMessage()
			if err != nil {
				return
			}
			f.frames.Add(1)
			if gjson.GetBytes(p, "type").String() == "response.steer" {
				_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.steer.accepted","steer":{"id":"s1","previous_response_id":"r1"}}`))
			} else {
				_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r1"}}`))
				_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"r1","output":[]}}`))
			}
		}
	}))
	t.Cleanup(upstream.Close)
	f.cfg = &config.Config{AuthDir: t.TempDir(), Codex: config.CodexConfig{ResponseSteering: true}}
	f.cfg.CodexResponseSteering = true
	f.cfg.APIKeys = []string{"steering-pool-key"}
	f.cfg.AccountPools = config.AccountPoolsConfig{Enabled: true, Groups: []config.AccountPoolGroup{
		{ID: "default", Name: "Default"}, {ID: "exclusive", Name: "Exclusive", Lease: true, CredentialIDs: []string{f.authID}},
	}, KeyRules: []config.AccountPoolKeyRule{{KeyHash: config.ClientKeyFingerprint("steering-pool-key"), Scope: "selected", GroupIDs: []string{"exclusive"}, LeaseInstance: "steering-test"}}}
	f.manager = coreauth.NewManager(nil, nil, nil)
	f.manager.SetConfig(f.cfg)
	f.manager.SetRetryConfig(0, 0, 0)
	f.manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(f.cfg))
	for _, id := range []string{f.authID, f.authID + "-outside"} {
		_, err := f.manager.Register(context.Background(), &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "test-key", "base_url": upstream.URL, "websockets": "true"}})
		if err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: f.model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&f.cfg.SDKConfig, f.manager))
	router := gin.New()
	router.GET("/v1/responses", func(c *gin.Context) {
		ctx := sdkaccess.WithClientKeyIdentity(c.Request.Context(), "steering-pool-key")
		c.Request = c.Request.WithContext(sdkaccess.WithGatewayIdentity(ctx, "steering-test", c.Request.Header.Get("Test-User")))
		h.ResponsesWebsocket(c)
	})
	f.downstream = httptest.NewServer(router)
	t.Cleanup(f.downstream.Close)
	return f
}

func (f *steeringPoolFixture) connect(t *testing.T, user string) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(f.downstream.URL, "http")+"/v1/responses", http.Header{"Test-User": {user}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetReadDeadline(time.Now().Add(8 * time.Second))
	return c
}
func steeringPoolSend(t *testing.T, c *websocket.Conn, p string) {
	t.Helper()
	if err := c.WriteMessage(websocket.TextMessage, []byte(p)); err != nil {
		t.Fatal(err)
	}
}
func steeringPoolRead(t *testing.T, c *websocket.Conn, event string) []byte {
	t.Helper()
	_, p, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(p, "type").String() != event {
		t.Fatalf("expected %s, got %s", event, p)
	}
	return p
}
func (f *steeringPoolFixture) start(t *testing.T, user string) *websocket.Conn {
	t.Helper()
	c := f.connect(t, user)
	steeringPoolSend(t, c, fmt.Sprintf(`{"type":"response.create","model":%q,"input":[]}`, f.model))
	steeringPoolRead(t, c, "response.created")
	steeringPoolRead(t, c, "response.completed")
	return c
}
func (f *steeringPoolFixture) waitReleased(t *testing.T, temporary bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		leases, err := f.manager.AccountPoolLeases()
		if err != nil {
			t.Fatal(err)
		}
		if (temporary && len(leases) == 0) || (!temporary && len(leases) == 1 && leases[0].Active == 0) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("socket leaked an active lease")
}

func TestResponsesSteeringExclusiveLeaseHeldAcrossResponses(t *testing.T) {
	f := newSteeringPoolFixture(t)
	c := f.start(t, "101")
	leases, err := f.manager.AccountPoolLeases()
	if err != nil || len(leases) != 1 || leases[0].Active != 1 || leases[0].Credential != f.authID {
		t.Fatalf("lease released at response boundary: %+v %v", leases, err)
	}
	other := f.connect(t, "202")
	steeringPoolSend(t, other, fmt.Sprintf(`{"type":"response.create","model":%q,"input":[]}`, f.model))
	_, p, readErr := other.ReadMessage()
	// Server-side pool errors intentionally close the socket without exposing details.
	if readErr == nil && (gjson.GetBytes(p, "type").String() != "error" || !strings.Contains(string(p), "pool_busy")) {
		t.Fatalf("competing owner was not denied: %s", p)
	}
	steeringPoolSend(t, c, `{"type":"response.steer","previous_response_id":"r1","input":"continue"}`)
	steeringPoolRead(t, c, "response.steer.accepted")
	_ = c.Close()
	select {
	case <-f.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not close after cancellation")
	}
	f.waitReleased(t, false)
	if f.connections.Load() != 1 || f.frames.Load() != 2 {
		t.Fatalf("lease escaped or input replayed: connections=%d frames=%d", f.connections.Load(), f.frames.Load())
	}
}

func TestResponsesSteeringTemporaryLeaseRemainsPerRequest(t *testing.T) {
	f := newSteeringPoolFixture(t)
	c := f.start(t, "1")
	f.waitReleased(t, true)
	steeringPoolSend(t, c, `{"type":"response.create","previous_response_id":"r1","input":[]}`)
	_, p, err := c.ReadMessage()
	if err == nil && (gjson.GetBytes(p, "type").String() != "error" || !strings.Contains(string(p), "pool_temporary_session_unsupported")) {
		t.Fatalf("temporary continuation accepted: %s", p)
	}
	if f.frames.Load() != 1 {
		t.Fatal("temporary lease sent a continuation upstream")
	}
}

func TestResponsesSteeringRejectsCrossSocketPreviousResponse(t *testing.T) {
	f := newSteeringPoolFixture(t)
	c := f.connect(t, "101")
	steeringPoolSend(t, c, fmt.Sprintf(`{"type":"response.create","model":%q,"previous_response_id":"other-socket","input":[]}`, f.model))
	p := steeringPoolRead(t, c, "error")
	if !strings.Contains(string(p), "previous_response_not_found") {
		t.Fatalf("wrong continuation error: %s", p)
	}
	if f.connections.Load() != 0 {
		t.Fatal("cross-socket continuation reached upstream")
	}
}

func TestResponsesSteeringLivePoolPolicyAndCooldown(t *testing.T) {
	for _, mode := range []string{"permission_revoked", "cooldown_then_frame", "cooldown_then_cancel"} {
		t.Run(mode, func(t *testing.T) {
			f := newSteeringPoolFixture(t)
			c := f.start(t, "101")
			cooldownUntil := time.Now().Add(time.Hour)
			if mode == "permission_revoked" {
				next := *f.cfg
				next.AccountPools = f.cfg.AccountPools
				next.AccountPools.KeyRules = []config.AccountPoolKeyRule{{KeyHash: config.ClientKeyFingerprint("steering-pool-key"), Scope: "selected", GroupIDs: []string{}, LeaseInstance: "steering-test"}}
				f.manager.SetConfig(&next)
			} else {
				retry := time.Hour
				f.manager.MarkResult(context.Background(), coreauth.Result{AuthID: f.authID, Provider: "codex", Model: f.model, Error: &coreauth.Error{HTTPStatus: 429, Message: "usage_limit_reached"}, CredentialScope: true, RetryAfter: &retry})
			}
			if mode == "cooldown_then_cancel" {
				_ = c.Close()
			} else {
				steeringPoolSend(t, c, `{"type":"response.steer","previous_response_id":"r1","input":"must not pass"}`)
				for {
					_, p, err := c.ReadMessage()
					if err != nil {
						break
					}
					if gjson.GetBytes(p, "type").String() != "error" {
						t.Fatalf("revoked account accepted frame: %s", p)
					}
				}
			}
			select {
			case <-f.closed:
			case <-time.After(3 * time.Second):
				t.Fatal("upstream did not close")
			}
			f.waitReleased(t, false)
			if f.frames.Load() != 1 || f.connections.Load() != 1 {
				t.Fatal("revoked frame was forwarded or replayed")
			}
			if mode != "permission_revoked" {
				current, _ := f.manager.GetByID(f.authID)
				if !current.Quota.Exceeded || current.Quota.NextRecoverAt.Before(cooldownUntil) {
					t.Fatalf("socket shutdown cleared/shortened cooldown: %+v", current.Quota)
				}
			}
			outside, _ := f.manager.GetByID(f.authID + "-outside")
			if outside.Unavailable || outside.LastError != nil {
				t.Fatal("unrelated account was cooled")
			}
		})
	}
}
