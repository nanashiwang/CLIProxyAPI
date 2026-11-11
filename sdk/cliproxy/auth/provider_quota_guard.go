package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// ProviderQuota reuses normalized windows, but never Codex's upstream-denial state.
func providerQuotaToken(a *Auth) string {
	if a == nil {
		return ""
	}
	if token := AccessTokenSHA256(a); token != "" {
		return token
	}
	if strings.EqualFold(a.Provider, "kimi") && (a.Attributes["api_key"] != "" || a.Attributes["access_token"] != "") {
		token := a.Attributes["access_token"]
		if token == "" {
			token = a.Attributes["api_key"]
		}
		sum := sha256.Sum256([]byte(token))
		return hex.EncodeToString(sum[:])
	}
	return ""
}
func providerQuotaEligible(a *Auth) bool {
	if a == nil || IsPluginVirtualAuth(a) || strings.TrimSpace(a.Attributes["base_url"]) != "" || providerQuotaToken(a) == "" {
		return false
	}
	if base, _ := a.Metadata["base_url"].(string); strings.TrimSpace(base) != "" {
		return false
	}
	switch strings.ToLower(a.Provider) {
	case "claude", "xai", "antigravity":
		return strings.TrimSpace(a.Attributes["api_key"]) == ""
	case "kimi":
		return true
	}
	return false
}
func providerQuotaIdentity(a *Auth) string {
	if !providerQuotaEligible(a) {
		return ""
	}
	identity := providerQuotaToken(a)
	for _, key := range []string{"account_id", "user_id", "sub", "email"} {
		if value, ok := a.Metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			identity = value
			break
		}
	}
	sum := sha256.Sum256([]byte(strings.ToLower(a.Provider) + ":" + identity))
	return hex.EncodeToString(sum[:])
}
func (m *Manager) providerQuotaGuardBlocked(a *Auth) bool {
	if !providerQuotaEligible(a) || a.ProviderQuota == nil || m.HomeEnabled() {
		return false
	}
	cfg, _ := m.runtimeConfig.Load().(*config.Config)
	policy := cfg.EffectiveQuotaGuard()
	if !policy.IsEnabled() {
		return false
	}
	for _, w := range a.ProviderQuota.Windows {
		p, j := policy.Window(w.WindowSeconds)
		if p == 0 {
			continue
		}
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", providerQuotaIdentity(a), w.Name, w.WindowSeconds)))
		unit := float64(binary.BigEndian.Uint64(sum[:8])>>11) / float64(uint64(1)<<53)
		if w.UsedPercent >= p+(2*unit-1)*j {
			return true
		}
	}
	return false
}
func providerQuotaRefreshTarget(a *Auth, now time.Time) (time.Time, bool) {
	var target time.Time
	if a.ProviderQuota != nil {
		for _, w := range a.ProviderQuota.Windows {
			if !w.ResetAt.IsZero() && !now.Before(w.ResetAt.Add(codexQuotaResetGrace)) && (target.IsZero() || w.ResetAt.Before(target)) {
				target = w.ResetAt
			}
		}
	}
	return target, true
}
func (m *Manager) applyProviderQuotaRefresh(base *Auth, windows []CodexQuotaWindow, started, now time.Time) bool {
	if len(windows) == 0 {
		return false
	}
	m.mu.Lock()
	a := m.auths[base.ID]
	if m.HomeEnabled() || a == nil || a.Disabled || a.Status == StatusDisabled || a.RegistrationEpoch != base.RegistrationEpoch || providerQuotaToken(a) != providerQuotaToken(base) || providerQuotaIdentity(a) != providerQuotaIdentity(base) {
		m.mu.Unlock()
		return false
	}
	if a.ProviderQuota != nil && started.Before(a.ProviderQuota.RequestStartedAt) {
		m.mu.Unlock()
		return false
	}
	s := a.ProviderQuota.clone()
	if s == nil {
		s = &CodexQuotaSnapshot{Identity: providerQuotaIdentity(a)}
	}
	// Keep missing/failed sources intact; a missing window never proves recovery.
	for _, w := range windows {
		found := false
		for i := range s.Windows {
			if s.Windows[i].Name == w.Name {
				s.Windows[i] = w
				found = true
				break
			}
		}
		if !found && len(s.Windows) < 128 {
			s.Windows = append(s.Windows, w)
		}
	}
	s.ObservedAt, s.RequestStartedAt = now, started
	a.ProviderQuota = s
	a.codexQuotaBlocked = m.codexQuotaRoutingBlocked(a)
	a.Generation++
	a.UpdatedAt = now
	snapshot := a.Clone()
	m.mu.Unlock()
	m.publishCodexQuota(snapshot, true)
	return true
}

type providerQuotaRequest struct {
	url, body string
	headers   map[string]string
}

func providerQuotaRequests(a *Auth) []providerQuotaRequest {
	switch strings.ToLower(a.Provider) {
	case "claude":
		return []providerQuotaRequest{{url: "https://api.anthropic.com/api/oauth/usage", headers: map[string]string{"anthropic-beta": "oauth-2025-04-20"}}}
	case "kimi":
		return []providerQuotaRequest{{url: "https://api.kimi.com/coding/v1/usages"}}
	case "xai":
		h := map[string]string{"x-xai-token-auth": "xai-grok-cli", "x-grok-client-version": "1.0.70", "User-Agent": "grok-pager/1.0.70 grok-shell/1.0.70 (macos; aarch64)"}
		for _, k := range []string{"sub", "subject", "user_id", "userId"} {
			if id, ok := a.Metadata[k].(string); ok && id != "" {
				h["x-userid"] = id
				break
			}
		}
		return []providerQuotaRequest{{url: "https://cli-chat-proxy.grok.com/v1/billing?format=credits", headers: h}, {url: "https://cli-chat-proxy.grok.com/v1/billing", headers: h}}
	case "antigravity":
		project, _ := a.Metadata["project_id"].(string)
		if project == "" {
			project = a.Attributes["project_id"]
		}
		if project == "" {
			return nil
		}
		body, _ := json.Marshal(map[string]string{"project": project})
		var out []providerQuotaRequest
		for _, host := range []string{"daily-cloudcode-pa.googleapis.com", "daily-cloudcode-pa.sandbox.googleapis.com", "cloudcode-pa.googleapis.com"} {
			out = append(out, providerQuotaRequest{url: "https://" + host + "/v1internal:retrieveUserQuotaSummary", body: string(body), headers: map[string]string{"User-Agent": "antigravity/cli/1.0.13 (aidev_client; os_type=MACOS; arch=ARM64)"}})
		}
		return out
	}
	return nil
}
func (m *Manager) refreshProviderQuota(ctx context.Context, job codexQuotaRefreshJob) {
	cfg, _ := m.runtimeConfig.Load().(*config.Config)
	if !cfg.EffectiveQuotaGuard().IsEnabled() {
		return
	}
	if rt := m.roundTripperFor(job.auth); rt != nil {
		ctx = context.WithValue(ctx, roundTripperContextKey{}, rt)
		ctx = context.WithValue(ctx, "cliproxy.roundtripper", rt)
	}
	var windows []CodexQuotaWindow
	for _, spec := range providerQuotaRequests(job.auth) {
		method := http.MethodGet
		if spec.body != "" {
			method = http.MethodPost
		}
		req, err := http.NewRequestWithContext(ctx, method, spec.url, bytes.NewBufferString(spec.body))
		if err != nil {
			continue
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		for k, v := range spec.headers {
			req.Header.Set(k, v)
		}
		resp, err := job.executor.HttpRequest(ctx, job.auth, req)
		if err != nil {
			continue
		}
		if resp == nil || resp.Body == nil {
			continue
		}
		const maxBytes = 1 << 20
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
		if closeErr := resp.Body.Close(); closeErr != nil {
			logEntryWithRequestID(ctx).Debug("failed to close provider quota response")
		}
		if readErr != nil || len(data) > maxBytes || resp.StatusCode != http.StatusOK || ctx.Err() != nil {
			continue
		}
		parsed := parseProviderQuota(job.auth.Provider, data, time.Now())
		windows = append(windows, parsed...)
		if strings.EqualFold(job.auth.Provider, "antigravity") && len(parsed) > 0 {
			break
		}
	}
	if ctx.Err() == nil {
		m.applyProviderQuotaRefresh(job.auth, windows, job.attempt.at, time.Now())
	}
}
