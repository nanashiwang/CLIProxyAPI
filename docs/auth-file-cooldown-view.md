# Auth file cooldown presentation

Selectively adapted from upstream `b4ff581dafa4583c0fa8b86afeedae9be72b93a3`.

`Auth.AvailabilityView` computes a read-only view of a manager snapshot. Both
`GET /v0/management/auth-files` and `GET /v0/management/account-pools` use it.
Expired transient credential/model cooldowns present as `active`, with an empty
status message and no expired retry deadline. A partial model cooldown does not
make an otherwise available credential globally unavailable; sparse model maps
are not treated as the complete supported-model catalog.

Local safety rules take precedence over the upstream implementation:

- A terminal 401 stays unavailable even with a scheduled refresh retry.
- Expired token metadata and explicit `token expired` failures stay in error.
- Confirmed Codex quota exhaustion stays unavailable until the existing recovery
  observer confirms recovery. Passing a reset timestamp alone is insufficient.
- Unknown quota recovery deadlines and persistent unavailability are not cleared.
- Disabled credentials retain their disabled status.
- Reads never clear errors, quota evidence, cooldown storage or scheduler state.
- Credential health is separate from account allocation. A recovered credential
  can still be occupied by an exclusive lease. Lease ownership, active streams,
  expiry/draining state and allocation remain governed by the lease store.

The management UI already derives warnings from `status`, `status_message` and
`unavailable`, and renders lease badges from the separate account-pool lease
records. It must not infer quota recovery or a free lease from a browser clock.
No frontend runtime change is required; frontend contract tests cover these rules.

Verification covers expired/future deadlines, model cooldowns, sparse states,
terminal failures, quota recovery guards, API consistency, concurrent GET/result
updates under the race detector, and occupied exclusive leases. Publication does
not deploy or restart an existing service.
