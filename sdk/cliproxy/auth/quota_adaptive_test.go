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

func TestQuotaLinearScheduleAndFreshness(t *testing.T) {
	zero := 0.0
	policy := config.CodexQuotaGuard{FiveHourJitter: &zero, WeeklyJitter: &zero}
	for _, provider := range []string{"codex", "claude", "xai", "kimi", "antigravity"} {
		for _, used := range []float64{0, 30, 60, 80, 85} {
			m := NewManager(nil, nil, nil)
			m.SetConfig(&config.Config{QuotaGuard: &policy})
			a := &Auth{Provider: provider, Metadata: map[string]any{"access_token": "fixture"}}
			now := time.Now()
			w := CodexQuotaWindow{Name: "short", WindowSeconds: 18000, UsedPercent: used, ObservedAt: now, rateKnown: true}
			snapshot := &CodexQuotaSnapshot{Windows: []CodexQuotaWindow{w}}
			if provider == "codex" {
				a.CodexQuota = snapshot
			} else {
				a.ProviderQuota = snapshot
			}
			a.QuotaGuard = &QuotaGuardStatus{LastAttemptAt: now}
			want := time.Duration(float64(time.Minute) + float64(29*time.Minute)*(90-used)/90)
			if got := quotaGuardInterval(a, policy); (got - want).Abs() > time.Nanosecond {
				t.Fatalf("%s %v: got %v want %v", provider, used, got, want)
			}
			until := now.Add(quotaIntervalCeiling(want) + time.Minute)
			if m.quotaGuardHealthBlocked(a, until.Add(-time.Second)) || !m.quotaGuardHealthBlocked(a, until) {
				t.Fatal("dynamic freshness mismatch")
			}
			snapshot.Windows = append(snapshot.Windows, CodexQuotaWindow{Name: "weekly", WindowSeconds: 604800, UsedPercent: 92, ObservedAt: now, rateKnown: true})
			if quotaGuardInterval(a, policy) >= want {
				t.Fatal("weekly window did not shorten interval")
			}
		}
	}
}

func TestQuotaLinearHistoryAndTraffic(t *testing.T) {
	now := time.Now()
	a := &Auth{Provider: "claude", Metadata: map[string]any{"access_token": "fixture"}}
	policy := config.CodexQuotaGuard{}
	w := CodexQuotaWindow{sampleValid: true, Name: "short", WindowSeconds: 18000, ObservedAt: now, ResetAt: now.Add(time.Hour)}
	if quotaWindowInterval(a, w, policy) != time.Minute {
		t.Fatal("cold observation skipped warmup")
	}
	next := w
	next.ObservedAt = now.Add(time.Minute)
	next = quotaWindowHistory(w, next)
	if !next.rateKnown || !next.idle || quotaWindowInterval(a, next, policy) != 30*time.Minute {
		t.Fatal("stable empty quota did not slow to 30 minutes")
	}
	a.Success = 1
	if quotaWindowInterval(a, next, policy) != time.Minute {
		t.Fatal("resumed activity did not accelerate")
	}
	a.Success = 0
	burst := next
	burst.ObservedAt = now.Add(2 * time.Minute)
	burst.UsedPercent = 40
	burst.requestCount = 10
	burst = quotaWindowHistory(next, burst)
	if quotaWindowInterval(a, burst, policy) != time.Minute {
		t.Fatal("rapid consumption not capped")
	}
	same := burst
	same.requestCount = 30
	same = quotaWindowHistory(burst, same)
	if same.requestCount != 10 || same.ratePerMinute != burst.ratePerMinute {
		t.Fatal("cache hit changed history")
	}
	reset := burst
	reset.ObservedAt = now.Add(3 * time.Minute)
	reset.UsedPercent = 0
	reset.rateKnown = false
	reset.ratePerMinute = 0
	if quotaWindowHistory(burst, reset).rateKnown {
		t.Fatal("reset retained rate")
	}
	next.idle = false
	a.Success = 20
	if quotaWindowInterval(a, next, policy) != time.Minute {
		t.Fatal("request budget ignored")
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
	if next.Sub(now) < time.Minute || next.Sub(now) > 66*time.Second || !next.Equal(header) {
		t.Fatal("scheduler and response deadline disagree")
	}
	a.QuotaGuard = &QuotaGuardStatus{LastAttemptAt: now}
	m.quotaGuardHealthBlocked(a, now)
	if !a.QuotaGuard.NextCheckAt.Equal(next) {
		t.Fatal("UI deadline differs from broker")
	}
}

func TestQuotaLinearTrafficHonorsCooldownAndStableThreshold(t *testing.T) {
	m := NewManager(nil, nil, nil)
	a := &Auth{Provider: "claude", Metadata: map[string]any{"access_token": "fixture", "email": "fixture"}}
	now := time.Now()
	w := CodexQuotaWindow{Name: "short", WindowSeconds: 18000, ObservedAt: now, rateKnown: true, idle: true}
	a.ProviderQuota = &CodexQuotaSnapshot{Windows: []CodexQuotaWindow{w}}
	a.Success = 1
	until := now.Add(10 * time.Minute)
	m.quotaHTTP.accounts = map[string]*quotaHTTPState{quotaHTTPKey(a): {nextRead: now.Add(30 * time.Minute), until: until}}
	if got := m.quotaHTTPNextCheck(a); !got.Equal(until) {
		t.Fatal("traffic bypassed provider cooldown", got)
	}
	before := providerQuotaStopThreshold(a, w, config.CodexQuotaGuard{})
	w.ResetAt = now.Add(7 * 24 * time.Hour)
	if providerQuotaStopThreshold(a.Clone(), w, config.CodexQuotaGuard{}) != before {
		t.Fatal("reset/redraw changed stable threshold")
	}
	// Persisted observations cannot provide runtime rate history after a restart.
	old := w
	old.sampleValid = false
	w.ObservedAt = now.Add(time.Minute)
	w.sampleValid = true
	if quotaWindowHistory(old, w).rateKnown {
		t.Fatal("restart skipped warmup")
	}
}
