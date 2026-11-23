# Agent Note: Usage health and provider transport evidence

Status: implemented

## Problem

The usage dashboard mixes caller cancellation with service failure. Codex transport selection uses a fixed size threshold and ignores explicit no-more-retry advice. The upstream monitor aborts immediately on transient GitHub failures.

## Decision

Extend existing Go/React statistics with optional service-health counters and rates, preserving legacy totals and billing. Persist allowlisted failure evidence; old records with insufficient evidence remain unknown. Count retained usage records, never infer final request outcomes from intermediate diagnostic attempts. Use each upstream connection's advertised size limit, retain conservative continuation replay fences and reject oversized warmup. Honor structured NO_MORE_RETRY without relaxing account permissions, leases or replay rules. Retry transient read-only GitHub failures at most three times.

An upstream 1009 can arrive between Go WebSocket write fragments. The reader attaches it to the active turn before acknowledging the close, and any response text closes that turn’s recovery window. Stream setup resolves an eligible rejection before exposing headers. HTTP handles the fallback and owns its usage record. Lease-bound and active duplex attempts retain the upstream rejection because closing their socket also ends ownership or interrupts independent client input.

Responses forwards repeated `x-codex-exclude` values. Account and model overrides apply before checking the handshake profile. An independent turn can redial. An incremental turn with changed exclusions needs a full replay and does not evict the original socket.

## Alternatives considered

Replacing totals changes historical reports and billing interpretation. Inferring cancellation from running diagnostic snapshots misclassifies observations captured before completion. Copying Rust storage or scheduling would duplicate CPA ownership. Keeping a fixed size boundary cannot use larger upstream connections. Unbounded monitoring retries hide outages. Retrying after a TCP reset or the first metadata frame risks duplicate generation. Comparing the first exclusion value would silently retain stale response fields when later values change.

## Validation

Passed outcome/persistence/dashboard tests; advertised limit, UTF-8 boundary, invalid/redial limit, HTTP fallback and continuation integration tests; no-more-retry terminal conversion, local/Home refresh, alias and bootstrap tests. Full Go suite, binary build and real management smoke passed. Targeted race tests and 19 offline monitor tests passed. Paired React verification and desktop/mobile local usage-route browser checks passed. Release assets are verified separately during publishing.

The upload and handshake regressions pass in the non-streaming, buffered and unbuffered executor entry points. The partial-upload test waits for the peer’s close acknowledgement between fragments. Wire tests check the count of HTTP attempts, repeated header values, operator precedence and socket reuse. Targeted race tests pass across three runs.

## Consequences

Older backend responses lack new fields, so the frontend must retain legacy presentation. Missing handshake limits provide no size evidence. No change permits replay after delivery or across an unauthorized account boundary.

## Related-note audit

The existing [stream-truncated-no-replay note](../bug-fix/2026-11-08-stream-truncated-no-replay.md) remains authoritative for replay. The October usage/diagnostic notes retain accounting and snapshot boundaries. This adds health evidence and transport negotiation without superseding those decisions.

Error origin is an independent stored dimension. A recorded upstream rejection, a typed transport failure, or an explicit local failure supplies evidence. A synthesized HTTP status alone cannot distinguish a proxy rejection from an upstream one. Unknown historical failures are filterable. OAuth replay cleanup examines the whole input for references before discarding optional inline text IDs.
