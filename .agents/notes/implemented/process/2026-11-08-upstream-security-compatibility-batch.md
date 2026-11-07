# Agent Note: Selective upstream security and browser compatibility batch

Status: implemented

Date: 2026-11-08
Owner: codex
Class: process

## Problem

`personal/main` is intentionally divergent from `origin/main`, so synchronizing the
upstream branch wholesale would mix unrelated product changes and create a large,
hard-to-review conflict surface. The first batch should take only updates whose
benefit is explicit and whose affected CPA contracts are already stable.

## Existing capabilities and impact

The existing backend already owns request-log filename generation and the Gin CORS
middleware. The sibling management frontend calls the backend through browser
requests, so the preflight behavior is part of the established management/API
entry path. No new route, storage format, provider contract, or frontend change is
needed for this batch. No prior active note owns these two upstream fixes.

## Decision

Cherry-pick these two upstream fixes as a single backend batch after reconciling
the request logger's existing CPA-specific request-ID handling:

- `179d43dc`: reject path separators and parent-directory sequences in generated
  request-log filenames and validate the final filename component.
- `058b803b`: replace the CORS request-header wildcard with explicit API headers and
  echo headers requested by browser preflight requests.

The batch deliberately excludes provider/model features, large executor rewrites,
and changes that require a new backend/frontend contract. The request logger keeps
CPA's existing atomic unique-file creation and shortened request-ID behavior while
adding upstream's separator, parent-directory and final-component validation. The
CORS middleware names the known API headers and echoes only headers requested by an
OPTIONS preflight; origins remain wildcard and credentials remain disabled.

## Alternatives considered

1. Merge or rebase the full upstream branch: rejected because the branches have
   substantial two-way divergence and the resulting conflicts would obscure review.
2. Manually rewrite both fixes: rejected initially because the upstream commits
   include focused regression tests and cherry-pick preserves their evidence.
3. Skip both fixes: rejected because request-log path confinement is a direct
   security boundary and wildcard CORS does not reliably authorize `Authorization`
   in strict browser preflight handling.

## Verification

Focused request-log and CORS tests pass. The implementation has no conflict markers,
passes `git diff --check`, and preserves the current CPA logging path. Full `go test
./...`, the required server build, note validation, release workflow, and remote
push are the remaining batch gates.

## Consequences

- **Benefit**: Crafted request paths cannot escape the configured request-log
  directory, and strict browsers can send `Authorization` and provider headers in
  preflighted requests.
- **Cost and limit**: The request logger still relies on the configured directory's
  filesystem permissions; CORS still allows any origin as before. Revisit this
  decision if credentialed browser access or a restricted-origin policy is required.

## Related-note audit

The capability-review and release-delivery notes remain applicable. No active note
was superseded; this note records only the selective upstream integration decision.
