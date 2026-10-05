# Agent Note: Explicit usage timezone and observed account facts

Status: implemented

## Problem

Daily usage buckets use UTC while the management page shows browser-local dates. Credit balances and Fast billing tiers are not visible in the existing account and usage flows. Clearing the selected usage record before the close animation finishes empties the modal.

## Existing capabilities and impact

The existing usage dashboard and record queries share absolute, half-open time filters. Billing is calculated once and stored as a snapshot. The management quota flow already fetches the official Codex usage payload and reset cards. Active notes were searched for usage, timezone, credit, modal and pricing; no owner note overlaps this decision. The existing process note governs workflow only.

## Decision

Extend the existing dashboard query with an optional validated IANA timezone, defaulting to UTC for older clients. The management page sends its browser timezone, matching existing custom-date input and record formatting, and the response declares its bucket timezone. Advance day buckets by calendar dates across DST. Keep rolling 24h/7d/30d intervals and stored UTC timestamps.

Show Fast from the effective billing snapshot, and parse Codex credit facts separately from reset cards, preserving decimal strings and distinguishing zero, unlimited and missing balances. Retain the selected record only through the usage modal exit and clear it afterwards. Add the officially verified GPT-6.1 Sol token prices to the embedded fallback without replacing dynamic catalog prices or repricing history. Extend feature watch paths after the upstream shared-component move; track these adaptations independently.

## Alternatives considered

- A global deployment timezone would align logs and scheduler calendars too, but this change concerns usage display; introducing a server-wide setting would unnecessarily change existing quota and scheduling semantics.
- Keeping UTC buckets and only adding a UTC label is backward compatible, but does not align daily totals with the viewer's filters. Explicit per-query timezone aligns all usage views while preserving old API defaults.
- Copying the entire upstream pricing and quota subsystem provides more fields, but would replace CPA accounting and account-pool semantics. Reuse existing snapshots and quota queries instead.

## Verification

Backend full Go suite, usage/pricing/management race checks, binary build and management smoke pass. Regression cases cover DST day lengths, midnight gaps, repeated hours, non-hour offsets and half-hour DST changes, invalid timezone rejection, UTC defaults, pricing thresholds/tiers and dynamic price precedence. Sixteen monitor regressions pass.

Frontend `bun run verify` passes 572 tests, TypeScript and production build, with one pre-existing AccountPoolsPage lint warning. Browser acceptance uses the real isolated Go management API for usage, plus synthetic quota fixtures: Asia/Almaty chart timezone, old-backend UTC fallback, Fast badge, modal footer/Escape close and reopen, exact decimal/zero/unlimited/missing balance, failure states, refresh and 390px dark layout. Actual upstream quota calls and production deployment are outside this verification.

## Consequences

Old backends still return UTC buckets; the new page must label that fallback truthfully. Browser timezone is a display choice, not an account scheduling policy. Tool charges and residency premiums remain outside the existing token-only cost schema. No raw credentials or account facts enter redacted diagnostic exports.

Implementation: [dashboard buckets](../../../../internal/usage/dashboard.go), [query validation](../../../../internal/api/handlers/management/usage_dashboard.go), [pricing fallback](../../../../internal/pricing/fallback.json), and [adaptation manifest](../../../../.github/upstream/codex-proxy-usage.json).
