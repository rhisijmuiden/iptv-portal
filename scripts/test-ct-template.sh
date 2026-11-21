#!/usr/bin/env bash
# Boots a Debian 13 root filesystem with systemd-nspawn and checks that IPTV
# Portal comes up with a one-time admin login, the way a Proxmox container
# would. Used by CI; needs root and systemd-container.
#
#   sudo scripts/test-ct-template.sh template debian-13-iptv-portal_amd64.tar.zst
#       first boot of a container made from the CT template
#   sudo scripts/test-ct-template.sh bare debian-13-standard_amd64.tar.zst iptv-portal_linux_amd64.tar.gz
#       install.sh on a bare Debian 13 container
#
# The container shares the host network, so ports 80 and 8080 must be free.
set -euo pipefail

MODE="${1:?usage: $0 template TEMPLATE | bare BASE ARCHIVE}"
ROOTFS_TAR="$(realpath "${2:?missing root filesystem}")"
ARCHIVE="${3:-}"
REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MACHINE="iptv-portal-test-$$"
WORK="$(mktemp -d)"
ROOTFS="${WORK}/rootfs"

cleanup() {
  machinectl terminate "$MACHINE" 2>/dev/null || true
  sleep 2
  if [[ -f "${WORK}/nspawn.log" && "${FAILED:-0}" -eq 1 ]]; then
    echo "--- container console ---"
    tail -n 60 "${WORK}/nspawn.log"
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

fail() {
  FAILED=1
  echo "FAIL: $*" >&2
  exit 1
}

# Runs a command inside the booted container and prints its output.
run() { systemd-run -M "$MACHINE" --quiet --wait --pipe -- "$@"; }

mkdir -p "$ROOTFS"
tar --zstd -xpf "$ROOTFS_TAR" -C "$ROOTFS" --numeric-owner
if [[ "$MODE" == bare ]]; then
  [[ -f "$ARCHIVE" ]] || fail "missing release archive"
  install -m 0755 "${REPO_DIR}/install.sh" "${ROOTFS}/root/install.sh"
  install -m 0644 "$ARCHIVE" "${ROOTFS}/root/iptv-portal.tar.gz"
fi

echo "==> Booting ${MODE} container"
systemd-nspawn --quiet --directory "$ROOTFS" --machine "$MACHINE" --boot >"${WORK}/nspawn.log" 2>&1 &
for _ in $(seq 60); do
  run true 2>/dev/null && break
  sleep 2
done
run true >/dev/null || fail "the container did not boot"
state="$(run systemctl is-system-running --wait || true)"
echo "    system state: ${state}"

if [[ "$MODE" == bare ]]; then
  echo "==> Running install.sh"
  out="$(run bash /root/install.sh --archive /root/iptv-portal.tar.gz)" || {
    echo "$out"
    fail "install.sh failed"
  }
  echo "$out"
  grep -q "One-time password:" <<<"$out" || fail "install.sh did not print the one-time password"
  grep -q "Admin panel:  *http://.*:8080/" <<<"$out" || fail "install.sh did not print the admin panel address"
else
  run systemctl is-enabled iptv-portal-first-boot >/dev/null || fail "first boot unit not enabled"
  run test -f /var/lib/iptv-portal/initial-credentials || fail "the first boot did not create the admin"
fi

echo "==> Checking the service"
for _ in $(seq 30); do
  curl -fsS -o /dev/null http://127.0.0.1:8080/login && break
  sleep 1
done
curl -fsS -o /dev/null http://127.0.0.1:8080/login || fail "admin panel not reachable on port 8080"
curl -fsS -o /dev/null http://127.0.0.1:80/get.php || [[ $? -eq 22 ]] || fail "portal not listening on port 80"
run systemctl is-active iptv-portal >/dev/null || fail "iptv-portal.service is not active"

echo "==> Checking the login shown to root"
shell="$(run bash -ic true 2>/dev/null)"
echo "$shell"
grep -q "Admin panel:" <<<"$shell" || fail "a root shell does not show the admin panel address"
grep -q "Username:  *admin" <<<"$shell" || fail "a root shell does not show the username"
password="$(sed -n 's/.*One-time password:  *//p' <<<"$shell")"
[[ -n "$password" ]] || fail "a root shell does not show the one-time password"

banner="$(run cat /etc/issue.d/iptv-portal.issue)"
grep -qF "$password" <<<"$banner" || fail "the console banner does not show the one-time password"
grep -qF 'http://\4:8080/' <<<"$banner" || fail "the console banner does not show the admin panel address"

echo "==> Logging in with the one-time password"
jar="${WORK}/cookies"
code="$(curl -sS -o /dev/null -w '%{http_code}' -c "$jar" -b "$jar" \
  --data-urlencode username=admin --data-urlencode "password=${password}" http://127.0.0.1:8080/login)"
[[ "$code" == 303 ]] || fail "login with the one-time password answered HTTP ${code}"
location="$(curl -sS -o /dev/null -w '%{redirect_url}' -b "$jar" http://127.0.0.1:8080/)"
[[ "$location" == */password ]] || fail "the one-time login is not sent to the password change (${location})"

echo "PASS: ${MODE}"
