#!/usr/bin/env bash
set -Eeuo pipefail

umask 077

INSTALL_DIR="${CPA_INSTALL_DIR:-/root/cliproxyapi}"
CONFIG_PATH="${INSTALL_DIR}/config.yaml"
INSTALLER_PATH="/usr/local/sbin/cliproxyapi-installer"
INSTALLER_URL="https://raw.githubusercontent.com/nanashiwang/cliproxyapi-installer/refs/heads/master/cliproxyapi-installer"
UPDATE_SCRIPT_URL="https://raw.githubusercontent.com/nanashiwang/CLIProxyAPI/main/deploy/cpa-node/update-node.sh"
PANEL_REPOSITORY="https://github.com/nanashiwang/Cli-Proxy-API-Management-Center"
RELEASE_API_URL="https://api.github.com/repos/nanashiwang/CLIProxyAPI/releases/latest"

log() {
  printf '[cpa-update] %s\n' "$*"
}

fail() {
  printf '[cpa-update] ERROR: %s\n' "$*" >&2
  exit 1
}

require_root() {
  [[ "${EUID}" -eq 0 ]] || fail "run this script as root"
  [[ -d "${INSTALL_DIR}" ]] || fail "CPA installation directory not found: ${INSTALL_DIR}"
  [[ -r "${CONFIG_PATH}" ]] || fail "CPA configuration not found: ${CONFIG_PATH}"
}

refresh_installer() {
  local tmp_path
  tmp_path="$(mktemp)"

  log "refreshing the personal release installer"
  curl --retry 5 --retry-all-errors -fsSL "${INSTALLER_URL}" -o "${tmp_path}"
  grep -q '^REPO_OWNER="nanashiwang"$' "${tmp_path}" || fail "unexpected installer repository owner"
  bash -n "${tmp_path}"
  install -m 0755 "${tmp_path}" "${INSTALLER_PATH}"
  rm -f "${tmp_path}"
}


wait_for_release_asset() {
  local arch asset_variant asset_suffix expected_pattern metadata result
  case "$(uname -m)" in
    x86_64 | amd64) arch="amd64" ;;
    aarch64 | arm64) arch="aarch64" ;;
    *) fail "unsupported Linux architecture: $(uname -m)" ;;
  esac

  asset_variant="default"
  if [[ -r "${INSTALL_DIR}/asset-variant.txt" ]]; then
    asset_variant="$(tr -d '[:space:]' < "${INSTALL_DIR}/asset-variant.txt")"
  fi
  asset_suffix=""
  [[ "${asset_variant}" == "no-plugin" ]] && asset_suffix="_no-plugin"
  expected_pattern="_linux_${arch}${asset_suffix}.tar.gz"

  log "waiting for the latest Release asset matching ${expected_pattern}"
  for attempt in $(seq 1 40); do
    metadata="$(curl --retry 3 --retry-all-errors -fsSL "${RELEASE_API_URL}" || true)"
    if [[ -n "${metadata}" ]]; then
      result="$(python3 -c '
import json, sys
pattern = sys.argv[1]
try:
    data = json.load(sys.stdin)
except Exception:
    raise SystemExit(1)
tag = str(data.get("tag_name") or "").removeprefix("v")
assets = [str(item.get("name") or "") for item in data.get("assets") or []]
matched = next((name for name in assets if name.endswith(pattern)), "")
if tag and matched:
    print(f"{tag}|{matched}")
' "${expected_pattern}" <<<"${metadata}" 2>/dev/null || true)"
      if [[ -n "${result}" ]]; then
        RELEASE_VERSION="${result%%|*}"
        RELEASE_ASSET="${result#*|}"
        export RELEASE_VERSION RELEASE_ASSET
        log "release ready: ${RELEASE_VERSION} (${RELEASE_ASSET})"
        return 0
      fi
    fi
    if [[ "${attempt}" -eq 40 ]]; then
      fail "latest Release is incomplete: missing asset matching ${expected_pattern}"
    fi
    sleep 15
  done
}

set_config_value() {
  CONFIG_PATH="${CONFIG_PATH}" PANEL_REPOSITORY="${PANEL_REPOSITORY}" python3 - <<'PY'
import os
import re
from datetime import datetime, timezone
from pathlib import Path

config_path = Path(os.environ["CONFIG_PATH"])
text = config_path.read_text()
backup = config_path.with_name(
    f"{config_path.name}.bak-{datetime.now(timezone.utc):%Y%m%d%H%M%S}"
)
backup.write_text(text)


def set_top_level(source, key, value):
    pattern = re.compile(rf"(?m)^{re.escape(key)}:\s*.*$")
    replacement = f"{key}: {value}"
    updated, count = pattern.subn(replacement, source, count=1)
    if count:
        return updated
    return source.rstrip() + f"\n{replacement}\n"


def set_nested(source, section, key, value):
    section_match = re.search(rf"(?m)^{re.escape(section)}:\s*$", source)
    if not section_match:
        raise RuntimeError(f"missing section: {section}")
    block_start = section_match.end()
    next_top = re.search(r"(?m)^[A-Za-z0-9][A-Za-z0-9_-]*:\s*", source[block_start:])
    block_end = block_start + (next_top.start() if next_top else len(source[block_start:]))
    block = source[block_start:block_end]
    pattern = re.compile(rf"(?m)^(\s{{2}}{re.escape(key)}:\s*).*$")
    updated_block, count = pattern.subn(rf"\g<1>{value}", block, count=1)
    if count:
        return source[:block_start] + updated_block + source[block_end:]
    return source[:block_end].rstrip() + f"\n  {key}: {value}\n" + source[block_end:]


def quoted(value):
    escaped = value.replace("\\", "\\\\").replace('"', '\\"')
    return f'"{escaped}"'

text = set_top_level(text, "usage-statistics-enabled", "true")
text = set_nested(text, "remote-management", "disable-control-panel", "false")
text = set_nested(text, "remote-management", "panel-github-repository", quoted(os.environ["PANEL_REPOSITORY"]))
config_path.write_text(text)
config_path.chmod(0o600)
print(f"configuration updated; backup={backup}")
PY
}

install_self_updating_command() {
  cat >/usr/local/sbin/update-cliproxyapi <<EOF_UPDATE
#!/usr/bin/env bash
set -Eeuo pipefail
UPDATE_SCRIPT_URL="${UPDATE_SCRIPT_URL}"
tmp_path="\$(mktemp)"
trap 'rm -f "\${tmp_path}"' EXIT
curl --retry 5 --retry-all-errors -fsSL "\${UPDATE_SCRIPT_URL}" -o "\${tmp_path}"
bash -n "\${tmp_path}"
install -m 0755 "\${tmp_path}" /usr/local/sbin/cpa-node-update
exec /usr/local/sbin/cpa-node-update
EOF_UPDATE
  chmod 0755 /usr/local/sbin/update-cliproxyapi
}

update_cpa() {
  log "reloading systemd units before upgrade"
  systemctl daemon-reload
  log "stopping the system service to avoid a text-file-busy replacement"
  systemctl stop cliproxyapi
  trap 'systemctl start cliproxyapi || true' EXIT
  log "upgrading CLIProxyAPI from the personal Release"
  "${INSTALLER_PATH}" upgrade
  local installed_version="unknown"
  [[ -r "${INSTALL_DIR}/version.txt" ]] && installed_version="$(tr -d '[:space:]' < "${INSTALL_DIR}/version.txt")"
  [[ "${installed_version}" == "${RELEASE_VERSION}" ]] || fail "version verification failed: expected ${RELEASE_VERSION}, got ${installed_version}"
  systemctl daemon-reload
  systemctl start cliproxyapi
  systemctl is-active --quiet cliproxyapi
  trap - EXIT
}

verify() {
  local version_file="${INSTALL_DIR}/version.txt"
  local version="unknown"
  [[ -r "${version_file}" ]] && version="$(tr -d '[:space:]' < "${version_file}")"
  log "CLIProxyAPI version: ${version}"
  log "service: $(systemctl is-active cliproxyapi)"
  log "management panel repository: ${PANEL_REPOSITORY}"
  log "usage statistics: enabled"
  if ! curl --max-time 20 -fsS http://127.0.0.1:8317/management.html -o /dev/null; then
    log "management panel check skipped or failed; inspect systemctl/journalctl if needed"
  else
    log "management panel: reachable"
  fi
}

main() {
  require_root
  refresh_installer
  set_config_value
  install_self_updating_command
  wait_for_release_asset
  update_cpa
  verify
}

main "$@"
