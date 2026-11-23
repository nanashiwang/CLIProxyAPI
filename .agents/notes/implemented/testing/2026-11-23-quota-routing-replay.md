# Agent Note: Compare quota pacing with shipped weighted routing

Status: implemented

## Problem

A quota-rich credential can reset while another exhausts its budget. Session affinity restricts how much traffic can be moved. Published aggregate usage lacks the session history required to reconstruct that constraint.

## Existing capabilities and impact

The weighted selector supports bounded accounting for sticky picks. The [quota guard](../../implemented/feature/2026-11-11-codex-quota-guard.md) owns admission and recovery. Freshness and polling notes govern the source observations independently of this comparison. The routing UI exposes configured weights. The experiment exercises the backend in isolation.

## Decision

Run both production weighted reservation paths on the same sequence of arrivals, concurrency durations, session identities and costs. The model permits one priority tier and a 60-minute sliding affinity TTL with failover. Six synthetic accounts have staggered resets and alternating 1:2 manual weights and capacities. Both arms start with identical budgets. The model charges synthetic cost units at admission and receives periodic observations. It reports upstream denials separately from requests with no eligible credential. The latter includes modeled concurrency saturation and stale guard observations.

## Alternatives considered

- Reconfiguring credential weights would reuse the weighted selector directly, but invalidates outstanding accounting whenever a weight changes.
- Trying the new policy on live sessions would provide real traffic, but would confound changes in demand with differences between policies.

## Consequences

The outcome depends on movable traffic. Hard affinity and delayed observations can worsen results. Differences at a finite horizon include the ending inventory of partially consumed windows. Never add short and weekly waste figures together. Quota polling is periodic in this fixture. It models concentrated session traffic and concurrent upstream work, while actual reservation overlap has separate deterministic settlement tests.

## Verification

Run `go test ./sdk/cliproxy/auth -run '^TestQuotaRoutingReplay$' -v -count=1`. Each row sums three fixed seeds and two simulated weeks per seed. Each pair shows fixed WRR followed by [quota pacing](../../implemented/feature/2026-11-23-quota-paced-routing.md). Expired units count the unused budget up to the guard threshold independently for each window.

| Workload | Expired 5h units | Expired 7d units | Upstream quota 429 | No eligible credential | Binding changes |
| --- | ---: | ---: | ---: | ---: | ---: |
| Mostly new | 89810 → 89659 | 97323 → 91399 | 0 → 0 | 0 → 0 | 0 → 0 |
| Long sessions | 101967 → 103537 | 94527 → 97758 | 0 → 1 | 0 → 0 | 3337 → 3279 |
| Weekly pressure | 210284 → 208759 | 0 → 0 | 86 → 86 | 92119 → 91756 | 5516 → 5408 |
| Delayed reset observation | 22846 → 21181 | 37731 → 37571 | 16 → 12 | 34560 → 34560 | 415 → 292 |
| Overlapping requests | 0 → 0 | 15104 → 15083 | 0 → 0 | 327354 → 327353 | 80045 → 79537 |

No production requests or credentials are used. Promote a pilot only after checking expired usable budget by window, quota denials, unavailable requests and binding changes for its actual workload.
