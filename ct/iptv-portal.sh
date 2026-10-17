#!/usr/bin/env bash
# Creates a Proxmox VE LXC container running IPTV Portal. Run on the Proxmox
# host:
#
#   bash -c "$(curl -fsSL https://raw.githubusercontent.com/rhisijmuiden/iptv-portal/main/ct/iptv-portal.sh)"
#
# Built on the community-scripts framework (https://community-scripts.github.io/ProxmoxVE/),
# following its ct/debian.sh. The framework creates the container and then
# runs install/iptv-portal-install.sh from this repository inside it.

# Make the framework fetch the ct/ and install/ scripts from this repository.
export COMMUNITY_SCRIPTS_URL="${COMMUNITY_SCRIPTS_URL:-https://raw.githubusercontent.com/rhisijmuiden/iptv-portal/main}"
_cs_boot="${COMMUNITY_SCRIPTS_CORE_DIR:-$(dirname "${BASH_SOURCE[0]}")/../../core}/core/build.func"
# shellcheck disable=SC1090
source "$_cs_boot" 2>/dev/null || source <(curl -fsSL "${COMMUNITY_SCRIPTS_CORE_URL:-https://raw.githubusercontent.com/community-scripts/core/main}/core/build.func")
# Source: https://github.com/rhisijmuiden/iptv-portal

APP="IPTV-Portal"
var_tags="${var_tags:-media;iptv}"
var_cpu="${var_cpu:-1}"
var_ram="${var_ram:-512}"
var_disk="${var_disk:-2}"
var_os="${var_os:-debian}"
var_version="${var_version:-13}"
var_arm64="${var_arm64:-yes}"
var_unprivileged="${var_unprivileged:-1}"

header_info "$APP"
variables
color
catch_errors

# Runs when this script is started inside an existing container: updates
# IPTV Portal to the latest release.
function update_script() {
  header_info
  check_container_storage
  check_container_resources
  if [[ ! -x /opt/iptv-portal/iptv-portal ]]; then
    msg_error "No ${APP} Installation Found!"
    exit
  fi
  msg_info "Updating ${APP}"
  $STD bash -c "$(curl -fsSL https://raw.githubusercontent.com/rhisijmuiden/iptv-portal/main/install.sh)"
  msg_ok "Updated ${APP} to $(/opt/iptv-portal/iptv-portal version)"
  exit
}

start
build_container
description

msg_ok "Completed successfully!\n"
echo -e "${CREATING}${GN}${APP} setup has been successfully initialized!${CL}"

# Create the one-time admin password now, so it is shown on this console.
CREDS="$(pct exec "$CTID" -- runuser -u iptv-portal -- env IPTV_PORTAL_DATA=/var/lib/iptv-portal /opt/iptv-portal/iptv-portal admin-reset)"
echo -e "${INFO}${YW} Admin panel login (one-time password, change it after logging in):${CL}"
while IFS= read -r line; do
  echo -e "${TAB}${BGN}${line}${CL}"
done <<<"$CREDS"
echo -e "${INFO}${YW} Portal address for TV boxes:${CL}"
echo -e "${TAB}${GATEWAY}${BGN}http://${IP}${CL}"
