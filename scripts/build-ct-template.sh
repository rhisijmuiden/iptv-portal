#!/usr/bin/env bash
# Builds the Proxmox VE container template: the official Proxmox Debian 13
# standard template with IPTV Portal installed by install.sh --template.
# A container made from it creates the admin with a one-time password on its
# first boot and shows the login on the console and in root shells.
#
#   sudo scripts/build-ct-template.sh iptv-portal_linux_amd64.tar.gz debian-13-iptv-portal_amd64.tar.zst
#
# Needs root (to keep file owners), curl, tar and zstd. The base template is
# checked against the SHA-512 sum in the Proxmox appliance index. Set
# KEEP_BASE to a file name to keep a copy of the base template.
set -euo pipefail

[[ $# -eq 2 ]] || {
  echo "usage: $0 IPTV_PORTAL_ARCHIVE OUTPUT.tar.zst" >&2
  exit 2
}
[[ $EUID -eq 0 ]] || {
  echo "run as root" >&2
  exit 1
}
ARCHIVE="$(realpath "$1")"
OUTPUT="$(realpath -m "$2")"
REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGES_URL="${PROXMOX_IMAGES_URL:-https://download.proxmox.com/images}"
APLINFO="${PROXMOX_APLINFO:-aplinfo-pve-9.dat}"
BASE_PACKAGE="${BASE_PACKAGE:-debian-13-standard}"

WORK="$(mktemp -d)"
ROOTFS="${WORK}/rootfs"
cleanup() {
  umount -R "${ROOTFS}/proc" 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "==> Looking up the newest ${BASE_PACKAGE} template"
curl -fsSL -o "${WORK}/aplinfo.dat" "${IMAGES_URL}/${APLINFO}"
# The index is a list of blank-line separated records with Package,
# Version, Location and sha512sum fields.
read -r location sha512 < <(
  awk -v pkg="$BASE_PACKAGE" '
    BEGIN { RS = ""; FS = "\n" }
    {
      p = ""; loc = ""; sum = ""
      for (i = 1; i <= NF; i++) {
        split($i, kv, ": ")
        if (kv[1] == "Package") p = kv[2]
        if (kv[1] == "Location") loc = kv[2]
        if (kv[1] == "sha512sum") sum = kv[2]
      }
      if (p == pkg && loc ~ /_amd64\.tar\.zst$/) print loc, sum
    }' "${WORK}/aplinfo.dat" | sort -V | tail -n1
)
[[ -n "${location:-}" && -n "${sha512:-}" ]] || {
  echo "no ${BASE_PACKAGE} amd64 template in ${IMAGES_URL}/${APLINFO}" >&2
  exit 1
}
base="${WORK}/$(basename "$location")"
echo "==> Downloading ${location}"
curl -fsSL -o "$base" "${IMAGES_URL}/${location}"
echo "${sha512}  ${base}" | sha512sum -c --quiet -
# The CI test installs on this base too, as a bare Debian 13.
if [[ -n "${KEEP_BASE:-}" ]]; then
  cp "$base" "$KEEP_BASE"
fi

echo "==> Unpacking"
mkdir -p "$ROOTFS"
tar --zstd -xpf "$base" -C "$ROOTFS" --numeric-owner

echo "==> Installing IPTV Portal"
install -m 0755 "${REPO_DIR}/install.sh" "${ROOTFS}/tmp/install.sh"
install -m 0644 "$ARCHIVE" "${ROOTFS}/tmp/iptv-portal.tar.gz"
# apt may run inside the chroot when the base lacks a dependency.
resolv="${ROOTFS}/etc/resolv.conf"
had_resolv=0
if [[ -e "$resolv" || -L "$resolv" ]]; then
  had_resolv=1
  mv "$resolv" "${resolv}.build"
fi
cp /etc/resolv.conf "$resolv"
mount -t proc proc "${ROOTFS}/proc"
chroot "$ROOTFS" /usr/bin/env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin HOME=/root \
  bash /tmp/install.sh --archive /tmp/iptv-portal.tar.gz --template
umount "${ROOTFS}/proc"
rm -f "$resolv"
[[ $had_resolv -eq 1 ]] && mv "${resolv}.build" "$resolv"
rm -rf "${ROOTFS}/tmp/install.sh" "${ROOTFS}/tmp/iptv-portal.tar.gz" "${ROOTFS}/var/cache/apt/archives/"*.deb
chroot "$ROOTFS" /opt/iptv-portal/iptv-portal version

echo "==> Packing ${OUTPUT}"
tar --numeric-owner -C "$ROOTFS" -cpf - . | zstd -q -19 -T0 -o "$OUTPUT" -f
ls -lh "$OUTPUT"
