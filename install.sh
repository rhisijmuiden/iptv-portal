#!/usr/bin/env bash
# IPTV Portal installer for Debian, Ubuntu and other systemd-based Linux
# systems (dedicated hardware, a VPS or a container).
#
#   curl -fsSL https://raw.githubusercontent.com/rhisijmuiden/iptv-portal/main/install.sh | sudo bash
#
# Running it again upgrades to the latest release and keeps all data.
# Options (also settable as environment variables):
#   --version vX.Y.Z     install a specific release      (IPTV_PORTAL_VERSION)
#   --admin-port PORT    admin panel port, default 8080   (IPTV_PORTAL_ADMIN_PORT)
#   --portal-port PORT   portal port, default 80          (IPTV_PORTAL_PORTAL_PORT)
#   --reset-admin        print a new one-time admin password
#   --uninstall          remove the service and binary, keep the data
#   --purge              with --uninstall, also delete the data
set -euo pipefail

REPO="rhisijmuiden/iptv-portal"
RELEASES_URL="${IPTV_PORTAL_RELEASES_URL:-https://github.com/${REPO}/releases}"
VERSION="${IPTV_PORTAL_VERSION:-latest}"
ADMIN_PORT="${IPTV_PORTAL_ADMIN_PORT:-}"
PORTAL_PORT="${IPTV_PORTAL_PORTAL_PORT:-}"
RESET_ADMIN=0
UNINSTALL=0
PURGE=0

APP_USER="iptv-portal"
INSTALL_DIR="/opt/iptv-portal"
DATA_DIR="/var/lib/iptv-portal"
BIN="${INSTALL_DIR}/iptv-portal"
UNIT="/etc/systemd/system/iptv-portal.service"

info() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mWarning:\033[0m %s\n' "$*" >&2; }
die() {
  printf '\033[1;31mError:\033[0m %s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<'USAGE'
Usage: install.sh [options]

  --version vX.Y.Z     install a specific release (default: latest)
  --admin-port PORT    admin panel port (default 8080)
  --portal-port PORT   portal port (default 80)
  --reset-admin        print a new one-time admin password
  --uninstall          remove the service and binary, keep the data
  --purge              with --uninstall, also delete the data
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
  --version) VERSION="${2:?--version needs a value}"; shift 2 ;;
  --admin-port) ADMIN_PORT="${2:?--admin-port needs a value}"; shift 2 ;;
  --portal-port) PORTAL_PORT="${2:?--portal-port needs a value}"; shift 2 ;;
  --reset-admin) RESET_ADMIN=1; shift ;;
  --uninstall) UNINSTALL=1; shift ;;
  --purge) PURGE=1; shift ;;
  -h | --help) usage; exit 0 ;;
  *) die "unknown option: $1" ;;
  esac
done

[[ $EUID -eq 0 ]] || die "run this script as root (for example with sudo)"
command -v systemctl >/dev/null || die "systemd is required"

# Runs an iptv-portal command as the service user, so database files keep
# the right owner.
portal_cmd() {
  runuser -u "$APP_USER" -- env IPTV_PORTAL_DATA="$DATA_DIR" "$BIN" "$@"
}

uninstall() {
  info "Removing IPTV Portal"
  systemctl disable --now iptv-portal 2>/dev/null || true
  rm -f "$UNIT" /usr/local/bin/iptv-portal
  rm -rf "$INSTALL_DIR"
  systemctl daemon-reload
  if [[ $PURGE -eq 1 ]]; then
    rm -rf "$DATA_DIR"
    userdel "$APP_USER" 2>/dev/null || true
    info "Removed, including all data"
  else
    info "Removed. The data is kept in ${DATA_DIR}; use --uninstall --purge to delete it too."
  fi
}

if [[ $UNINSTALL -eq 1 ]]; then
  uninstall
  exit 0
fi

install_dependencies() {
  local missing=()
  for cmd in curl tar sha256sum runuser; do
    command -v "$cmd" >/dev/null || missing+=("$cmd")
  done
  [[ ${#missing[@]} -eq 0 ]] && return
  if command -v apt-get >/dev/null; then
    info "Installing ${missing[*]}"
    apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq curl tar coreutils util-linux ca-certificates >/dev/null
  else
    die "please install: ${missing[*]}"
  fi
}

detect_arch() {
  case "$(uname -m)" in
  x86_64 | amd64) echo amd64 ;;
  aarch64 | arm64) echo arm64 ;;
  *) die "unsupported CPU architecture: $(uname -m) (amd64 and arm64 are supported)" ;;
  esac
}

port_in_use() {
  if command -v ss >/dev/null; then
    ss -Hltn "sport = :$1" 2>/dev/null | grep -q .
  else
    return 1
  fi
}

download() {
  local arch="$1" tmp="$2" base
  if [[ "$VERSION" == "latest" ]]; then
    base="${RELEASES_URL}/latest/download"
  else
    base="${RELEASES_URL}/download/${VERSION}"
  fi
  local file="iptv-portal_linux_${arch}.tar.gz"
  info "Downloading ${VERSION} release for ${arch}"
  curl -fsSL -o "${tmp}/${file}" "${base}/${file}" ||
    die "download failed: ${base}/${file} (is there a published release?)"
  if curl -fsSL -o "${tmp}/checksums.txt" "${base}/checksums.txt"; then
    (cd "$tmp" && grep " ${file}\$" checksums.txt | sha256sum -c --quiet -) ||
      die "checksum of the download does not match"
  else
    warn "no checksums.txt in the release, skipping verification"
  fi
  tar -xzf "${tmp}/${file}" -C "$tmp" iptv-portal
}

write_unit() {
  cat >"$UNIT" <<EOF
[Unit]
Description=IPTV Portal
Documentation=https://github.com/${REPO}
After=network-online.target
Wants=network-online.target

[Service]
User=${APP_USER}
Group=${APP_USER}
Environment=IPTV_PORTAL_DATA=${DATA_DIR}
ExecStart=${BIN} serve
Restart=always
RestartSec=3
# Lets the service listen on ports below 1024 (the portal uses port 80).
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=true

[Install]
WantedBy=multi-user.target
EOF
}

main() {
  install_dependencies
  local arch fresh=0
  arch="$(detect_arch)"
  [[ -f "${DATA_DIR}/portal.db" ]] || fresh=1

  TMP_DIR="$(mktemp -d)"
  trap 'rm -rf "$TMP_DIR"' EXIT
  download "$arch" "$TMP_DIR"

  if ! id "$APP_USER" >/dev/null 2>&1; then
    info "Creating system user ${APP_USER}"
    useradd --system --home-dir "$DATA_DIR" --shell /usr/sbin/nologin "$APP_USER"
  fi
  # The service owns its install directory so it can update itself from the
  # admin panel.
  install -d -o "$APP_USER" -g "$APP_USER" -m 0755 "$INSTALL_DIR"
  install -d -o "$APP_USER" -g "$APP_USER" -m 0750 "$DATA_DIR"
  local was_running=0
  systemctl is-active --quiet iptv-portal 2>/dev/null && was_running=1
  install -o "$APP_USER" -g "$APP_USER" -m 0755 "${TMP_DIR}/iptv-portal" "${BIN}.new"
  mv -f "${BIN}.new" "$BIN"
  ln -sf "$BIN" /usr/local/bin/iptv-portal

  if [[ $fresh -eq 1 ]]; then
    if [[ -z "$PORTAL_PORT" ]] && port_in_use 80; then
      PORTAL_PORT=8081
      warn "port 80 is already in use (a reverse proxy?), the portal will use port ${PORTAL_PORT}"
    fi
    if [[ -z "$ADMIN_PORT" ]] && port_in_use 8080; then
      ADMIN_PORT=8090
      warn "port 8080 is already in use, the admin panel will use port ${ADMIN_PORT}"
    fi
  fi
  local port_args=()
  [[ -n "$ADMIN_PORT" ]] && port_args+=(-admin "$ADMIN_PORT")
  [[ -n "$PORTAL_PORT" ]] && port_args+=(-portal "$PORTAL_PORT")
  if [[ ${#port_args[@]} -gt 0 ]]; then
    portal_cmd set-port "${port_args[@]}" >/dev/null
  fi

  write_unit
  systemctl daemon-reload
  systemctl enable --quiet iptv-portal
  if [[ $was_running -eq 1 ]]; then
    systemctl restart iptv-portal
  else
    systemctl start iptv-portal
  fi

  local version
  version="$("$BIN" version)"
  if [[ $fresh -eq 1 || $RESET_ADMIN -eq 1 ]]; then
    local creds
    creds="$(portal_cmd admin-reset)"
    echo
    info "IPTV Portal ${version} is installed and running"
    echo
    while IFS= read -r line; do printf '    %s\n' "$line"; done <<<"$creds"
    echo
    echo "    Log in to the admin panel and choose a new password."
    echo "    Behind a reverse proxy? Set the public URL under Settings."
    echo
  else
    info "IPTV Portal updated to ${version}"
  fi
}

main
