# Agent Note: Reduce Claude polling and require sustained quota recovery

Status: implemented

## Problem

Production Claude observations alternate between successful reads and 429 responses. Near-threshold checks run every fifteen seconds; one success erases retry escalation. Existing shared caching and reset sleep do not address that oscillation. Logs establish repeated 429 responses, not the upstream limit's precise account or egress scope.

## Decision

Keep the shared quota broker and scheduler. Claude uses two-minute normal and one-minute near-threshold intervals plus zero-to-ten-percent positive query jitter. Freshness remains tied to original observations at twice the provider's base interval. Other providers retain their existing base intervals. Scheduler and management headers share the actual cached next-read deadline.

Across providers, retain failure escalation until three valid network quota observations succeed and thirty minutes pass without another retryable failure. Cache hits, profile success and empty payloads never count as recovery. Preserve history from pruning for up to twenty-four idle hours, bounded by the existing identity cap. Retry-After remains authoritative; unknown/failed/stale quota continues to block routing and known threshold resets still sleep.

## Alternatives considered

Uniformly slowing every provider affects accounts without observed throttling. Removing fail-closed behavior avoids pauses by risking reserve depletion. Treating all Claude accounts as one rate-limit bucket assumes unverified upstream scope. A single successful response is insufficient evidence of sustained recovery.

## Verification

Tests cover intermittent 429/success sequences, valid recovery duration/count, invalid payloads, cached next-read scheduling, provider intervals and stale admission. Full backend tests, focused race tests, build and management smoke pass; 603 frontend tests, lint and build also pass. Four locale hints describe the provider-specific observation lifetime.

## Consequences

Claude observations can lag longer, increasing potential reserve overshoot for in-flight or between-poll requests. Existing thresholds are preserved. Runtime history is process-local and token-scoped. Three successes plus thirty minutes is a conservative recovery policy, not a claim about Anthropic's undocumented rate limit.

## Related notes

This partially supersedes immediate successful-read reset in [shared quota cooldown](../../implemented/bug-fix/2026-11-12-shared-quota-query-cooldown.md); cache, concurrency and reset-time verification remain unchanged.

The fixed provider-specific cadence is superseded by [linear quota polling](../../implemented/feature/2026-11-12-linear-quota-polling.md). Sustained recovery and bounded retry history remain in force.
