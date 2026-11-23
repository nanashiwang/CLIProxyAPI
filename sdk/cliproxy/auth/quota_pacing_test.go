package auth

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestQuotaPacingHotReloadAndSelection(t *testing.T) {
	for _, affinity := range []bool{false, true} {
		t.Run(fmt.Sprint(affinity), func(t *testing.T) {
			m, _, a := newQuotaTestManager(t)
			b := a.Clone()
			b.ID = a.ID + "-b"
			b.Metadata["account_id"] = "account-b"
			b, err := m.Register(t.Context(), b)
			if err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(b.ID, "codex", []*registry.ModelInfo{{ID: "pool-model"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(b.ID) })
			base := &WeightedRoundRobinSelector{}
			if affinity {
				sticky := NewSessionAffinitySelector(base)
				t.Cleanup(sticky.Stop)
				m.SetSelector(sticky)
			} else {
				m.SetSelector(base)
			}
			cfg := &config.Config{Routing: config.RoutingConfig{Strategy: "weighted-round-robin"}}
			m.SetConfig(cfg)
			now := time.Now()
			m.observeCodexQuota(a, []CodexQuotaWindow{quotaWindow("primary", 15, now.Add(time.Hour))}, now)
			m.observeCodexQuota(b, []CodexQuotaWindow{quotaWindow("primary", 15, now.Add(time.Hour)), quotaWindow("secondary", 75, now.Add(6*24*time.Hour))}, now)
			inspect := func(id string, want float64, window string, active bool) {
				t.Helper()
				snapshot, _ := m.GetByID(id)
				f, w, _, on := snapshot.QuotaPacing(now)
				if on != active || f != want || w != window {
					t.Fatalf("factor/window/active = %v,%q,%t; want %v,%q,%t", f, w, on, want, window, active)
				}
			}
			inspect(a.ID, 1, "", false)
			cfg.Routing.QuotaPacing = true
			m.SetConfig(cfg)
			inspect(a.ID, 1.5, "5h", true)
			inspect(b.ID, 0.5, "7d", true)
			counts := map[string]int{}
			for i := range 40 {
				auth, _, err := m.pickNext(t.Context(), "codex", "pool-model", coreexecutor.Options{Headers: http.Header{"Session_id": {fmt.Sprint(i)}}}, nil)
				if err != nil {
					t.Fatal(err)
				}
				counts[auth.ID]++
			}
			if counts[a.ID] != 30 || counts[b.ID] != 10 {
				t.Fatalf("effective distribution %v", counts)
			}
			if affinity {
				opts := coreexecutor.Options{Headers: http.Header{"Session_id": {"persistent"}}}
				prev, _, err := m.pickNext(t.Context(), "codex", "pool-model", opts, nil)
				if err != nil {
					t.Fatal(err)
				}
				cfg.Routing.QuotaPacing = false
				m.SetConfig(cfg)
				next, _, err := m.pickNext(t.Context(), "codex", "pool-model", opts, nil)
				if err != nil || next.ID != prev.ID {
					t.Fatalf("binding after reload = %v, %v", next, err)
				}
			}
			cfg.Routing.QuotaPacing = true
			cfg.Routing.QuotaPacingPool = "pilot"
			cfg.AccountPools = config.AccountPoolsConfig{Enabled: true, Groups: []config.AccountPoolGroup{{ID: "default", Name: "Default"}, {ID: "pilot", Name: "Pilot", CredentialIDs: []string{a.ID}}}}
			m.SetConfig(cfg)
			inspect(a.ID, 1.5, "5h", true)
			inspect(b.ID, 1, "", false)
			snapshot, _ := m.GetByID(a.ID)
			if q, w := snapshot.quotaPace.at(now.Add(6 * time.Minute)); q != 4 || w != "" {
				t.Fatal("expired snapshot steered routing")
			}
		})
	}
}

func TestQuotaPacingReservationsSurviveObservationChanges(t *testing.T) {
	for _, size := range []int{2, maxExactWeightedPoolMembers + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			w := &WeightedRoundRobinSelector{}
			var auths []*Auth
			for i := range size {
				auths = append(auths, &Auth{ID: fmt.Sprintf("%06d", i), quotaPace: quotaPace{active: true, quarters: 4, until: time.Now().Add(time.Hour)}})
			}
			picked, old := w.reserve("codex:model", auths, auths[0].ID)
			if old == nil || picked != auths[0] {
				t.Fatal("missing sticky reservation")
			}
			auths[0].quotaPace.quarters = 2
			_, recent := w.reserve("codex:model", auths, "")
			if recent == nil || recent.state != old.state {
				t.Fatal("observation replaced accounting state")
			}
			recent.finish(true)
			old.finish(false)
			old.finish(true)
			if old.state.inFlight != 0 {
				t.Fatal("unsettled reservations")
			}
			var sum int64
			for _, member := range old.state.members {
				if member.inFlight != 0 || member.pending != 0 {
					t.Fatal("unsettled member")
				}
				sum += member.current
			}
			if sum != 0 {
				t.Fatal("nonconserving credit")
			}
		})
	}
}

func TestQuotaPacingNeutralSharesAndStickyCompensation(t *testing.T) {
	w := &WeightedRoundRobinSelector{}
	until := time.Now().Add(time.Hour)
	auths := []*Auth{
		{ID: "a", Attributes: map[string]string{AttributeWeight: "3"}, quotaPace: quotaPace{active: true, quarters: 4, until: until}},
		{ID: "b", quotaPace: quotaPace{active: true, quarters: 4, until: until}},
	}
	for range 4 {
		a, r := w.reserve("codex:model", auths, "b")
		if a.ID != "b" {
			t.Fatal("sticky binding moved")
		}
		r.finish(true)
	}
	for range 12 {
		a, r := w.reserve("codex:model", auths, "")
		if a.ID != "a" {
			t.Fatal("cold request missed sticky compensation")
		}
		r.finish(true)
	}
	ref := &WeightedRoundRobinSelector{}
	plain := []*Auth{auths[0].Clone(), auths[1].Clone()}
	for _, a := range plain {
		a.quotaPace = quotaPace{}
	}
	for range 80 {
		a, r := w.reserve("codex:model", auths, "")
		b, original := ref.reserve("codex:model", plain, "")
		r.finish(true)
		original.finish(true)
		if a.ID != b.ID {
			t.Fatal("neutral 3:1 routing lost baseline parity")
		}
	}
}
