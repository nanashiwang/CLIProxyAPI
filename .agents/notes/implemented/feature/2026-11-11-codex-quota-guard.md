# Agent Note: Codex proactive quota guard

Status: implemented

## Problem

Codex accounts can exhaust session or weekly allowances before operators can stop routing.

## Decision

Extend standalone quota observation, selection and refresh with an enabled-by-default consumed-percentage guard (90 ± 5 for five hours; 93 ± 3 weekly). Reuse the management YAML editor and add visual controls. Stable account/window-type hashing distributes thresholds uniformly without request-time redraws. Either window blocks; observed lower usage permits recovery. Clock expiry alone never clears usage. Home authority and non-OAuth credentials remain outside scope.

## Alternatives considered

Per-request randomness is simple but oscillates routing. A second quota poller duplicates credential fencing and persistence. Reuse existing observation and refresh instead.

## Verification

Validate bounds, configuration reload, both independent windows, recovery, selector exclusion and existing tests/builds. Release via personal workflows.

## Consequences

Quota is observed rather than reserved; in-flight calls can exceed a threshold. Missing upstream usage cannot enforce protection. Existing quota refresh cadence bounds recovery latency.

## Related notes

Active quota searches found usage-timezone-and-account-facts (display/accounting only) and kimi-concurrency-admission (different provider); neither owns this routing decision.

Threshold samples remain stable across quota resets to tolerate drifting relative reset timestamps; changing percentages rescales the same uniform sample. No per-request randomness is introduced.

Verified Go full tests, server build, management smoke checks and focused race tests; frontend 588 tests, lint (one existing warning), type check and production build pass. An isolated real backend accepts visual changes and reloads the saved YAML; desktop and 390px mobile layouts were inspected. No production service is changed.
