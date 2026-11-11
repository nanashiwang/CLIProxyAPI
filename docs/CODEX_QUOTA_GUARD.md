# Codex quota guard

In the management configuration page, open **Quota fallback → Codex quota protection**.
The guard is enabled by default for standalone Codex OAuth accounts. API keys,
custom endpoints, plugin credentials and Home dispatch are outside this policy.

```yaml
codex-quota-guard:
  enabled: true
  five-hour-used-percent: 90
  five-hour-jitter: 5
  weekly-used-percent: 93
  weekly-jitter: 3
```

Values describe **consumed quota**, not remaining quota. Defaults stop each account
at a uniformly distributed threshold between 85–95% consumed for its five-hour
window and 90–96% consumed for its weekly window. Either window can pause routing.
Set jitter to zero for an exact threshold. Threshold minus jitter must be above
zero, and threshold plus jitter must not exceed 100. Invalid configurations are
rejected. Configuration changes take effect through the normal hot-reload path.

The sample is stable for an account identity and window duration, across requests,
refreshes, quota resets and restarts. This avoids oscillation caused by upstream
relative-reset timestamp drift. Changing the configured bounds rescales the sample.

The existing scheduler excludes paused accounts and can select other eligible
accounts. Existing response headers and authenticated usage checks supply quota
observations. Paused accounts remain eligible for background checks, normally at
most once per 30 minutes, with an additional check after the reported reset plus
the existing two-minute grace period. Routing resumes after observed usage is below
both thresholds, subject to any independent authentication or upstream cooldown.
Passing a reset deadline alone never fabricates fresh quota.

This is an observation-based guard, not a token reservation. Unknown usage cannot
be protected until a valid observation arrives, and calls already in flight can
exceed the threshold. Disabling error cooling or manually resetting cooldown does
not bypass the guard; use `enabled: false` to disable proactive protection.
