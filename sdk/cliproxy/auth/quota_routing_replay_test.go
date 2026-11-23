package auth

import (
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"math/rand"
	"strconv"
	"testing"
	"time"
)

// Virtual minutes and integer cost units are shared by both arms. Each arm
// maintains its own upstream budgets and receives observations on the same clock.
const (
	replayWeek     = 7 * 24 * 60
	replayAccounts = 6
)

type replayWindow struct {
	period, capacity, stop, used, reset int
}

type replayAccount struct {
	auth               *Auth
	base               int
	upstream, observed [2]replayWindow
	observedAt         int
	finishes           []int
}

type replayScenario struct {
	name                                          string
	batch, stickyPercent, pollMinutes, concurrent int
	weeklyPressure                                bool
}

type replayMetrics struct {
	requests, accepted, unavailable, denied int
	hits, moves, cold                       int
	expired                                 [2]int
}

func (m replayMetrics) String() string {
	return fmt.Sprintf("requests=%d accepted=%d unavailable=%d quota429=%d sticky=%d cold=%d rebind=%d expired5h=%d expired7d=%d",
		m.requests, m.accepted, m.unavailable, m.denied, m.hits, m.cold, m.moves, m.expired[0], m.expired[1])
}

func replayPool(s replayScenario) []replayAccount {
	pool := make([]replayAccount, replayAccounts)
	for i := range pool {
		base := 1 + i%2
		pool[i] = replayAccount{auth: &Auth{ID: strconv.Itoa(i), Provider: "codex", Metadata: map[string]any{"access_token": "fixture", "account_id": strconv.Itoa(i)}, Attributes: map[string]string{AttributeWeight: strconv.Itoa(base)}}, base: base}
		for j, period := range []int{300, replayWeek} {
			capacity := 500 * base
			stopPercent := 90
			if j == 1 {
				capacity *= 34
				stopPercent = 93
				if s.weeklyPressure {
					capacity = capacity * 2 / 3
				}
			}
			reset := period * (i + 1) / (replayAccounts + 1)
			stop := capacity * stopPercent / 100
			used := stop * (period - reset) / period
			pool[i].upstream[j] = replayWindow{period, capacity, stop, used, reset}
		}
		pool[i].observed = pool[i].upstream
	}
	return pool
}

func (a *replayAccount) tick(now int, s replayScenario, m *replayMetrics) {
	active := a.finishes[:0]
	for _, end := range a.finishes {
		if end > now {
			active = append(active, end)
		}
	}
	a.finishes = active
	for j := range a.upstream {
		w := &a.upstream[j]
		if now == w.reset {
			m.expired[j] += max(0, w.stop-w.used)
			w.used = 0
			w.reset += w.period
		}
	}
	if now%s.pollMinutes == 0 {
		a.observed = a.upstream
		a.observedAt = now
	}
}

func (a *replayAccount) eligible(now, concurrent int) bool {
	if len(a.finishes) >= concurrent || now-a.observedAt >= 31 {
		return false
	}
	for _, w := range a.observed {
		if w.used >= w.stop {
			return false
		}
	}
	return true
}

func virtualReplayTime(minute int) time.Time { return time.Unix(int64(minute)*60, 0).UTC() }

func (a *replayAccount) observePace(minute int, cfg *config.Config) {
	now := virtualReplayTime(minute)
	a.auth.QuotaGuard = &QuotaGuardStatus{State: "ready"}
	a.auth.quotaGuardValidUntil = now.Add(31 * time.Minute)
	s := &CodexQuotaSnapshot{Identity: codexQuotaIdentity(a.auth), ObservedAt: now}
	for j, w := range a.observed {
		s.Windows = append(s.Windows, CodexQuotaWindow{Name: []string{"5h", "7d"}[j], UsedPercent: 100 * float64(w.used) / float64(w.capacity), WindowSeconds: int64(w.period) * 60, ResetAt: virtualReplayTime(w.reset), ObservedAt: now})
	}
	a.auth.CodexQuota = s
	a.auth.quotaPace = observedQuotaPace(a.auth, cfg, now)
}

func replayPick(p *WeightedRoundRobinSelector, pool []replayAccount, eligible []int, bound, now int) int {
	auths := make([]*Auth, 0, len(eligible))
	ids := make([]string, 0, len(eligible))
	weights := make([]int64, 0, len(eligible))
	for _, i := range eligible {
		auths = append(auths, pool[i].auth)
		ids = append(ids, pool[i].auth.ID)
		weights = append(weights, int64(pool[i].base))
	}
	id := ""
	if bound >= 0 {
		id = pool[bound].auth.ID
	}
	a, r := p.reservePoolScopedAt("codex:replay", 0, "", auths, weights, ids, id, virtualReplayTime(now))
	r.finish(true)
	index, err := strconv.Atoi(a.ID)
	if err != nil {
		panic(err)
	}
	return index
}

type replayBinding struct{ account, last int }

func runQuotaRoutingReplay(s replayScenario, seed int64, paced bool) replayMetrics {
	pool := replayPool(s)
	var picker WeightedRoundRobinSelector
	zero := 0.0
	cfg := &config.Config{Routing: config.RoutingConfig{Strategy: "weighted-round-robin", QuotaPacing: paced}, QuotaGuard: &config.CodexQuotaGuard{FiveHourJitter: &zero, WeeklyJitter: &zero}}
	bindings := make(map[int]replayBinding)
	rng := rand.New(rand.NewSource(seed))
	var m replayMetrics
	for now := 0; now <= 2*replayWeek; now++ {
		for i := range pool {
			pool[i].tick(now, s, &m)
			if paced && now%s.pollMinutes == 0 {
				pool[i].observePace(now, cfg)
			}
		}
		if now == 2*replayWeek {
			break
		}
		// Identical exogenous draws even when the two policies reject different requests.
		for range s.batch {
			m.requests++
			cost := 1 + rng.Intn(5)
			duration := 1 + rng.Intn(3)
			long := rng.Intn(100) < s.stickyPercent
			session := rng.Intn(8)
			var eligible []int
			binding, known := bindings[session]
			known = long && known && now-binding.last < 60
			bound := -1
			for i := range pool {
				if pool[i].eligible(now, s.concurrent) {
					eligible = append(eligible, i)
					if known && binding.account == i {
						bound = i
					}
				}
			}
			if len(eligible) == 0 {
				m.unavailable++
				continue
			}
			i := replayPick(&picker, pool, eligible, bound, now)
			switch {
			case bound >= 0:
				m.hits++
			case known:
				m.moves++
			default:
				m.cold++
			}
			if long {
				bindings[session] = replayBinding{i, now}
			}
			a := &pool[i]
			denied := false
			for _, w := range a.upstream {
				if w.used+cost > w.capacity {
					denied = true
				}
			}
			if denied {
				m.denied++
				// Model an immediate authoritative quota response.
				a.observed = a.upstream
				a.observedAt = now
				if paced {
					a.observePace(now, cfg)
				}
				continue
			}
			m.accepted++
			a.finishes = append(a.finishes, now+duration)
			for j := range a.upstream {
				a.upstream[j].used += cost
			}
		}
	}
	return m
}

func TestQuotaRoutingReplay(t *testing.T) {
	for _, s := range []replayScenario{
		{"mostly-new", 4, 10, 5, 16, false},
		{"long-sessions", 4, 95, 5, 16, false},
		{"weekly-pressure", 5, 50, 5, 16, true},
		{"delayed-reset", 5, 50, 35, 16, false},
		{"overlap", 10, 75, 5, 3, false},
	} {
		t.Run(s.name, func(t *testing.T) {
			for _, seed := range []int64{17, 43, 89} {
				for _, paced := range []bool{false, true} {
					m := runQuotaRoutingReplay(s, seed, paced)
					if m.accepted+m.denied+m.unavailable != m.requests || m.hits+m.cold+m.moves != m.accepted+m.denied {
						t.Fatalf("request accounting mismatch: %s", m)
					}
					t.Logf("seed=%d paced=%t %s", seed, paced, m)
				}
			}
		})
	}
}
