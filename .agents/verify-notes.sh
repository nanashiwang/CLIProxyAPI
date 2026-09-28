#!/usr/bin/env bash
set -euo pipefail
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
command -v bun >/dev/null 2>&1 || { echo 'Bun is required for note checks (CI uses 1.3.14).' >&2; exit 1; }
test -d .agents/notes || { echo 'Missing .agents/notes directory.' >&2; exit 1; }
for script in verify-agent-note-tree verify-agent-note-format verify-archived-agent-notes; do
  bun ".agents/skills/write-notes-like-deepseek/scripts/${script}.ts"
done
