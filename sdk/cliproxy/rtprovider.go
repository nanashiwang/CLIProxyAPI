package cliproxy

import (
	"net/http"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/poo"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// defaultRoundTripperProvider returns a per-auth HTTP RoundTripper based on
// the Auth.ProxyURL value. When PoO is enabled it wraps the direct/proxy
// transport with the Nitro Enclave relay transport.
type defaultRoundTripperProvider struct {
	mu    sync.RWMutex
	cache map[string]http.RoundTripper
	cfg   *config.Config
}

func newDefaultRoundTripperProvider(cfg ...*config.Config) *defaultRoundTripperProvider {
	var current *config.Config
	if len(cfg) > 0 {
		current = cfg[0]
	}
	return &defaultRoundTripperProvider{cache: make(map[string]http.RoundTripper), cfg: current}
}

// RoundTripperFor implements coreauth.RoundTripperProvider.
func (p *defaultRoundTripperProvider) RoundTripperFor(auth *coreauth.Auth) http.RoundTripper {
	if auth == nil {
		return nil
	}
	proxyStr := strings.TrimSpace(auth.ProxyURL)
	if proxyStr == "" && p.cfg != nil {
		proxyStr = strings.TrimSpace(p.cfg.ProxyURL)
	}
	pooEnabled := p.cfg != nil && p.cfg.PoOParentGateway.Enabled
	if proxyStr == "" && !pooEnabled {
		return nil
	}
	cacheKey := auth.ID + "\x00" + proxyStr
	if pooEnabled {
		cacheKey += "\x00poo\x00" + p.cfg.PoOParentGateway.RelayURL()
	}
	p.mu.RLock()
	rt := p.cache[cacheKey]
	p.mu.RUnlock()
	if rt != nil {
		return rt
	}

	var fallback http.RoundTripper = http.DefaultTransport
	if proxyStr != "" {
		transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyStr)
		if errBuild != nil {
			log.Errorf("%v", errBuild)
			return nil
		}
		if transport != nil {
			fallback = transport
		}
	}
	if pooEnabled {
		rt = poo.NewTransport(p.cfg.PoOParentGateway, proxyStr, auth.ID, fallback)
	} else {
		rt = fallback
	}
	p.mu.Lock()
	p.cache[cacheKey] = rt
	p.mu.Unlock()
	return rt
}
