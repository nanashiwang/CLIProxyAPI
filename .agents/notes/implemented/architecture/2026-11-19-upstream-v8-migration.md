# Agent Note: Upstream v8 migration with personal capabilities

Status: implemented

## Problem

The personal branch remains on v7 while upstream advances its configuration layout, management API, executor pipeline and plugin lifecycle. Selective backports no longer provide feature parity. The user requests a full upstream merge while retaining personal capabilities.

## Decision

The personal branch merges upstream v8 history and retain personal account pools, exclusive leases, quota guards, OpenCode integration, persistent pricing and usage, stable provider identities, transport boundaries and verified release delivery. Use the upstream v8 management contract while registering personal operational endpoints under `/v8/management`. Keep legacy configuration loading and top-level quota-guard fields. Keep personal-only catalog entries while current upstream metadata governs shared entries.

Upstream's epoch-aware auth mutations and generation checks supersede equivalent earlier backports. Home owns its credentials: local recovery reuses fresh Home snapshots rather than directly refreshing those credentials. Local OAuth refresh retains its own lifecycle. The translator review finds upstream includes the earlier structured-output, tool-name, namespace, cache-write, terminal-frame and overflow fixes. Its new apply_patch custom-tool support supersedes the earlier drop-only behavior. Thinking retains personal reasoning observations around the new configuration-update extractor.

The paired frontend uses `/config/api-keys/:family` and `/credentials` plus personal versioned endpoints. Deployment scripts target the canonical v8 paths while retaining legacy block YAML; they preserve unrelated content and fail before writing unsupported inline parent maps. This avoids adding an uninstalled YAML runtime dependency to the standalone updater. Nginx keeps management access restrictions on both API generations. Release builders use the module's Go 1.26.10 baseline and the deployment bundle pins the matching CPA frontend release.

PoO-enabled SSE forwarding waits for proof/final error after the response terminal frame. Native Codex discovery keeps account-pool filtering. Internal stream startup prioritizes cancellation. Ordinary quota cooldown remains capped at one hour while forced cooldown stays independent; nonterminal 401 recovery follows upstream refresh without making the credential eligible before success.

Existing personal-only JSON helpers retain their current dependencies to avoid unrelated mechanical rewrites during the lifecycle migration. Shared translator paths use upstream’s ojson pipeline.

The Codex bootstrap budget follows upstream's 48 raw SSE/WebSocket frames and 1 MiB boundary, counting untranslated frames as well as visible events; cancellation still releases personal capacity/lease accounting. Kimi final payload normalization and NO_MORE_RETRY remain in force. Claude and Grok identities follow upstream (2.1.321 and 1.0.46). The automatic tag workflow fetches upstream tags explicitly; pushing merged branch history alone does not transfer its release baseline. The release body uses the Chinese authored summary, first-parent personal commits and a full comparison link: listing every upstream commit would exceed practical release-note size.

## Alternatives considered

Continuing selected patches keeps a smaller diff but cannot satisfy the requested full feature set. Replacing the fork with upstream loses operational capabilities and historical billing. A blind ours/theirs resolution preserves only one side of conflicting lifecycle contracts. The merge uses upstream's current mechanisms where they include the earlier fix, and reconnects personal-only extensions explicitly.

The automatic release source gate uses Ubuntu with default CGO, matching upstream's full PR test/build/smoke workflow. Waiting for an unacquired Intel macOS runner blocks every later platform build; the generic Linux gate allows those builds to proceed. Darwin archive jobs retain Intel runners, Apple SDK/CGO and architecture verification. The local Darwin full suite covers platform-specific power/renewal tests; final publication still requires all release targets. This refines the runner placement in the [release delivery note](../process/2026-10-18-release-download-and-container-delivery.md).

## Consequences

This is a coordinated API/configuration migration. Legacy v7 YAML remains readable; v8 configuration writes preserve personal root settings, provider/key identities, notes and test-model. Personal endpoints coexist with the upstream v8 API. Nonterminal renewal failures retain their refresh evidence without poisoning an otherwise valid access token; terminal failures remain isolated and do not repeatedly refresh. Auth, replay, accounting and quota behavior need their own regression coverage beyond compilation.

Node scripts preserve existing block indentation and missing EOF newlines when editing targeted paths. Inline parent maps, tab indentation and sequence parents fail before the original file is overwritten. Installer-generated Nginx management ACLs migrate to both v0/v8 routes with validation and rollback. The deployment bundle's Go version comes from the tagged module, and the Release stays draft until all binaries, final checksums and the deployment bundle have been uploaded and verified. Draft releases can be retried by the tag workflow, and node-update readiness timeout returns a failing exit status. Container publication follows the completed Release; online node rollout remains an operator action.

## Verification

The final `go test -mod=readonly ./...` run passes all 110 packages with tests (29 more have no tests), and the required server build succeeds. Targeted race checks pass for auth/management and executor/helpers. Focused WebSocket interrupt tests also pass repeated execution. The real v8 smoke loads legacy YAML, round-trips grouped provider identity/note/test-model, and verifies account pools, credentials, capacity, usage import/export, model observations, timezone buckets, pagination and unknown pricing.

The paired Bun 1.4.3 frontend passes 2,581 tests, lint (one pre-existing warning), TypeScript and production build. A real browser connected to the local newly built backend loads nine primary routes with zero console errors; synthetic fixtures cover quota/health alerts, provider preferences and per-file model controls at desktop/mobile sizes. Node installer/updater tests pass eleven cases including v7/v8 configuration, non-default indentation, missing EOF newline, ACL migration and rollback. Archive/download, deployment bundle and checksum regression suites and workflow YAML/shell parsing pass. Note gates pass. Public Release assets are a separate post-push verification gate.

## Related-note audit

The existing identity, quota, service-health, truncated-stream and release-delivery notes retain their invariants. The November selective-security note describes an earlier limited batch and is not expanded into this different migration decision.
