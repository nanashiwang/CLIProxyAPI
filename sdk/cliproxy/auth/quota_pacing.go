package auth

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// quotaPace is immutable once an auth snapshot has been published. The factor
// uses quarter steps. A neutral sample keeps its accounting pool across refreshes.
type quotaPace struct {
	active   bool
	quarters int64
	window   string
	until    time.Time
}

func observedQuotaPace(a *Auth, cfg *config.Config, now time.Time) quotaPace {
	if cfg == nil || !cfg.Routing.QuotaPacing || cfg.Home.Enabled || !codexQuotaOAuth(a) {
		return quotaPace{}
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Routing.Strategy)) {
	case "weighted-round-robin", "weightedroundrobin", "wrr":
	default:
		return quotaPace{}
	}
	if id := cfg.Routing.QuotaPacingPool; id != "" && (cfg.AccountPoolPolicy == nil || cfg.AccountPoolPolicy.GroupForCredential(a.ID) != id) {
		return quotaPace{}
	}
	p := quotaPace{active: true, quarters: 4}
	policy := cfg.EffectiveQuotaGuard()
	if !policy.IsEnabled() || a.QuotaGuard == nil || a.QuotaGuard.State != "ready" || a.CodexQuota == nil || a.CodexQuota.Identity != codexQuotaIdentity(a) || len(a.CodexQuota.Windows) == 0 {
		return p
	}
	best := math.Inf(1)
	until := a.quotaGuardValidUntil
	for _, w := range a.CodexQuota.Windows {
		sample := quotaWindowObserved(a.CodexQuota, w)
		left := w.ResetAt.Sub(sample).Seconds()
		if w.WindowSeconds <= 0 || left <= 0 || left > float64(w.WindowSeconds)+60 || !validCodexUsed(w.UsedPercent) || sample.After(now) {
			return p
		}
		stop := codexQuotaStopThreshold(a, w, policy)
		if stop <= 0 {
			return p
		}
		pace := (1 - w.UsedPercent/stop) * float64(w.WindowSeconds) / left
		if pace < best {
			best = pace
			p.window = w.Name
			if w.WindowSeconds == 18000 {
				p.window = "5h"
			} else if w.WindowSeconds == 604800 {
				p.window = "7d"
			}
		}
		// Old samples can still be eligible under the guard. Avoid steering
		// additional traffic based on observations more than five minutes old.
		if w.ResetAt.Before(until) {
			until = w.ResetAt
		}
		if expiry := sample.Add(5 * time.Minute); expiry.Before(until) {
			until = expiry
		}
	}
	p.quarters = int64(math.Round(4 * min(1.5, max(0.5, best))))
	p.until = until
	return p
}

func (p quotaPace) at(now time.Time) (int64, string) {
	if p.quarters <= 0 || !now.Before(p.until) {
		return 4, ""
	}
	return p.quarters, p.window
}

// QuotaPacing reports the same factor used for weighted dispatch on this snapshot.
func (a *Auth) QuotaPacing(now time.Time) (factor float64, window string, validFor time.Duration, active bool) {
	q, w := a.quotaPace.at(now)
	return float64(q) / 4, w, max(0, a.quotaPace.until.Sub(now)), a.quotaPace.active
}

// A fixed unit keeps credit earned by earlier requests comparable across changes
// in the sum of effective weights. Each reservation retains its signed deltas.
const pacedCreditUnit int64 = 1 << 40

type pacedDelta struct {
	id     string
	epoch  uint64
	amount int64
}

func quotaPacingShares(auths []*Auth, ids []string, weights []int64, now time.Time) []int64 {
	active := false
	for _, a := range auths {
		if a != nil && a.quotaPace.active {
			active = true
			break
		}
	}
	if !active {
		return nil
	}
	shares := make([]int64, len(ids))
	var sum int64
	for i, a := range auths {
		if a == nil {
			continue
		}
		j := i
		if j >= len(ids) || ids[j] != a.ID {
			j = sort.SearchStrings(ids, a.ID)
		}
		if j == len(ids) || ids[j] != a.ID {
			continue
		}
		quarters, _ := a.quotaPace.at(now)
		shares[j] = weights[j] * quarters
		sum += shares[j]
	}
	if sum <= 0 {
		return nil
	}
	var cumulative, allocated int64
	for i, w := range shares {
		cumulative += w
		next := int64(math.Round(float64(cumulative) / float64(sum) * float64(pacedCreditUnit)))
		shares[i] = next - allocated
		allocated = next
	}
	return shares
}

func (s *WeightedRoundRobinSelector) reservePacedLocked(state *weightedPoolState, available []*Auth, ids []string, shares []int64, bound string) (*Auth, *weightedReservation) {
	var picked *Auth
	var best int64
	for _, a := range available {
		if a == nil || (bound != "" && a.ID != bound) {
			continue
		}
		i := sort.SearchStrings(ids, a.ID)
		j, ok := state.findMember(a.ID)
		if !ok || i >= len(ids) || ids[i] != a.ID {
			continue
		}
		m := state.members[j]
		score := saturatingAddInt64(saturatingAddInt64(m.current, m.pending), shares[i])
		if picked == nil || score > best {
			picked, best = a, score
		}
	}
	if picked == nil {
		return nil, nil
	}
	r := &weightedReservation{selector: s, state: state, generation: state.generation, bound: bound != "", paced: make([]pacedDelta, len(ids))}
	for i, id := range ids {
		j, _ := state.findMember(id)
		d := shares[i]
		if id == picked.ID {
			d -= pacedCreditUnit
		}
		r.paced[i] = pacedDelta{id, state.members[j].epoch, d}
	}
	if r.bound && !r.pacedChargeFits() {
		return picked, nil
	}
	for _, d := range r.paced {
		i, _ := state.findMember(d.id)
		m := &state.members[i]
		m.pending += d.amount
		if state.dynamic {
			m.inFlight++
		}
	}
	state.inFlight++
	if state.dynamic {
		state.lastActiveIDs = append([]string(nil), ids...)
	}
	return picked, r
}

func (r *weightedReservation) pacedChargeFits() bool {
	for _, d := range r.paced {
		if i, ok := r.state.findMemberWithEpoch(d.id, d.epoch); ok {
			m := r.state.members[i]
			next := saturatingAddInt64(saturatingAddInt64(m.current, m.pending), d.amount)
			if next > maxSmoothWeightedCreditRounds*pacedCreditUnit || next < -maxSmoothWeightedCreditRounds*pacedCreditUnit {
				return false
			}
		}
	}
	return true
}

func (r *weightedReservation) finishPacedLocked(committed bool) {
	for _, d := range r.paced {
		if i, ok := r.state.findMemberWithEpoch(d.id, d.epoch); ok {
			m := &r.state.members[i]
			m.pending -= d.amount
			if r.state.dynamic {
				m.inFlight--
			}
		}
	}
	if committed && (!r.bound || r.pacedChargeFits()) {
		for _, d := range r.paced {
			if i, ok := r.state.findMemberWithEpoch(d.id, d.epoch); ok {
				r.state.members[i].current = saturatingAddInt64(r.state.members[i].current, d.amount)
			}
		}
	}
	r.state.inFlight--
	if r.state.inFlight == 0 {
		r.state.boundCredits(pacedCreditUnit)
		if r.state.dynamic {
			r.state.pruneDynamicMembers(r.state.lastActiveIDs)
		}
	}
}
