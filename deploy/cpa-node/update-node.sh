#!/usr/bin/env bash
set -Eeuo pipefail

umask 077

INSTALL_DIR="${CPA_INSTALL_DIR:-/root/cliproxyapi}"
DOCKER_DIR="${CPA_DOCKER_DIR:-/opt/cpa}"
DOCKER_CONTAINER="${CPA_DOCKER_CONTAINER:-cli-proxy-api}"
DEPLOY_MODE="${CPA_DEPLOY_MODE:-auto}"
CONFIG_PATH="${INSTALL_DIR}/config.yaml"
METADATA_DIR="${INSTALL_DIR}"
INSTALLER_PATH="/usr/local/sbin/cliproxyapi-installer"
INSTALLER_URL="https://raw.githubusercontent.com/nanashiwang/cliproxyapi-installer/refs/heads/master/cliproxyapi-installer"
UPDATE_SCRIPT_URL="https://raw.githubusercontent.com/nanashiwang/CLIProxyAPI/main/deploy/cpa-node/update-node.sh"
PANEL_REPOSITORY="https://github.com/nanashiwang/Cli-Proxy-API-Management-Center"
LATEST_CHECKSUMS_URL="https://github.com/nanashiwang/CLIProxyAPI/releases/latest/download/checksums.txt"

log() {
  printf '[cpa-update] %s\n' "$*"
}

fail() {
  printf '[cpa-update] ERROR: %s\n' "$*" >&2
  exit 1
}

require_root() {
  [[ "${EUID}" -eq 0 ]] || fail "run this script as root"
}

detect_deployment() {
  case "${DEPLOY_MODE}" in
    systemd)
      DEPLOY_MODE="systemd"
      ;;
    docker)
      DEPLOY_MODE="docker"
      ;;
    auto)
      if [[ -d "${INSTALL_DIR}" && -r "${INSTALL_DIR}/config.yaml" ]]; then
        DEPLOY_MODE="systemd"
      elif command -v docker >/dev/null 2>&1 && docker inspect "${DOCKER_CONTAINER}" >/dev/null 2>&1; then
        DEPLOY_MODE="docker"
      else
        fail "unable to detect CPA deployment; set CPA_INSTALL_DIR or CPA_DOCKER_DIR/CPA_DOCKER_CONTAINER"
      fi
      ;;
    *)
      fail "invalid CPA_DEPLOY_MODE: ${DEPLOY_MODE} (expected auto, systemd, or docker)"
      ;;
  esac

  if [[ "${DEPLOY_MODE}" == "systemd" ]]; then
    CONFIG_PATH="${INSTALL_DIR}/config.yaml"
    METADATA_DIR="${INSTALL_DIR}"
    [[ -d "${INSTALL_DIR}" ]] || fail "CPA installation directory not found: ${INSTALL_DIR}"
    [[ -r "${CONFIG_PATH}" ]] || fail "CPA configuration not found: ${CONFIG_PATH}"
    log "deployment: systemd (${INSTALL_DIR})"
    return
  fi

  command -v docker >/dev/null 2>&1 || fail "Docker is not installed"
  docker inspect "${DOCKER_CONTAINER}" >/dev/null 2>&1 || fail "Docker container not found: ${DOCKER_CONTAINER}"
  CONFIG_PATH="${DOCKER_DIR}/config.yaml"
  METADATA_DIR="${DOCKER_DIR}"
  [[ -d "${DOCKER_DIR}" ]] || fail "Docker CPA directory not found: ${DOCKER_DIR}"
  [[ -r "${CONFIG_PATH}" ]] || fail "CPA configuration not found: ${CONFIG_PATH}"
  log "deployment: docker (${DOCKER_DIR}, container=${DOCKER_CONTAINER})"
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
  local arch asset_variant asset_suffix expected_pattern checksums result
  case "$(uname -m)" in
    x86_64 | amd64) arch="amd64" ;;
    aarch64 | arm64) arch="aarch64" ;;
    *) fail "unsupported Linux architecture: $(uname -m)" ;;
  esac

  asset_variant="default"
  if [[ -r "${METADATA_DIR}/asset-variant.txt" ]]; then
    asset_variant="$(tr -d '[:space:]' < "${METADATA_DIR}/asset-variant.txt")"
  fi
  asset_suffix=""
  [[ "${asset_variant}" == "no-plugin" ]] && asset_suffix="_no-plugin"
  expected_pattern="_linux_${arch}${asset_suffix}.tar.gz"

  log "waiting for the latest published asset matching ${expected_pattern}"
  for attempt in $(seq 1 40); do
    checksums="$(curl --connect-timeout 10 --max-time 60 --retry 3 -fsSL "${LATEST_CHECKSUMS_URL}" 2>/dev/null || true)"
    if [[ -n "${checksums}" ]]; then
      result="$(python3 -c '
import sys
pattern = sys.argv[1]
for line in sys.stdin:
    parts = line.split()
    if len(parts) < 2:
        continue
    filename = parts[-1]
    prefix = "CLIProxyAPI_"
    if filename.startswith(prefix) and filename.endswith(pattern):
        version = filename[len(prefix):-len(pattern)]
        if version:
            print(f"{version}|{filename}")
            break
' "${expected_pattern}" <<<"${checksums}" 2>/dev/null || true)"
      if [[ -n "${result}" ]]; then
        RELEASE_VERSION="${result%%|*}"
        RELEASE_ASSET="${result#*|}"
        RELEASE_ASSET_VARIANT="${asset_variant}"
        export RELEASE_VERSION RELEASE_ASSET RELEASE_ASSET_VARIANT
        log "release ready: ${RELEASE_VERSION} (${RELEASE_ASSET})"
        return 0
      fi
    fi
    if [[ "${attempt}" -eq 40 ]]; then
      fail "latest published Release is incomplete: missing asset matching ${expected_pattern}"
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
if [[ "\${EUID}" -ne 0 ]]; then
  exec sudo /usr/local/sbin/update-cliproxyapi "\$@"
fi
UPDATE_SCRIPT_URL="${UPDATE_SCRIPT_URL}"
tmp_path="\$(mktemp)"
trap 'rm -f "\${tmp_path}"' EXIT
curl --retry 5 --retry-all-errors -fsSL "\${UPDATE_SCRIPT_URL}" -o "\${tmp_path}"
bash -n "\${tmp_path}"
install -m 0755 "\${tmp_path}" /usr/local/sbin/cpa-node-update
exec /usr/local/sbin/cpa-node-update "\$@"
EOF_UPDATE
  chmod 0755 /usr/local/sbin/update-cliproxyapi
  ln -sfn /usr/local/sbin/update-cliproxyapi /usr/local/bin/update-cliproxyapi
}

download_docker_binary() {
  local work_dir archive_path checksums expected_checksum actual_checksum binary_path
  work_dir="$(mktemp -d)"
  archive_path="${work_dir}/${RELEASE_ASSET}"

  log "downloading ${RELEASE_ASSET} for Docker deployment"
  curl --retry 5 --retry-all-errors -fsSL \
    "https://github.com/nanashiwang/CLIProxyAPI/releases/download/v${RELEASE_VERSION}/${RELEASE_ASSET}" \
    -o "${archive_path}"

  checksums="$(curl --retry 5 --retry-all-errors -fsSL "${LATEST_CHECKSUMS_URL}")"
  expected_checksum="$(awk -v file="${RELEASE_ASSET}" '$2 == file {print $1; exit}' <<<"${checksums}")"
  [[ "${expected_checksum}" =~ ^[[:xdigit:]]{64}$ ]] || fail "checksum not found for ${RELEASE_ASSET}"
  actual_checksum="$(sha256sum "${archive_path}" | awk '{print $1}')"
  [[ "${actual_checksum}" == "${expected_checksum}" ]] || fail "checksum verification failed for ${RELEASE_ASSET}"

  tar -xzf "${archive_path}" -C "${work_dir}"
  binary_path="${work_dir}/cli-proxy-api"
  [[ -f "${binary_path}" ]] || fail "release archive does not contain cli-proxy-api"
  chmod 0755 "${binary_path}"
  RELEASE_BINARY_PATH="${binary_path}"
  export RELEASE_BINARY_PATH
  log "release checksum verified"
}

configured_port() {
  local port
  port="$(sed -nE 's/^[[:space:]]*port:[[:space:]]*([0-9]+).*$/\1/p' "${CONFIG_PATH}" | head -n 1)"
  if [[ "${port}" =~ ^[0-9]+$ ]]; then
    printf '%s\n' "${port}"
  else
    printf '8317\n'
  fi
}

wait_for_management_panel() {
  local port="$(configured_port)"
  for attempt in $(seq 1 30); do
    if curl --connect-timeout 2 --max-time 5 -fsS "http://127.0.0.1:${port}/management.html" -o /dev/null; then
      return 0
    fi
    sleep 1
  done
  return 1
}

update_systemd() {
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

update_docker() {
  local backup_dir current_version backup_binary image_ref
  backup_dir="${DOCKER_DIR}/backups"
  install -d -m 0700 "${backup_dir}"
  current_version="$(docker exec "${DOCKER_CONTAINER}" /CLIProxyAPI/CLIProxyAPI --version 2>&1 | head -n 1 || true)"
  image_ref="$(docker inspect "${DOCKER_CONTAINER}" --format '{{.Config.Image}}')"
  backup_binary="${backup_dir}/CLIProxyAPI.before-${RELEASE_VERSION}-$(date -u +%Y%m%d%H%M%S)"
  docker cp "${DOCKER_CONTAINER}:/CLIProxyAPI/CLIProxyAPI" "${backup_binary}"
  chmod 0600 "${backup_binary}"
  log "current Docker binary: ${current_version:-unknown}"

  log "stopping Docker container before replacing the binary"
  docker stop "${DOCKER_CONTAINER}" >/dev/null
  trap 'docker start "${DOCKER_CONTAINER}" >/dev/null 2>&1 || true' EXIT
  docker cp "${RELEASE_BINARY_PATH}" "${DOCKER_CONTAINER}:/CLIProxyAPI/CLIProxyAPI"
  docker start "${DOCKER_CONTAINER}" >/dev/null
  docker inspect "${DOCKER_CONTAINER}" --format '{{.State.Running}}' | grep -qx true || fail "Docker container failed to start"
  printf '%s\n' "${RELEASE_VERSION}" > "${DOCKER_DIR}/version.txt"
  printf '%s\n' "${RELEASE_ASSET_VARIANT:-default}" > "${DOCKER_DIR}/asset-variant.txt"
  if [[ -n "${image_ref}" && "${image_ref}" != "<none>" ]]; then
    if docker commit "${DOCKER_CONTAINER}" "${image_ref}" >/dev/null; then
      log "updated Docker image snapshot: ${image_ref}"
    else
      log "warning: unable to snapshot the updated Docker image; the running container is healthy"
    fi
  fi
  trap - EXIT
}

update_cpa() {
  if [[ "${DEPLOY_MODE}" == "docker" ]]; then
    download_docker_binary
    update_docker
  else
    update_systemd
  fi
}

verify() {
  local version="unknown" service_status port
  port="$(configured_port)"
  if [[ "${DEPLOY_MODE}" == "docker" ]]; then
    version="$(docker exec "${DOCKER_CONTAINER}" /CLIProxyAPI/CLIProxyAPI --version 2>&1 | head -n 1 || true)"
    service_status="$(docker inspect "${DOCKER_CONTAINER}" --format '{{.State.Status}}')"
  else
    [[ -r "${INSTALL_DIR}/version.txt" ]] && version="$(tr -d '[:space:]' < "${INSTALL_DIR}/version.txt")"
    service_status="$(systemctl is-active cliproxyapi)"
  fi
  log "CLIProxyAPI version: ${version}"
  log "service: ${service_status}"
  log "management panel repository: ${PANEL_REPOSITORY}"
  log "usage statistics: enabled"
  if wait_for_management_panel; then
    log "management panel: reachable (port ${port})"
  else
    log "management panel check skipped or failed after 30s; inspect service/container logs"
  fi
}

main() {
  require_root
  detect_deployment
  if [[ "${DEPLOY_MODE}" == "systemd" ]]; then
    refresh_installer
  fi
  set_config_value
  install_self_updating_command
  wait_for_release_asset
  update_cpa
  verify
}

main "$@"
