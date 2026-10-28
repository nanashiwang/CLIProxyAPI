# Agent Note: Classify Kimi concurrency admission separately from authentication

Status: implemented

## Problem

Kimi can report a concurrent request limit using access_terminated_error on HTTP 403. Generic authentication handling then imposes a thirty-minute model cooldown, although the credential can serve another request after capacity becomes available.

## Existing capabilities and impact

The Kimi executor already serves Chat, Responses and delegated Messages. The auth manager owns pool access, fallback, cooldown and the no-replay-after-output boundary. Reuse these mechanisms instead of adding executor retries. The K3.1 Pro request compatibility note owns sampling and forced-tool reconciliation; it remains independent of error admission handling. No existing active note owns this concurrency classification.

## Decision

The Kimi boundary identifies only the exact structured Kimi admission error on HTTP 400, 403 or 429, normalizes it to retryable 503/upstream_concurrency, and uses a two-second cooldown on the failed credential/model pair. Preserve original error causes, existing quota state, longer cooldowns, cooling opt-outs and stream delivery semantics.

## Alternatives considered

- Treat all access termination as busy: simpler matching, but risks rotating on permanently invalid credentials or exhausted subscriptions; retain exact matching.
- Treat it as quota exhaustion with exponential backoff: reuses quota propagation but misreports the problem and penalizes unrelated models; use a distinct code and short model-scoped cooldown.
- Retry inside the executor: reduces scheduler work but hides attempts and risks duplicate streaming output; retain manager-owned fallback.

## Testing

After integration with personal main c23440b7, the full Go suite (`go test -p 2 ./... -count=1 -timeout 180s`) passes using Go 1.26.0 and GOMAXPROCS=2. Targeted race regressions, server compilation, management API smoke checks, note validation and whitespace checks pass. Tests verify busy-to-ready fallback, bounded all-busy handling, no replay after output, all three protocol entries, preservation of existing quota cooldowns and unrelated errors. Synthetic transports avoid live credentials or quota consumption.

## Consequences

Two seconds is a local admission policy, not an upstream reset estimate. Exact-message matching deliberately leaves unknown future wording unchanged. The upstream message does not establish a shared cross-model limit, so only the failed model is cooled. Deployment remains an operator action after merge.

## Related notes

The [K3.1 Pro parameter compatibility decision](2026-10-27-kimi-k31-pro-request-compatibility.md) covers final-wire normalization, not admission failures. Both paths retain their existing execution and canonical-thinking boundaries.
