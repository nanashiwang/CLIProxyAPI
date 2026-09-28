# Provenance and local adaptation

Source: https://github.com/czm15053/write-notes-like-deepseek
Pinned upstream commit: `8f3b404cf9fd8102f96561f390219877abf094a8`.
Imported on 2026-09-29 from the reviewed project-local bundle in newapi commit `599fb93745755ae9953ae68e811b292a04f9f3b7`.
The upstream README declares MIT, but that upstream commit has no standalone LICENSE file; no license text is invented here.

The bundle contains the original Skill, reference documents, verification/archive/board scripts and the board HTML template. Upstream text is retained in its original language as third-party material. Promotional images, demo notes, Git metadata and asset-import/export utilities are excluded.

The proposed and implemented templates add an existing-capabilities/impact section. This repository replaces the bundle provenance text; other source files retain the reviewed bundle content. Updates require reviewing the source diff and preserving local policy rather than copying the latest version blindly.

Project AGENTS.md and user authorization override imported interaction defaults. The local entry point is `bash .agents/verify-notes.sh`; see `docs/AGENT_NOTES.md`. Bun is a development/CI tool only. No runtime package manager is replaced.
