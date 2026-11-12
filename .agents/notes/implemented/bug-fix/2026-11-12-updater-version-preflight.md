# Agent Note: Compare installed versions before node update side effects

Status: implemented

## Problem

The systemd updater stops a healthy service before the delegated installer detects an identical version. It also rewrites unchanged configuration and reports panel failure after thirty seconds, while observed startup takes forty-three seconds. Docker similarly replaces an identical binary.

## Decision

Keep the existing self-updating launcher and release installer. Resolve the published architecture asset and read the installed binary's help banner before refreshing the installer, writing configuration, installing command wrappers or stopping services. Equal versions return success immediately in both deployment modes. Unknown, missing or unreadable installed versions abort without mutation. The launcher itself still refreshes updater code, which lets existing nodes receive the fix without upgrading the service. Actual upgrades retain existing behavior and allow a bounded 180-second readiness wait with suppressed transient curl errors.

## Alternatives considered

Checking inside the delegated installer is too late to prevent the outer service stop. Trusting version.txt is simpler but can disagree with the installed binary, especially in Docker. Restarting an equal version for configuration normalization contradicts the requested no-op and can interrupt active calls.

## Verification

Six regression tests cover equal, different, unavailable and unparseable versions in both deployment modes, no mutations on no-op or failed discovery, slow startup and bounded readiness. Shell syntax and Go compilation pass. The automatic source verification runs this regression suite before release tagging. Deployment verifies equal-version behavior against the live node using PID, configuration and backup-count comparisons.

## Consequences

This command no longer repairs configuration or restarts a stopped service merely because it is invoked with an equal version. It remains an updater, not a health-repair command. A stopped Docker container cannot expose its binary version and requires explicit operational recovery.

## Related notes

[Release delivery](../../implemented/process/2026-10-18-release-download-and-container-delivery.md) remains the owner of asset and container publication; this change only alters node-side preflight and readiness. Existing quota and request compatibility notes do not overlap.
