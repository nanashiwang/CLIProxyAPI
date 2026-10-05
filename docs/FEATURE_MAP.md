# CLIProxyAPI: capability map

Initially verified on 2026-09-29; usage timezone, pricing and quota facts rechecked on 2026-10-05. This is a scoped navigation aid, not a complete product audit or a replacement for [existing guidance](../AGENTS.md). Recheck implementation when using it; extend entries only as tasks confirm the relevant behavior. Do not treat roadmap ideas as implemented features.

| Capability | Entry / UI | API / orchestration | Implementation / data | Review boundary |
|---|---|---|---|---|
| HTTP routing and provider execution | [internal/api/server.go](../internal/api/server.go) | [internal/runtime/executor](../internal/runtime/executor) | [internal/translator](../internal/translator) | Trace the executor and canonical thinking pipeline before changing protocol translation; preserve existing translator restrictions. |
| Credentials, pools and selection | [internal/api/handlers/management/account_pools.go](../internal/api/handlers/management/account_pools.go) | [sdk/cliproxy/auth](../sdk/cliproxy/auth) | [internal/store](../internal/store) | Capacity, leases, cooling and credential storage have distinct roles; inspect selection and refresh together. |
| Usage and diagnostics | [internal/api/handlers/management/usage_dashboard.go](../internal/api/handlers/management/usage_dashboard.go) | [internal/usage](../internal/usage) | [internal/api/handlers/management/usage_statistics.go](../internal/api/handlers/management/usage_statistics.go) | Requests and execution attempts are distinct. Optional IANA timezone controls DST-aware chart buckets; omitted timezone stays UTC. Coordinate response contracts with the management frontend. |
| Token pricing | [internal/pricing/calculate.go](../internal/pricing/calculate.go) | [internal/pricing/service.go](../internal/pricing/service.go) | [internal/pricing/fallback.json](../internal/pricing/fallback.json) | Dynamic catalog overrides embedded fallback; billing snapshots remain immutable. GPT-6.1 Sol fallback supports context thresholds and effective service tiers. |
| Management assets and reload | [internal/managementasset](../internal/managementasset) | [internal/watcher](../internal/watcher) | [cmd/server](../cmd/server) | The sibling management frontend consumes /v0/management; verify both sides for contract changes. |

Use personal/main, not upstream origin, for project pushes. The parent CPA scope includes only this backend and Cli-Proxy-API-Management-Center. Retain English authored docs/comments and required Go compile checks.

Before adding a feature, inspect adjacent flows and the current source of truth; shared filters, data definitions and access rules must not diverge across entry points.
