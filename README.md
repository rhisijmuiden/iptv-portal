# IPTV Portal

A lightweight portal that collects individual, publicly available m3u8 streams (for example from local broadcasters) in one place. Users add the portal to their TV box or IPTV app (such as MyTVOnline on a Formuler) and get all channels and, where available, the program guide.

The portal is only an index: TV boxes connect directly to the broadcaster's stream. No video passes through the server.

> **Status:** in development. The admin panel with channel management works. Users and invitations, the Xtream Codes API, M3U and EPG output, the installer and the update feature are coming next.

## Components

| Component | Default port | Purpose |
|---|---|---|
| Admin panel | 8080 | Manage channels, categories, imports and settings |
| Portal | 80 | The address users enter on their TV box |

Both ports, the portal name and the public URL can be changed under **Settings** in the admin panel. After a port change, that component restarts on the new port right away.

### Behind a reverse proxy

When the portal runs behind a reverse proxy (Nginx, Caddy, Nginx Proxy Manager, Traefik), set **Settings → Public URL** to the proxy address, for example `https://tv.example.com`. Every link sent to TV boxes is built from this address.

## Build and run

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

## Importing channels

Under **Import** you can upload an M3U playlist, paste one, or have it downloaded from a URL. The `tvg-id` (EPG mapping), `tvg-logo` and `group-title` (category) attributes are kept. A channel whose stream URL already exists is updated instead of added twice.
