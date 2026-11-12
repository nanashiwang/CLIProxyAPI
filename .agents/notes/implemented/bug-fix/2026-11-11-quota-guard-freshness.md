# Agent Note: Fail closed on missing or stale quota observations

Status: implemented

## Problem

The quota guard can miss reserve margins between polls or after silent failed observations. Its batch wait also delays other checks when an upstream hangs. Production diagnosis established exhausted short-window usage and large requests, but no historic quota evidence exists to attribute the incident to a particular failed poll.

## Decision

Extend the existing guard, selectors, refresh worker lifecycle and management auth projection. Keep threshold settings and provider eligibility. Once background verification starts, unknown, failed and stale quota observations pause new calls. Check supported accounts every minute, or every 15 seconds within ten percentage points of the actual sampled threshold. Observations expire after two polling intervals; individual window timestamps prevent partial responses from refreshing missing windows. Codex retains upstream-denial recovery fencing. A five-second coordinator reconciles deadlines independently of four persistent workers and a bounded 100-job queue, with admission checks enforcing deadlines even between ticks. No new upstream timeout or generation cancellation is introduced.

Expose sanitized guard health and normalized provider snapshots through the existing management auth response. Log successful observations with used/threshold/reset values and failures with fixed categories/status, never tokens, response bodies or raw transport errors. The production formatter explicitly allowlists the diagnostic fields, quotes window names and state/category strings, and continues omitting unrecognized credential/body fields. This is required in addition to structured-entry tests: an entry can contain evidence that the configured formatter silently drops. Retain the prior threshold note for sampling, adapters and persistence; this decision supersedes its fail-open and fixed polling tradeoffs.

The [shared query cooldown policy](../../implemented/bug-fix/2026-11-12-shared-quota-query-cooldown.md) supersedes periodic checks during provider cooldowns and known stopped-window sleeps. Freshness and failure protection remain in force.

## Alternatives considered

Keeping unknown observations available favors uptime but silently defeats reserve protection. A usage request before every inference adds latency and can amplify upstream throttling. Killing in-flight streams can break client work and still cannot undo already charged usage. Lowering thresholds alone cannot address failed observations. This change accepts temporarily unavailable accounts when quota verification fails; explicit guard disable remains the operator escape hatch.

## Verification

Tests cover query failures, missing/partial windows, stale timestamps, delayed responses, recovery, hot disable, independent coordinator progress, request-time fencing, all supported providers, redacted logging, production formatter output/newline escaping and management projection. Focused race tests, full Go tests, server build and management smoke checks pass. The frontend retains its existing configuration contract and documents the new protection policy in all locales.

## Consequences

Extra quota queries and fail-closed checks can reduce available capacity during provider outages or with unsupported quota payloads. Large already-admitted requests can still cross thresholds. One hung worker does not stall the remaining workers or newly queued accounts. Four hung refresh workers stop observations, but cannot keep stale credentials available. Startup verification begins with the existing refresh lifecycle; normal admission before that lifecycle is initialized retains compatibility for SDK embedders.

## Related notes

[Original guard](../../implemented/feature/2026-11-11-codex-quota-guard.md) remains the owner of thresholds and adapters.
