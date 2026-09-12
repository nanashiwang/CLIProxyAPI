package handlers

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestAccountPoolsRejectUnscopedDirectPluginExecutors(t *testing.T) {
	m := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{SDKConfig: config.SDKConfig{AccountPools: config.AccountPoolsConfig{Enabled: true, Groups: []config.AccountPoolGroup{{ID: "default", Name: "Default"}}}}}
	m.SetConfig(cfg)
	h := NewBaseAPIHandlers(&cfg.SDKConfig, m)
	host := &handlerDirectExecutorRouteHost{}
	h.SetModelRouterHost(host)
	_, _, err := h.executeWithPluginExecutor(context.Background(), "openai", "openai", "m", "m", nil, "", "plugin", modelExecutionOptions{})
	if err == nil || err.StatusCode != http.StatusServiceUnavailable {
		t.Fatal("direct plugin execution bypassed account groups")
	}
	_, _, err = h.countWithPluginExecutor(context.Background(), "openai", "m", "m", nil, "", "plugin", modelExecutionOptions{})
	if err == nil || err.StatusCode != http.StatusServiceUnavailable {
		t.Fatal("plugin count bypassed account groups")
	}
	_, _, errs := h.streamWithPluginExecutor(context.Background(), "openai", "openai", "m", "m", nil, "", "plugin", modelExecutionOptions{})
	err = <-errs
	if err == nil || err.StatusCode != http.StatusServiceUnavailable || host.lastPluginID != "" {
		t.Fatal("plugin stream reached unscoped executor")
	}
}
