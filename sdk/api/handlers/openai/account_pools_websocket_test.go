package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestResponsesWebsocketAccountPoolIsolationAndRevocation(t *testing.T) {
	m := coreauth.NewManager(nil, coreauth.NewSessionAffinitySelector(&coreauth.RoundRobinSelector{}), nil)
	e := &websocketAuthCaptureExecutor{}
	m.RegisterExecutor(e)
	ids := []string{uuid.NewString(), uuid.NewString()}
	reg := registry.GetGlobalRegistry()
	for _, id := range ids {
		reg.RegisterClient(id, "test-provider", []*registry.ModelInfo{{ID: "test-pool-model"}})
		_, err := m.Register(context.Background(), &coreauth.Auth{ID: id, Provider: "test-provider", Status: coreauth.StatusActive})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { reg.UnregisterClient(id) })
	}
	cfg := &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{"a", "b"}, AccountPools: config.AccountPoolsConfig{Enabled: true, Groups: []config.AccountPoolGroup{
		{ID: "default", Name: "Default"}, {ID: "a", Name: "A", CredentialIDs: ids[:1]}, {ID: "b", Name: "B", CredentialIDs: ids[1:]},
	}, KeyRules: []config.AccountPoolKeyRule{
		{KeyHash: config.ClientKeyFingerprint("a"), Scope: "selected", GroupIDs: []string{"a"}},
		{KeyHash: config.ClientKeyFingerprint("b"), Scope: "selected", GroupIDs: []string{"b"}},
	}}}}
	m.SetConfig(cfg)
	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, m))
	router := gin.New()
	router.GET("/v1/responses", func(c *gin.Context) {
		// The fixture emulates identity established by the real access middleware.
		key := c.GetHeader("X-Test-Key")
		c.Set("userApiKey", key)
		c.Request = c.Request.WithContext(sdkaccess.WithClientKeyIdentity(c.Request.Context(), key))
		c.Next()
	}, h.ResponsesWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()
	dial := func(key string) *websocket.Conn {
		t.Helper()
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", http.Header{"X-Test-Key": []string{key}, "Session-Id": []string{"same-session"}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	a, b := dial("a"), dial("b")
	turn := func(conn *websocket.Conn) string {
		t.Helper()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"test-pool-model","input":[{"role":"user","content":"hello"}]}`)); err != nil {
			t.Fatal(err)
		}
		_, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		return gjson.GetBytes(payload, "type").String()
	}
	if turn(a) != "response.completed" || turn(b) != "response.completed" {
		t.Fatal("group websocket execution failed")
	}
	got := e.AuthIDs()
	if len(got) != 2 || got[0] != ids[0] || got[1] != ids[1] {
		t.Fatalf("shared session escaped groups: %v", got)
	}
	cfg.AccountPools.KeyRules[0].GroupIDs = []string{"b"}
	m.SetConfig(cfg)
	if turn(a) != "response.completed" {
		t.Fatal("new group permission was not applied to existing websocket")
	}
	got = e.AuthIDs()
	if len(got) != 3 || got[2] != ids[1] {
		t.Fatalf("old pinned account survived reassignment: %v", got)
	}
	cfg.APIKeys = []string{"b"}
	m.SetConfig(cfg)
	if turn(a) != "error" {
		t.Fatal("revoked key was not rejected on existing websocket")
	}
	if len(e.AuthIDs()) != 3 {
		t.Fatal("revoked websocket still reached an executor")
	}
}
