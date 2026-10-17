# IPTV Portal

A lightweight portal that collects individual, publicly available m3u8 streams (for example from local broadcasters) in one place. Users add the portal to their TV box or IPTV app (such as MyTVOnline on a Formuler) and get all channels and, where available, the program guide.

The portal is only an index: TV boxes connect directly to the broadcaster's stream. No video passes through the server.

> **Status:** in development. Channel management, users and invitations, the Xtream Codes API, M3U playlists, the program guide, the installers and updating from the admin panel work.

![Dashboard](docs/screenshots/dashboard.png)

## Features

- **Channels as an index, not a proxy.** Store the m3u8 address, logo, category and EPG ID of each channel. Players are redirected to the source, so the server needs almost no bandwidth.
- **Imports** from an M3U file, pasted text or a URL, and a built-in browser for the public [iptv-org](https://github.com/iptv-org/iptv) channel database.
- **Users, packages and invitations.** Decide per user which channels they see, set expiry dates, switch accounts off, and send invitation links so people create their own login.
- **Xtream Codes API, M3U and XMLTV.** Works with MyTVOnline (Formuler), TiviMate, IPTV Smarters and most other players.
- **Program guide** merged from your own XMLTV sources and filtered per user.
- **Settings in the browser**: portal name, public URL and both ports, ready for a reverse proxy.
- **Updates from the admin panel** with release notes, an optional daily check and automatic rollback.
- **Simple to host**: one Go binary with SQLite, an installer for any systemd Linux and a script for Proxmox VE LXC.

See [The admin panel](#the-admin-panel) below for a page-by-page tour.

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

## The admin panel

The admin panel runs on port 8080 by default. On the first start you log in with `admin` and the one-time password from the installer (or the log) and choose a new password. The screenshots below use sample data.

### Dashboard

The [dashboard](docs/screenshots/dashboard.png) (shown at the top) counts channels, categories and active users, shows the address users enter on their TV box, and whether the admin panel and the portal are running. When the daily update check finds a new release, a banner appears here.

### Channels

All channels with their logo, stream address, category, EPG ID and status. Search by name or URL, filter by category, switch a channel off without deleting it, or add one by hand.

![Channels](docs/screenshots/channels.png)

Each channel has a name, stream URL, logo URL, EPG ID, category (or a new one typed in place), sort order and an enabled switch.

<details>
<summary>Editing a channel</summary>

![Edit channel](docs/screenshots/channel-edit.png)
</details>

### Categories

Categories group channels in the player and are the building blocks of packages. Rename them, change their order or delete them; channels in a deleted category are kept without a category.

<details>
<summary>Screenshot</summary>

![Categories](docs/screenshots/categories.png)
</details>

### Import

Upload an M3U playlist, paste one, or have it downloaded from a URL. `tvg-id`, `tvg-logo` and `group-title` are kept, or you put everything in one category. Channels whose stream URL already exists are updated instead of added twice. See [Importing channels](#importing-channels).

<details>
<summary>Screenshot</summary>

![Import](docs/screenshots/import.png)
</details>

### iptv-org

Browse the public iptv-org channel database, filtered by country (the Netherlands by default), category or name. Tick the streams you want, choose their category and, optionally, the packages that get it. See [From iptv-org](#from-iptv-org).

![iptv-org browser](docs/screenshots/iptv-org.png)

### Guide

Add XMLTV sources and see, per channel, whether the guide has data for it. The guide refreshes every 6 hours or with **Refresh now**. See [Program guide (EPG)](#program-guide-epg).

![Program guide](docs/screenshots/guide.png)

### Users

The accounts players log in with: packages, expiry date, last time the player connected, and a note. Expired and disabled accounts are marked and lose access on their next channel switch.

![Users](docs/screenshots/users.png)

<details>
<summary>Editing a user</summary>

![Edit user](docs/screenshots/user-edit.png)
</details>

### Packages

A package is either every channel (including channels added later) or a selection of categories. Users get one or more packages.

<details>
<summary>Screenshots</summary>

![Packages](docs/screenshots/packages.png)

![Edit package](docs/screenshots/package-edit.png)
</details>

### Invitations

Create a link that lets someone choose their own username and password. You set the packages, how many people can use the link, how long the account stays valid and until when the link works. Revoke a link at any time. See [Users, packages and invitations](#users-packages-and-invitations).

![Invitations](docs/screenshots/invitations.png)

The link opens on the portal, not the admin panel. After signing up, the new user sees the details for their TV box:

<p>
<img src="docs/screenshots/portal-invite.png" alt="Invitation page" width="49%">
<img src="docs/screenshots/portal-welcome.png" alt="Account ready page" width="49%">
</p>

### Settings

Portal name, public URL (for a reverse proxy or your own domain), and the ports of the admin panel and the portal. A port change takes effect right away. See [Components](#components).

<details>
<summary>Screenshot</summary>

![Settings](docs/screenshots/settings.png)
</details>

### Info

The installed version, the latest release and the release notes of every version. Check for updates by hand or every 24 hours, and update with one click. See [Updates](#updates).

![Info](docs/screenshots/info.png)

## Components

| Component | Default port | Purpose |
|---|---|---|
| Admin panel | 8080 | Manage channels, categories, imports (M3U and iptv-org) and settings |
| Portal | 80 | The address users enter on their TV box |

Both ports, the portal name and the public URL can be changed under **Settings** in the admin panel. After a port change, that component restarts on the new port right away.

### Behind a reverse proxy

When the portal runs behind a reverse proxy, set **Settings → Public URL** to the proxy address, for example `https://tv.example.com`. Every link sent to TV boxes is built from this address. Examples are in [docs/installation.md](docs/installation.md#behind-a-reverse-proxy).

## Releases

Pushing a tag such as `v0.1.0`, or running the **Release** workflow under *Actions* with a version, builds the Linux binaries (amd64 and arm64) and publishes a GitHub release with generated release notes. The installers download the latest release.

## Updates

The **Info** page in the admin panel shows the installed version and the release notes of every version. **Check for updates** asks GitHub for new releases; with *Check automatically every 24 hours* switched on (the default), the dashboard shows a banner when a new version is out. **Update now** downloads the release, verifies its checksum, checks that it starts, keeps the current program as `iptv-portal.old` and restarts the portal. Settings, channels and users are kept. See [docs/installation.md](docs/installation.md#updating) for rolling back.

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

### Sending login details by email

Under **Settings → Mail server** you can enter your own SMTP server (host, port, STARTTLS/TLS/none, login and sender address) and send a test email. Once it is set up:

- When adding a user or setting a new password, tick **Email the login details to the user** to send the portal URL, username, password and playlist links to the user's email address.
- **Email new password** on a user's page generates a new password and emails it. Passwords are stored as hashes, so an existing password can't be sent again.
- Invitations can be emailed when you create them, or from the "Invitation created" box.
- People signing up through an invitation can enter their email address and get their login details by email.

The SMTP password is stored in the portal database. Many providers (Gmail, Outlook) need an app password instead of your normal password.

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

### From iptv-org

Under **iptv-org** you browse the public channel database of [iptv-org](https://iptv-org.github.io/). It opens on the Netherlands; filter by country, category or name, tick the streams you want and choose where they go: the iptv-org category, the country name, one of your own categories or a new one. You can also add that category to one or more packages straight away, so the channels show up for the users who have those packages. Each channel keeps its name, logo and iptv-org id as EPG ID (for example `NPO1.nl@SD`). Labels such as *Geo-blocked* or *Not 24/7* and streams that need special headers are marked, because TV boxes connect to the stream directly.

The list is downloaded from `https://iptv-org.github.io/api` and kept in memory for 12 hours; **Reload list** fetches it again. Set `IPTV_PORTAL_IPTVORG_API` to use a mirror.
