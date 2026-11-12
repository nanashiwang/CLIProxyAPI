package auth

import (
	"context"
	"math"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
)

const quotaGuardPollInterval = time.Minute
const quotaGuardNearInterval = 15 * time.Second

// QuotaGuardStatus is sanitized runtime health, not an upstream error cooldown.
type QuotaGuardStatus struct {
	State         string    `json:"state"`
	Failure       string    `json:"failure,omitempty"`
	HTTPStatus    int       `json:"http_status,omitempty"`
	NextCheckAt   time.Time `json:"next_check_at,omitempty"`
	LastFailureAt time.Time `json:"-"`
	LastAttemptAt time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt time.Time `json:"last_success_at,omitempty"`
	ValidUntil    time.Time `json:"valid_until,omitempty"`
}

func quotaGuardSnapshot(a *Auth) *CodexQuotaSnapshot {
	if providerQuotaEligible(a) {
		return a.ProviderQuota
	}
	return a.CodexQuota
}

func quotaGuardThreshold(a *Auth, w CodexQuotaWindow, policy config.CodexQuotaGuard) float64 {
	if providerQuotaEligible(a) {
		return providerQuotaStopThreshold(a, w, policy)
	}
	return codexQuotaStopThreshold(a, w, policy)
}

// quotaWindowHistory never learns from repeated cached observations.
func quotaWindowHistory(previous, next CodexQuotaWindow) CodexQuotaWindow {
	next.rateKnown, next.ratePerMinute = false, 0
	if next.ObservedAt.Equal(previous.ObservedAt) {
		next.rateKnown, next.ratePerMinute = previous.rateKnown, previous.ratePerMinute
		next.requestCount, next.idle = previous.requestCount, previous.idle
		return next
	}
	if !previous.sampleValid || previous.ObservedAt.IsZero() || !next.ObservedAt.After(previous.ObservedAt) || next.UsedPercent < previous.UsedPercent ||
		(!previous.ResetAt.IsZero() && !next.ResetAt.IsZero() && next.ResetAt.Sub(previous.ResetAt).Abs() > 2*time.Minute) {
		return next
	}
	next.rateKnown = true
	next.idle = next.requestCount == previous.requestCount
	next.ratePerMinute = math.Max((next.UsedPercent-previous.UsedPercent)/next.ObservedAt.Sub(previous.ObservedAt).Minutes(), previous.ratePerMinute/2)
	return next
}

func quotaWindowInterval(a *Auth, w CodexQuotaWindow, policy config.CodexQuotaGuard) time.Duration {
	threshold := quotaGuardThreshold(a, w, policy)
	if threshold <= 0 || w.UsedPercent >= threshold || !w.rateKnown {
		return time.Minute
	}
	margin := threshold - w.UsedPercent
	interval := time.Duration(float64(time.Minute) + float64(29*time.Minute)*math.Min(1, math.Max(0, margin/threshold)))
	if w.ratePerMinute > 0 {
		// Check four times before predicted exhaustion, bounded by the query floor.
		predicted := margin / w.ratePerMinute / 4 * float64(time.Minute)
		if predicted < float64(interval) {
			interval = time.Duration(math.Max(float64(time.Minute), predicted))
		}
	}
	requests := a.Success + a.Failed
	if requests > w.requestCount && (w.idle || requests-w.requestCount >= 20) {
		interval = time.Minute
	}
	return interval
}

func quotaGuardInterval(a *Auth, policy config.CodexQuotaGuard) time.Duration {
	if a.QuotaGuard != nil && a.QuotaGuard.Failure != "" {
		return time.Minute
	}
	interval := 30 * time.Minute
	s := quotaGuardSnapshot(a)
	if s == nil || len(s.Windows) == 0 {
		return time.Minute
	}
	for _, w := range s.Windows {
		if d := quotaWindowInterval(a, w, policy); d < interval {
			interval = d
		}
	}
	return interval
}

func quotaWindowObserved(s *CodexQuotaSnapshot, w CodexQuotaWindow) time.Time {
	if !w.ObservedAt.IsZero() {
		return w.ObservedAt
	}
	if !s.RequestStartedAt.IsZero() {
		return s.RequestStartedAt
	}
	return s.ObservedAt
}

func quotaIntervalCeiling(d time.Duration) time.Duration {
	return min(d+d/10, 30*time.Minute)
}

func quotaGuardDue(a *Auth, policy config.CodexQuotaGuard, jitter bool) time.Time {
	if a.QuotaGuard != nil && a.QuotaGuard.Failure != "" {
		return a.QuotaGuard.LastAttemptAt.Add(time.Minute)
	}
	var due time.Time
	if s := quotaGuardSnapshot(a); s != nil {
		for _, w := range s.Windows {
			d := quotaWindowInterval(a, w, policy)
			if jitter {
				d = quotaIntervalCeiling(d)
			}
			t := quotaWindowObserved(s, w).Add(d)
			if due.IsZero() || t.Before(due) {
				due = t
			}
		}
	}
	return due
}

// quotaGuardHealthBlocked runs under the manager lock alongside routing state updates.
func (m *Manager) quotaGuardHealthBlocked(a *Auth, now time.Time) bool {
	a.quotaGuardValidUntil = time.Time{}
	if a.QuotaGuard == nil {
		return false
	}
	h := a.QuotaGuard
	previous := h.State
	cfg, _ := m.runtimeConfig.Load().(*config.Config)
	policy := cfg.EffectiveQuotaGuard()
	h.NextCheckAt = quotaGuardDue(a, policy, false)
	if h.NextCheckAt.IsZero() {
		h.NextCheckAt = h.LastAttemptAt.Add(time.Minute)
	}
	if deadline := quotaThresholdWake(a, policy, now); policy.IsEnabled() && deadline.After(h.NextCheckAt) {
		h.NextCheckAt = deadline
	}
	if deadline := m.quotaHTTPNextCheck(a); deadline.After(h.NextCheckAt) {
		h.NextCheckAt = deadline
	}
	blocked := false
	h.ValidUntil = time.Time{}
	switch {
	case m.HomeEnabled() || !policy.IsEnabled() || (!providerQuotaEligible(a) && !codexQuotaOAuth(a)):
		h.State = "disabled"
	case h.Failure != "":
		h.State, blocked = "query_failed", true
	default:
		s := quotaGuardSnapshot(a)
		if s == nil || len(s.Windows) == 0 {
			h.State, blocked = "checking", true
		} else {
			h.State = "ready"
			for _, w := range s.Windows {
				observed := quotaWindowObserved(s, w)
				until := observed.Add(quotaIntervalCeiling(quotaWindowInterval(a, w, policy)) + time.Minute)
				if h.ValidUntil.IsZero() || until.Before(h.ValidUntil) {
					h.ValidUntil = until
				}
			}
			a.quotaGuardValidUntil = h.ValidUntil
			if !now.Before(h.ValidUntil) {
				h.State, blocked = "stale", true
			}
			for _, w := range s.Windows {
				if w.UsedPercent >= quotaGuardThreshold(a, w, policy) {
					h.State, blocked = "threshold", true
					break
				}
			}
		}
	}
	if previous != h.State {
		log.WithFields(log.Fields{"auth_index": a.Index, "provider": a.Provider, "state": h.State, "previous_state": previous, "valid_until": h.ValidUntil}).Info("quota guard state changed")
	}
	return blocked
}

func quotaRoutingBlocked(a *Auth, now time.Time) bool {
	return a.codexQuotaBlocked || (!a.quotaGuardValidUntil.IsZero() && !now.Before(a.quotaGuardValidUntil))
}

func quotaGuardObserved(a *Auth, started, now time.Time) {
	if a.QuotaGuard == nil {
		a.QuotaGuard = &QuotaGuardStatus{}
	}
	h := a.QuotaGuard
	// An older passive response must not erase a newer failed check.
	if started.Before(h.LastFailureAt) && h.Failure != "" {
		return
	}
	h.Failure, h.HTTPStatus = "", 0
	h.LastSuccessAt = now
}

func (m *Manager) logQuotaGuardObservation(a *Auth) {
	cfg, _ := m.runtimeConfig.Load().(*config.Config)
	policy := cfg.EffectiveQuotaGuard()
	s := quotaGuardSnapshot(a)
	if s == nil {
		return
	}
	for _, w := range s.Windows {
		log.WithFields(log.Fields{"auth_index": a.Index, "provider": a.Provider, "window": w.Name, "used_percent": w.UsedPercent, "stop_percent": quotaGuardThreshold(a, w, policy), "reset_at": w.ResetAt, "observed_at": w.ObservedAt, "next_check_at": a.QuotaGuard.NextCheckAt}).Info("quota guard observation")
	}
}

// finishQuotaGuardFailure never records response bodies, credentials or raw transport errors.
func (m *Manager) finishQuotaGuardFailure(ctx context.Context, job codexQuotaRefreshJob, category string, status int) {
	if ctx.Err() != nil {
		return
	}
	m.mu.Lock()
	a := m.auths[job.auth.ID]
	if a == nil || a.Disabled || a.RegistrationEpoch != job.auth.RegistrationEpoch || AccessTokenSHA256(a) != AccessTokenSHA256(job.auth) || providerQuotaToken(a) != providerQuotaToken(job.auth) {
		m.mu.Unlock()
		return
	}
	if s := quotaGuardSnapshot(a); s != nil && !s.RequestStartedAt.Before(job.attempt.at) {
		m.mu.Unlock()
		return
	}
	if a.QuotaGuard == nil {
		a.QuotaGuard = &QuotaGuardStatus{}
	}
	if a.QuotaGuard.LastAttemptAt.After(job.attempt.at) {
		m.mu.Unlock()
		return
	}
	a.QuotaGuard.LastAttemptAt = job.attempt.at
	a.QuotaGuard.LastFailureAt = job.attempt.at
	a.QuotaGuard.Failure, a.QuotaGuard.HTTPStatus = category, status
	a.codexQuotaBlocked = m.codexQuotaRoutingBlocked(a)
	a.Generation++
	snapshot := a.Clone()
	m.mu.Unlock()
	log.WithFields(log.Fields{"auth_index": snapshot.Index, "provider": snapshot.Provider, "category": category, "http_status": status, "next_check_at": snapshot.QuotaGuard.NextCheckAt}).Warn("quota guard query failed")
	m.publishCodexQuota(snapshot, false)
}

// reconcileQuotaGuardHealth is independent of network workers, including hung requests.
func (m *Manager) reconcileQuotaGuardHealth(now time.Time) {
	var snapshots []*Auth
	m.mu.Lock()
	for _, a := range m.auths {
		if a.QuotaGuard == nil {
			cfg, _ := m.runtimeConfig.Load().(*config.Config)
			if m.HomeEnabled() || !cfg.EffectiveQuotaGuard().IsEnabled() || (!providerQuotaEligible(a) && !codexQuotaOAuth(a)) {
				continue
			}
			a.QuotaGuard = &QuotaGuardStatus{}
		}
		previous, deadline, state := a.codexQuotaBlocked, a.quotaGuardValidUntil, a.QuotaGuard.State
		blocked := m.codexQuotaRoutingBlockedAt(a, now)
		if previous != blocked || !deadline.Equal(a.quotaGuardValidUntil) || state != a.QuotaGuard.State {
			a.codexQuotaBlocked = blocked
			a.Generation++
			a.UpdatedAt = now
			snapshots = append(snapshots, a.Clone())
		}
	}
	m.mu.Unlock()
	for _, a := range snapshots {
		m.publishCodexQuota(a, false)
	}
}

// Known stopped windows need no new observations until every blocking reset is due.
func quotaThresholdWake(a *Auth, policy config.CodexQuotaGuard, now time.Time) time.Time {
	var deadline time.Time
	if s := quotaGuardSnapshot(a); s != nil {
		for _, w := range s.Windows {
			if w.UsedPercent < quotaGuardThreshold(a, w, policy) {
				continue
			}
			if w.ResetAt.IsZero() {
				continue
			}
			due := w.ResetAt.Add(codexQuotaResetGrace)
			if due.After(now) && due.After(deadline) {
				deadline = due
			}
		}
	}
	return deadline
}
