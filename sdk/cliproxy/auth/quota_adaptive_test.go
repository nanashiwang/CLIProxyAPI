package auth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestQuotaIntermittentFailureRetainsHistory(t *testing.T) {
	m := NewManager(nil, nil, nil)
	a := &Auth{ID: "adaptive", Provider: "claude", Metadata: map[string]any{"access_token": "fixture"}}
	now := time.Now()
	m.quotaHTTP.now = func() time.Time { return now }
	req, _ := http.NewRequest("GET", "https://api.anthropic.com/api/oauth/usage", nil)
	status, body, calls := 429, "limited", 0
	fetch := func() (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	read := func() {
		r, e := m.QuotaHTTPRequest(context.Background(), a, req, fetch)
		if e != nil {
			t.Fatal(e)
		}
		r.Body.Close()
	}
	read()
	state := m.quotaHTTP.accounts[quotaHTTPKey(a)]
	now = state.until.Add(time.Second)
	status = 200
	body = `{"five_hour":{"utilization":5}}`
	read()
	if state.failures != 1 || state.successes != 1 {
		t.Fatal("one success erased backoff")
	}
	before := calls
	read()
	if calls != before || state.successes != 1 {
		t.Fatal("cache counted as recovery")
	}
	now = state.nextRead.Add(time.Second)
	status = 429
	read()
	if state.failures != 2 || state.until.Sub(now) < 4*time.Minute {
		t.Fatal("intermittent failure lost escalation")
	}
	now = state.until.Add(time.Second)
	status = 200
	read()
	now = state.nextRead.Add(time.Second)
	body = `{}`
	read()
	if state.successes != 0 || state.failures != 2 {
		t.Fatal("empty payload fabricated recovery")
	}
	body = `{"five_hour":{"utilization":5}}`
	for i := 0; i < 3; i++ {
		now = state.nextRead.Add(time.Second)
		read()
	}
	if state.failures != 2 {
		t.Fatal("recovery cleared before quiet period")
	}
	now = state.lastFailure.Add(quotaRecoveryPeriod + time.Second)
	read()
	if state.failures != 0 {
		t.Fatal("sustained recovery did not clear history")
	}
}

func TestClaudeQuotaScheduleAndFreshness(t *testing.T) {
	for _, near := range []bool{false, true} {
		m := NewManager(nil, nil, nil)
		a := &Auth{Provider: "claude", Metadata: map[string]any{"access_token": "fixture"}}
		now := time.Now()
		m.quotaHTTP.now = func() time.Time { return now }
		used := 5.0
		if near {
			used = providerQuotaStopThreshold(a, CodexQuotaWindow{Name: "claude:five_hour", WindowSeconds: 18000}, config.CodexQuotaGuard{}) - 5
		}
		a.ProviderQuota = &CodexQuotaSnapshot{Windows: []CodexQuotaWindow{{Name: "claude:five_hour", WindowSeconds: 18000, UsedPercent: used, ObservedAt: now}}}
		a.QuotaGuard = &QuotaGuardStatus{LastAttemptAt: now}
		base := 2 * time.Minute
		if near {
			base = time.Minute
		}
		if quotaGuardInterval(a, config.CodexQuotaGuard{}) != base {
			t.Fatal("wrong Claude interval")
		}
		// Original observations expire even if no further upstream response arrives.
		if m.quotaGuardHealthBlocked(a, now) || !m.quotaGuardHealthBlocked(a, now.Add(2*base)) {
			t.Fatal("wrong provider freshness")
		}
	}
	// Providers without observed throttling keep their original normal/near cadence.
	for _, p := range []string{"codex", "xai", "kimi", "antigravity"} {
		normal, near := quotaGuardIntervals(&Auth{Provider: p})
		if normal != time.Minute || near != 15*time.Second {
			t.Fatal(p)
		}
	}
}

func TestClaudeQuotaNextCheckIncludesJitter(t *testing.T) {
	m := NewManager(nil, nil, nil)
	a := &Auth{Provider: "claude", Metadata: map[string]any{"access_token": "fixture"}}
	now := time.Now()
	m.quotaHTTP.now = func() time.Time { return now }
	req, _ := http.NewRequest("GET", "https://api.anthropic.com/api/oauth/usage", nil)
	r, e := m.QuotaHTTPRequest(context.Background(), a, req, func() (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"five_hour":{"utilization":5}}`))}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	next := m.quotaHTTPNextCheck(a)
	header, _ := time.Parse(time.RFC3339Nano, r.Header.Get("X-CPA-Quota-Next-Check"))
	if next.Sub(now) < 2*time.Minute || next.Sub(now) > 132*time.Second || !next.Equal(header) {
		t.Fatal("scheduler and response deadline disagree")
	}
	a.QuotaGuard = &QuotaGuardStatus{LastAttemptAt: now}
	m.quotaGuardHealthBlocked(a, now)
	if !a.QuotaGuard.NextCheckAt.Equal(next) {
		t.Fatal("UI deadline differs from broker")
	}
}
