# IPTV Portal

A lightweight portal that collects individual, publicly available m3u8 streams (for example from local broadcasters) in one place. Users add the portal to their TV box or IPTV app (such as MyTVOnline on a Formuler) and get all channels and, where available, the program guide.

The portal is only an index: TV boxes connect directly to the broadcaster's stream. No live TV passes through the server.

It can also offer the movies and series of your own **Jellyfin** or **Plex** server as On Demand.

> **Status:** in development. Channel management, users and invitations, the Xtream Codes API, M3U playlists, the program guide, On Demand from Jellyfin and Plex, the installers and updating from the admin panel work.

![Dashboard](docs/screenshots/dashboard.png)

## Features

- **Channels as an index, not a proxy.** Store the m3u8 address, logo, category and EPG ID of each channel. Players are redirected to the source, so the server needs almost no bandwidth.
- **Imports** from an M3U file, pasted text or a URL, and a built-in browser for the public [iptv-org](https://github.com/iptv-org/iptv) channel database.
- **Users, packages and invitations.** Decide per user which channels they see, set expiry dates and connection limits, switch accounts off, and send invitation links so people create their own login.
- **Xtream Codes API, M3U and XMLTV.** Works with MyTVOnline (Formuler), TiviMate, IPTV Smarters and most other players.
- **Program guide** merged from your own XMLTV sources and filtered per user.
- **Stream health checks** that test every channel like a player would, show which streams are down and can switch dead channels off until they work again.
- **Settings in the browser**: portal name, public URL and both ports, ready for a reverse proxy.
- **Microsoft Entra sign-in** for the admin panel (optional), limited to the groups or app roles you choose, with local accounts as a fallback.
- **Backups** to FTP, FTPS or SCP: full and incremental, scheduled or by hand, optionally encrypted, and restored from the admin panel.
- **Updates from the admin panel** with release notes, stable and test release channels, an optional daily check and automatic rollback.
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

### Stream health

The portal tests every channel's stream on a schedule (every hour by default): it loads the playlist, follows a master playlist to its first variant and fetches the newest video segment. The channel list shows **working**, **failing** (with the reason) or **unchecked** for each channel and can be filtered on it; **Check streams** tests them all now and **Check** tests one.

Under **Stream health** you set the interval (5 minutes to a week, or off) and can let the portal switch a channel off after a number of failed checks in a row. Such a channel is switched on again as soon as its stream works; switching a channel on or off by hand stops that. Off by default.

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

Portal name, public URL (for a reverse proxy or your own domain), and the ports of the admin panel and the portal. A port change takes effect right away. See [Components](#components). The same page holds the [mail server](#sending-login-details-by-email) and [Microsoft Entra sign-in](#signing-in-with-microsoft-entra).

<details>
<summary>Screenshot</summary>

![Settings](docs/screenshots/settings.png)
</details>

### Backups

Where backups go (FTP, FTPS or SCP), when they run, how many are kept and the encryption passphrase. Make a backup by hand, or pick one on the server and restore it. See [Backup and restore](#backup-and-restore).

### Info

The installed version, the latest release and the release notes of every version. Choose the stable or test release channel, check for updates by hand or every 24 hours, and update with one click. See [Updates](#updates).

![Info](docs/screenshots/info.png)

## Components

| Component | Default port | Purpose |
|---|---|---|
| Admin panel | 8080 | Manage channels, categories, imports (M3U and iptv-org), On Demand media servers and settings |
| Portal | 80 | The address users enter on their TV box |

Both ports, the portal name and the public URL can be changed under **Settings** in the admin panel. After a port change, that component restarts on the new port right away.

### Behind a reverse proxy

When the portal runs behind a reverse proxy, set **Settings → Public URL** to the proxy address, for example `https://tv.example.com`. Every link sent to TV boxes is built from this address. Examples are in [docs/installation.md](docs/installation.md#behind-a-reverse-proxy).

## Releases

Pushing a tag such as `v0.1.0`, or running the **Release** workflow under *Actions* with a version, builds the Linux binaries (amd64 and arm64) and publishes a GitHub release with generated release notes. The installers download the latest stable release.

There are two release channels:

| Channel | Versions | Example |
|---|---|---|
| Stable | Normal releases | `v0.4.0` |
| Test | Pre-releases, and the stable releases | `v0.4.0-rc.1`, `v0.4.0-beta.2`, `v0.4.0-alpha.1` |

A version with an `-rc.N`, `-beta.N` or `-alpha.N` suffix is published as a GitHub pre-release. Only portals on the test channel are offered it; the installers and stable portals ignore it. Once `v0.4.0` itself is released, portals on the test channel move on to it like everyone else. To try a pre-release on a fresh install, pass it to the installer: `--version v0.4.0-rc.1`.

## Updates

The **Info** page in the admin panel shows the installed version and the release notes of every version. **Release channel** picks which releases are offered: *Stable* (the default) or *Test*, which also offers pre-releases (see [Releases](#releases)). **Check for updates** asks GitHub for new releases; with *Check automatically every 24 hours* switched on (the default), the dashboard shows a banner when a new version is out. **Update now** downloads the release, verifies its checksum, checks that it starts, keeps the current program as `iptv-portal.old` and restarts the portal. Settings, channels and users are kept. See [docs/installation.md](docs/installation.md#updating) for rolling back.

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
iptv-portal station [--reset-admin]       Apply the Build My TV Station settings (see below)
iptv-portal version                       Print the version
```

All commands accept `-data DIR` (default `$IPTV_PORTAL_DATA` or `/var/lib/iptv-portal`).

Locked out after changing the admin panel port? Reset it and restart the service:

```sh
iptv-portal set-port -admin 8080
systemctl restart iptv-portal
```

## Running as part of Build My TV Station

[Build My TV Station](https://github.com/rhisijmuiden/BulldMyTVStation) can deploy the portal as one part of a station. It follows the [station contract](https://github.com/rhisijmuiden/BulldMyTVStation/blob/main/docs/station-contract.md) (version 1); a standalone install is not affected by any of this.

- `GET /healthz` answers on both ports with `{"status":"ok","app":"iptv-portal","version":"…","contract":1}`.
- `iptv-portal station [--reset-admin]`, run as root, reads `STATION_SERVICE_TOKEN`, `STATION_BASE_PATH`, `STATION_PUBLIC_URL` (stored as the public URL) and `STATION_TRUSTED_PROXIES`, stores them in the settings, restarts `iptv-portal.service` when something changed, waits for `/healthz` and writes `/var/lib/iptv-portal/station.json`. It prints the same JSON on stdout, with `initialPassword` added after `--reset-admin`. A variable that is not set keeps its setting; an empty one clears it.
- With a service token, `Authorization: Bearer <token>` gives access to a JSON API on the admin port: `GET` and `POST /api/v1/channels`, `PUT` and `DELETE /api/v1/channels/{id}`. A channel has `name`, `streamUrl`, `logoUrl`, `category` (a name, created when missing), `epgId`, `sortOrder` and `enabled`.
- The base path (for example `/portal-admin`) applies to the admin panel only; the portal TV boxes use stays at `/`. The reverse proxy forwards the full path unchanged.
- With trusted proxies set, `X-Forwarded-For`, `-Proto` and `-Host` are only believed from those addresses.

## Users, packages and invitations

- A **package** is a set of channels: either all channels, or the channels in selected categories. A package can also include **On Demand** (movies and series from your media servers).
- A **user** is an account an IPTV player logs in with. Each user gets one or more packages, and can have an expiry date.
- An **invitation** is a link you send to someone. They open it, choose their own username and password, and see the details to set up their TV box. You choose which packages the account gets, how many times the link can be used, how long the account stays valid and until when the link works.

Invitation links point to the portal (port 80 by default), not the admin panel, so the admin panel does not have to be reachable from the internet.

### Connection limits

To stop people from sharing an account, set **Maximum connections per user** on a package, or **Maximum connections** on a user to override it. It is the number of places (IP addresses) that can watch at the same time; a user with several packages gets the highest limit, and leaving it empty everywhere means no limit. Players see the limit and their current connections in the Xtream account info.

Because players fetch the stream straight from the broadcaster, the portal only sees the moment a channel or On Demand title is opened. A place therefore keeps its connection for 15 minutes after it last opened a stream, or for as long as its player keeps talking to the portal (for example to fetch the guide). Devices behind the same internet connection share one IP address and count as one place; switching channels never counts as a new connection. When a user moved to another place and is refused, **Release connections** on the user page frees their connections right away.

### Sending login details by email

Under **Settings → Mail server** you can enter your own SMTP server (host, port, STARTTLS/TLS/none, login and sender address) and send a test email. Once it is set up:

- When adding a user or setting a new password, tick **Email the login details to the user** to send the portal URL, username, password and playlist links to the user's email address.
- **Email new password** on a user's page generates a new password and emails it. Passwords are stored as hashes, so an existing password can't be sent again.
- Invitations can be emailed when you create them, or from the "Invitation created" box.
- People signing up through an invitation can enter their email address and get their login details by email.

The SMTP password is stored in the portal database. Many providers (Gmail, Outlook) need an app password instead of your normal password.

### Who is online

The dashboard shows how many users are online and which channel each player opened last, with its IP address and player name. Streams go straight from the broadcaster to the player, so the portal only sees the player's own requests: a user counts as *online* after a request in the last 15 minutes, and as *watching* when they opened a channel in that time. The portal cannot tell when someone stops watching, and the list starts empty after a restart.

## Signing in with Microsoft Entra

Admins can sign in to the admin panel with their Microsoft work account (Entra ID, formerly Azure AD). It is off until you switch it on under **Settings → Microsoft Entra sign-in**. Local admin accounts keep working next to it, so `iptv-portal admin-reset` always gets you back in.

1. In the [Entra admin center](https://entra.microsoft.com), go to **App registrations → New registration**. Pick *Accounts in this organizational directory only*, choose the platform **Web** and paste the **Redirect URI** shown on the settings page (it ends in `/login/entra/callback`).
2. Under **Certificates & secrets**, create a client secret and copy its **Value**.
3. Decide who may sign in:
   - **App roles** (recommended): under **App roles**, create a role such as `Portal.Admin`, then assign people or groups to it under **Enterprise applications → (your app) → Users and groups**.
   - **Groups**: under **Token configuration**, add the *groups* claim (security groups) and use the group's object ID.
   - Or tick **Allow every account that can sign in to the app** and turn on **Assignment required** in the enterprise application, so only assigned people can sign in.
4. In the portal, enter the **Directory (tenant) ID**, **Application (client) ID**, the secret value and the allowed roles or group IDs, tick **Show "Sign in with Microsoft"** and save.

Behind a reverse proxy, fill in **Admin panel URL** with the address you open the panel on (for example `https://admin.tv.example.com`), so the redirect URI does not depend on how the proxy forwards the host name. The redirect URI in Entra must match it exactly.

The first sign-in creates an admin account named after the Microsoft sign-in name (for example `jane@example.com`) with a random password, so that account can only be used through Microsoft. If a local admin account with that name already exists, it is used and keeps its password. Removing someone's role or group blocks their next sign-in; a session that is already open stays valid until they log out or it expires after 7 days without use. The client secret is stored in the portal database; Entra secrets expire, so renew it in time.

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

## On Demand from Jellyfin or Plex

Under **On Demand** you add a Jellyfin or Plex server: its address as seen from the portal (for example `http://192.168.1.10:8096` or `http://192.168.1.10:32400`) and a Jellyfin API key (*Dashboard → API Keys*) or a Plex token (`X-Plex-Token`). The portal lists the movie and series libraries and offers all of them; untick the ones you do not want to share. Then tick **On Demand** in the packages that should get it.

TV boxes see each movie library as a category under *Movies* (VOD) and each series library under *Series*, with posters, descriptions, ratings, seasons and episodes. The libraries are read again every hour, or straight away with **Refresh now**. Item ids stay the same between refreshes, so favourites and resume points on the box keep working.

Playback has two modes per server:

- **Through the portal** (default): the box plays `/movie/USER/PASS/ID.mkv` or `/series/...` from the portal, which streams the original file from the media server, with seeking. The token never reaches the boxes and the media server does not have to be reachable from the internet, but the video does pass through the portal. Behind Nginx, add `proxy_buffering off;` (see [docs/installation.md](docs/installation.md)).
- **Directly**: the box is redirected to the media server, like live channels. The server address must then be reachable from the boxes, and the token is part of the stream address the box sees, so use this only on a trusted network.

Files are sent as they are (no transcoding), which the Formuler and most IPTV apps play fine. Posters are always served through the portal.

## Backup and restore

Under **Backups** the portal backs up its database (channels, categories, users, packages, invitations, media servers and all settings) to another server. The guide and the On Demand catalog are downloaded again after a restore, so they are not part of a backup.

**Remote location.** Choose one of:

- **SCP over SSH** (recommended). Log in with a password or a key: **Create an SSH key** makes an Ed25519 key in the portal and shows the public key to add to `~/.ssh/authorized_keys` on the backup server. The first **Save and test connection** trusts the server's host key and stores its fingerprint; from then on the portal refuses a server with another key. The server needs the `scp`, `ls`, `mkdir`, `mv` and `rm` commands, as any Linux server or NAS with SSH has.
- **FTPS** with explicit TLS (`AUTH TLS`, usually port 21) or implicit TLS (usually port 990). The certificate is checked like a website's. For a self-signed certificate, the connection test shows its fingerprint; enter it under *Trusted certificate fingerprint* to trust exactly that certificate.
- **FTP** without encryption, for a trusted local network only.

The directory is created when it does not exist. The connection test writes and deletes a small file to prove the portal can write there.

**Full and incremental.** A full backup holds the whole database. An incremental backup holds only the parts (32 KiB blocks) that changed since the previous backup, so it is usually a few kilobytes. **Back up now** makes an incremental backup when it can, **Full backup now** always a full one. A new full backup is made every *Full backup every* days, and also when the remote location or passphrase changes or the previous backup is missing on the server. *Keep* sets how many full backups stay on the server, each with its incremental backups; older ones are deleted after each backup.

**Schedule.** Every hour, every 6 or 12 hours, daily or weekly (Sunday) at a chosen hour. A backup that fails is shown on the Backups page and in the log, and tried again at the next scheduled time.

**Encryption.** With a passphrase, every backup is encrypted with [age](https://age-encryption.org) (scrypt) before it leaves the server; the files end in `.tar.gz.age`. Without the passphrase a backup cannot be restored, so keep it somewhere safe. Changing it starts a new chain with a full backup.

**Restore.** **Restore** lists the backups on the server. Restoring one downloads it together with the full backup and the incremental backups it builds on, rebuilds the database, checks it against the checksum in the backup and with SQLite's integrity check, and then restarts the portal with it. The previous database stays in the data directory as `portal.db.before-restore`. After a restore you log in with the admin accounts of the backup.

To move to a new server or recover from a lost one: install the portal, log in with the one-time password, enter the same remote location (and passphrase) under **Backups**, and restore the newest backup.

A backup is a gzipped tar file with `manifest.json` (what the backup restores to) and either `portal.db` (full) or `blocks` (incremental). Without the portal, a full backup can be unpacked with `tar -xzf` (after `age -d` when encrypted) to get the database file.

## Program guide (EPG)

Under **Guide** you list XMLTV sources (plain or `.xml.gz`). They are downloaded every 6 hours, merged, and served to each user for the channels they can watch. A channel is linked to the guide through its **EPG ID**, which must match a channel id in a source. When you import a playlist that announces its guide with `x-tvg-url`, that guide is added as a source automatically. Channels without guide data still play.

## Importing channels

Under **Import** you can upload an M3U playlist, paste one, or have it downloaded from a URL. The `tvg-id` (EPG mapping), `tvg-logo` and `group-title` (category) attributes are kept. A channel whose stream URL already exists is updated instead of added twice.

### From iptv-org

Under **iptv-org** you browse the public channel database of [iptv-org](https://iptv-org.github.io/). It opens on the Netherlands; filter by country, category or name, tick the streams you want and choose where they go: the iptv-org category, the country name, one of your own categories or a new one. You can also add that category to one or more packages straight away, so the channels show up for the users who have those packages. Each channel keeps its name, logo and iptv-org id as EPG ID (for example `NPO1.nl@SD`). Labels such as *Geo-blocked* or *Not 24/7* and streams that need special headers are marked, because TV boxes connect to the stream directly.

The list is downloaded from `https://iptv-org.github.io/api` and kept in memory for 12 hours; **Reload list** fetches it again. Set `IPTV_PORTAL_IPTVORG_API` to use a mirror.
