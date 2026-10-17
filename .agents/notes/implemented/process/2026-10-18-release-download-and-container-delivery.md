# Agent Note: Verify archived build inputs and publish personal containers

Status: implemented

## Problem

The FreeBSD amd64 job fails while downloading its pinned 14.3 sysroot, blocking the entire personal release despite successful Linux builds. Quiet wget output hides the transport failure. The inherited DockerHub workflow targets the upstream namespace and is not dispatched by the personal tag workflow, so a successful binary release does not deliver a personal container.

## Decision

Keep the 14.3 baseline and pinned SHA256. Add bounded downloads with visible failures and an HTTPS Princeton mirror fallback with an identical 14.3 MANIFEST digest; never extract bytes that fail the pinned digest. Publish repository-owned GHCR containers from the same release tag after all binary release gates succeed, using the existing Dockerfile and native amd64/arm64 runners. A manual run accepts an existing published release tag, resolves its exact Git commit and rebuilds that source with the corrected workflow. This allows delivery fixes without changing an already published tag. Container smoke tests run with the runner UID/GID in their mounted temporary working directory, so cleanup can remove all test data. Serialize container publication and update latest only for GitHub's current public release.

## Alternatives considered

- Skipping FreeBSD or publishing the incomplete draft bypasses the existing release completeness gate; rejected.
- The official HTTP archive is also unreachable from the GitHub runner and offers no availability improvement; preserve HTTPS-only downloads.
- Upgrading the sysroot changes the compatibility baseline without need; retain the pinned archive.
- Using the upstream DockerHub namespace requires unrelated credentials and risks publishing to another owner; use GHCR with the repository token.
- Migrating production from systemd to Docker exceeds artifact delivery; preserve binary assets and leave deployment to the operator.

## Testing

Four local download regression tests pass, including primary failure, fallback success, corrupt bodies, complete failure and stale-file cleanup. Shell syntax and actionlint pass. The server builds and the management API smoke suite passes. GitHub FreeBSD verification run 39060235999 downloads the Princeton copy, verifies the original SHA256 and successfully compiles the CGO binary after the official source times out. The .59 release passes all ten binary targets and publishes its checksums and deployment bundle. Both container architectures pass version and management assertions; their first delivery attempt exposes root-owned temporary data cleanup and is correctly rejected. The corrected workflow uses the runner UID/GID. Container run 39061682934 passes both architectures, their management tests and cleanup, then publishes the multi-platform manifest. Anonymous registry verification returns both Linux architectures and digest `sha256:1dbfcde56944cf4275a8d790a30fc768933e2274fba29bc0ebb9c69ba7596b1c`. The Linux amd64 release archive and deployment ZIP also match their published checksums. Production rollout and live Claude-provider acceptance remain operator-owned and unperformed.

## Consequences

The official archive is a single-location service. Every HTTPS source is authenticated by a fixed in-repository digest, never by a checksum dynamically trusted from the same download. The Princeton mirror may later prune old releases, so complete network failure remains a hard error. Network outages can still prevent release. GHCR package visibility is verified separately from build success. Containers do not imply a production rollout or live Claude acceptance.

## Related-note audit

The capability-review process note supplies the documentation contract; no existing release/download note owns this decision. The Claude capability note concerns runtime behavior and remains independent.
