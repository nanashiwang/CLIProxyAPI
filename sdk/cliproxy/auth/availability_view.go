package auth

import (
	"strings"
	"time"
)

func isPersistentAuthFailure(auth *Auth, now time.Time) bool {
	if auth == nil {
		return false
	}
	// Preserve the local terminal 401 rule, including pending refresh retries.
	if hasUnauthorizedAuthFailure(auth) {
		return true
	}
	// An OAuth credential whose access token is expired cannot be used to serve requests.
	if exp, ok := auth.ExpirationTime(); ok && !exp.IsZero() && !exp.After(now) {
		return true
	}
	// An explicit token expiration status.
	if strings.EqualFold(strings.TrimSpace(auth.StatusMessage), "token expired") {
		return true
	}
	return false
}

func isModelStateBlocked(state *ModelState, now time.Time) bool {
	if state == nil {
		return false
	}
	if state.Status == StatusDisabled {
		return true
	}
	blocked, _, _ := availabilityBlock(state.Unavailable, state.Quota.Exceeded, state.NextRetryAfter, state.Quota.NextRecoverAt, now)
	return blocked
}

// AvailabilityView projects expired cooldowns without changing routing, quota or leases.
// Call it on a manager snapshot, never on a concurrently mutable Auth.
func (auth *Auth) AvailabilityView(now time.Time) (unavailable bool, status Status, statusMessage string, nextRetry time.Time) {
	if auth == nil {
		return false, StatusActive, "", time.Time{}
	}
	unavailable = auth.Unavailable
	status = auth.Status
	statusMessage = auth.StatusMessage
	if !auth.NextRetryAfter.IsZero() {
		nextRetry = auth.NextRetryAfter
	}

	if auth.Disabled || auth.Status == StatusDisabled {
		return unavailable, StatusDisabled, statusMessage, nextRetry
	}

	// Never reconcile an active authentication or token failure to active.
	if isPersistentAuthFailure(auth, now) || auth.codexQuotaBlocked || (auth.CodexQuota != nil && auth.CodexQuota.Exhausted) {
		if !nextRetry.IsZero() && !nextRetry.After(now) {
			nextRetry = time.Time{}
		}
		return true, StatusError, statusMessage, nextRetry
	}

	// Share the selector's deadline rules, including quota with no recovery deadline.
	hasActiveCredCooldown := false
	if len(auth.ModelStates) == 0 || auth.Quota.Exceeded || !auth.NextRetryAfter.IsZero() {
		hasActiveCredCooldown, _, nextRetry = availabilityBlock(auth.Unavailable, auth.Quota.Exceeded, auth.NextRetryAfter, auth.Quota.NextRecoverAt, now)
	}

	// Check per-model states.
	hasSchedulableModels := false
	allSchedulableBlocked := true
	hasActiveModelCooldown := false
	hadAnyModelCooldown := false
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		if state.Status == StatusDisabled {
			continue
		}
		hasSchedulableModels = true
		if !state.NextRetryAfter.IsZero() || (state.Quota.Exceeded && !state.Quota.NextRecoverAt.IsZero()) {
			hadAnyModelCooldown = true
		}
		if (!state.NextRetryAfter.IsZero() && state.NextRetryAfter.After(now)) ||
			(state.Quota.Exceeded && !state.Quota.NextRecoverAt.IsZero() && state.Quota.NextRecoverAt.After(now)) {
			hasActiveModelCooldown = true
		}
		if !isModelStateBlocked(state, now) {
			allSchedulableBlocked = false
		}
	}

	hadCooldown := !auth.NextRetryAfter.IsZero() ||
		(auth.Quota.Exceeded && !auth.Quota.NextRecoverAt.IsZero()) ||
		hadAnyModelCooldown

	// If there is an active credential cooldown, keep unavailable/error.
	// If all recorded models are blocked and the credential itself was marked unavailable, keep unavailable/error.
	if hasActiveCredCooldown || (hasSchedulableModels && allSchedulableBlocked && auth.Unavailable) {
		if !nextRetry.IsZero() && !nextRetry.After(now) {
			nextRetry = time.Time{}
		}
		return true, StatusError, statusMessage, nextRetry
	}

	// If the credential was not marked unavailable and has no active credential cooldown, keep unavailable=false.
	if !auth.Unavailable && !hasActiveCredCooldown {
		if status == StatusError && hasSchedulableModels && !allSchedulableBlocked {
			status = StatusActive
			statusMessage = ""
		}
		return false, status, statusMessage, time.Time{}
	}

	// If a cooldown was recorded but has expired (and no active model cooldown blocks all models):
	if hadCooldown && !hasActiveCredCooldown && !hasActiveModelCooldown {
		return false, StatusActive, "", time.Time{}
	}

	// If partial models are still cooling, the credential as a whole remains available for other models.
	if hadCooldown && hasSchedulableModels && !allSchedulableBlocked {
		if status == StatusError && !hasActiveCredCooldown {
			status = StatusActive
			statusMessage = ""
		}
		return false, status, statusMessage, time.Time{}
	}

	// If nextRetry is in the past, do not expose a past retry deadline.
	if !nextRetry.IsZero() && !nextRetry.After(now) {
		nextRetry = time.Time{}
	}

	return unavailable, status, statusMessage, nextRetry
}
