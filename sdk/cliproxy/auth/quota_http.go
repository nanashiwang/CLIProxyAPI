package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// quotaHTTPState belongs to a credential identity, across quota and profile URLs.
type quotaHTTPState struct {
	busy     chan struct{}
	until    time.Time
	failures int
	cache    map[string]quotaHTTPResult
}
type quotaHTTPResult struct {
	status            int
	header            http.Header
	body              []byte
	observed, expires time.Time
}
type quotaHTTPStore struct {
	mu       sync.Mutex
	accounts map[string]*quotaHTTPState
	now      func() time.Time
	active   int
	changed  chan struct{}
}

func quotaHTTPKey(a *Auth) string {
	token := providerQuotaToken(a)
	if token == "" {
		token = AccessTokenSHA256(a)
	}
	return fmt.Sprintf("%s:%s:%v:%v", a.Provider, token, a.Metadata["account_id"], a.Metadata["project_id"])
}

// IsQuotaRead restricts shared caching to known read-only provider endpoints.
func IsQuotaRead(a *Auth, req *http.Request) bool {
	if a == nil || req == nil || req.URL == nil || (req.Method != http.MethodGet && !(a.Provider == "antigravity" && req.Method == http.MethodPost)) || req.URL.User != nil || req.Host != "" && req.Host != req.URL.Host {
		return false
	}
	if !providerQuotaEligible(a) && !codexQuotaOAuth(a) {
		return false
	}
	u := req.URL.String()
	switch strings.ToLower(a.Provider) {
	case "antigravity":
		return u == "https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary" || u == "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:retrieveUserQuotaSummary" || u == "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary"
	case "claude":
		return u == "https://api.anthropic.com/api/oauth/usage" || u == "https://api.anthropic.com/api/oauth/profile"
	case "codex":
		if requested := req.Header.Get("Chatgpt-Account-Id"); requested != "" && requested != fmt.Sprint(a.Metadata["account_id"]) {
			return false
		}
		return u == "https://chatgpt.com/backend-api/wham/usage"
	case "kimi":
		return u == "https://api.kimi.com/coding/v1/usages"
	case "xai":
		return u == "https://cli-chat-proxy.grok.com/v1/billing" || u == "https://cli-chat-proxy.grok.com/v1/billing?format=credits"
	}
	return false
}
func quotaRetryAfter(value string, now time.Time) time.Time {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && seconds >= 0 && seconds <= 366*86400 {
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if date, err := http.ParseTime(value); err == nil && date.After(now) {
		return date
	}
	return time.Time{}
}
func quotaBackoff(n int) time.Duration {
	if n > 5 {
		n = 5
	}
	if n < 1 {
		n = 1
	}
	d := 2 * time.Minute * time.Duration(1<<(n-1))
	if d > 30*time.Minute {
		d = 30 * time.Minute
	}
	return d + time.Duration(rand.Int64N(int64(d/5)+1))
}
func quotaHTTPResponse(r quotaHTTPResult, next time.Time) *http.Response {
	h := r.header.Clone()
	if h == nil {
		h = make(http.Header)
	}
	if !r.observed.IsZero() {
		h.Set("X-CPA-Quota-Observed-At", r.observed.UTC().Format(time.RFC3339Nano))
	}
	if !next.IsZero() {
		h.Set("X-CPA-Quota-Next-Check", next.UTC().Format(time.RFC3339Nano))
	}
	return &http.Response{StatusCode: r.status, Header: h, Body: io.NopCloser(bytes.NewReader(r.body))}
}
func quotaHTTPWaiting(until time.Time) *http.Response {
	seconds := int64(time.Until(until).Seconds()) + 1
	if seconds < 1 {
		seconds = 1
	}
	return quotaHTTPResponse(quotaHTTPResult{status: 429, header: http.Header{"Retry-After": []string{strconv.FormatInt(seconds, 10)}}, body: []byte(`{"error":{"message":"Quota check cooling down"}}`)}, until)
}
func (m *Manager) quotaHTTPCooldown(a *Auth) time.Time {
	m.quotaHTTP.mu.Lock()
	defer m.quotaHTTP.mu.Unlock()
	if s := m.quotaHTTP.accounts[quotaHTTPKey(a)]; s != nil {
		return s.until
	}
	return time.Time{}
}
func quotaObservedAt(resp *http.Response, fallback time.Time) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, resp.Header.Get("X-CPA-Quota-Observed-At")); err == nil && t.Before(fallback) {
		return t
	}
	return fallback
}

// QuotaHTTPRequest shares in-flight calls, cached responses and upstream cooldowns.
// The caller retains ownership of auth validation and transport selection.
func (m *Manager) QuotaHTTPRequest(ctx context.Context, a *Auth, req *http.Request, fetch func() (*http.Response, error)) (*http.Response, error) {
	if !IsQuotaRead(a, req) {
		return fetch()
	}
	key := quotaHTTPKey(a)
	// Headers affect account scoping; do not mix custom management requests.
	endpoint := req.URL.String()
	if req.Method == http.MethodPost {
		if req.GetBody == nil {
			return fetch()
		}
		b, e := req.GetBody()
		if e != nil {
			return nil, e
		}
		data, e := io.ReadAll(io.LimitReader(b, 1<<20))
		_ = b.Close()
		if e != nil {
			return nil, e
		}
		endpoint += fmt.Sprintf(":%x", sha256.Sum256(data))
	}
	for {
		now := m.quotaHTTPTime()
		cfg, _ := m.runtimeConfig.Load().(*config.Config)
		policy := cfg.EffectiveQuotaGuard()
		wake := time.Time{}
		if policy.IsEnabled() {
			wake = quotaThresholdWake(a, policy, now)
		}
		m.quotaHTTP.mu.Lock()
		if m.quotaHTTP.accounts == nil {
			m.quotaHTTP.accounts = make(map[string]*quotaHTTPState)
		}
		s := m.quotaHTTP.accounts[key]
		if s == nil {
			for k, v := range m.quotaHTTP.accounts {
				if v.busy != nil || now.Before(v.until) {
					continue
				}
				expired := true
				for _, c := range v.cache {
					if now.Before(c.expires) {
						expired = false
						break
					}
				}
				if expired {
					delete(m.quotaHTTP.accounts, k)
				}
			}
			if len(m.quotaHTTP.accounts) >= 4096 {
				m.quotaHTTP.mu.Unlock()
				return quotaHTTPWaiting(now.Add(time.Minute)), nil
			}
			s = &quotaHTTPState{cache: make(map[string]quotaHTTPResult)}
			m.quotaHTTP.accounts[key] = s
		}
		if now.Before(s.until) {
			until := s.until
			m.quotaHTTP.mu.Unlock()
			return quotaHTTPWaiting(until), nil
		}
		if now.Before(wake) {
			if r, ok := s.cache[endpoint]; ok {
				m.quotaHTTP.mu.Unlock()
				return quotaHTTPResponse(r, wake), nil
			}
			m.quotaHTTP.mu.Unlock()
			return quotaHTTPWaiting(wake), nil
		}
		if r, ok := s.cache[endpoint]; ok && now.Before(r.expires) {
			m.quotaHTTP.mu.Unlock()
			return quotaHTTPResponse(r, r.expires), nil
		}
		if s.busy != nil {
			done := s.busy
			m.quotaHTTP.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if m.quotaHTTP.active >= codexQuotaRefreshWorkers {
			if m.quotaHTTP.changed == nil {
				m.quotaHTTP.changed = make(chan struct{})
			}
			done := m.quotaHTTP.changed
			m.quotaHTTP.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		m.quotaHTTP.active++
		s.busy = make(chan struct{})
		m.quotaHTTP.mu.Unlock()
		resp, err := fetch()
		r := quotaHTTPResult{observed: now}
		if err == nil && resp != nil && resp.Body != nil {
			r.status, r.header = resp.StatusCode, resp.Header.Clone()
			r.body, err = io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
			_ = resp.Body.Close()
			if len(r.body) > 1<<20 {
				err = fmt.Errorf("quota response too large")
			}
		} else if err == nil {
			err = fmt.Errorf("empty quota response")
		}
		m.quotaHTTP.mu.Lock()
		next := time.Time{}
		if ctx.Err() != nil {
			// A cancelled caller must not impose a provider cooldown.
		} else if err != nil || r.status == 429 || r.status >= 500 {
			s.failures++
			next = m.quotaHTTPTime().Add(quotaBackoff(s.failures))
			if deadline := quotaRetryAfter(r.header.Get("Retry-After"), m.quotaHTTPTime()); deadline.After(m.quotaHTTPTime()) {
				next = deadline
			}
			s.until = next
		} else if r.status >= 200 && r.status < 300 {
			if !strings.HasSuffix(req.URL.Path, "/profile") {
				s.failures = 0
			}
			ttl := quotaGuardPollInterval
			if strings.HasSuffix(req.URL.Path, "/profile") {
				ttl = 6 * time.Hour
			} else if cfg, _ := m.runtimeConfig.Load().(*config.Config); cfg != nil {
				copy := a.Clone()
				windows := parseProviderQuota(a.Provider, r.body, now)
				if a.Provider == "codex" {
					windows, _, _, _ = parseCodexQuotaPayload(r.body, now)
				}
				if len(windows) > 0 {
					if a.Provider == "codex" {
						copy.CodexQuota = &CodexQuotaSnapshot{Windows: windows}
					} else {
						copy.ProviderQuota = &CodexQuotaSnapshot{Windows: windows}
					}
					copy.QuotaGuard = nil
				}
				ttl = quotaGuardInterval(copy, cfg.EffectiveQuotaGuard())
			}
			r.expires = now.Add(ttl)
			if len(s.cache) >= 16 {
				var oldest string
				var expiry time.Time
				for k, v := range s.cache {
					if oldest == "" || v.expires.Before(expiry) {
						oldest, expiry = k, v.expires
					}
				}
				delete(s.cache, oldest)
			}
			s.cache[endpoint] = r
			next = r.expires
		}
		close(s.busy)
		s.busy = nil
		m.quotaHTTP.active--
		if m.quotaHTTP.changed != nil {
			close(m.quotaHTTP.changed)
			m.quotaHTTP.changed = nil
		}
		m.quotaHTTP.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return quotaHTTPResponse(r, next), nil
	}
}

func (m *Manager) quotaHTTPTime() time.Time {
	if m.quotaHTTP.now != nil {
		return m.quotaHTTP.now()
	}
	return time.Now()
}

// RecordQuotaReadFailure brings management-originated failures into routing protection.
func (m *Manager) RecordQuotaReadFailure(ctx context.Context, a *Auth, status int) {
	cfg, _ := m.runtimeConfig.Load().(*config.Config)
	if a == nil || m.HomeEnabled() || !cfg.EffectiveQuotaGuard().IsEnabled() || (!providerQuotaEligible(a) && !codexQuotaOAuth(a)) {
		return
	}
	m.finishQuotaGuardFailure(ctx, codexQuotaRefreshJob{auth: a, attempt: &codexQuotaRefreshAttempt{at: time.Now()}}, "http_error", status)
}
