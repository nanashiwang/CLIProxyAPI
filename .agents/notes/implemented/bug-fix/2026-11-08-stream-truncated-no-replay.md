# Agent Note: Do not replay accepted generations after a truncated stream

Status: implemented

## Problem

An upstream Responses request may be accepted and begin producing output before the stream ends without its required `[DONE]` terminal marker. Treating that locally detected truncation as an ordinary 502 can rotate to another credential and replay a generation that was already submitted, while also marking the original credential unavailable.

## Existing capabilities and impact

The OpenAI-compatible executor already rejects Responses streams that end without `[DONE]`; `sdk/cliproxy/auth` separately decides whether an error should stop credential fallback and whether it should cool the selected credential. This change connects that existing terminal-frame check to the existing auth error classification boundary. Gemini and Vertex stream terminal detection use different code paths and are not changed here.

## Decision

Classify the executor's locally synthesized missing-terminal-frame error with a typed marker. Preserve that classification through `resultErrorFromError`, stop fallback for the accepted generation, and skip credential cooldown. Keep the public 502 response and existing partial output behavior.

## Alternatives considered

- **Keep treating the failure as a generic 502** — rejected because it can replay an already accepted generation and penalize the wrong credential.
- **Apply the marker to all provider streams** — deferred because the Gemini/Vertex paths do not currently share the same terminal-frame validation contract.

## Verification

- The existing Responses EOF test asserts the typed marker.
- Auth classification preserves the marker, skips cooldown, and stops fallback.
- `go test ./sdk/cliproxy/auth -run 'TestStreamTruncatedErrorsStopFallbackWithoutCoolingCredential|TestFileCooldownStateStore_StateRelativePath' -count=1` passed.
- `go test ./internal/runtime/executor -run '^TestOpenAICompatExecutorResponsesStreamFailsOnEOFWithoutDone$' -count=1` passed.
- `go test ./...`, `go build -o /dev/null ./cmd/server`, and `bash .agents/verify-notes.sh` passed.

## Consequences

The stream may have ended before the provider actually accepted meaningful work, but replay is not safe to infer from a missing terminal frame after the stream began. The conservative behavior is to return the error without switching credentials. Gemini and Vertex EOF classification remains a separate review item because their CPA stream paths currently lack the same terminal-frame validation.
