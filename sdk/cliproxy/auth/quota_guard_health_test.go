package auth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

func TestQuotaGuardFailureRedactionAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		err    error
	}{
		{"unauthorized", 401, "secret-response", nil}, {"rate_limited", 429, "secret-response", nil},
		{"server_failure", 503, "secret-response", nil}, {"malformed", 200, "secret-response", nil},
		{"unknown", 200, `{"active":true}`, nil}, {"transport", 0, "", errors.New("secret-transport-token")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, e, a := newQuotaTestManager(t)
			var output bytes.Buffer
			old := log.StandardLogger().Out
			log.SetOutput(&output)
			defer log.SetOutput(old)
			e.fetch = func(context.Context, *Auth, *http.Request) (*http.Response, error) {
				if tc.err != nil {
					return nil, tc.err
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			}
			now := time.Now()
			refreshQuotaAt(m, context.Background(), now)
			got, _ := m.GetByID(a.ID)
			if !got.codexQuotaBlocked || got.QuotaGuard.State != "query_failed" || got.QuotaGuard.HTTPStatus != tc.status {
				t.Fatalf("unprotected failure: %+v", got.QuotaGuard)
			}
			if strings.Contains(output.String(), "secret-") || strings.Contains(output.String(), "test-token") {
				t.Fatal("sensitive diagnostic output")
			}
			if !strings.Contains(output.String(), "quota guard query failed") {
				t.Fatal("failure was silent")
			}
			e.fetch = func(context.Context, *Auth, *http.Request) (*http.Response, error) {
				return quotaResponse(2, now.Add(time.Hour), true), nil
			}
			refreshQuotaAt(m, context.Background(), now.Add(3*time.Minute))
			got, _ = m.GetByID(a.ID)
			if got.codexQuotaBlocked || got.QuotaGuard.State != "ready" || got.QuotaGuard.Failure != "" {
				t.Fatalf("did not recover: %+v", got.QuotaGuard)
			}
		})
	}
}

func TestQuotaGuardFreshnessAllProviders(t *testing.T) {
	for _, provider := range []string{"codex", "claude", "xai", "kimi", "antigravity"} {
		t.Run(provider, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			a, _ := m.Register(context.Background(), &Auth{ID: provider, Provider: provider, Metadata: map[string]any{"access_token": "fixture", "account_id": "fixture", "project_id": "project"}})
			now := time.Now()
			m.reconcileQuotaGuardHealth(now)
			got, _ := m.GetByID(a.ID)
			if !got.codexQuotaBlocked || got.QuotaGuard.State != "checking" {
				t.Fatal("initial verification did not pause")
			}
			w := []CodexQuotaWindow{{Name: "short", UsedPercent: 2, WindowSeconds: 18000}}
			if provider == "codex" {
				m.applyCodexQuotaRefresh(got, w, false, true, now, now)
			} else {
				m.applyProviderQuotaRefresh(got, w, now, now)
			}
			got, _ = m.GetByID(a.ID)
			if got.codexQuotaBlocked {
				t.Fatal("valid data did not resume")
			}
			blocked, _, _ := isAuthBlockedForModel(got, "model", now.Add(2*time.Minute))
			if !blocked || !isCredentialBlocked(got, 1, now.Add(2*time.Minute)) {
				t.Fatal("request admission reused stale data")
			}
			m.reconcileQuotaGuardHealth(now.Add(2 * time.Minute))
			got, _ = m.GetByID(a.ID)
			if got.QuotaGuard.State != "stale" {
				t.Fatal("stale state not projected")
			}
			disabled := false
			m.SetConfig(&config.Config{QuotaGuard: &config.CodexQuotaGuard{Enabled: &disabled}})
			got, _ = m.GetByID(a.ID)
			if quotaRoutingBlocked(got, now.Add(time.Hour)) {
				t.Fatal("explicit disable did not clear stale guard")
			}
		})
	}
}

func TestQuotaGuardPartialWindowsAndDelayedObservation(t *testing.T) {
	m := NewManager(nil, nil, nil)
	a, _ := m.Register(context.Background(), &Auth{ID: "partial", Provider: "claude", Metadata: map[string]any{"access_token": "fixture"}})
	now := time.Now()
	first := now.Add(-2 * time.Minute)
	m.applyProviderQuotaRefresh(a, []CodexQuotaWindow{{Name: "short", UsedPercent: 5, WindowSeconds: 18000}, {Name: "weekly", UsedPercent: 5, WindowSeconds: 604800}}, first, first)
	m.applyProviderQuotaRefresh(a, []CodexQuotaWindow{{Name: "short", UsedPercent: 5, WindowSeconds: 18000}}, now, now)
	got, _ := m.GetByID(a.ID)
	if !got.codexQuotaBlocked || got.QuotaGuard.State != "stale" {
		t.Fatal("partial success refreshed missing window")
	}
	near := providerQuotaStopThreshold(a, CodexQuotaWindow{Name: "short", WindowSeconds: 18000}, config.CodexQuotaGuard{}) - 5
	m.applyProviderQuotaRefresh(a, []CodexQuotaWindow{{Name: "short", UsedPercent: near, WindowSeconds: 18000}, {Name: "weekly", UsedPercent: 5, WindowSeconds: 604800}}, now.Add(time.Second), now.Add(time.Second))
	got, _ = m.GetByID(a.ID)
	if quotaRoutingBlocked(got, now.Add(30*time.Second)) || !quotaRoutingBlocked(got, now.Add(31*time.Second)) {
		t.Fatal("near-limit lifetime is not 30 seconds")
	}
	// Completion time must not make a slow near-limit observation fresh.
	m2 := NewManager(nil, nil, nil)
	b, _ := m2.Register(context.Background(), &Auth{ID: "slow", Provider: "claude", Metadata: a.Metadata})
	near = providerQuotaStopThreshold(b, CodexQuotaWindow{Name: "short", WindowSeconds: 18000}, config.CodexQuotaGuard{}) - 5
	m2.applyProviderQuotaRefresh(b, []CodexQuotaWindow{{Name: "short", UsedPercent: near, WindowSeconds: 18000}}, now.Add(-time.Minute), now)
	got, _ = m2.GetByID(b.ID)
	if !got.codexQuotaBlocked {
		t.Fatal("slow response received a new freshness lifetime")
	}
}

func TestQuotaGuardAdaptivePolling(t *testing.T) {
	for _, used := range []float64{2, 80, 99} {
		m, e, a := newQuotaTestManager(t)
		now := time.Now()
		e.fetch = func(context.Context, *Auth, *http.Request) (*http.Response, error) {
			return quotaResponse(used, now.Add(time.Hour), true), nil
		}
		refreshQuotaAt(m, context.Background(), now)
		refreshQuotaAt(m, context.Background(), now.Add(14*time.Second))
		if e.calls.Load() != 1 {
			t.Fatal("polled too early")
		}
		refreshQuotaAt(m, context.Background(), now.Add(15*time.Second))
		if used == 80 && e.calls.Load() != 2 {
			t.Fatal("near-limit polling not accelerated")
		}
		if used != 80 && e.calls.Load() != 1 {
			t.Fatal("low/stopped quota polled too often")
		}
		refreshQuotaAt(m, context.Background(), now.Add(time.Minute))
		got, _ := m.GetByID(a.ID)
		if (used != 99 && e.calls.Load() < 2) || (used == 99 && e.calls.Load() != 1) || got.QuotaGuard == nil {
			t.Fatal("periodic checks lost")
		}
	}
}

func TestQuotaGuardSchedulerDeadlineAndHotDisable(t *testing.T) {
	m, _, a := newQuotaTestManager(t)
	now := time.Now()
	m.applyCodexQuotaRefresh(a, []CodexQuotaWindow{quotaWindow("primary", 1, now.Add(time.Hour))}, false, true, now, now)
	// Publish ready, then expire the shared snapshot without a scheduler upsert.
	m.scheduler.mu.Lock()
	meta := m.scheduler.providers["codex"].auths[a.ID]
	meta.auth.quotaGuardValidUntil = now.Add(-time.Second)
	m.scheduler.mu.Unlock()
	if _, err := m.scheduler.pickSingle(context.Background(), "codex", "pool-model", coreexecutor.Options{}, nil); err == nil {
		t.Fatal("scheduler cache bypassed deadline")
	}
	disabled := false
	m.SetConfig(&config.Config{QuotaGuard: &config.CodexQuotaGuard{Enabled: &disabled}})
	if _, err := m.scheduler.pickSingle(context.Background(), "codex", "pool-model", coreexecutor.Options{}, nil); err != nil {
		t.Fatal("hot disable left cached deadline", err)
	}
}

func TestQuotaGuardCoordinatorWhileWorkerBlocked(t *testing.T) {
	m, e, a := newQuotaTestManager(t)
	now := time.Now()
	started := make(chan struct{})
	release := make(chan struct{})
	lateChecked := make(chan struct{}, 1)
	e.fetch = func(ctx context.Context, checked *Auth, _ *http.Request) (*http.Response, error) {
		if checked.ID != a.ID {
			lateChecked <- struct{}{}
			return quotaResponse(1, time.Now().Add(time.Hour), true), nil
		}
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, ctx.Err()
	}
	m.applyCodexQuotaRefresh(a, []CodexQuotaWindow{quotaWindow("primary", 2, now.Add(time.Hour))}, false, true, now.Add(-119*time.Second), now)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { m.runCodexQuotaRefresh(ctx); close(done) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	_, _ = m.Register(context.Background(), &Auth{ID: "late-account", Provider: "codex", Metadata: map[string]any{"access_token": "fixture", "account_id": "late"}})
	deadline := time.After(7 * time.Second)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			close(release)
			t.Fatal("worker stalled freshness coordinator")
		case <-ticker.C:
			got, _ := m.GetByID(a.ID)
			if got.QuotaGuard.State == "stale" {
				select {
				case <-lateChecked:
				case <-time.After(time.Second):
					t.Fatal("slow worker prevented another account refresh")
				}
				cancel()
				close(release)
				<-done
				return
			}
		}
	}
}
