#!/usr/bin/env bash
# Runs inside the LXC container created by ct/iptv-portal.sh.
# Source: https://github.com/rhisijmuiden/iptv-portal

# shellcheck disable=SC1091
source /dev/stdin <<<"$FUNCTIONS_FILE_PATH"
color
verb_ip6
catch_errors
setting_up_container
network_check
update_os

msg_info "Installing IPTV Portal"
$STD bash -c "$(curl -fsSL https://raw.githubusercontent.com/rhisijmuiden/iptv-portal/main/install.sh)"
msg_ok "Installed IPTV Portal $(/opt/iptv-portal/iptv-portal version)"

motd_ssh
customize
cleanup_lxc
