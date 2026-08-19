#!/usr/bin/env bash
set -Eeuo pipefail

INVENTORY_FILE="${1:-}"
UPDATE_SCRIPT_URL="https://raw.githubusercontent.com/nanashiwang/CLIProxyAPI/main/deploy/cpa-node/update-node.sh"

if [[ -z "${INVENTORY_FILE}" || ! -r "${INVENTORY_FILE}" ]]; then
  printf 'Usage: %s servers.tsv\n' "$0" >&2
  printf 'TSV format: node<TAB>host<TAB>port\n' >&2
  exit 2
fi

success=0
failed=0
while IFS=$'\t' read -r node host port extra; do
  [[ -z "${node}" || "${node:0:1}" == "#" ]] && continue
  [[ -z "${host}" ]] && continue
  port="${port:-22}"
  if [[ -n "${extra:-}" ]]; then
    printf '[batch] invalid line for %s: too many columns\n' "${node}" >&2
    failed=$((failed + 1))
    continue
  fi

  printf '\n[batch] updating %s (%s:%s)\n' "${node}" "${host}" "${port}"
  if ssh -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new \
    -p "${port}" "root@${host}" \
    "curl --retry 5 --retry-all-errors -fsSL '${UPDATE_SCRIPT_URL}' | bash"; then
    success=$((success + 1))
    printf '[batch] %s succeeded\n' "${node}"
  else
    failed=$((failed + 1))
    printf '[batch] %s failed; continuing\n' "${node}" >&2
  fi
done < "${INVENTORY_FILE}"

printf '\n[batch] completed: success=%d failed=%d\n' "${success}" "${failed}"
(( failed == 0 ))
