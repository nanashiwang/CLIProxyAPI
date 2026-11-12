package auth

import (
	"context"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

const (
	codexQuotaResetGrace      = 2 * time.Minute
	codexQuotaRefreshInterval = 30 * time.Minute
	codexQuotaCheckInterval   = 5 * time.Second
	codexQuotaRefreshWorkers  = 4
	codexQuotaRefreshBatch    = 100
)

type codexQuotaRefreshAttempt struct {
	at       time.Time // Retain Go's monotonic clock for interval throttling.
	epoch    uint64
	inFlight bool
}
type codexQuotaRefreshJob struct {
	auth     *Auth
	executor ProviderExecutor
	attempt  *codexQuotaRefreshAttempt
}

func codexQuotaRefreshTarget(a *Auth, now time.Time) (time.Time, bool) {
	s := a.CodexQuota
	if s == nil {
		return time.Time{}, true
	}
	var target time.Time
	for _, w := range s.Windows {
		if w.ResetAt.IsZero() {
			continue
		}
		if s.Exhausted {
			if !w.LimitReached {
				continue
			}
		} else if (w.UsedPercent <= 0 && !w.LimitReached) || now.Before(w.ResetAt.Add(codexQuotaResetGrace)) {
			continue
		}
		if target.IsZero() || w.ResetAt.Before(target) {
			target = w.ResetAt
		}
	}
	if s.Exhausted {
		for _, pending := range s.Recovery {
			if !pending.ResetAt.IsZero() && (target.IsZero() || pending.ResetAt.Before(target)) {
				target = pending.ResetAt
			}
		}
		if target.IsZero() {
			target = s.DeniedResetAt
		}
		return target, true
	}
	return target, !target.IsZero()
}

func codexQuotaRefreshDue(last *codexQuotaRefreshAttempt, target, now time.Time) bool {
	if last == nil {
		return true
	}
	if last.inFlight {
		return false
	}
	if now.Sub(last.at) >= codexQuotaRefreshInterval {
		return true
	}
	due := target.Add(codexQuotaResetGrace)
	return !target.IsZero() && last.at.Before(due) && !now.Before(due)
}

func (m *Manager) reserveCodexQuotaRefreshes(now time.Time) []codexQuotaRefreshJob {
	return m.reserveCodexQuotaRefreshesLimit(now, codexQuotaRefreshBatch)
}
func (m *Manager) reserveCodexQuotaRefreshesLimit(now time.Time, limit int) []codexQuotaRefreshJob {
	if limit <= 0 {
		return nil
	}
	if m.HomeEnabled() {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.codexQuotaRefresh == nil {
		m.codexQuotaRefresh = make(map[string]*codexQuotaRefreshAttempt)
	}
	ids := make([]string, 0, len(m.auths))
	for id := range m.auths {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		left, right := m.codexQuotaRefresh[ids[i]], m.codexQuotaRefresh[ids[j]]
		if left == nil || right == nil {
			if left == nil && right == nil {
				return ids[i] < ids[j]
			}
			return left == nil
		}
		if left.at.Equal(right.at) {
			return ids[i] < ids[j]
		}
		return left.at.Before(right.at)
	})
	var jobs []codexQuotaRefreshJob
	for _, id := range ids {
		a := m.auths[id]
		if (!codexQuotaOAuth(a) && !providerQuotaEligible(a)) || a.Disabled || a.Status == StatusDisabled || hasUnauthorizedAuthFailure(a) {
			continue
		}
		if expiry, ok := a.ExpirationTime(); ok && !expiry.After(now) {
			continue
		}
		executor := m.executors[executorKeyFromAuth(a)]
		if executor == nil {
			continue
		}
		cfg, _ := m.runtimeConfig.Load().(*config.Config)
		policy := cfg.EffectiveQuotaGuard()
		target, candidate := codexQuotaRefreshTarget(a, now)
		if policy.IsEnabled() {
			candidate = true
		}
		if providerQuotaEligible(a) {
			target, candidate = providerQuotaRefreshTarget(a, now)
		}
		if m.codexQuotaGuardBlocked(a) {
			candidate = true
		}
		if !candidate {
			if last := m.codexQuotaRefresh[id]; last != nil && !last.inFlight {
				delete(m.codexQuotaRefresh, id)
			}
			continue
		}
		last := m.codexQuotaRefresh[id]
		if last != nil && last.epoch != a.RegistrationEpoch {
			last = nil
		}
		due := codexQuotaRefreshDue(last, target, now)
		if policy.IsEnabled() && last != nil {
			due = !last.inFlight && (due || now.Sub(last.at) >= quotaGuardInterval(a, policy))
		}
		if now.Before(m.quotaHTTPCooldown(a)) || (policy.IsEnabled() && now.Before(quotaThresholdWake(a, policy, now))) {
			due = false
		}
		if !due {
			continue
		}
		attempt := &codexQuotaRefreshAttempt{at: now, epoch: a.RegistrationEpoch, inFlight: true}
		m.codexQuotaRefresh[id] = attempt
		if policy.IsEnabled() {
			if a.QuotaGuard == nil {
				a.QuotaGuard = &QuotaGuardStatus{}
			}
			a.QuotaGuard.LastAttemptAt = now
			a.codexQuotaBlocked = m.codexQuotaRoutingBlocked(a)
			a.Generation++
		}

		jobs = append(jobs, codexQuotaRefreshJob{auth: a.Clone(), executor: executor, attempt: attempt})
		if len(jobs) >= limit {
			break
		}
	}
	return jobs
}

func (m *Manager) runCodexQuotaRefresh(ctx context.Context) {
	ticker := time.NewTicker(codexQuotaCheckInterval)
	defer ticker.Stop()
	queue := make(chan codexQuotaRefreshJob, codexQuotaRefreshBatch)
	defer close(queue)
	for i := 0; i < codexQuotaRefreshWorkers; i++ {
		go func() {
			for job := range queue {
				m.runQuotaRefreshJob(ctx, job)
			}
		}()
	}
	for {
		if ctx.Err() != nil {
			return
		}
		m.reconcileQuotaGuardHealth(time.Now())
		// Only the coordinator sends; workers can only increase available queue space.
		jobs := m.reserveCodexQuotaRefreshesLimit(time.Now(), cap(queue)-len(queue))
		for _, job := range jobs {
			m.publishCodexQuota(job.auth, false)
			queue <- job
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) runQuotaRefreshJob(ctx context.Context, job codexQuotaRefreshJob) {
	if ctx.Err() == nil {
		m.refreshCodexQuota(ctx, job)
	}
	m.mu.Lock()
	if m.codexQuotaRefresh[job.auth.ID] == job.attempt {
		job.attempt.inFlight = false
	}
	m.mu.Unlock()
}

func (m *Manager) refreshCodexQuotas(ctx context.Context, now time.Time) {
	if ctx.Err() != nil {
		return
	}
	jobs := m.reserveCodexQuotaRefreshes(now)
	queue := make(chan codexQuotaRefreshJob, len(jobs))
	for _, job := range jobs {
		m.publishCodexQuota(job.auth, false)
		queue <- job
	}
	close(queue)
	var wg sync.WaitGroup
	for i := 0; i < codexQuotaRefreshWorkers && i < len(jobs); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range queue {
				m.runQuotaRefreshJob(ctx, job)
			}
		}()
	}
	wg.Wait()
}

func (m *Manager) refreshCodexQuota(ctx context.Context, job codexQuotaRefreshJob) {
	m.mu.RLock()
	current := m.auths[job.auth.ID]
	valid := current != nil && !current.Disabled && current.Status != StatusDisabled && current.RegistrationEpoch == job.auth.RegistrationEpoch && (providerQuotaEligible(job.auth) || current.Generation == job.auth.Generation) && !m.HomeEnabled()
	if valid && providerQuotaEligible(job.auth) {
		valid = providerQuotaToken(current) == providerQuotaToken(job.auth) && providerQuotaIdentity(current) == providerQuotaIdentity(job.auth)
	}
	if valid {
		if expiry, ok := current.ExpirationTime(); ok && !expiry.After(time.Now()) {
			valid = false
		}
	}
	m.mu.RUnlock()
	if !valid {
		return
	}
	if providerQuotaEligible(job.auth) {
		m.refreshProviderQuota(ctx, job)
		return
	}
	category, status, applied := "request_failed", 0, false
	defer func() {
		if !applied {
			m.finishQuotaGuardFailure(ctx, job, category, status)
		}
	}()
	// This GET never acquires/replaces/releases a request lease or opens a response session.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/backend-api/wham/usage", nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "codex-cli")
	if account, ok := job.auth.Metadata["account_id"].(string); ok {
		req.Header.Set("Chatgpt-Account-Id", account)
	}
	if rt := m.roundTripperFor(job.auth); rt != nil {
		ctx = context.WithValue(ctx, roundTripperContextKey{}, rt)
		ctx = context.WithValue(ctx, "cliproxy.roundtripper", rt)
	}
	resp, err := m.QuotaHTTPRequest(ctx, job.auth, req, func() (*http.Response, error) { return job.executor.HttpRequest(ctx, job.auth, req) })
	if err != nil {
		return
	}
	if resp == nil || resp.Body == nil {
		return
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			logEntryWithRequestID(ctx).Debug("failed to close Codex quota response")
		}
	}()
	status = resp.StatusCode
	if resp.StatusCode != http.StatusOK {
		category = "http_error"
		return
	}
	const maxQuotaBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxQuotaBytes+1))
	if err != nil || len(data) > maxQuotaBytes || ctx.Err() != nil {
		category = "invalid_body"
		return
	}
	now := time.Now()
	windows, denied, allowed, err := parseCodexQuotaPayload(data, now)
	if err != nil {
		category = "invalid_payload"
		return
	}
	category = "observation_rejected"
	applied = m.applyCodexQuotaRefresh(job.auth, windows, denied, allowed, quotaObservedAt(resp, job.attempt.at), now)
}
