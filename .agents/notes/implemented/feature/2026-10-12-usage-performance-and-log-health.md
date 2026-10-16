# Agent Note: Observed output timing and bounded analytics/logging work

Status: implemented

## Problem

First response bytes are labelled as first tokens; dashboard scans hold the statistics read lock, and log loss is not visible. The frontend also locks a vulnerable source-map-js build dependency.

## Existing capabilities

Reuse existing usage records, dashboard filters, persisted billing snapshots, diagnostic attempts, and logging configuration. The active [October 5 timezone/account-facts note](2026-10-05-usage-timezone-and-account-facts.md) partially overlaps these entry points but retains its independent timezone and billing decisions. The process and OpenCode monitor notes are unrelated.

## Decision

Add optional first-byte/output/text/reasoning and valid per-response upstream timings without changing legacy stored TTFT fields. Derive upstream throughput only from valid upstream durations; identify local estimates separately and preserve unknown history. Observe bounded protocol frames without affecting forwarding or retry/lease/cooling/accounting behavior.

Copy the selected event range under the store lock, aggregate outside it, and cache bounded dashboard snapshots with source-version invalidation for record/import/clear/configure. Serialize builds to limit concurrent allocation. Rolling queries may reuse at most one second of the same version and return their actual snapshot bounds.

Use bounded nonblocking operational file logs, error reserve capacity, and explicit dropped/write-error counters. Add allowlisted failure categories and expose health to authenticated management clients. Billable usage delivery retains its existing lossless queue semantics.

## Alternatives considered

- Replacing storage with upstream PostgreSQL projections provides persistent aggregates but adds a migration and changes CPA deployment requirements. Reuse the Go store and benchmark lock isolation plus bounded caching first.
- Replacing legacy TTFT in place simplifies clients but relabels historical first-byte observations. Add optional timing facts and explicit frontend fallbacks instead.
- Dropping usage events when a bounded queue fills prevents growth but silently corrupts billing. Limit operational logs only; accounting remains independent.

## Verification

Go full tests, targeted race checks for usage/executor/logging/management/SDK, server build and the real management API smoke pass. Protocol tests cover prelude, retries, fragmentation, oversized frames and absent timestamps. Cache tests cover invalidation, concurrent readers, detached snapshots and empty date ranges. Log tests cover stalled sinks, reserved errors, drain and write failures; failure categories cover wrapped/joined errors and capture-before-finish ordering.

Frontend verification passes 577 tests, TypeScript and production build, with one pre-existing AccountPoolsPage lint warning. Browser acceptance uses the production HTML with an isolated real Go backend and synthetic records: desktop, 390px dark, first output versus historical first byte, redacted connection failure (including null event lists), log-loss alert, old-backend field fallback, refresh and failed requests. No real upstream model call or production deployment is part of acceptance.

The local Apple M5 benchmark with 50,000 synthetic records measures approximately 33.5 ms for a build and 0.16 ms for a cache hit. It demonstrates repeated-query savings, not production throughput. Monitor and deployment-bundle regression suites also pass. Existing mainline release workflows publish the frontend before the backend bundle pins that version; release completion and uploaded asset checks are verified separately from code validation.

## Consequences

Bounded observation skips oversized protocol frames and reports unknown timings. Historical first-byte observations retain their meaning. Cached rolling snapshots have a declared one-second freshness bound; misses still scan selected events. Async operational logs may be dropped under pressure, with observable counters; this is not permission to discard billing records. Graceful shutdown drains accepted logs, but abnormal termination can lose pending operational logs.
