# Agent Note: Capability review and repository decision notes

Status: implemented

## Problem

Page-local changes can duplicate existing capabilities; chat-only reasoning does not travel with the repository. Changes need evidence from adjacent entry points and durable rationale without replacing existing project rules.

## Existing capabilities and impact

Repository rules, README, tracked source entry points and CI were inspected. No existing .agents/notes, docs/adr or docs/decisions tree was found. Existing architecture and product documentation remain authoritative. Only workflow documentation and an independent verification job are affected.

## Decision

The repository now has a scoped capability map, a project-local pinned decision-note Skill, and an offline Bun verification entry point. Preserve current build tools, release rules and deployment boundaries. Record decisions, not invented historical explanations. Continue within existing authorization instead of adding routine confirmation gates.

## Alternatives considered

- Chat reminders alone are cheap but cannot preserve decisions alongside source changes.
- Installing only the Skill preserves rationale but cannot discover overlooked product capabilities; require code and adjacent-entry review as well.
- A shared absolute path saves duplicated tooling but breaks standalone clones and CI; vendor a fixed copy into each repository.

## Verification

Local note checks pass from outside the repository, and project documentation links resolve. Original tracked AGENTS.md content is preserved as an unchanged prefix. Isolated temporary fixtures confirm rejection of missing alternatives, dead links, missing seals, modified archived text and modified text plus replacement seals against a Git baseline. Remote CI results are recorded by the actual workflow runs, not assumed from local success. Unrelated local changes are excluded from this task.

## Consequences

Maps can become stale and need source verification. Structural checks do not establish design quality or guarantee complete note coverage. Bun is a development-only prerequisite, not an application dependency.

The benefit is searchable design rationale and explicit adjacent-capability review. The maintenance cost is keeping maps and notes aligned with code. This workflow-only update does not change application versions, publish a release or deploy services.

The existing required server compilation passed: `go build -o /tmp/cpa-notes-check-server ./cmd/server`.
