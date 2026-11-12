# Agent Note: Linear quota polling with bounded observation age

Status: implemented

## Problem

Fixed polling remains too frequent for accounts with ample quota. The user chooses a linear one-to-thirty-minute interval relative to each account's stable sampled stop threshold, shared by all eligible providers.

## Decision

Reuse normalized windows, the quota broker, scheduler, and frontend cached diagnostics. Each window uses 1 + 29 * clamp((threshold - used) / threshold, 0, 1) minutes. Take the shortest interval. Keep deterministic account/window thresholds across resets. Require two distinct observations before long intervals; reset or decreasing usage restarts warmup. A conservative observed consumption rate caps the interval to one quarter of predicted time to threshold. Twenty completed requests since observation, or resumed activity after an idle observation pair, caps it at one minute. These are risk heuristics, not token-to-quota conversion. Retain original timestamps, add at most ten percent scheduling jitter (capped at thirty minutes), and expire data at the per-window maximum scheduled interval plus one minute grace. Cache and scheduler deadlines must shorten together. Retry-After and stopped-window reset verification remain authoritative.

## Alternatives considered

Fixed two-minute Claude checks are simple but still query idle accounts too frequently. Quadratic interpolation accelerates near-limit checks earlier, but the user selected linear interpolation. Pure percentage scheduling misses rapid changes, while inferring percentage consumption from token counts assumes undocumented provider accounting.

## Verification

Tests verify formula endpoints and stable jitter thresholds, window minimum, warmup/rate/reset/traffic behavior, stale and partial observations, cached deadline shortening, 429 precedence, and reset sleep. Backend full tests, focused race tests, build and management smoke pass; frontend tests, lint, type checking and build pass. Release assets and deployed health are checked during publication.

## Consequences

In-flight or external usage can exceed reserve between observations. Runtime rate history restarts conservatively after process restart. Slow responses can expire before completion. Longer freshness deliberately trades query pressure for observation lag.

## Related notes

Partially supersedes fixed intervals in [adaptive recovery](../../implemented/bug-fix/2026-11-12-adaptive-quota-recovery.md), preserving sustained failure recovery. [Shared broker](../../implemented/bug-fix/2026-11-12-shared-quota-query-cooldown.md) and [freshness protection](../../implemented/bug-fix/2026-11-11-quota-guard-freshness.md) retain fail-closed and original timestamp invariants. The multi-provider feature note overlaps only in interval documentation; Kimi admission and usage reporting notes are unrelated.
