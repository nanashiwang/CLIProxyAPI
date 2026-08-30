package cliproxy

import (
	"context"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/opencode"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	log "github.com/sirupsen/logrus"
)

const defaultOpenCodeRefreshInterval = 5 * time.Minute

func (s *Service) currentOpenCodeCatalog() *opencode.Catalog {
	if s == nil {
		return nil
	}
	s.openCodeCatalogMu.Lock()
	catalog := s.openCodeCatalog
	s.openCodeCatalogMu.Unlock()
	return catalog
}

func (s *Service) syncOpenCodeCatalog(cfg *config.Config) {
	if s == nil {
		return
	}
	enabled := cfg != nil && cfg.OpenCode.Enabled && !cfg.Home.Enabled
	interval := defaultOpenCodeRefreshInterval
	if enabled && cfg.OpenCode.RefreshSeconds > 0 {
		interval = time.Duration(cfg.OpenCode.RefreshSeconds) * time.Second
	}

	catalogChanged := false
	s.openCodeCatalogMu.Lock()
	if !enabled {
		s.openCodeCatalogGeneration++
		cancel := s.openCodeCatalogCancel
		s.openCodeCatalogCancel = nil
		s.openCodeCatalogInterval = 0
		s.openCodeCatalogMu.Unlock()
		if cancel != nil {
			cancel()
		}
		return
	}
	proxyURL := ""
	if cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}
	if s.openCodeCatalog == nil {
		s.openCodeCatalog = newOpenCodeCatalog(cfg)
		s.openCodeCatalogOwned = true
		s.openCodeCatalogProxy = proxyURL
	} else if s.openCodeCatalogOwned && s.openCodeCatalogProxy != proxyURL {
		// The catalog uses the global proxy for public metadata. Rebuild it when
		// that proxy changes so hot reload does not keep a stale transport.
		s.openCodeCatalog = newOpenCodeCatalog(cfg)
		s.openCodeCatalogProxy = proxyURL
		s.openCodeCatalogInterval = 0
		catalogChanged = true
	}
	// Matching intervals include a loop that is still starting. This prevents
	// concurrent config applications from launching duplicate refresh loops.
	if s.openCodeCatalogInterval == interval {
		s.openCodeCatalogMu.Unlock()
		return
	}
	previousCancel := s.openCodeCatalogCancel
	catalog := s.openCodeCatalog
	startFn := s.openCodeCatalogStartFn
	s.openCodeCatalogCancel = nil
	s.openCodeCatalogInterval = interval
	s.openCodeCatalogGeneration++
	generation := s.openCodeCatalogGeneration
	s.openCodeCatalogMu.Unlock()

	if catalogChanged {
		s.rebindOpenCodeExecutor(cfg, catalog)
	}

	if previousCancel != nil {
		previousCancel()
	}
	var cancel context.CancelFunc
	if startFn != nil {
		cancel = startFn(context.Background(), interval, s.refreshOpenCodeModelRegistrations)
	} else {
		cancel = context.CancelFunc(catalog.Start(context.Background(), interval, s.refreshOpenCodeModelRegistrations))
	}
	if cancel == nil {
		cancel = func() {}
	}

	s.openCodeCatalogMu.Lock()
	if s.openCodeCatalogGeneration != generation || s.openCodeCatalogInterval != interval {
		s.openCodeCatalogMu.Unlock()
		cancel()
		return
	}
	s.openCodeCatalogCancel = cancel
	s.openCodeCatalogMu.Unlock()
	log.Infof("OpenCode model catalog refresh started (interval=%s)", interval)
}

func (s *Service) rebindOpenCodeExecutor(cfg *config.Config, catalog *opencode.Catalog) {
	if s == nil || s.coreManager == nil || cfg == nil || catalog == nil || !cfg.OpenCode.Enabled || cfg.Home.Enabled || !s.hasOpenCodeAuth() {
		return
	}
	s.executorRegistrationMu.Lock()
	defer s.executorRegistrationMu.Unlock()
	existing, ok := s.coreManager.Executor("opencode")
	if !ok {
		return
	}
	if _, ok := existing.(*runtimeexecutor.OpenCodeExecutor); !ok {
		return
	}
	s.coreManager.RegisterExecutor(runtimeexecutor.NewOpenCodeExecutor(cfg, catalog))
}

func (s *Service) stopOpenCodeCatalog() {
	if s == nil {
		return
	}
	s.openCodeCatalogMu.Lock()
	s.openCodeCatalogGeneration++
	cancel := s.openCodeCatalogCancel
	s.openCodeCatalogCancel = nil
	s.openCodeCatalogInterval = 0
	s.openCodeCatalogMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Service) refreshOpenCodeModelRegistrations() {
	if s == nil || s.coreManager == nil {
		return
	}
	catalog := s.currentOpenCodeCatalog()
	if catalog == nil {
		return
	}
	s.cfgMu.RLock()
	enabled := s.cfg != nil && s.cfg.OpenCode.Enabled && !s.cfg.Home.Enabled
	s.cfgMu.RUnlock()
	if !enabled {
		return
	}

	auths := s.coreManager.List()
	openCodeAuths := make([]*coreauth.Auth, 0, len(auths))
	for _, auth := range auths {
		if auth == nil || auth.Disabled || auth.Status == coreauth.StatusDisabled || !strings.EqualFold(strings.TrimSpace(auth.Provider), "opencode") {
			continue
		}
		openCodeAuths = append(openCodeAuths, auth)
	}
	if len(openCodeAuths) == 0 {
		return
	}
	s.registerModelsForAuthBatch(context.Background(), openCodeAuths)
	s.coreManager.RefreshSchedulerAll()
	snapshot := catalog.Snapshot()
	log.Infof("re-registered OpenCode models for %d auth(s) (Zen=%d, Go=%d)", len(openCodeAuths), snapshot.Zen, snapshot.Go)
}

func (s *Service) reconcileOpenCodeConfigAuths(ctx context.Context, cfg *config.Config) {
	if s == nil || s.coreManager == nil || cfg == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	configSynth := synthesizer.NewConfigSynthesizer()
	desired, errSynthesize := configSynth.Synthesize(&synthesizer.SynthesisContext{
		Config:      cfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errSynthesize != nil {
		log.WithError(errSynthesize).Warn("failed to reconcile OpenCode config auths")
		return
	}
	desiredIDs := make(map[string]struct{})
	for _, auth := range desired {
		if isOpenCodeConfigAuth(auth) {
			desiredIDs[auth.ID] = struct{}{}
		}
	}
	openCodeEnabled := cfg.OpenCode.Enabled && !cfg.Home.Enabled
	for _, auth := range s.coreManager.List() {
		if !openCodeEnabled && auth != nil && strings.EqualFold(strings.TrimSpace(auth.Provider), "opencode") {
			GlobalModelRegistry().UnregisterClient(auth.ID)
			if isOpenCodeConfigAuth(auth) {
				s.coreManager.Remove(ctx, auth.ID)
			}
			continue
		}
		if !isOpenCodeConfigAuth(auth) {
			continue
		}
		if _, exists := desiredIDs[auth.ID]; exists {
			continue
		}
		GlobalModelRegistry().UnregisterClient(auth.ID)
		s.coreManager.Remove(ctx, auth.ID)
	}
	if !openCodeEnabled || !s.hasOpenCodeAuth() {
		s.coreManager.UnregisterExecutor("opencode")
	}
}

func (s *Service) hasOpenCodeAuth() bool {
	if s == nil || s.coreManager == nil {
		return false
	}
	for _, auth := range s.coreManager.List() {
		if auth != nil && !auth.Disabled && auth.Status != coreauth.StatusDisabled && strings.EqualFold(strings.TrimSpace(auth.Provider), "opencode") {
			return true
		}
	}
	return false
}

func isOpenCodeConfigAuth(auth *coreauth.Auth) bool {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "opencode") || auth.AuthSourceKind() != coreauth.AuthSourceConfig {
		return false
	}
	if auth.Attributes == nil {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(auth.Attributes["source"])), "config:opencode-")
}
