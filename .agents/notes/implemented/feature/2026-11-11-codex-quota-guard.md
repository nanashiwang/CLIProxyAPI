# Agent Note: Multi-provider proactive quota guard

Status: implemented

## Problem

Quota protection must apply to supported account providers rather than only Codex. Operators need stable thresholds and recovery independent of error cooldowns.

## Decision

The common `quota-guard` overlays legacy `codex-quota-guard` values per field. Absent fields retain existing short-window 90 ± 5 and weekly 93 ± 3 defaults; monthly windows use an independently configurable 93 ± 3 default. The visual editor reads both blocks and writes only changed common fields, preserving unknown YAML. All four locales and field-search/validation registries use the common labels.

Codex keeps its passive observations and upstream-denial recovery contract. Claude OAuth, xAI/Grok OAuth, Kimi (including first-party API keys accepted by its usage endpoint), and Antigravity share the existing bounded worker pool. Explicit provider percentages/counts become normalized windows in a separately persisted `provider_quota` snapshot. New adapters poll at most once per minute, including while stopped. Oldest attempts receive priority so more than 200 accounts cannot starve. Credentials, identities, registration epochs and quota observation timestamps fence responses. Ordinary request generation changes do not discard these independent usage snapshots; quota refresh never clears authentication or error cooldowns.

Any observed window stops the entire account, including model/group-specific windows. Missing, invalid, failed and health-only observations preserve earlier evidence; reset deadlines alone never prove recovery. Stable account/window hashing avoids redraws across requests, resets and restarts. Home authority, custom endpoints and plugins remain outside this local polling policy. Paid xAI health probes cannot provide a percentage and are never used to infer quota or send billable chat probes.

## Alternatives considered

Per-request randomness oscillates selection. Per-request usage calls add a network request to every inference; bounded background polling reuses the existing worker lifecycle. Independent per-provider pollers duplicate identity fencing, scheduling and persistence. Clearing all windows on every partial response makes a missing/failed weekly or monthly endpoint incorrectly resume an account. A universal five-hour assumption misclassifies Grok billing cycles; explicit monthly settings preserve their semantics.

## Verification

Focused parser and lifecycle fixtures cover all four added providers, unknown/zero values, legacy overlay, independent windows, polling fairness, persistence, stale responses and credential replacement. Go full tests, focused race checks, the server build and management API smoke checks pass. Frontend verification passes 589 tests, lint (one existing warning), type checking and production build. An isolated backend and browser confirm legacy-value loading, validation rejection, common YAML saves and hot reload; desktop and 390px mobile layouts were inspected. Provider responses are exercised through fixtures, not live account calls.

## Consequences

This is observed usage, not reserved capacity: unknown quotas and calls between polls or already in flight can exceed the threshold. Large fleets and slow endpoints can delay refreshes; work is bounded to 100 jobs and four workers. Missing window evidence deliberately retains stops until valid lower usage or an explicit operator policy change. Provider windows are capped at 128 and names at 256 bytes. Generic normalized windows reuse the existing snapshot representation without reusing Codex upstream-denial state. The cost is extra usage requests; the benefit is cross-provider enforcement without requiring the management page to remain open.

## Related notes

The usage-timezone-and-account-facts note owns display/accounting. The Kimi concurrency-admission note owns request rejection, independent of usage protection. Existing release-delivery and capability-review notes still apply.
