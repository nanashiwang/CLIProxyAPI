package auth

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestCodexGuardIndependentWindowsAndRecovery(t *testing.T) {
	m, _, a := newQuotaTestManager(t)
	now := time.Now()
	observe := func(session, week float64) *Auth {
		live, _ := m.GetByID(a.ID)
		m.observeCodexQuota(live, []CodexQuotaWindow{quotaWindow("primary", session, now.Add(5*time.Hour)), quotaWindow("secondary", week, now.Add(7*24*time.Hour))}, time.Now())
		got, _ := m.GetByID(a.ID)
		return got
	}
	for _, tc := range []struct {
		session, week float64
		blocked       bool
	}{{96, 0, true}, {0, 97, true}, {96, 97, true}, {0, 97, true}, {0, 0, false}} {
		got := observe(tc.session, tc.week)
		if got.codexQuotaBlocked != tc.blocked {
			t.Fatalf("%+v: blocked=%v", tc, got.codexQuotaBlocked)
		}
		blocked, _, _ := isAuthBlockedForModel(got, "pool-model", now)
		if blocked != tc.blocked || isCredentialBlocked(got, 1, now) != tc.blocked {
			t.Fatal("selection bypassed quota guard")
		}
		if got.CodexQuota.Exhausted {
			t.Fatal("protective stop must not fabricate upstream exhaustion")
		}
	}
	got := observe(96, 0)
	if len(m.reserveCodexQuotaRefreshes(now)) != 0 {
		t.Fatal("stopped account should wait for its reset")
	}
	enabled := false
	m.SetConfig(&config.Config{CodexQuotaGuard: config.CodexQuotaGuard{Enabled: &enabled}})
	got, _ = m.GetByID(a.ID)
	if got.codexQuotaBlocked {
		t.Fatal("disable did not resume")
	}
	m.SetConfig(&config.Config{DisableCooling: true})
	got, _ = m.GetByID(a.ID)
	if !got.codexQuotaBlocked {
		t.Fatal("cooling bypassed guard")
	}
	_, _, _ = m.ResetQuota(context.Background(), a.ID)
	got, _ = m.GetByID(a.ID)
	if !got.codexQuotaBlocked {
		t.Fatal("manual cooldown reset bypassed observed usage")
	}
	base, _ := m.GetByID(a.ID)
	if !m.applyCodexQuotaRefresh(base, []CodexQuotaWindow{quotaWindow("primary", 0, now.Add(10*time.Hour)), quotaWindow("secondary", 0, now.Add(7*24*time.Hour))}, false, true, time.Now(), time.Now()) {
		t.Fatal("refresh rejected")
	}
	got, _ = m.GetByID(a.ID)
	if got.codexQuotaBlocked {
		t.Fatal("fresh reset did not resume")
	}
}

func TestCodexGuardStableDistribution(t *testing.T) {
	policy := config.CodexQuotaGuard{}
	w := quotaWindow("primary", 90, time.Now())
	min, max := 100.0, 0.0
	for i := 0; i < 1000; i++ {
		a := &Auth{Provider: "codex", Metadata: map[string]any{"account_id": fmt.Sprint(i)}}
		threshold := codexQuotaStopThreshold(a, w, policy)
		if threshold < 85 || threshold > 95 {
			t.Fatal(threshold)
		}
		moved := w
		moved.ResetAt = moved.ResetAt.Add(time.Minute)
		if threshold != codexQuotaStopThreshold(a, moved, policy) {
			t.Fatal("reset clock drift redrew threshold")
		}
		if threshold < min {
			min = threshold
		}
		if threshold > max {
			max = threshold
		}
	}
	if min > 86 || max < 94 {
		t.Fatal("distribution is not spread", min, max)
	}
}
