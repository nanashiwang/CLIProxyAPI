# Agent Note: Usage health and provider transport evidence

Status: implemented

## Problem

The usage dashboard mixes caller cancellation with service failure. Codex transport selection uses a fixed size threshold and ignores explicit no-more-retry advice. The upstream monitor aborts immediately on transient GitHub failures.

## Decision

Extend existing Go/React statistics with optional service-health counters and rates, preserving legacy totals and billing. Persist allowlisted failure evidence; old records with insufficient evidence remain unknown. Count retained usage records, never infer final request outcomes from intermediate diagnostic attempts. Use each upstream connection's advertised size limit, retain conservative continuation replay fences and reject oversized warmup. Honor structured NO_MORE_RETRY without relaxing account permissions, leases or replay rules. Retry transient read-only GitHub failures at most three times.

## Alternatives considered

Replacing totals changes historical reports and billing interpretation. Inferring cancellation from running diagnostic snapshots misclassifies observations captured before completion. Copying Rust storage or scheduling would duplicate CPA ownership. Keeping a fixed size boundary cannot use larger upstream connections. Unbounded monitoring retries hide outages.

## Validation

Passed outcome/persistence/dashboard tests; advertised limit, UTF-8 boundary, invalid/redial limit, HTTP fallback and continuation integration tests; no-more-retry terminal conversion, local/Home refresh, alias and bootstrap tests. Full Go suite, binary build and real management smoke passed. Targeted race tests and 19 offline monitor tests passed. Paired React verification and desktop/mobile local usage-route browser checks passed. Release assets are verified separately during publishing.

## Consequences

Older backend responses lack new fields, so the frontend must retain legacy presentation. Missing handshake limits provide no size evidence. No change permits replay after delivery or across an unauthorized account boundary.

## Related-note audit

The existing [stream-truncated-no-replay note](../bug-fix/2026-11-08-stream-truncated-no-replay.md) remains authoritative for replay. The October usage/diagnostic notes retain accounting and snapshot boundaries. This adds health evidence and transport negotiation without superseding those decisions.

Error origin is an independent stored dimension. A recorded upstream rejection, a typed transport failure, or an explicit local failure supplies evidence. A synthesized HTTP status alone cannot distinguish a proxy rejection from an upstream one. Unknown historical failures are filterable. OAuth replay cleanup examines the whole input for references before discarding optional inline text IDs.
