# CLIProxyAPI: decision-note workflow

The [pinned project-local Skill](../.agents/skills/write-notes-like-deepseek/UPSTREAM.md) records rationale alongside code. It does not replace existing project instructions or architecture documents. The repository is self-contained: no absolute path to another checkout or global Codex memory is required.

## Working sequence

1. State the business outcome. Read [the capability map](FEATURE_MAP.md), verify current code and permissions, and inspect related UI/API/data paths before choosing a solution.
2. Search active proposed, implemented and rejected notes. Reuse an existing note when only its facts change. Record new non-trivial decisions with the [proposal template](../.agents/skills/write-notes-like-deepseek/templates/proposed.md), including evidence of existing capabilities, affected entry points, real alternatives and acceptance criteria.
3. Proceed within existing user authorization. Imported confirmation defaults do not introduce repeated approval requirements. Missing material decisions still need user input; proposed notes do not themselves authorize deployment.
4. Once implementation and relevant checks are complete, rewrite Proposal as Decision and acceptance/risks as actual validation/consequences; move to implemented in the same commit as code. Decision reversals get new notes with links. Archive only eligible superseded implemented notes, not abandoned proposals.
5. Run the note checks and the existing tests/build required for the changed area. Update affected map entries. Mechanical edits do not require new notes; do not retroactively invent historical decisions.

## Layout and commands

Notes live under `.agents/notes/{proposed,implemented,rejected,archived}/{class}/yyyy-mm-dd-topic.md`. Classes are feature, bug-fix, simplification, architecture, process and testing. Create directories as needed. No central note index is required; the capability map indexes code, not individual notes.

Bun is used only to execute the pinned TypeScript tooling (CI: 1.3.14); it adds no application dependency and does not replace the project's package manager. From any directory, invoke the shell script by its path; auxiliary commands below run from the repository root:

```sh
bash .agents/verify-notes.sh
bun .agents/skills/write-notes-like-deepseek/scripts/archive-agent-note.ts <old-note> --superseded-by <new-note>
bun .agents/skills/write-notes-like-deepseek/scripts/build-board.ts --bundle .agents/notes /tmp/decision-board.html 'Project decisions'
```

The archive command mutates files; inspect the diff and inbound links. The optional board bundles note content, so keep private notes and credentials out of published artifacts. Do not commit generated boards or real customer data, passwords, tokens or private keys.

## Verification boundaries

The independent Verify Agent Notes CI job runs on main pushes and pull requests, with full Git history. Archive comparison uses the pre-push commit or PR base, not the new HEAD. CI failure does not automatically configure branch protection; no branch protection changes are included here.

Checks enforce directory/format/link and archive-seal integrity, not completeness of product research, truth of alternatives, or optimality of a design. Existing business tests, builds, release checks and production acceptance remain separate. Use personal/main, not upstream origin, for project pushes. The parent CPA scope includes only this backend and Cli-Proxy-API-Management-Center. Retain English authored docs/comments and required Go compile checks.
