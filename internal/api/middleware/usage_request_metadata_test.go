package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

type entryMetadataUsagePlugin struct{ records chan coreusage.Record }

func (p *entryMetadataUsagePlugin) HandleUsage(_ context.Context, record coreusage.Record) {
	p.records <- record
}

func TestUsageRequestMetadataCoversDirectUnauthorizedPublish(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreusage.NewManager(1)
	defer manager.Stop()
	plugin := &entryMetadataUsagePlugin{records: make(chan coreusage.Record, 1)}
	manager.Register(plugin)
	engine := gin.New()
	engine.Use(UsageRequestMetadataMiddleware())
	engine.GET("/direct-route", func(c *gin.Context) {
		// Some relay routes use this execution marker before their downstream
		// upgrade succeeds. The entry snapshot must remain HTTP on a 401 failure.
		ctx := coreexecutor.WithDownstreamWebsocket(context.WithValue(c.Request.Context(), "gin", c))
		manager.Publish(ctx, coreusage.Record{Model: "direct-home-failure", Failed: true})
		c.Request.Header.Set("User-Agent", "later-mutated-value")
		c.Status(http.StatusUnauthorized)
	})
	req := httptest.NewRequest(http.MethodGet, "/direct-route", nil)
	req.RemoteAddr = "192.0.2.15:44556"
	req.Header.Set("User-Agent", "direct-client/1")
	req.Header.Set("X-Forwarded-For", "203.0.113.90")
	req.Header.Set("X-Real-IP", "203.0.113.91")
	req.Header.Set("CF-Connecting-IP", "203.0.113.92")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, req)
	select {
	case record := <-plugin.records:
		if record.ClientTransport != "http" || record.UpstreamTransport != "" || record.ClientIP != "192.0.2.15" || record.UserAgent != "direct-client/1" {
			t.Fatalf("unsafe/late direct entry metadata: %+v", record)
		}
	case <-time.After(time.Second):
		t.Fatal("direct usage record missing")
	}
}
