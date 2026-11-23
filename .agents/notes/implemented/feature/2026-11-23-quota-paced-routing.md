# Agent Note: Scope observed quota pacing to weighted routing

Status: implemented

## Problem

Operators need to direct free selections towards expiring Codex budgets while preserving model, priority, account-group and session constraints.

## Existing capabilities and impact

The [quota guard](../../implemented/feature/2026-11-11-codex-quota-guard.md) supplies stable thresholds, identity and freshness. Existing local weighted routing accounts for retry subsets, pending reservations and sticky picks. Home owns its routing.

## Decision

A boolean modifier on weighted routing enables pacing for standalone Codex OAuth credentials. An optional account-pool ID scopes the rollout. Authorization and local eligibility precede the weighted selector. A fresh guard observation contributes a soft factor, computed at the sample time from remaining usable budget divided by remaining window time. The limiting window determines a quarter-step factor in [0.5, 1.5]. The validity deadline is the earliest guard expiry, window reset or five minutes from the oldest observation. Other eligible credentials contribute their base weight.

Static and paced pools have separate accounting namespaces. The configured weights determine pool lifecycle. A paced pick earns normalized fractions of one request and debits the chosen credential by one whole request. Each outstanding reservation owns its deltas and member epochs. Out-of-order settlement and quota observation changes preserve existing credit. The synchronous and mixed-provider paths share this engine. Enabling pacing alone leaves the affinity selector and bindings in place.

## Alternatives considered

- Weighted power-of-two selection spreads active requests but would change the semantics of the existing weighted strategy and its affinity compensation.
- Writing effective weights into credential configuration invalidates prior credits and outstanding reservations on every weight change.

## Consequences

New allocations can respond to short and weekly quota differences. Strong session affinity limits steering. Aging evidence neutralizes a soft adjustment independently of all guard and upstream-denial recovery rules. Pacing stores one signed delta per eligible member during selection. Normalization and member lookups add work in the opt-in path.

## Verification

The [replay](../../implemented/testing/2026-11-23-quota-routing-replay.md) measures tradeoffs. Focused tests exercise config reload, scope, sticky compensation, regular and affinity selection, large pools and rollback across an observation change. The management smoke script reads, enables, scopes and disables the field through v8.
