# Agent Note: Reconcile K3.1 Pro sampling and forced tool requests

Status: implemented

## Problem

Production K3.1 Pro requests fail with `invalid temperature: only 1 is allowed for this model` and `tool_choice 'specified' is incompatible with thinking enabled`. Kimi and OpenCode share this model but use different execution paths. Removing thinking alone leaves the provider's default enabled, and payload rules can reintroduce incompatible parameters after canonical thinking translation.

## Decision

Use one narrowly scoped final-wire normalizer under executor/helps for `k3.1-pro` and `k3.1-pro-highspeed`, with their `kimi-` aliases, including existing context/thinking suffixes. Omit supplied numeric temperature so the upstream chooses its mode-specific default: a live non-thinking Chat request with temperature 1 was rejected with `only 0.6 is allowed`, while the original thinking requests required 1. Preserve forced tool selection and explicitly disable thinking using the target protocol's representation, after translation, canonical thinking and payload rules. Retain automatic tool selection and other models unchanged. Use the existing Kimi delegation into Claude with an optional payload hook, and the shared OpenCode translation path, covering streaming and non-streaming Chat, Messages and Responses.

## Alternatives considered

- Payload configuration can fix temperature immediately, but cannot safely reconcile forced choices across all protocols or protect against later overrides; use a shared model-specific boundary instead.
- Rewriting forced tool selection to auto preserves reasoning but loses the caller's required function call; disable reasoning for that request instead.
- Retrying the upstream 400 adds latency and consumes another attempt for a deterministic input conflict; normalize before the first send.
- Changing the shared translator or global Claude behavior would affect unrelated providers. A Kimi-owned hook preserves that boundary.

## Consequences

Forced tool calls no longer generate reasoning on that request. Numeric temperature is omitted, including otherwise valid values, because upstream sampling is fixed by thinking mode. Omitted/invalid nonnumeric temperature and other models remain unchanged. This fixes deterministic parameter conflicts, not upstream concurrency, credential expiry or gateway timeouts. Responses uses the existing canonical `reasoning.effort=none` representation.

## Testing

The full Go suite, executor/helper race checks, server build, management API smoke and archive-download tests pass. Regression tests cover twelve Kimi/OpenCode Chat/Messages/Responses stream/non-stream routes with conflicting payload overrides; helper tests cover aliases, automatic and forced tool choices, missing thinking defaults, temperature values, idempotence and preservation of unrelated fields.

Live pre-deployment probes against the existing service confirm that forced Chat with explicit disabled thinking and omitted temperature returns a tool call, and Responses with reasoning.effort=none returns a tool call. Chat with disabled thinking and temperature 1 instead receives the upstream 0.6-only error. The existing Messages path still reproduces the forced-tool/thinking conflict because its generic forced-tool helper deletes the disabled marker; the new Kimi hook runs after that helper. These probes establish the wire constraints, not acceptance of the new binary. Deployment and post-deployment validation are separate operational steps.

## Related-note audit

No active note matches Kimi, tool_choice, sampling or temperature. The Claude review protocol continuity note owns beta forwarding and remains independent; this change does not alter fingerprint trust or header capabilities.
