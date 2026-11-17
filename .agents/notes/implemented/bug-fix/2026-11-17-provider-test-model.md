# Agent Note: Persist and search provider test models

Status: implemented

## Problem

The OpenAI-compatible editor sends test-model, but the backend drops it from its typed configuration and management response. Reopening the editor selects the first model. Large model lists are difficult to navigate.

## Decision

The backend persists optional provider-level test-model in JSON/YAML and expose it through the existing management GET, PUT and PATCH paths. Empty means automatic selection; omitted PATCH preserves the selection. This is management test metadata, not a routing default. The frontend extends the existing Select with opt-in search for test selectors, preserving selected labels independently of search results.

## Alternatives considered

Browser-only persistence avoids a backend change but loses the preference across browsers and disagrees with the existing API payload. A separate model picker duplicates portal positioning and keyboard behavior; reuse Select instead.

## Verification

A management regression test saves a non-first model, reloads configuration and reads it back. PATCH unrelated fields preserves it; empty clears it. Browser checks confirm search supports case-insensitive label/value matches, no matches, keyboard selection and reopening without stale queries. Existing non-searchable selects retain their behavior.

## Consequences

Older backends still discard this field; deploy matching backend and frontend releases. Other provider families retain their existing persistence semantics.

## Related note review

Active-note searches for test-model, testModel and provider selection found no existing owner for this contract. Quota and auth notes are unrelated.

Backend `go test ./...` and server build pass. Frontend `bun run verify` passes (one pre-existing AccountPoolsPage lint warning). Browser checks exercise the provider editor with an isolated local backend and no live credentials.
