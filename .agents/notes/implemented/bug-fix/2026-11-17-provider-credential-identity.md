# Agent Note: Stable provider and credential identity with per-key notes

Status: implemented

## Problem

OpenAI-compatible auth IDs include the mutable provider name. Renaming replaces runtime credentials while account pools retain the old IDs as missing members. Multiple keys have no independent operator label.

## Decision

The implementation extends the existing configuration, management API and provider editor with persisted provider and key IDs plus per-key note. Names and notes are display metadata. Preserve legacy credential IDs during migration so pool membership and leases retain their references. New management-created records receive opaque IDs. Existing records keep IDs through rename, key editing and reordering; removal and replacement are distinct operations. The migration recovers an already orphaned legacy group reference only when its full legacy hash matches exactly one current credential and membership is unambiguous; otherwise keep the missing reference inert.

The implementation reuses the existing synthesizer, account-pool list and shared key editor. The frontend preserves IDs through normalization, editing, serialization and unknown-field merges, and uses IDs to target provider updates. Notes appear in collapsed key cards and pool credential names. Do not use array position to transfer another key's identity or note.

## Alternatives considered

Regenerating all IDs as UUIDs is simple for new installations but breaks existing pool and lease references. Name-change-specific cleanup removes the symptom but repeats the defect after other credential edits. Persist existing identities and assign opaque IDs to new entries instead. Removing every missing pool member could silently change access restrictions; only cryptographically verified, unique legacy matches are eligible for recovery.

## Verification

Regression tests cover repeated renames, key rotation and reordering, independent notes and clearing, ID-targeted PATCH, duplicate ID rejection, runtime synthesis, reload and stable group membership. Historical recovery tests cover unique matches, duplicate keys, conflicting historical bindings and mismatches. Browser acceptance uses an isolated real backend with two synthetic keys and a preassigned group; rename and both notes survive save/reopen and restart.

## Consequences

Legacy configuration migrations must preserve ordering and disabled entries. Ambiguous historical references must remain unavailable instead of granting access. Backend and frontend versions must be deployed together.

## Related note review

The provider-test-model note overlaps the editor and serialization paths but owns a separate preference contract. Usage/account-facts and quota notes concern display statistics and scheduling, not credential identity, and are unrelated. No active note owns provider or key identity.

Identity migration retains historical credential ID strings as opaque values; new management entries use UUIDs. Provider routing uses persisted IDs while pool UI exposes the provider family and current display name. First-load migration requires a writable config and preserves YAML comments. Read-only deployments must pre-migrate their config. Existing unknown/ambiguous pool references are not deleted.

Validation: full backend Go tests, server build, management smoke and focused race checks pass. Frontend verify passes 618 tests, lint, TypeScript and production build (one pre-existing AccountPoolsPage lint warning). Browser verification confirms two distinct notes and exactly two group members after rename and backend restart.
