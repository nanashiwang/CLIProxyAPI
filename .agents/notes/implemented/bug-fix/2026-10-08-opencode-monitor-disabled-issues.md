# Agent Note: Preserve OpenCode review results with Issues disabled

Status: implemented

## Problem

Actions run 37440052861 fails in the review step. Its authenticated job log ends with `the 'nanashiwang/CLIProxyAPI' repository has disabled issues` followed by exit code 1. The Issue listing error is suppressed, so the workflow attempts to create an Issue even when the repository disables Issues. No active note owns this monitor; the usage-monitor mention in the usage timezone note is unrelated.

## Decision

The monitor reads repository `has_issues` only after detecting an upstream change. Enabled Issues retain the existing title, body/comment status-marker deduplication and create/comment paths. Disabled Issues use the existing HEAD/tag/tag-SHA/release marker, hashed into an artifact name. A paginated repository artifact lookup finds an unexpired matching result and links its run in the current summary. Otherwise the original full Issue report is written to the summary and uploaded as a 90-day artifact. Only this monitor gains `actions: read`; upstream detection and watched files are unchanged.

## Alternatives considered

- Summary alone is the smallest fallback, but it has no artifact index for preserving cross-run deduplication.
- An Actions cache can store a marker, but a cache hit does not guarantee a retrievable review report; retention and eviction can separate the marker from its report.
- Ignoring Issue command failures avoids a red run but loses the review and conceals unrelated authorization/API failures. This implementation explicitly checks the repository setting and preserves other failures.

## Consequences

The fallback provides a downloadable report and a summary without enabling Issues or adding credentials. Deduplication is bounded by artifact availability: expired/deleted results are saved again rather than suppressing an unavailable report. Re-enabling Issues resumes the original Issue flow, which can create an Issue for a state previously recorded only as an artifact. Artifact upload/API errors remain visible failures. Existing monitor concurrency is retained.

## Validation

Eight offline tests execute the actual workflow Bash with a strict fake gh and real jq. They cover disabled Issues, full report content, pagination, marker components, expiration, enabled create/comment, body/comment deduplication, no change, unrelated failures, Bash syntax and the upload contract. Run `python -m unittest discover -s .github/scripts -p test_opencode2api_monitor.py -v` with PyYAML and jq installed. The local environment has neither Go nor Bun, so the required binary build and note checks could not run locally; repository CI must supply those checks. Real artifact upload has not been exercised by the offline dry-run.
