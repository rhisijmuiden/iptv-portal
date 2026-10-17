# IPTV Portal

A lightweight portal that collects individual, publicly available m3u8 streams (for example from local broadcasters) in one place. Users add the portal to their TV box or IPTV app (such as MyTVOnline on a Formuler) and get all channels and, where available, the program guide.

The portal is only an index: TV boxes connect directly to the broadcaster's stream. No video passes through the server.

> **Status:** in development. Channel management, users and invitations, the Xtream Codes API, M3U playlists, the program guide and the installers work. Updating from the admin panel is coming next.

## Installation

On a dedicated server or VPS (Debian, Ubuntu or another systemd-based Linux):

```sh
curl -fsSL https://raw.githubusercontent.com/rhisijmuiden/iptv-portal/main/install.sh | sudo bash
```

As a Proxmox VE LXC container, on the Proxmox host:

```sh
bash -c "$(curl -fsSL https://raw.githubusercontent.com/rhisijmuiden/iptv-portal/main/ct/iptv-portal.sh)"
```

Both print the admin panel address, the username and a one-time password at the end. See [docs/installation.md](docs/installation.md) for options, reverse proxy examples (Nginx, Caddy, Nginx Proxy Manager, Traefik), updating and troubleshooting.

## Components

| Component | Default port | Purpose |
|---|---|---|
| Admin panel | 8080 | Manage channels, categories, imports and settings |
| Portal | 80 | The address users enter on their TV box |

Both ports, the portal name and the public URL can be changed under **Settings** in the admin panel. After a port change, that component restarts on the new port right away.

### Behind a reverse proxy

When the portal runs behind a reverse proxy, set **Settings → Public URL** to the proxy address, for example `https://tv.example.com`. Every link sent to TV boxes is built from this address. Examples are in [docs/installation.md](docs/installation.md#behind-a-reverse-proxy).

## Releases

Pushing a tag such as `v0.1.0` builds the Linux binaries (amd64 and arm64) and publishes a GitHub release with generated release notes. The installers download the latest release.

## Build from source

Requires Go 1.26 or newer.

```sh
make build
IPTV_PORTAL_DATA=./data ./bin/iptv-portal serve
```

On the first start without an admin account, the account `admin` is created with a one-time password, which is printed to the log. You must change it after logging in.

## Commands

```text
iptv-portal serve                         Start the portal
iptv-portal admin-reset [-username N]     Create an admin or set a new one-time password
iptv-portal set-port [-admin P] [-portal P]
                                          Change the ports from the command line
iptv-portal version                       Print the version
```

All commands accept `-data DIR` (default `$IPTV_PORTAL_DATA` or `/var/lib/iptv-portal`).

Locked out after changing the admin panel port? Reset it and restart the service:

```sh
iptv-portal set-port -admin 8080
systemctl restart iptv-portal
```

## Users, packages and invitations

- A **package** is a set of channels: either all channels, or the channels in selected categories.
- A **user** is an account an IPTV player logs in with. Each user gets one or more packages, and can have an expiry date.
- An **invitation** is a link you send to someone. They open it, choose their own username and password, and see the details to set up their TV box. You choose which packages the account gets, how many times the link can be used, how long the account stays valid and until when the link works.

Invitation links point to the portal (port 80 by default), not the admin panel, so the admin panel does not have to be reachable from the internet.

## Setting up a TV box

Users add the portal as an **Xtream Codes** login (called *XC* in some apps) with:

| Field | Value |
|---|---|
| Server / portal URL | The portal address, e.g. `http://192.168.1.10` or `https://tv.example.com` |
| Username / password | The user's own login |

On a Formuler with MyTVOnline: *Add portal* → *Xtream Codes* → enter the details above. Channels, categories, logos and the program guide are loaded automatically.

Apps that only take a playlist can use:

```text
<portal>/get.php?username=USER&password=PASS&type=m3u_plus&output=m3u8
<portal>/xmltv.php?username=USER&password=PASS        (program guide)
```

Streams are not proxied. A player asks the portal for `/live/USER/PASS/ID.m3u8` (or `.ts`) and is redirected to the broadcaster's own m3u8 address. Disabling or expiring a user takes effect on their next channel switch.

## Program guide (EPG)

Under **Guide** you list XMLTV sources (plain or `.xml.gz`). They are downloaded every 6 hours, merged, and served to each user for the channels they can watch. A channel is linked to the guide through its **EPG ID**, which must match a channel id in a source. When you import a playlist that announces its guide with `x-tvg-url`, that guide is added as a source automatically. Channels without guide data still play.

## Importing channels

Under **Import** you can upload an M3U playlist, paste one, or have it downloaded from a URL. The `tvg-id` (EPG mapping), `tvg-logo` and `group-title` (category) attributes are kept. A channel whose stream URL already exists is updated instead of added twice.
