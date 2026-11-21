#!/usr/bin/env bash
# IPTV Portal installer for Debian (12 and 13), Ubuntu and other
# systemd-based Linux systems: dedicated hardware, a VM, a VPS or an LXC
# container. A bare Debian install is enough; missing tools are installed
# with apt.
#
#   curl -fsSL https://raw.githubusercontent.com/rhisijmuiden/iptv-portal/main/install.sh | sudo bash
#   wget -qO- https://raw.githubusercontent.com/rhisijmuiden/iptv-portal/main/install.sh | bash    (as root)
#
# Running it again upgrades to the latest release and keeps all data.
# Options (also settable as environment variables):
#   --version vX.Y.Z     install a specific release      (IPTV_PORTAL_VERSION)
#   --admin-port PORT    admin panel port, default 8080   (IPTV_PORTAL_ADMIN_PORT)
#   --portal-port PORT   portal port, default 80          (IPTV_PORTAL_PORTAL_PORT)
#   --archive FILE       install from a downloaded iptv-portal_linux_*.tar.gz
#                                                         (IPTV_PORTAL_ARCHIVE)
#   --reset-admin        print a new one-time admin password
#   --template           prepare a container template: install everything, but
#                        create the admin on the first boot and start nothing
#                        (used to build the Proxmox CT template)
#   --uninstall          remove the service and binary, keep the data
#   --purge              with --uninstall, also delete the data
set -euo pipefail

# "su" without "-" on Debian keeps the user's PATH, which lacks the sbin
# directories that useradd and runuser live in.
export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:${PATH:-}"

REPO="rhisijmuiden/iptv-portal"
RELEASES_URL="${IPTV_PORTAL_RELEASES_URL:-https://github.com/${REPO}/releases}"
VERSION="${IPTV_PORTAL_VERSION:-latest}"
ADMIN_PORT="${IPTV_PORTAL_ADMIN_PORT:-}"
PORTAL_PORT="${IPTV_PORTAL_PORTAL_PORT:-}"
ARCHIVE="${IPTV_PORTAL_ARCHIVE:-}"
RESET_ADMIN=0
TEMPLATE=0
UNINSTALL=0
PURGE=0

APP_USER="iptv-portal"
INSTALL_DIR="/opt/iptv-portal"
DATA_DIR="/var/lib/iptv-portal"
BIN="${INSTALL_DIR}/iptv-portal"
UNIT="/etc/systemd/system/iptv-portal.service"
FIRST_BOOT_UNIT="/etc/systemd/system/iptv-portal-first-boot.service"
PROFILE_SCRIPT="/etc/profile.d/iptv-portal.sh"
ISSUE_LINK="/etc/issue.d/iptv-portal.issue"
BASHRC="/etc/bash.bashrc"
BASHRC_MARK="# added by the IPTV Portal installer"

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
  --archive FILE       install from a downloaded iptv-portal_linux_*.tar.gz
  --reset-admin        print a new one-time admin password
  --template           prepare a container template (admin made on first boot)
  --uninstall          remove the service and binary, keep the data
  --purge              with --uninstall, also delete the data
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
  --version) VERSION="${2:?--version needs a value}"; shift 2 ;;
  --admin-port) ADMIN_PORT="${2:?--admin-port needs a value}"; shift 2 ;;
  --portal-port) PORTAL_PORT="${2:?--portal-port needs a value}"; shift 2 ;;
  --archive) ARCHIVE="${2:?--archive needs a file}"; shift 2 ;;
  --reset-admin) RESET_ADMIN=1; shift ;;
  --template) TEMPLATE=1; shift ;;
  --uninstall) UNINSTALL=1; shift ;;
  --purge) PURGE=1; shift ;;
  -h | --help) usage; exit 0 ;;
  *) die "unknown option: $1" ;;
  esac
done

[[ $EUID -eq 0 ]] || die "run this script as root (for example with sudo, or after su -)"
command -v systemctl >/dev/null ||
  die "systemd is required (in a container, use a template with systemd such as Debian 13)"

# Runs an iptv-portal command as the service user, so database files keep
# the right owner.
portal_cmd() {
  runuser -u "$APP_USER" -- env IPTV_PORTAL_DATA="$DATA_DIR" "$BIN" "$@"
}

uninstall() {
  info "Removing IPTV Portal"
  systemctl disable --now iptv-portal 2>/dev/null || true
  systemctl disable iptv-portal-first-boot 2>/dev/null || true
  rm -f "$UNIT" "$FIRST_BOOT_UNIT" "$PROFILE_SCRIPT" "$ISSUE_LINK" /usr/local/bin/iptv-portal
  [[ -f "$BASHRC" ]] && sed -i "/${BASHRC_MARK}\$/d" "$BASHRC"
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

# Installs what a bare Debian may lack. The portal itself needs
# ca-certificates for updates and for streams and guides over HTTPS.
install_dependencies() {
  local -A package_of=([curl]=curl [tar]=tar [sha256sum]=coreutils [runuser]=util-linux [useradd]=passwd)
  local cmd packages=()
  for cmd in tar sha256sum runuser useradd; do
    command -v "$cmd" >/dev/null || packages+=("${package_of[$cmd]}")
  done
  [[ -n "$ARCHIVE" ]] || command -v curl >/dev/null || packages+=(curl)
  [[ -f /etc/ssl/certs/ca-certificates.crt ]] || packages+=(ca-certificates)
  [[ ${#packages[@]} -eq 0 ]] && return
  command -v apt-get >/dev/null || die "please install: ${packages[*]}"
  info "Installing ${packages[*]}"
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends "${packages[@]}" >/dev/null
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
  if [[ -n "$ARCHIVE" ]]; then
    [[ -f "$ARCHIVE" ]] || die "archive not found: ${ARCHIVE}"
    info "Installing from ${ARCHIVE}"
    tar -xzf "$ARCHIVE" -C "$tmp" iptv-portal
    return
  fi
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

# Console support. On the first boot of a container made from the template,
# iptv-portal-first-boot creates the admin with a one-time password (on a
# normal install the installer does that itself, so the unit is skipped).
# The console login banner (/etc/issue.d) and every interactive root shell
# (SSH, pct enter) then show the admin panel address and, until it has been
# changed, that login.
write_console_support() {
  cat >"$FIRST_BOOT_UNIT" <<EOF
[Unit]
Description=IPTV Portal first boot setup (admin with a one-time password)
Documentation=https://github.com/${REPO}
ConditionPathExists=!${DATA_DIR}/portal.db
Before=iptv-portal.service

[Service]
Type=oneshot
User=${APP_USER}
Group=${APP_USER}
Environment=IPTV_PORTAL_DATA=${DATA_DIR}
ExecStart=${BIN} admin-reset
StandardOutput=null

[Install]
WantedBy=multi-user.target
EOF

  cat >"$PROFILE_SCRIPT" <<EOF
# Shows the IPTV Portal admin panel address, and the one-time login until it
# has been changed, in interactive root shells. Added by install.sh.
case "\$-" in
*i*)
  if [ -z "\${IPTV_PORTAL_LOGIN_SHOWN:-}" ] && [ "\$(id -u)" = 0 ] && [ -f ${DATA_DIR}/portal.db ]; then
    export IPTV_PORTAL_LOGIN_SHOWN=1
    iptv_portal_login="\$(/usr/sbin/runuser -u ${APP_USER} -- env IPTV_PORTAL_DATA=${DATA_DIR} ${BIN} login-info 2>/dev/null)"
    if [ -n "\$iptv_portal_login" ]; then
      printf '\\n  IPTV Portal\\n%s\\n\\n' "\$(printf '%s\\n' "\$iptv_portal_login" | sed 's/^/  /')"
    fi
    unset iptv_portal_login
  fi
  ;;
esac
EOF
  chmod 0644 "$PROFILE_SCRIPT"
  # pct enter and su start shells that are no login shell; those read
  # bash.bashrc instead of profile.d.
  if [[ -f "$BASHRC" ]] && ! grep -qF "$BASHRC_MARK" "$BASHRC"; then
    printf '[ -r %s ] && . %s %s\n' "$PROFILE_SCRIPT" "$PROFILE_SCRIPT" "$BASHRC_MARK" >>"$BASHRC"
  fi
  install -d -m 0755 /etc/issue.d
  ln -sfn "${DATA_DIR}/login-banner.issue" "$ISSUE_LINK"
}

main() {
  install_dependencies
  local arch fresh=0
  arch="$(detect_arch)"
  [[ -f "${DATA_DIR}/portal.db" ]] || fresh=1
  if [[ $TEMPLATE -eq 1 ]]; then
    [[ $fresh -eq 1 ]] || die "--template needs a system without IPTV Portal data"
    [[ -z "$ADMIN_PORT$PORTAL_PORT" ]] || die "--template keeps the default ports; change them in the admin panel"
  fi

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
  if [[ $TEMPLATE -eq 0 ]] && systemctl is-active --quiet iptv-portal 2>/dev/null; then
    was_running=1
  fi
  install -o "$APP_USER" -g "$APP_USER" -m 0755 "${TMP_DIR}/iptv-portal" "${BIN}.new"
  mv -f "${BIN}.new" "$BIN"
  ln -sf "$BIN" /usr/local/bin/iptv-portal

  write_unit
  write_console_support
  local version
  version="$("$BIN" version)"

  if [[ $TEMPLATE -eq 1 ]]; then
    # In a chroot, systemctl enable only creates the links, which is all a
    # template needs.
    systemctl enable --quiet iptv-portal-first-boot iptv-portal
    info "IPTV Portal ${version} is prepared; the admin is created on the first boot"
    return
  fi

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
  # Create the admin before the first start, so the service does not make
  # one of its own. Releases before login-info print the login right away.
  local login=""
  if [[ $fresh -eq 1 || $RESET_ADMIN -eq 1 ]]; then
    login="$(portal_cmd admin-reset)"
  fi

  systemctl daemon-reload
  systemctl enable --quiet iptv-portal-first-boot iptv-portal
  if [[ $was_running -eq 1 ]]; then
    systemctl restart iptv-portal
  else
    systemctl start iptv-portal
  fi

  if "$BIN" help | grep -q login-info; then
    login="$(portal_cmd login-info -write-banner)"
  fi
  if [[ $fresh -eq 1 || $RESET_ADMIN -eq 1 ]]; then
    echo
    info "IPTV Portal ${version} is installed and running"
    echo
    while IFS= read -r line; do printf '    %s\n' "$line"; done <<<"$login"
    echo
  else
    info "IPTV Portal updated to ${version}"
  fi
}

main
