# Agent Note: Share quota query cooldowns and stop polling sleeping accounts

Status: implemented

## Problem

Background verification and management quota reads independently reach the same provider. A 429 does not respect Retry-After, and stopped accounts continue polling every minute even when their next reset is days away. Failing closed protects capacity but repeated queries prolong unavailability.

## Decision

Reuse the auth manager and management APICall contract. At most four actual quota HTTP requests run concurrently across background and management callers. Known authenticated read-only quota endpoints share in-flight work, cached response bytes and credential-scoped cooldowns. Profile reads cache for six hours. Quota cache lifetime follows the normal/near-threshold interval computed from the received observation. Headers expose the original observation time and next check deadline; cached reads never renew freshness. API-key/custom/plugin eligibility remains unchanged. Transport cancellation does not create a provider cooldown.

Honor valid Retry-After seconds or HTTP dates. Otherwise retry 429, server errors and transport failures after exponentially increasing two-minute delays, capped at a thirty-minute base plus up to twenty percent jitter. Successful quota reads participate in the sustained-recovery policy in [adaptive quota recovery](../../implemented/bug-fix/2026-11-12-adaptive-quota-recovery.md); a single success no longer clears escalation. Stopped accounts with known blocking resets sleep until the latest blocking reset plus the existing grace; the deadline only permits verification and never proves recovery. Unknown reset dates retain bounded periodic verification. The browser shows the server deadline and disables both individual and batch refresh while waiting.

The cache is process-local, bounded to 4096 credential identities and sixteen endpoint variants per identity, and prunes expired idle entries. Token replacement changes identity; duplicate credentials share reads. Restart revalidates rather than persisting private response bodies. This partially supersedes the fixed periodic retry policy in [freshness protection](../../implemented/bug-fix/2026-11-11-quota-guard-freshness.md), while retaining failed/stale admission fences.

## Alternatives considered

Only lengthening polling intervals still permits independent browser retries and weakens observation freshness. Client-only debounce cannot coordinate tabs or backend workers. Releasing accounts when a reset timer expires trusts a clock without quota evidence. Sharing arbitrary generic proxy responses risks mixing unrelated requests, so only recognized quota reads participate.

## Verification

Tests verify concurrent deduplication, credential isolation, Retry-After seconds/date, fallback backoff, stopped-account sleep, original observation age, management failure admission, and fresh verification after reset. Focused race tests, full backend tests/build/smoke and 592 frontend tests plus lint/build pass. Browser checks verify the deadline, disabled individual refresh, batch skip, mobile layout and deadline expiry. Artifact publication and deployment remain separately verified operations.

## Consequences

Provider-wide or exit-IP limits can still throttle multiple distinct credentials. Unknown/empty quota responses continue to pause the account. In-flight inference can exceed the reserve. Shared quota reads are not permission to change authentication, inferred balances or account error cooldowns.
