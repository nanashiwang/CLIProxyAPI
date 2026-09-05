#!/usr/bin/env bash
set -Eeuo pipefail

umask 077

ENV_FILE="${1:-/root/cpa-node.env}"
INSTALLER_URL="https://raw.githubusercontent.com/nanashiwang/cliproxyapi-installer/refs/heads/master/cliproxyapi-installer"
INSTALLER_PATH="/usr/local/sbin/cliproxyapi-installer"
INSTALL_DIR="/root/cliproxyapi"
CONFIG_PATH="${INSTALL_DIR}/config.yaml"
SERVICE_PATH="/etc/systemd/system/cliproxyapi.service"
PANEL_REPOSITORY="https://github.com/nanashiwang/Cli-Proxy-API-Management-Center"
UPDATE_SCRIPT_URL="https://raw.githubusercontent.com/nanashiwang/CLIProxyAPI/main/deploy/cpa-node/update-node.sh"

log() {
  printf '[cpa-node] %s\n' "$*"
}

fail() {
  printf '[cpa-node] ERROR: %s\n' "$*" >&2
  exit 1
}

require_root() {
  [[ "${EUID}" -eq 0 ]] || fail "run this script as root"
}

load_environment() {
  [[ -r "${ENV_FILE}" ]] || fail "environment file not found: ${ENV_FILE}"
  # shellcheck disable=SC1090
  source "${ENV_FILE}"

  : "${CPA_NODE:?CPA_NODE is required}"
  : "${CPA_DOMAIN:?CPA_DOMAIN is required}"
  : "${CPA_MANAGEMENT_KEY:?CPA_MANAGEMENT_KEY is required}"
  : "${CPA_API_KEYS:?CPA_API_KEYS is required}"
  : "${ACME_EMAIL:?ACME_EMAIL is required}"

  SING_BOX_CONFIG="${SING_BOX_CONFIG:-/etc/sing-box/config.json}"
  CPA_PORT="${CPA_PORT:-8317}"
  CPA_COMMERCIAL_MODE="${CPA_COMMERCIAL_MODE:-true}"
  CPA_LOG_LIMIT_MB="${CPA_LOG_LIMIT_MB:-512}"
  CPA_ERROR_LOG_MAX_FILES="${CPA_ERROR_LOG_MAX_FILES:-10}"
  CPA_ADMIN_CIDR="${CPA_ADMIN_CIDR:-}"
  ENABLE_UFW="${ENABLE_UFW:-0}"
  SSH_PORT="${SSH_PORT:-22}"

  [[ "${CPA_NODE}" =~ ^cpa[0-9]+$ ]] || fail "invalid CPA_NODE: ${CPA_NODE}"
  [[ "${CPA_DOMAIN}" =~ ^[A-Za-z0-9.-]+$ ]] || fail "invalid CPA_DOMAIN: ${CPA_DOMAIN}"
  [[ "${CPA_PORT}" =~ ^[0-9]+$ ]] || fail "invalid CPA_PORT: ${CPA_PORT}"
  [[ "${CPA_COMMERCIAL_MODE}" == "true" || "${CPA_COMMERCIAL_MODE}" == "false" ]] || fail "CPA_COMMERCIAL_MODE must be true or false"
  [[ -z "${CPA_ADMIN_CIDR}" || "${CPA_ADMIN_CIDR}" =~ ^[0-9A-Fa-f:.]+/[0-9]+$ ]] || fail "invalid CPA_ADMIN_CIDR: ${CPA_ADMIN_CIDR}"
  [[ ${#CPA_MANAGEMENT_KEY} -ge 12 ]] || fail "CPA_MANAGEMENT_KEY must contain at least 12 characters"
  [[ -r "${SING_BOX_CONFIG}" ]] || fail "sing-box config not found: ${SING_BOX_CONFIG}"
}

install_packages() {
  log "installing required packages"
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y \
    ca-certificates curl wget tar nginx openssl python3 git
}

install_cpa() {
  log "installing CPA from the personal release repository"
  curl --retry 5 --retry-all-errors -fsSL "${INSTALLER_URL}" -o "${INSTALLER_PATH}"
  grep -q '^REPO_OWNER="nanashiwang"$' "${INSTALLER_PATH}" || fail "unexpected installer repository owner"
  chmod 0755 "${INSTALLER_PATH}"
  "${INSTALLER_PATH}" install
  [[ -x "${INSTALL_DIR}/cli-proxy-api" ]] || fail "CPA binary was not installed"
  [[ -f "${CONFIG_PATH}" ]] || fail "CPA config was not installed"
}

configure_cpa() {
  log "applying the reusable CPA configuration"
  cp -a "${CONFIG_PATH}" "${CONFIG_PATH}.bak.$(date +%Y%m%d%H%M%S)"

  CONFIG_PATH="${CONFIG_PATH}" \
  SING_BOX_CONFIG="${SING_BOX_CONFIG}" \
  CPA_MANAGEMENT_KEY="${CPA_MANAGEMENT_KEY}" \
  CPA_API_KEYS="${CPA_API_KEYS}" \
  CPA_PORT="${CPA_PORT}" \
  CPA_COMMERCIAL_MODE="${CPA_COMMERCIAL_MODE}" \
  CPA_LOG_LIMIT_MB="${CPA_LOG_LIMIT_MB}" \
  CPA_ERROR_LOG_MAX_FILES="${CPA_ERROR_LOG_MAX_FILES}" \
  CPA_PANEL_REPOSITORY="${PANEL_REPOSITORY}" \
  python3 - <<'PY'
import json
import os
import re
from pathlib import Path
from urllib.parse import quote

config_path = Path(os.environ["CONFIG_PATH"])
sing_box_path = Path(os.environ["SING_BOX_CONFIG"])
text = config_path.read_text()


def quoted(value):
    return json.dumps(str(value), ensure_ascii=False)


def set_top_level(source, key, value):
    pattern = re.compile(rf"(?m)^{re.escape(key)}:\s*.*$")
    replacement = f"{key}: {value}"
    updated, count = pattern.subn(replacement, source, count=1)
    if count != 1:
        raise RuntimeError(f"missing top-level setting: {key}")
    return updated


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
    if count != 1:
        raise RuntimeError(f"missing setting: {section}.{key}")
    return source[:block_start] + updated_block + source[block_end:]


def set_api_keys(source, values):
    lines = source.splitlines(keepends=True)
    start = next((i for i, line in enumerate(lines) if re.match(r"^api-keys:\s*$", line.rstrip("\n"))), None)
    if start is None:
        raise RuntimeError("missing api-keys section")
    end = start + 1
    while end < len(lines):
        line = lines[end]
        if line.strip() == "" or line.startswith(" ") or line.startswith("\t"):
            end += 1
            continue
        break
    replacement = ["api-keys:\n"] + [f"  - {quoted(value)}\n" for value in values]
    return "".join(lines[:start] + replacement + lines[end:])


sing_box = json.loads(sing_box_path.read_text())
inbound = None
for candidate in sing_box.get("inbounds", []):
    if candidate.get("listen_port") and candidate.get("users"):
        inbound = candidate
        break
if inbound is None:
    raise RuntimeError("no authenticated sing-box inbound found")

user = inbound["users"][0]
username = str(user.get("username", ""))
password = str(user.get("password", ""))
if not username or not password:
    raise RuntimeError("sing-box inbound username or password is empty")

proxy_url = (
    f"socks5://{quote(username, safe='')}:{quote(password, safe='')}"
    f"@127.0.0.1:{int(inbound['listen_port'])}/"
)

api_keys = [item.strip() for item in os.environ["CPA_API_KEYS"].split(",") if item.strip()]
if not api_keys:
    raise RuntimeError("CPA_API_KEYS does not contain a usable key")
if any(item.startswith("replace-") or item.startswith("your-") for item in api_keys):
    raise RuntimeError("CPA_API_KEYS still contains placeholder values")

text = set_top_level(text, "host", quoted("127.0.0.1"))
text = set_top_level(text, "port", int(os.environ["CPA_PORT"]))
text = set_nested(text, "remote-management", "allow-remote", "true")
text = set_nested(text, "remote-management", "secret-key", quoted(os.environ["CPA_MANAGEMENT_KEY"]))
text = set_nested(text, "remote-management", "disable-control-panel", "false")
text = set_nested(text, "remote-management", "panel-github-repository", quoted(os.environ["CPA_PANEL_REPOSITORY"]))
text = set_api_keys(text, api_keys)
text = set_top_level(text, "commercial-mode", os.environ["CPA_COMMERCIAL_MODE"])
text = set_top_level(text, "logging-to-file", "true")
text = set_top_level(text, "logs-max-total-size-mb", int(os.environ["CPA_LOG_LIMIT_MB"]))
text = set_top_level(text, "error-logs-max-files", int(os.environ["CPA_ERROR_LOG_MAX_FILES"]))
text = set_top_level(text, "usage-statistics-enabled", "true")
text = set_top_level(text, "redis-usage-queue-retention-seconds", 3600)
text = set_top_level(text, "proxy-url", quoted(proxy_url))
text = set_top_level(text, "request-retry", 3)
text = set_top_level(text, "max-retry-credentials", 0)
text = set_top_level(text, "max-retry-interval", 30)
text = set_top_level(text, "ws-auth", "true")

config_path.write_text(text)
os.chmod(config_path, 0o600)
PY
}

install_service() {
  log "installing the systemd service"
  cat >"${SERVICE_PATH}" <<EOF_SERVICE
[Unit]
Description=CLIProxyAPI
Wants=network-online.target
After=network-online.target sing-box.service
Requires=sing-box.service
StartLimitIntervalSec=0

[Service]
Type=simple
User=root
Group=root
Environment=HOME=/root
WorkingDirectory=${INSTALL_DIR}
ExecStartPre=/usr/bin/test -x ${INSTALL_DIR}/cli-proxy-api
ExecStartPre=/usr/bin/test -r ${CONFIG_PATH}
ExecStart=${INSTALL_DIR}/cli-proxy-api -config ${CONFIG_PATH}
Restart=always
RestartSec=3
TimeoutStopSec=30
LimitNOFILE=1048576
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full

[Install]
WantedBy=multi-user.target
EOF_SERVICE

  chmod 0644 "${SERVICE_PATH}"
  systemctl daemon-reload
  systemctl enable --now cliproxyapi
}

install_certificate() {
  log "installing acme.sh and issuing an ECC certificate"
  if [[ ! -x /root/.acme.sh/acme.sh ]]; then
    if ! curl --retry 5 --retry-all-errors -fsSL https://get.acme.sh | sh -s email="${ACME_EMAIL}"; then
      log "the acme.sh archive installer failed; falling back to a shallow Git clone"
      local acme_source="/root/.cache/cpa-node-acme.sh"
      install -d -m 0700 /root/.cache
      if [[ -d "${acme_source}/.git" ]]; then
        git -C "${acme_source}" pull --ff-only
      else
        rm -rf "${acme_source}"
        git -c http.version=HTTP/1.1 clone --depth 1 \
          https://github.com/acmesh-official/acme.sh.git "${acme_source}"
      fi
      (cd "${acme_source}" && ./acme.sh --install --accountemail "${ACME_EMAIL}")
    fi
  fi
  [[ -x /root/.acme.sh/acme.sh ]] || fail "acme.sh installation failed"

  systemctl start nginx || true
  if ! /root/.acme.sh/acme.sh --issue \
    --alpn \
    --listen-v4 \
    --keylength ec-256 \
    -d "${CPA_DOMAIN}" \
    --server letsencrypt \
    --pre-hook "systemctl stop nginx" \
    --post-hook "systemctl start nginx"; then
    systemctl start nginx || true
    fail "certificate issuance failed"
  fi

  install -d -m 0755 "/etc/nginx/ssl/${CPA_NODE}"
  /root/.acme.sh/acme.sh --install-cert \
    -d "${CPA_DOMAIN}" \
    --ecc \
    --key-file "/etc/nginx/ssl/${CPA_NODE}/key.pem" \
    --fullchain-file "/etc/nginx/ssl/${CPA_NODE}/fullchain.pem" \
    --reloadcmd "systemctl reload nginx"
}

install_nginx() {
  log "installing the Nginx reverse proxy"
  install -d -m 0755 /etc/nginx/snippets
  if [[ -n "${CPA_ADMIN_CIDR}" ]]; then
    cat >/etc/nginx/snippets/cpa-management-allow.conf <<EOF_ACL
allow ${CPA_ADMIN_CIDR};
deny all;
EOF_ACL
  else
    printf '%s\n' '# Management access is not IP-restricted.' >/etc/nginx/snippets/cpa-management-allow.conf
  fi
  chmod 0644 /etc/nginx/snippets/cpa-management-allow.conf
  cat >"/etc/nginx/sites-available/${CPA_DOMAIN}" <<EOF_NGINX
limit_req_zone \$binary_remote_addr zone=cpa_management:10m rate=10r/s;
map \$http_upgrade \$connection_upgrade {
    default upgrade;
    '' close;
}

server {
    listen 80;
    listen [::]:80;
    server_name ${CPA_DOMAIN};

    return 301 https://\$host\$request_uri;
}

server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name ${CPA_DOMAIN};
    server_tokens off;

    ssl_certificate /etc/nginx/ssl/${CPA_NODE}/fullchain.pem;
    ssl_certificate_key /etc/nginx/ssl/${CPA_NODE}/key.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_session_cache shared:CPA_SSL:10m;
    ssl_session_timeout 1d;
    ssl_session_tickets off;

    client_max_body_size 100m;

    add_header Strict-Transport-Security "max-age=15552000" always;
    add_header X-Content-Type-Options "nosniff" always;
    add_header X-Frame-Options "SAMEORIGIN" always;
    add_header Referrer-Policy "no-referrer" always;

    location = /healthz {
        access_log off;
        default_type text/plain;
        return 200 "ok\n";
    }

    location ~ ^/(management\.html|v0/management(?:/|$)) {
        include /etc/nginx/snippets/cpa-management-allow.conf;
        limit_req zone=cpa_management burst=30 nodelay;
        proxy_pass http://127.0.0.1:${CPA_PORT};
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection \$connection_upgrade;
        proxy_buffering off;
        proxy_request_buffering off;
        proxy_connect_timeout 10s;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }

    location / {
        proxy_pass http://127.0.0.1:${CPA_PORT};
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection \$connection_upgrade;
        proxy_buffering off;
        proxy_request_buffering off;
        proxy_connect_timeout 10s;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
EOF_NGINX

  chmod 0644 "/etc/nginx/sites-available/${CPA_DOMAIN}"
  ln -sfn "/etc/nginx/sites-available/${CPA_DOMAIN}" "/etc/nginx/sites-enabled/${CPA_DOMAIN}"
  rm -f /etc/nginx/sites-enabled/default
  nginx -t
  systemctl enable nginx
  systemctl reload nginx
}

install_update_command() {
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

configure_firewall() {
  if [[ "${ENABLE_UFW}" != "1" ]]; then
    log "UFW management is disabled"
    return
  fi

  log "configuring UFW"
  DEBIAN_FRONTEND=noninteractive apt-get install -y ufw
  ufw allow "${SSH_PORT}/tcp"
  ufw allow 80/tcp
  ufw allow 443/tcp
  ufw deny "${CPA_PORT}/tcp"
  ufw --force enable
}

verify_installation() {
  log "verifying services"
  systemctl is-active --quiet sing-box
  systemctl is-active --quiet cliproxyapi
  systemctl is-active --quiet nginx
  nginx -t
  curl -fsS --max-time 20 "https://${CPA_DOMAIN}/healthz" >/dev/null

  printf '\nCPA node deployed successfully.\n'
  printf 'API: https://%s\n' "${CPA_DOMAIN}"
  printf 'Management: https://%s/management.html\n' "${CPA_DOMAIN}"
  printf 'Update command: update-cliproxyapi\n'
}

main() {
  require_root
  load_environment
  install_packages
  install_cpa
  configure_cpa
  install_service
  install_certificate
  install_nginx
  install_update_command
  configure_firewall
  verify_installation
}

main "$@"
