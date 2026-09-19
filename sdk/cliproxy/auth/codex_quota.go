package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// CodexQuotaWindow preserves upstream usage even after its reset deadline passes.
type CodexQuotaWindow struct {
	Name          string    `json:"name"`
	UsedPercent   float64   `json:"used_percent"`
	WindowSeconds int64     `json:"window_seconds"`
	ResetAt       time.Time `json:"reset_at"`
	LimitReached  bool      `json:"limit_reached"`
}

// CodexQuotaSnapshot separates observed usage from confirmed routing exhaustion.
type CodexQuotaRecoveryWindow struct {
	WindowSeconds int64     `json:"window_seconds"`
	ResetAt       time.Time `json:"reset_at"`
	Candidate     bool      `json:"candidate,omitempty"`
}

type CodexQuotaSnapshot struct {
	Recovery         []CodexQuotaRecoveryWindow `json:"recovery,omitempty"`
	ObservedAt       time.Time                  `json:"observed_at"`
	RequestStartedAt time.Time                  `json:"request_started_at"`
	Identity         string                     `json:"identity"`
	Windows          []CodexQuotaWindow         `json:"windows"`
	Exhausted        bool                       `json:"exhausted"`
	DeniedResetAt    time.Time                  `json:"denied_reset_at,omitempty"`
}

func (s *CodexQuotaSnapshot) clone() *CodexQuotaSnapshot {
	if s == nil {
		return nil
	}
	out := *s
	out.Windows = append([]CodexQuotaWindow(nil), s.Windows...)
	out.Recovery = append([]CodexQuotaRecoveryWindow(nil), s.Recovery...)
	return &out
}

func codexQuotaIdentity(a *Auth) string {
	if a == nil || !strings.EqualFold(a.Provider, "codex") {
		return ""
	}
	account, _ := a.Metadata["account_id"].(string)
	if account == "" {
		return AccessTokenSHA256(a)
	}
	sum := sha256.Sum256([]byte("codex-account:" + account))
	return hex.EncodeToString(sum[:])
}

func codexQuotaOAuth(a *Auth) bool {
	return a != nil && strings.EqualFold(a.Provider, "codex") &&
		strings.TrimSpace(a.Attributes["api_key"]) == "" && strings.TrimSpace(a.Attributes["base_url"]) == "" &&
		!IsPluginVirtualAuth(a) && AccessTokenSHA256(a) != ""
}

type codexQuotaObserverKey struct{}
type codexQuotaExecution struct {
	manager  *Manager
	authID   string
	epoch    uint64
	identity string
}

func codexQuotaExecutionMatches(ctx context.Context, a *Auth) bool {
	execution, _ := ctx.Value(codexQuotaObserverKey{}).(*codexQuotaExecution)
	return execution == nil || a == nil || a.ID != execution.authID || (a.RegistrationEpoch == execution.epoch && codexQuotaIdentity(a) == execution.identity)
}

func (m *Manager) withCodexQuotaObserver(ctx context.Context, a *Auth) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if !codexQuotaOAuth(a) {
		return ctx
	}
	return context.WithValue(ctx, codexQuotaObserverKey{}, &codexQuotaExecution{manager: m, authID: a.ID, epoch: a.RegistrationEpoch, identity: codexQuotaIdentity(a)})
}

// CodexQuotaObserver binds passive observations to the actual credential epoch.
// It is absent for Home dispatch, API keys, and executors outside this manager.
func CodexQuotaObserver(ctx context.Context, a *Auth) func(http.Header) {
	if ctx == nil || !codexQuotaOAuth(a) {
		return nil
	}
	execution, _ := ctx.Value(codexQuotaObserverKey{}).(*codexQuotaExecution)
	if execution == nil || !codexQuotaExecutionMatches(ctx, a) {
		return nil
	}
	m := execution.manager
	if m == nil || m.HomeEnabled() {
		return nil
	}
	base, started := a.Clone(), time.Now()
	return func(headers http.Header) {
		windows := codexQuotaHeaderWindows(headers, time.Now())
		if len(windows) == 0 {
			return
		}
		m.observeCodexQuota(base, windows, started)
	}
}

func codexQuotaHeaderWindows(h http.Header, now time.Time) []CodexQuotaWindow {
	if active := strings.TrimSpace(h.Get("X-Codex-Active-Limit")); active != "" && !strings.EqualFold(active, "codex") {
		return nil
	}
	var windows []CodexQuotaWindow
	for _, name := range []string{"primary", "secondary"} {
		prefix := "X-Codex-" + name + "-"
		used, errUsed := strconv.ParseFloat(h.Get(prefix+"Used-Percent"), 64)
		minutes, errMinutes := strconv.ParseInt(h.Get(prefix+"Window-Minutes"), 10, 64)
		if errUsed != nil || errMinutes != nil || !validCodexUsed(used) || minutes <= 0 || minutes > 525600 {
			continue
		}
		reset := time.Time{}
		if sec, err := strconv.ParseInt(h.Get(prefix+"Reset-At"), 10, 64); err == nil && sec > 0 && sec <= 253402300799 {
			reset = time.Unix(sec, 0)
		} else if sec, err := strconv.ParseInt(h.Get(prefix+"Reset-After-Seconds"), 10, 64); err == nil && sec >= 0 && sec <= 366*86400 {
			reset = now.Add(time.Duration(sec) * time.Second).Round(0)
		}
		windows = append(windows, CodexQuotaWindow{Name: name, UsedPercent: used, WindowSeconds: minutes * 60, ResetAt: reset})
	}
	return windows
}

func validCodexUsed(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }

func (m *Manager) observeCodexQuota(base *Auth, windows []CodexQuotaWindow, started time.Time) {
	now := time.Now()
	m.mu.Lock()
	a := m.auths[base.ID]
	if m.HomeEnabled() || a == nil || a.Disabled || a.Status == StatusDisabled || a.RegistrationEpoch != base.RegistrationEpoch || AccessTokenSHA256(a) != AccessTokenSHA256(base) || codexQuotaIdentity(a) != codexQuotaIdentity(base) {
		m.mu.Unlock()
		return
	}
	previous := a.CodexQuota
	if previous != nil && started.Before(previous.RequestStartedAt) {
		m.mu.Unlock()
		return
	}
	s := previous.clone()
	if s == nil {
		s = &CodexQuotaSnapshot{Identity: codexQuotaIdentity(a)}
	}
	s.Windows = mergeCodexQuotaWindows(s.Windows, windows, false)
	s.ObservedAt, s.RequestStartedAt = now, started
	a.CodexQuota = s
	a.Generation++
	a.UpdatedAt = now
	snapshot := a.Clone()
	m.mu.Unlock()
	m.publishCodexQuota(snapshot, false)
}

func mergeCodexQuotaWindows(previous, incoming []CodexQuotaWindow, authoritative bool) []CodexQuotaWindow {
	out := append([]CodexQuotaWindow(nil), previous...)
	for _, w := range incoming {
		found := false
		for i := range out {
			if out[i].WindowSeconds != w.WindowSeconds {
				continue
			}
			// Match window duration so a primary/secondary reorder cannot transfer exhaustion.
			if out[i].LimitReached && (!authoritative || w.UsedPercent >= 100) {
				w.LimitReached = true
			}
			out[i], found = w, true
			break
		}
		if !found {
			out = append(out, w)
		}
	}
	return out
}

func (m *Manager) markCodexQuotaFailureLocked(a *Auth, result Result, now time.Time) {
	if !codexQuotaOAuth(a) || m.HomeEnabled() || result.Success || result.Error == nil || result.Error.HTTPStatus != http.StatusTooManyRequests || !poolLeaseTerminalFailure(result.Error) {
		return
	}
	if a.CodexQuota == nil {
		a.CodexQuota = &CodexQuotaSnapshot{Identity: codexQuotaIdentity(a)}
	}
	s := a.CodexQuota
	s.Exhausted = true
	// An in-flight success cannot erase a later denial or its window attribution.
	s.RequestStartedAt = now
	if result.RetryAfter != nil {
		s.DeniedResetAt = now.Add(*result.RetryAfter).Round(0)
	}
	for i := range s.Windows {
		if s.Windows[i].UsedPercent >= 100 {
			s.Windows[i].LimitReached = true
		}
	}
	s.resetRecovery()
	a.codexQuotaBlocked = !m.cooldownDisabledForAuth(a)
}

type codexQuotaPayload struct {
	RateLimit *struct {
		Allowed      *bool                    `json:"allowed"`
		LimitReached *bool                    `json:"limit_reached"`
		Primary      *codexQuotaPayloadWindow `json:"primary_window"`
		Secondary    *codexQuotaPayloadWindow `json:"secondary_window"`
	} `json:"rate_limit"`
}
type codexQuotaPayloadWindow struct {
	Used       *float64 `json:"used_percent"`
	Seconds    int64    `json:"limit_window_seconds"`
	ResetAt    int64    `json:"reset_at"`
	ResetAfter *int64   `json:"reset_after_seconds"`
}

func parseCodexQuotaPayload(data []byte, now time.Time) ([]CodexQuotaWindow, bool, bool, error) {
	var payload codexQuotaPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, false, false, fmt.Errorf("invalid Codex quota JSON")
	}
	rate := payload.RateLimit
	if rate == nil {
		return nil, false, false, fmt.Errorf("missing Codex quota rate_limit")
	}
	denied := (rate.Allowed != nil && !*rate.Allowed) || (rate.LimitReached != nil && *rate.LimitReached)
	allowed := rate.Allowed != nil && *rate.Allowed && !denied
	var windows []CodexQuotaWindow
	for i, raw := range []*codexQuotaPayloadWindow{rate.Primary, rate.Secondary} {
		if raw == nil {
			continue
		}
		if raw.Used == nil || !validCodexUsed(*raw.Used) || raw.Seconds <= 0 || raw.Seconds > 366*86400 {
			return nil, false, false, fmt.Errorf("invalid Codex quota window")
		}
		reset := time.Time{}
		if raw.ResetAt > 253402300799 {
			return nil, false, false, fmt.Errorf("invalid Codex quota reset")
		}
		if raw.ResetAt > 0 {
			reset = time.Unix(raw.ResetAt, 0)
		} else if raw.ResetAfter != nil && *raw.ResetAfter >= 0 && *raw.ResetAfter <= 366*86400 {
			reset = now.Add(time.Duration(*raw.ResetAfter) * time.Second).Round(0)
		}
		name := "primary"
		if i == 1 {
			name = "secondary"
		}
		windows = append(windows, CodexQuotaWindow{Name: name, UsedPercent: *raw.Used, WindowSeconds: raw.Seconds, ResetAt: reset, LimitReached: denied && *raw.Used >= 100})
	}
	if len(windows) == 0 {
		return nil, false, false, fmt.Errorf("missing Codex quota windows")
	}
	return windows, denied, allowed, nil
}

func (m *Manager) applyCodexQuotaRefresh(base *Auth, windows []CodexQuotaWindow, denied, allowed bool, started, now time.Time) bool {
	m.mu.Lock()
	a := m.auths[base.ID]
	// Fence the entire fetch against concurrent failures, observations, reloads and credential changes.
	if m.HomeEnabled() || a == nil || a.Disabled || a.Status == StatusDisabled || a.RegistrationEpoch != base.RegistrationEpoch || a.Generation != base.Generation || AccessTokenSHA256(a) != AccessTokenSHA256(base) || codexQuotaIdentity(a) != codexQuotaIdentity(base) {
		m.mu.Unlock()
		return false
	}
	previous := a.CodexQuota
	s := previous.clone()
	if s == nil {
		s = &CodexQuotaSnapshot{Identity: codexQuotaIdentity(a)}
		// Existing installations may only have persisted the request-driven cooldown.
		s.Exhausted = a.Quota.Exceeded && a.LastError != nil && a.LastError.HTTPStatus == 429 && poolLeaseTerminalFailure(a.LastError)
	}
	wasExhausted := s.Exhausted
	if wasExhausted && len(s.Recovery) == 0 {
		s.resetRecovery()
	}
	s.Windows = mergeCodexQuotaWindows(s.Windows, windows, false)
	s.ObservedAt, s.RequestStartedAt = now, started
	if denied {
		s.Exhausted = true
		s.resetRecovery()
	} else if wasExhausted {
		if len(s.Recovery) == 0 {
			s.resetRecovery()
		}
		s.confirmRecovery(windows, allowed)
	}
	recovered := wasExhausted && !s.Exhausted
	if !s.Exhausted {
		s.DeniedResetAt = time.Time{}
	}
	a.CodexQuota = s
	a.codexQuotaBlocked = s.Exhausted && !m.cooldownDisabledForAuth(a)
	if recovered {
		clearConfirmedCodexQuotaCooldown(a, now)
	}
	a.Generation++
	a.UpdatedAt = now
	snapshot := a.Clone()
	m.mu.Unlock()
	m.publishCodexQuota(snapshot, true)
	return true
}

// resetRecovery anchors independent exhausted-window evidence without changing raw usage.
func (s *CodexQuotaSnapshot) resetRecovery() {
	s.Recovery = nil
	for _, w := range s.Windows {
		matchesReset := !s.DeniedResetAt.IsZero() && !w.ResetAt.IsZero() && math.Abs(w.ResetAt.Sub(s.DeniedResetAt).Seconds()) <= 1
		if w.LimitReached || matchesReset {
			s.Recovery = append(s.Recovery, CodexQuotaRecoveryWindow{WindowSeconds: w.WindowSeconds, ResetAt: w.ResetAt})
		}
	}
	if len(s.Recovery) == 0 {
		for _, w := range s.Windows {
			s.Recovery = append(s.Recovery, CodexQuotaRecoveryWindow{WindowSeconds: w.WindowSeconds, ResetAt: w.ResetAt})
		}
	}
}

func (s *CodexQuotaSnapshot) confirmRecovery(incoming []CodexQuotaWindow, allowed bool) {
	pending := make([]CodexQuotaRecoveryWindow, 0, len(s.Recovery))
	for _, baseline := range s.Recovery {
		candidate, recovered := false, false
		for _, w := range incoming {
			if w.WindowSeconds != baseline.WindowSeconds {
				continue
			}
			usable := allowed && !w.LimitReached && w.UsedPercent < 100
			if baseline.ResetAt.IsZero() {
				baseline.ResetAt = w.ResetAt
			} else if usable {
				rolled := w.ResetAt.After(baseline.ResetAt)
				recovered = (rolled && w.UsedPercent < 10) || (!rolled && baseline.Candidate)
				candidate = !rolled
			}
			break
		}
		if recovered {
			for i := range s.Windows {
				if s.Windows[i].WindowSeconds == baseline.WindowSeconds {
					s.Windows[i].LimitReached = false
				}
			}
		} else {
			baseline.Candidate = candidate
			pending = append(pending, baseline)
		}
	}
	// A newly exhausted window must join the recovery set independently.
	for _, w := range incoming {
		if w.UsedPercent < 100 && !w.LimitReached {
			continue
		}
		found := false
		for _, p := range pending {
			if p.WindowSeconds == w.WindowSeconds {
				found = true
				break
			}
		}
		if !found {
			pending = append(pending, CodexQuotaRecoveryWindow{WindowSeconds: w.WindowSeconds, ResetAt: w.ResetAt})
		}
	}
	s.Recovery = pending
	s.Exhausted = !allowed || len(pending) > 0
}

// Recovery only clears quota-derived state. Authentication/model/transport failures remain intact.
func clearConfirmedCodexQuotaCooldown(a *Auth, now time.Time) {
	for _, state := range a.ModelStates {
		if state == nil || !state.Quota.Exceeded || (state.Quota.Reason != "quota" && state.Quota.Reason != "credential_quota") {
			continue
		}
		if state.Quota.Reason == "quota" && state.LastError != nil && !poolLeaseTerminalFailure(state.LastError) {
			continue
		}
		quotaDeadline := state.Quota.NextRecoverAt
		state.Quota = QuotaState{}
		if !state.NextRetryAfter.After(quotaDeadline) && (state.LastError == nil || (state.LastError.HTTPStatus == 429 && poolLeaseTerminalFailure(state.LastError))) {
			resetModelState(state, now)
		}
	}
	if a.Quota.Reason == "credential_quota" || a.Quota.Reason == "quota" {
		quotaDeadline := a.Quota.NextRecoverAt
		a.Quota = QuotaState{}
		if !a.NextRetryAfter.After(quotaDeadline) && (a.LastError == nil || (a.LastError.HTTPStatus == 429 && poolLeaseTerminalFailure(a.LastError))) {
			a.Unavailable = false
			a.NextRetryAfter = time.Time{}
		}
	}
	if a.LastError != nil && a.LastError.HTTPStatus == 429 && poolLeaseTerminalFailure(a.LastError) {
		a.LastError = nil
		a.StatusMessage = ""
	}
	if !hasModelError(a, now) && a.LastError == nil {
		a.Status = StatusActive
	}
}

func (m *Manager) publishCodexQuota(a *Auth, persist bool) {
	if m.scheduler != nil {
		m.scheduler.upsertAuth(a)
	}
	models, epoch := registry.GetGlobalRegistry().GetModelsAndEpochForClient(a.ID)
	projections := make([]registry.ClientModelProjection, 0, len(models))
	for _, model := range models {
		if model != nil {
			projections = append(projections, m.clientModelProjectionForAuth(a, model.ID, time.Now()))
		}
	}
	registry.GetGlobalRegistry().ApplyClientModelProjections(a.ID, epoch, a.Generation, projections)
	if persist {
		m.persistCooldownStates(context.Background())
	} else {
		m.codexQuotaDirty.Store(true)
	}
}

func (m *Manager) flushCodexQuotaObservations() {
	if m.codexQuotaDirty.Swap(false) {
		m.persistCooldownStates(context.Background())
	}
}

func (m *Manager) runCodexQuotaPersistence(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.flushCodexQuotaObservations()
		}
	}
}
