# Multi-provider quota guard

Open **Configuration → Quota fallback → Multi-provider quota protection**.
Protection defaults to enabled. All thresholds describe consumed quota.

```yaml
quota-guard:
  enabled: true
  five-hour-used-percent: 90
  five-hour-jitter: 5
  weekly-used-percent: 93
  weekly-jitter: 3
  monthly-used-percent: 93
  monthly-jitter: 3
```

The common block overrides legacy `codex-quota-guard` fields individually. Omitted
fields retain the legacy value or the defaults above. No manual migration is
required. Saving a visual field writes its common value and preserves unrelated
YAML. Bounds must satisfy 0 < threshold - jitter and threshold + jitter <= 100.
Zero jitter makes an exact threshold. Configuration reload applies immediately.

| Provider | Usage source | Windows |
|---|---|---|
| Codex OAuth | Existing usage headers and authenticated usage refresh | Five-hour and weekly |
| Claude OAuth | Anthropic OAuth usage endpoint | Five-hour, weekly and reported scoped weekly limits |
| Grok/xAI OAuth | Existing CLI billing endpoints | Weekly credits, reported product percentages and monthly included allowance |
| Kimi | Coding usages endpoint, OAuth or first-party API key | Explicit duration/unit windows and weekly summary |
| Antigravity OAuth | Project quota summary endpoint | Explicit five-hour and weekly buckets |

Threshold selection uses actual duration classes: shorter than seven days uses the
short-window setting, seven to under 28 days uses weekly, and at least 28 days
uses monthly. Grok monthly billing is explicitly classified as monthly rather than
five-hour usage. Unknown-duration windows are not assigned a fabricated period.
Paid xAI API health checks without numeric quotas do not enable percentage protection.
The guard never sends paid chat probes to discover quota.

Each account/window receives a stable uniformly distributed sample within its
configured range. Any window, including scoped model/group windows, pauses the
whole account; other eligible accounts remain available. The protection is independent
of error-cooling toggles and manual cooldown resets.

The added providers use the existing bounded worker pool with a one-minute poll
interval and oldest-attempt priority. Codex retains its existing passive observations,
30-minute refresh policy and reset-grace checks. Fresh lower usage resumes routing
only when all retained windows are below their thresholds and other failures permit
selection. Missing windows, malformed responses, failed requests and wall-clock
expiry never fabricate a zero balance or successful recovery. Snapshots survive restart
and are fenced against stale responses and credential replacement.

This is observation-based protection, not token reservation. Work between polls or
already in flight may exceed a threshold. Large fleets or slow upstream requests may
delay checks. Local provider polling applies only in standalone mode; Home dispatch,
plugin credentials and custom endpoints retain their existing authority. API keys
without a supported usage endpoint cannot supply subscription percentages.
