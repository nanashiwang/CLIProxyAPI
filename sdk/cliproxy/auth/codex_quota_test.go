package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type quotaTestExecutor struct {
	*poolCaptureExecutor
	calls atomic.Int32
	fetch func(context.Context, *Auth, *http.Request) (*http.Response, error)
}

func (*quotaTestExecutor) Identifier() string { return "codex" }
func (e *quotaTestExecutor) HttpRequest(ctx context.Context, a *Auth, r *http.Request) (*http.Response, error) {
	e.calls.Add(1)
	return e.fetch(ctx, a, r)
}
func quotaResponse(used float64, reset time.Time, allowed bool) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"rate_limit":{"allowed":%t,"primary_window":{"used_percent":%v,"reset_at":%d,"limit_window_seconds":18000}}}`, allowed, used, reset.Unix())))}
}
func newQuotaTestManager(t *testing.T) (*Manager, *quotaTestExecutor, *Auth) {
	t.Helper()
	withQuotaCooldownEnabled(t)
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(0, 0, 0)
	e := &quotaTestExecutor{poolCaptureExecutor: &poolCaptureExecutor{failures: map[string]bool{}}}
	e.fetch = func(_ context.Context, _ *Auth, r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.String() != "https://chatgpt.com/backend-api/wham/usage" || r.Header.Get("Chatgpt-Account-Id") != "account" {
			t.Errorf("unexpected quota request: %s %s", r.Method, r.URL)
		}
		return quotaResponse(0, time.Now().Add(5*time.Hour), true), nil
	}
	m.RegisterExecutor(e)
	a, err := m.Register(context.Background(), &Auth{ID: t.Name(), Provider: "codex", Status: StatusActive, Metadata: map[string]any{"access_token": "test-token", "account_id": "account"}})
	if err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: "pool-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
	return m, e, a
}
func seedQuota(m *Manager, a *Auth, s *CodexQuotaSnapshot) *Auth {
	m.mu.Lock()
	defer m.mu.Unlock()
	live := m.auths[a.ID]
	s.Identity = codexQuotaIdentity(live)
	live.CodexQuota = s.clone()
	live.codexQuotaBlocked = s.Exhausted && !m.cooldownDisabledForAuth(live)
	live.Generation++
	return live.Clone()
}
func quotaWindow(name string, used float64, reset time.Time) CodexQuotaWindow {
	seconds := int64(18000)
	if name == "secondary" {
		seconds = 604800
	}
	return CodexQuotaWindow{Name: name, UsedPercent: used, WindowSeconds: seconds, ResetAt: reset}
}

func TestCodexQuotaResetGraceAndUnchangedWindowThrottle(t *testing.T) {
	m, e, a := newQuotaTestManager(t)
	now := time.Now()
	reset := now.Add(-codexQuotaResetGrace + time.Second)
	seedQuota(m, a, &CodexQuotaSnapshot{ObservedAt: now.Add(-time.Hour), Windows: []CodexQuotaWindow{quotaWindow("primary", 74, reset)}})
	e.fetch = func(context.Context, *Auth, *http.Request) (*http.Response, error) {
		return quotaResponse(74, reset, true), nil
	}
	m.refreshCodexQuotas(context.Background(), now)
	if e.calls.Load() != 0 {
		t.Fatal("first observation bypassed reset grace")
	}
	m.refreshCodexQuotas(context.Background(), now.Add(time.Second))
	if e.calls.Load() != 1 {
		t.Fatal("grace boundary did not refresh")
	}
	for i := 0; i < 10; i++ {
		m.refreshCodexQuotas(context.Background(), now.Add(time.Minute))
	}
	if e.calls.Load() != 1 {
		t.Fatal("expired unchanged reset caused a refresh storm")
	}
	m.refreshCodexQuotas(context.Background(), now.Add(time.Second+codexQuotaRefreshInterval))
	if e.calls.Load() != 2 {
		t.Fatal("periodic retry was lost")
	}
	got, _ := m.GetByID(a.ID)
	if got.CodexQuota.Windows[0].UsedPercent != 74 || !got.CodexQuota.Windows[0].ResetAt.Equal(time.Unix(reset.Unix(), 0)) {
		t.Fatal("wall clock fabricated a reset", got.CodexQuota)
	}
}

func TestCodexQuotaCandidateWindowsAndIndependentResetBoundary(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name      string
		used      float64
		reset     time.Time
		exhausted bool
		want      bool
	}{
		{"zero expired", 0, now.Add(-time.Hour), false, false},
		{"nonzero before grace", 74, now.Add(-time.Minute), false, false},
		{"nonzero at grace", 74, now.Add(-2 * time.Minute), false, true},
		{"normal 100 before grace", 100, now.Add(-time.Minute), false, false},
		{"unknown reset", 74, time.Time{}, false, false},
		{"exhausted far reset", 100, now.Add(7 * 24 * time.Hour), true, true},
		{"exhausted unknown reset", 100, time.Time{}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := quotaWindow("primary", tc.used, tc.reset)
			w.LimitReached = tc.exhausted
			a := &Auth{CodexQuota: &CodexQuotaSnapshot{Exhausted: tc.exhausted, Windows: []CodexQuotaWindow{w}}}
			_, got := codexQuotaRefreshTarget(a, now)
			if got != tc.want {
				t.Fatalf("candidate=%v", got)
			}
		})
	}
	last := &codexQuotaRefreshAttempt{at: now}
	target := now.Add(time.Minute)
	if codexQuotaRefreshDue(last, target, target.Add(2*time.Minute-time.Nanosecond)) {
		t.Fatal("early grace probe")
	}
	if !codexQuotaRefreshDue(last, target, target.Add(2*time.Minute)) {
		t.Fatal("reset grace must override periodic throttle once")
	}
	last.at = target.Add(2 * time.Minute)
	if codexQuotaRefreshDue(last, target, last.at.Add(time.Second)) {
		t.Fatal("repeated reset boundary")
	}
	// Wall-clock rollback must not interpret a negative elapsed interval as due.
	if codexQuotaRefreshDue(last, time.Time{}, last.at.Add(-time.Hour)) {
		t.Fatal("clock rollback retriggered refresh")
	}
}

func TestCodexQuotaEarlyRecoveryAndIndependentWindowAttribution(t *testing.T) {
	m, _, a := newQuotaTestManager(t)
	now := time.Now()
	weekly := quotaWindow("secondary", 100, now.Add(7*24*time.Hour))
	weekly.LimitReached = true
	a = seedQuota(m, a, &CodexQuotaSnapshot{ObservedAt: now.Add(-time.Hour), Exhausted: true, Windows: []CodexQuotaWindow{quotaWindow("primary", 15, now.Add(-time.Hour)), weekly}})
	if !m.applyCodexQuotaRefresh(a, []CodexQuotaWindow{quotaWindow("primary", 0, now.Add(5*time.Hour))}, false, true, now, now) {
		t.Fatal("apply")
	}
	got, _ := m.GetByID(a.ID)
	if !got.CodexQuota.Exhausted || !got.CodexQuota.Windows[1].LimitReached || got.CodexQuota.Windows[0].LimitReached {
		t.Fatal("partial recovery lost weekly attribution", got.CodexQuota)
	}
	m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: "codex", Model: "pool-model", Success: true})
	got, _ = m.GetByID(a.ID)
	if blocked, _, _ := isAuthBlockedForModel(got, "pool-model", now.Add(30*24*time.Hour)); !blocked {
		t.Fatal("time/success cleared confirmed exhaustion")
	}
	// A fresh weekly observation confirms an early upstream reset before the old deadline.
	if !m.applyCodexQuotaRefresh(got, []CodexQuotaWindow{quotaWindow("secondary", 6, weekly.ResetAt)}, false, true, now, now) {
		t.Fatal("apply")
	}
	got, _ = m.GetByID(a.ID)
	if !got.CodexQuota.Exhausted {
		t.Fatal("single unchanged-window snapshot released exhaustion")
	}
	m.applyCodexQuotaRefresh(got, []CodexQuotaWindow{quotaWindow("secondary", 6, weekly.ResetAt)}, false, true, now, now)
	got, _ = m.GetByID(a.ID)
	if got.CodexQuota.Exhausted || got.codexQuotaBlocked {
		t.Fatal("early reset did not recover")
	}
}

func TestCodexQuotaConcurrentReservationAndStaleResultFence(t *testing.T) {
	for _, mutation := range []string{"429", "passive", "reload", "reregister", "token", "disable", "reset"} {
		t.Run(mutation, func(t *testing.T) {
			m, e, a := newQuotaTestManager(t)
			started, release := make(chan struct{}), make(chan struct{})
			e.fetch = func(context.Context, *Auth, *http.Request) (*http.Response, error) {
				close(started)
				<-release
				return quotaResponse(0, time.Now().Add(time.Hour), true), nil
			}
			done := make(chan struct{})
			go func() { m.refreshCodexQuotas(context.Background(), time.Now()); close(done) }()
			<-started
			var wg sync.WaitGroup
			for i := 0; i < 20; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); m.refreshCodexQuotas(context.Background(), time.Now().Add(time.Hour)) }()
			}
			wg.Wait()
			if e.calls.Load() != 1 {
				t.Fatalf("duplicate in-flight probes=%d", e.calls.Load())
			}
			switch mutation {
			case "429":
				m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: "codex", Model: "pool-model", CredentialScope: true, Error: &Error{HTTPStatus: 429, Message: "usage_limit_reached"}})
			case "passive":
				m.observeCodexQuota(a, []CodexQuotaWindow{quotaWindow("primary", 37, time.Now().Add(time.Hour))}, time.Now())
			case "reload":
				_, _ = m.Update(context.Background(), a)
			case "reregister":
				m.Remove(context.Background(), a.ID)
				_, _ = m.Register(context.Background(), a)
			case "token":
				a.Metadata["access_token"] = "new-token"
				_, _ = m.Update(context.Background(), a)
			case "disable":
				a.Disabled = true
				_, _ = m.Update(context.Background(), a)
			case "reset":
				_, _, _ = m.ResetQuota(context.Background(), a.ID)
			}
			close(release)
			<-done
			got, _ := m.GetByID(a.ID)
			if got.CodexQuota != nil && !got.CodexQuota.ObservedAt.IsZero() && got.CodexQuota.Windows[0].UsedPercent == 0 {
				t.Fatal("stale fetch overwrote newer state")
			}
			if mutation == "429" && !got.codexQuotaBlocked {
				t.Fatal("stale allowed fetch erased 429")
			}
		})
	}
}

func TestCodexQuotaEarlyProbeAndEndpointFailureThrottle(t *testing.T) {
	for _, reset := range []time.Time{time.Time{}, time.Now().Add(7 * 24 * time.Hour)} {
		m, e, a := newQuotaTestManager(t)
		now := time.Now()
		seedQuota(m, a, &CodexQuotaSnapshot{Exhausted: true, DeniedResetAt: reset})
		e.fetch = func(context.Context, *Auth, *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("unavailable"))}, nil
		}
		m.refreshCodexQuotas(context.Background(), now)
		m.refreshCodexQuotas(context.Background(), now.Add(time.Minute))
		if e.calls.Load() != 1 {
			t.Fatal("failed probe not throttled")
		}
		e.fetch = func(context.Context, *Auth, *http.Request) (*http.Response, error) {
			return quotaResponse(3, time.Now().Add(time.Hour), true), nil
		}
		m.refreshCodexQuotas(context.Background(), now.Add(codexQuotaRefreshInterval))
		m.refreshCodexQuotas(context.Background(), now.Add(2*codexQuotaRefreshInterval))
		got, _ := m.GetByID(a.ID)
		if got.codexQuotaBlocked || got.CodexQuota.Windows[0].UsedPercent != 3 {
			t.Fatal("periodic early recovery failed")
		}
		if got.Success != 0 || got.Failed != 0 {
			t.Fatal("probe counted as generation traffic")
		}
	}
}

func TestCodexQuotaPassiveUsageOrderingAndNotRecovery(t *testing.T) {
	m, _, a := newQuotaTestManager(t)
	ctx := m.withCodexQuotaObserver(context.Background(), a)
	older := CodexQuotaObserver(ctx, a)
	newer := CodexQuotaObserver(ctx, a)
	headers := func(used string) http.Header {
		return http.Header{"X-Codex-Primary-Used-Percent": {used}, "X-Codex-Primary-Window-Minutes": {"300"}, "X-Codex-Primary-Reset-At": {"100"}}
	}
	newer(headers("15"))
	older(headers("90"))
	got, _ := m.GetByID(a.ID)
	if got.CodexQuota.Windows[0].UsedPercent != 15 {
		t.Fatal("older request regressed observation")
	}
	m.MarkResult(ctx, Result{AuthID: a.ID, Provider: "codex", Model: "pool-model", CredentialScope: true, Error: &Error{HTTPStatus: 429, Message: "usage_limit_reached"}})
	latest := CodexQuotaObserver(ctx, got)
	latest(headers("8"))
	got, _ = m.GetByID(a.ID)
	if !got.codexQuotaBlocked || got.CodexQuota.Windows[0].UsedPercent != 8 {
		t.Fatal("passive usage cleared confirmed denial or lost observed usage")
	}
	clone := got.Clone()
	clone.CodexQuota.Windows[0].UsedPercent = 99
	if got.CodexQuota.Windows[0].UsedPercent != 8 {
		t.Fatal("snapshot clone aliases windows")
	}
}

func TestCodexQuotaPersistenceExpiredUsageAndRecoveryPolicy(t *testing.T) {
	m, _, a := newQuotaTestManager(t)
	store := NewFileCooldownStateStore(t.TempDir())
	m.SetCooldownStateStore(store)
	now := time.Now()
	w := quotaWindow("secondary", 100, now.Add(-time.Hour))
	w.LimitReached = true
	seedQuota(m, a, &CodexQuotaSnapshot{ObservedAt: now.Add(-2 * time.Hour), Exhausted: true, Windows: []CodexQuotaWindow{quotaWindow("primary", 15, now.Add(time.Hour)), w}})
	m.PersistCooldownStates(context.Background())
	restored := NewManager(nil, nil, nil)
	_, _ = restored.Register(context.Background(), a)
	restored.SetCooldownStateStore(store)
	if err := restored.RestoreCooldownStates(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := restored.GetByID(a.ID)
	if got.CodexQuota == nil || got.CodexQuota.Windows[1].UsedPercent != 100 || !got.codexQuotaBlocked || got.CodexQuota.Windows[0].LimitReached {
		t.Fatal("expired snapshot lost on restart", got.CodexQuota)
	}
	restored.SetConfig(&config.Config{DisableCooling: true})
	got, _ = restored.GetByID(a.ID)
	if got.codexQuotaBlocked || got.CodexQuota.Windows[1].UsedPercent != 100 {
		t.Fatal("disable cooling corrupted raw observation")
	}
	restored.SetConfig(&config.Config{})
	got, _ = restored.GetByID(a.ID)
	if !got.codexQuotaBlocked {
		t.Fatal("cooling re-enable ignored observed denial")
	}
	_, _, _ = restored.ResetQuota(context.Background(), a.ID)
	got, _ = restored.GetByID(a.ID)
	if got.CodexQuota.Exhausted || got.codexQuotaBlocked || got.CodexQuota.Windows[1].UsedPercent != 100 {
		t.Fatal("manual reset fabricated observed usage")
	}
	data, err := json.Marshal(got.CodexQuota)
	if err != nil || strings.Contains(string(data), "test-token") {
		t.Fatal("unsafe snapshot serialization")
	}
}

func TestCodexQuotaRecoveryPreservesUnrelatedCooldown(t *testing.T) {
	m, _, a := newQuotaTestManager(t)
	now := time.Now()
	retry := time.Hour
	m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: "codex", Model: "pool-model", CredentialScope: true, RetryAfter: &retry, Error: &Error{HTTPStatus: 429, Message: "usage_limit_reached"}})
	m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: "codex", Model: "other-model", Error: &Error{HTTPStatus: 404, Message: "model not found"}})
	a, _ = m.GetByID(a.ID)
	m.applyCodexQuotaRefresh(a, []CodexQuotaWindow{quotaWindow("primary", 5, now.Add(time.Hour))}, false, true, now, now)
	a, _ = m.GetByID(a.ID)
	m.applyCodexQuotaRefresh(a, []CodexQuotaWindow{quotaWindow("primary", 5, now.Add(time.Hour))}, false, true, now, now)
	got, _ := m.GetByID(a.ID)
	if blocked, _, _ := isAuthBlockedForModel(got, "pool-model", now); blocked {
		t.Fatal("quota model not recovered")
	}
	if blocked, _, _ := isAuthBlockedForModel(got, "other-model", now); !blocked {
		t.Fatal("quota recovery erased model error")
	}
}

func TestCodexQuotaSkipsIneligibleCredentials(t *testing.T) {
	for _, kind := range []string{"expired", "disabled", "api-key", "custom-base", "unauthorized", "home"} {
		t.Run(kind, func(t *testing.T) {
			m, e, a := newQuotaTestManager(t)
			switch kind {
			case "expired":
				a.Metadata["expired"] = time.Now().Add(-time.Hour).Format(time.RFC3339)
			case "disabled":
				a.Disabled = true
			case "api-key":
				a.Attributes = map[string]string{"api_key": "key"}
			case "custom-base":
				a.Attributes = map[string]string{"base_url": "https://custom.invalid"}
			case "unauthorized":
				a.LastError = &Error{HTTPStatus: 401, Message: "unauthorized"}
			case "home":
				m.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
			}
			_, _ = m.Update(context.Background(), a)
			m.refreshCodexQuotas(context.Background(), time.Now())
			if e.calls.Load() != 0 {
				t.Fatal("ineligible credential probed")
			}
		})
	}
}

// Compile-time check: quota traffic uses the existing executor contract.
var _ ProviderExecutor = (*quotaTestExecutor)(nil)

func newQuotaLeaseManager(t *testing.T, single bool) (*Manager, *quotaTestExecutor) {
	t.Helper()
	m, c, e := leaseManager(t)
	for _, a := range m.List() {
		a.Provider = "codex"
		a.Metadata = map[string]any{"access_token": "test-token", "account_id": "account-" + a.ID}
		_, err := m.Register(context.Background(), a)
		if err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: "pool-model"}})
	}
	if single {
		c.AccountPools.Groups[1].CredentialIDs = c.AccountPools.Groups[1].CredentialIDs[:1]
	}
	for i := range c.AccountPools.KeyRules {
		if c.AccountPools.KeyRules[i].KeyHash == config.ClientKeyFingerprint("key-all") {
			c.AccountPools.KeyRules[i].Scope = "selected"
			c.AccountPools.KeyRules[i].GroupIDs = []string{"a"}
		}
	}
	m.SetConfig(c)
	qe := &quotaTestExecutor{poolCaptureExecutor: e}
	qe.fetch = func(context.Context, *Auth, *http.Request) (*http.Response, error) {
		return quotaResponse(4, time.Now().Add(time.Hour), true), nil
	}
	m.RegisterExecutor(qe)
	return m, qe
}

func TestCodexQuotaSingleExclusiveLeaseRecoversWithoutRebindingOrReleasingStream(t *testing.T) {
	m, e := newQuotaLeaseManager(t, true)
	caller := leaseCaller("key-all", "101")
	req := coreexecutor.Request{Model: "pool-model"}
	ctx, done, err := m.beginPoolLease(caller, []string{"codex"}, req, coreexecutor.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	before := ctx.Value(requestPoolLeaseKey{}).(*requestPoolLease).Lease
	producer := make(chan coreexecutor.StreamChunk)
	streamCtx, cancel := context.WithCancel(ctx)
	stream := holdPoolLeaseStream(streamCtx, &coreexecutor.StreamResult{Chunks: producer}, done)
	cancel()
	retry := 7 * 24 * time.Hour
	m.MarkResult(context.Background(), Result{AuthID: before.Credential, Provider: "codex", Model: "pool-model", CredentialScope: true, RetryAfter: &retry, Error: &Error{HTTPStatus: 429, Message: "usage_limit_reached"}})
	a, _ := m.GetByID(before.Credential)
	if a.NextRetryAfter.After(time.Now().Add(time.Hour)) || a.CodexQuota == nil || !a.CodexQuota.Exhausted || a.CodexQuota.DeniedResetAt.Before(time.Now().Add(6*24*time.Hour)) {
		t.Fatal("quota ceiling changed observed reset or failed to bound retry", a)
	}
	if unavailable, status, _, _ := a.AvailabilityView(time.Now().Add(2 * time.Hour)); !unavailable || status != StatusError {
		t.Fatal("quota ceiling hid exhaustion from management")
	}
	if blocked, _, _ := isAuthBlockedForModel(a, "pool-model", time.Now().Add(8*24*time.Hour)); !blocked {
		t.Fatal("old reset silently unblocked exhausted account")
	}
	// Recovery is quota-only; the canceled producer is still alive and owns its slot.
	m.refreshCodexQuotas(context.Background(), time.Now())
	m.refreshCodexQuotas(context.Background(), time.Now().Add(codexQuotaRefreshInterval))
	after, ok, err := m.poolLeaseRuntime.store.Lookup(before.Owner, before.Policy, time.Now())
	if err != nil || !ok || after.ID != before.ID || after.Credential != before.Credential || after.Active != 1 {
		t.Fatalf("probe mutated active lease: %+v %v", after, err)
	}
	continuation := coreexecutor.Request{Model: "pool-model", Payload: []byte(`{"previous_response_id":"same-account-response"}`)}
	if _, err := m.Execute(caller, []string{"codex"}, continuation, coreexecutor.Options{}); err != nil {
		t.Fatalf("safe same-account continuation failed: %v", err)
	}
	e.mu.Lock()
	ids := append([]string(nil), e.ids...)
	e.mu.Unlock()
	if len(ids) != 1 || ids[0] != before.Credential {
		t.Fatal("recovered continuation changed credential", ids)
	}
	if _, err := m.Execute(leaseCaller("key-all", "102"), []string{"codex"}, req, coreexecutor.Options{}); err == nil {
		t.Fatal("recovery let another user steal active exclusive account")
	}
	close(producer)
	for range stream.Chunks {
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		after, _, _ = m.poolLeaseRuntime.store.Lookup(before.Owner, before.Policy, time.Now())
		if after.Active == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("producer completion failed to release occupancy")
}

func TestCodexQuotaRecoveredOldAccountCannotRebindReplacedLease(t *testing.T) {
	m, _ := newQuotaLeaseManager(t, false)
	caller := leaseCaller("key-all", "101")
	req := coreexecutor.Request{Model: "pool-model"}
	if _, err := m.Execute(caller, []string{"codex"}, req, coreexecutor.Options{}); err != nil {
		t.Fatal(err)
	}
	before, _ := m.AccountPoolLeases()
	retry := time.Hour
	m.MarkResult(context.Background(), Result{AuthID: before[0].Credential, Provider: "codex", Model: "pool-model", CredentialScope: true, RetryAfter: &retry, Error: &Error{HTTPStatus: 429, Message: "usage_limit_reached"}})
	continuation := coreexecutor.Request{Model: "pool-model", Payload: []byte(`{"previous_response_id":"old-response"}`)}
	if _, err := m.Execute(caller, []string{"codex"}, continuation, coreexecutor.Options{}); err == nil {
		t.Fatal("exhausted continuation was allowed")
	}
	if _, err := m.Execute(caller, []string{"codex"}, req, coreexecutor.Options{}); err != nil {
		t.Fatal(err)
	}
	replaced, _ := m.AccountPoolLeases()
	if replaced[0].Credential == before[0].Credential || replaced[0].ID == before[0].ID {
		t.Fatal("failover did not replace account")
	}
	m.refreshCodexQuotas(context.Background(), time.Now())
	m.refreshCodexQuotas(context.Background(), time.Now().Add(codexQuotaRefreshInterval))
	after, _ := m.AccountPoolLeases()
	if after[0].ID != replaced[0].ID || after[0].Credential != replaced[0].Credential {
		t.Fatal("quota recovery rebound replaced lease")
	}
	if _, err := m.Execute(caller, []string{"codex"}, continuation, coreexecutor.Options{}); err == nil {
		t.Fatal("recovery allowed old previous_response_id after replacement")
	}
}

func TestCodexQuotaWindowReorderingAndMalformedRefresh(t *testing.T) {
	now := time.Now()
	exhausted := quotaWindow("secondary", 100, now.Add(-time.Hour))
	exhausted.LimitReached = true
	normal := quotaWindow("primary", 15, now.Add(time.Hour))
	incoming := quotaWindow("primary", 86, now.Add(time.Hour))
	incoming.WindowSeconds = 604800
	windows := mergeCodexQuotaWindows([]CodexQuotaWindow{normal, exhausted}, []CodexQuotaWindow{incoming}, true)
	if windows[0].UsedPercent != 15 || windows[0].LimitReached || windows[1].UsedPercent != 86 || windows[1].LimitReached {
		t.Fatal("reordered windows changed attribution", windows)
	}
	for _, payload := range []string{`{}`, `{"rate_limit":{"allowed":true}}`, `{"rate_limit":{"primary_window":{"used_percent":null,"limit_window_seconds":18000}}}`, `{"rate_limit":{"primary_window":{"used_percent":-1,"limit_window_seconds":18000}}}`} {
		if _, _, _, err := parseCodexQuotaPayload([]byte(payload), now); err == nil {
			t.Fatalf("accepted incomplete quota evidence: %s", payload)
		}
	}
	// A displayed 100% is distinct from an explicit provider denial.
	windows, denied, allowed, err := parseCodexQuotaPayload([]byte(`{"rate_limit":{"allowed":true,"primary_window":{"used_percent":100,"limit_window_seconds":18000,"reset_at":100}}}`), now)
	if err != nil || denied || !allowed || windows[0].LimitReached {
		t.Fatal("numeric usage incorrectly marked denied")
	}
}

func TestCodexQuotaOldExecutionCannotDenyReregisteredAccount(t *testing.T) {
	m, _, a := newQuotaTestManager(t)
	oldCtx := m.withCodexQuotaObserver(context.Background(), a)
	m.Remove(context.Background(), a.ID)
	_, _ = m.Register(context.Background(), a)
	m.MarkResult(oldCtx, Result{AuthID: a.ID, Provider: "codex", Model: "pool-model", CredentialScope: true, Error: &Error{HTTPStatus: 429, Message: "usage_limit_reached"}})
	current, _ := m.GetByID(a.ID)
	if current.CodexQuota != nil || current.Quota.Exceeded || current.Failed != 0 {
		t.Fatal("old execution poisoned re-registered auth")
	}
}

func TestCodexQuotaPassivePersistenceIsCoalescedOffStreamPath(t *testing.T) {
	m, _, a := newQuotaTestManager(t)
	store := &recordingCooldownStateStore{}
	m.SetCooldownStateStore(store)
	observer := CodexQuotaObserver(m.withCodexQuotaObserver(context.Background(), a), a)
	for i := 0; i < 10; i++ {
		observer(http.Header{"X-Codex-Primary-Used-Percent": {"30"}, "X-Codex-Primary-Window-Minutes": {"300"}, "X-Codex-Primary-Reset-At": {"100"}})
	}
	if store.saveCount.Load() != 0 {
		t.Fatal("passive stream observation performed synchronous disk I/O")
	}
	m.flushCodexQuotaObservations()
	m.flushCodexQuotaObservations()
	if store.saveCount.Load() != 1 {
		t.Fatal("observations were not coalesced")
	}
}

func TestCodexQuotaRecoveryEvidenceCannotUseOneLaggingSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name          string
		advance       time.Duration
		used          float64
		firstRecovery bool
	}{
		{"same window lag", 0, 86, false},
		{"rolled low use", time.Hour, 5, true},
		{"rolled high use", time.Hour, 86, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, a := newQuotaTestManager(t)
			now := time.Now()
			reset := now.Add(time.Hour)
			seedQuota(m, a, &CodexQuotaSnapshot{ObservedAt: now.Add(-time.Minute), Windows: []CodexQuotaWindow{quotaWindow("primary", 86, reset)}})
			retry := time.Hour
			m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: "codex", Model: "pool-model", RetryAfter: &retry, CredentialScope: true, Error: &Error{HTTPStatus: 429, Message: "usage_limit_reached"}})
			a, _ = m.GetByID(a.ID)
			incoming := []CodexQuotaWindow{quotaWindow("primary", tc.used, reset.Add(tc.advance))}
			m.applyCodexQuotaRefresh(a, incoming, false, true, now, now)
			a, _ = m.GetByID(a.ID)
			if a.CodexQuota.Exhausted == tc.firstRecovery {
				t.Fatal("wrong first confirmation", a.CodexQuota)
			}
			if a.CodexQuota.Windows[0].UsedPercent != tc.used {
				t.Fatal("recovery changed raw usage")
			}
			if tc.advance == 0 {
				// A new real denial invalidates the first confirmation.
				m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: "codex", Model: "pool-model", RetryAfter: &retry, CredentialScope: true, Error: &Error{HTTPStatus: 429, Message: "usage_limit_reached"}})
				a, _ = m.GetByID(a.ID)
				m.applyCodexQuotaRefresh(a, incoming, false, true, now, now)
				a, _ = m.GetByID(a.ID)
				if !a.CodexQuota.Exhausted {
					t.Fatal("new denial reused old recovery evidence")
				}
				m.applyCodexQuotaRefresh(a, incoming, false, true, now, now)
				a, _ = m.GetByID(a.ID)
				if a.CodexQuota.Exhausted {
					t.Fatal("two same-window confirmations failed to recover")
				}
			}
		})
	}
}
