package cliproxy

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestSyncOpenCodeCatalogConcurrentSameIntervalStartsOneLoop(t *testing.T) {
	service := &Service{}
	var starts atomic.Int32
	var stops atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	service.openCodeCatalogStartFn = func(context.Context, time.Duration, func()) context.CancelFunc {
		if starts.Add(1) == 1 {
			close(started)
			<-release
		}
		return func() { stops.Add(1) }
	}
	cfg := &config.Config{OpenCode: config.OpenCodeConfig{Enabled: true, RefreshSeconds: 60}}

	firstDone := make(chan struct{})
	go func() {
		service.syncOpenCodeCatalog(cfg)
		close(firstDone)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("catalog start did not begin")
	}

	const callers = 32
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			service.syncOpenCodeCatalog(cfg)
		}()
	}
	wg.Wait()
	if got := starts.Load(); got != 1 {
		t.Fatalf("catalog start calls while first start was blocked = %d, want 1", got)
	}
	close(release)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("initial catalog sync did not finish")
	}
	if got := stops.Load(); got != 0 {
		t.Fatalf("catalog stop calls = %d, want 0", got)
	}

	service.stopOpenCodeCatalog()
	if got := stops.Load(); got != 1 {
		t.Fatalf("catalog stop calls after shutdown = %d, want 1", got)
	}
}

func TestSyncOpenCodeCatalogIntervalChangeStopsOldLoop(t *testing.T) {
	service := &Service{}
	var starts atomic.Int32
	stopped := make(chan struct{}, 2)
	service.openCodeCatalogStartFn = func(context.Context, time.Duration, func()) context.CancelFunc {
		starts.Add(1)
		return func() { stopped <- struct{}{} }
	}
	cfg := &config.Config{OpenCode: config.OpenCodeConfig{Enabled: true, RefreshSeconds: 60}}
	service.syncOpenCodeCatalog(cfg)
	cfg.OpenCode.RefreshSeconds = 120
	service.syncOpenCodeCatalog(cfg)

	if got := starts.Load(); got != 2 {
		t.Fatalf("catalog start calls = %d, want 2", got)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("old catalog loop was not stopped after interval change")
	}

	service.stopOpenCodeCatalog()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("current catalog loop was not stopped")
	}
}

func TestSyncOpenCodeCatalogDisabledStopsLoop(t *testing.T) {
	service := &Service{}
	var stops atomic.Int32
	service.openCodeCatalogStartFn = func(context.Context, time.Duration, func()) context.CancelFunc {
		return func() { stops.Add(1) }
	}
	enabled := &config.Config{OpenCode: config.OpenCodeConfig{Enabled: true, RefreshSeconds: 60}}
	service.syncOpenCodeCatalog(enabled)
	service.syncOpenCodeCatalog(&config.Config{})
	if got := stops.Load(); got != 1 {
		t.Fatalf("catalog stop calls after disabling = %d, want 1", got)
	}
	service.syncOpenCodeCatalog(&config.Config{})
	if got := stops.Load(); got != 1 {
		t.Fatalf("disabled sync repeated catalog stop calls = %d, want 1", got)
	}
}

func TestSyncOpenCodeCatalogGlobalProxyChangeRestartsOwnedCatalog(t *testing.T) {
	service := &Service{}
	var starts atomic.Int32
	var stops atomic.Int32
	service.openCodeCatalogStartFn = func(context.Context, time.Duration, func()) context.CancelFunc {
		starts.Add(1)
		return func() { stops.Add(1) }
	}
	cfg := &config.Config{OpenCode: config.OpenCodeConfig{Enabled: true, RefreshSeconds: 60}}
	cfg.ProxyURL = "direct"
	service.syncOpenCodeCatalog(cfg)
	cfg.ProxyURL = "http://proxy.example:8080"
	service.syncOpenCodeCatalog(cfg)
	if got := starts.Load(); got != 2 {
		t.Fatalf("catalog start calls after proxy change = %d, want 2", got)
	}
	if got := stops.Load(); got != 1 {
		t.Fatalf("catalog stop calls after proxy change = %d, want 1", got)
	}
}

func TestSyncOpenCodeCatalogRebindsNativeExecutorAfterOwnedCatalogReplacement(t *testing.T) {
	cfg := &config.Config{OpenCode: config.OpenCodeConfig{Enabled: true, RefreshSeconds: 60}}
	cfg.ProxyURL = "direct"
	service := &Service{
		cfg:         cfg,
		coreManager: coreauth.NewManager(nil, nil, nil),
		openCodeCatalogStartFn: func(context.Context, time.Duration, func()) context.CancelFunc {
			return func() {}
		},
	}
	auth := &coreauth.Auth{
		ID:       "opencode-auth",
		Provider: "opencode",
		Attributes: map[string]string{
			"tier":     "zen",
			"base_url": internalconfig.DefaultOpenCodeZenURL,
			"api_key":  "secret",
		},
	}
	if _, err := service.coreManager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	service.syncOpenCodeCatalog(cfg)
	service.registerExecutorForAuth(auth, true)
	first, ok := service.coreManager.Executor("opencode")
	if !ok {
		t.Fatal("expected OpenCode executor")
	}

	cfg.ProxyURL = "http://proxy.example:8080"
	service.syncOpenCodeCatalog(cfg)
	second, ok := service.coreManager.Executor("opencode")
	if !ok {
		t.Fatal("expected OpenCode executor after catalog replacement")
	}
	if first == second {
		t.Fatal("OpenCode executor still references the replaced catalog")
	}
	if _, ok := second.(*runtimeexecutor.OpenCodeExecutor); !ok {
		t.Fatalf("executor type = %T, want *executor.OpenCodeExecutor", second)
	}
	service.stopOpenCodeCatalog()
}

type reservedOpenCodeExecutor struct{ serviceTestPluginExecutor }

func (reservedOpenCodeExecutor) Identifier() string { return "opencode" }

func TestRegisterExecutorForAuthDoesNotReplaceReservedNonNativeExecutor(t *testing.T) {
	cfg := &config.Config{OpenCode: config.OpenCodeConfig{Enabled: true}}
	manager := coreauth.NewManager(nil, nil, nil)
	reserved := reservedOpenCodeExecutor{}
	manager.RegisterExecutor(reserved)
	service := &Service{cfg: cfg, coreManager: manager}
	auth := &coreauth.Auth{ID: "opencode-auth", Provider: "opencode", Status: coreauth.StatusActive}

	service.registerExecutorForAuth(auth, false)
	got, ok := manager.Executor("opencode")
	if !ok || got != reserved {
		t.Fatalf("reserved executor = %T, want unchanged reserved executor", got)
	}
}
