# Agent Note: Verify archived build inputs and publish personal containers

Status: implemented

## Problem

The FreeBSD amd64 job fails while downloading its pinned 14.3 sysroot, blocking the entire personal release despite successful Linux builds. Quiet wget output hides the transport failure. The inherited DockerHub workflow targets the upstream namespace and is not dispatched by the personal tag workflow, so a successful binary release does not deliver a personal container.

## Decision

Keep the 14.3 baseline and pinned SHA256. Add bounded downloads with visible failures and a documented FreeBSD archive fallback; never extract bytes that fail the pinned digest. Publish repository-owned GHCR containers from the same release tag after all binary release gates succeed, using the existing Dockerfile and native amd64/arm64 runners. A manual tagged run can retry container delivery. Serialize container publication and update latest only for GitHub's current public release.

## Alternatives considered

- Skipping FreeBSD or publishing the incomplete draft bypasses the existing release completeness gate; rejected.
- Upgrading the sysroot changes the compatibility baseline without need; retain the pinned archive.
- Using the upstream DockerHub namespace requires unrelated credentials and risks publishing to another owner; use GHCR with the repository token.
- Migrating production from systemd to Docker exceeds artifact delivery; preserve binary assets and leave deployment to the operator.

## Testing

Four local download regression tests pass, including primary failure, fallback success, corrupt bodies, complete failure and stale-file cleanup. Shell syntax and actionlint pass. The server builds and the management API smoke suite passes. Real FreeBSD archive availability and actual GHCR delivery still require CI verification; local validation does not establish release or deployment success.

## Consequences

The official archive is a single-location service. HTTP fallback for a public archive is authenticated by a fixed in-repository digest, never by a checksum fetched from that same transport. Network outages can still prevent release. GHCR package visibility is verified separately from build success. Containers do not imply a production rollout or live Claude acceptance.

## Related-note audit

The capability-review process note supplies the documentation contract; no existing release/download note owns this decision. The Claude capability note concerns runtime behavior and remains independent.
