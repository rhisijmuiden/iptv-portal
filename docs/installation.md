# Installation

IPTV Portal is a single program with no dependencies. It runs on any 64-bit Linux system with systemd (amd64 or arm64): a bare Debian 13 on dedicated hardware, a VM, a VPS or a container, or Proxmox VE.

- [Debian 13: hardware, VM or container](#debian-13-hardware-vm-or-container)
- [Proxmox CT template](#proxmox-ct-template)
- [Proxmox LXC container](#proxmox-lxc-container)
- [Behind a reverse proxy](#behind-a-reverse-proxy)
- [Updating](#updating)
- [Uninstalling](#uninstalling)
- [Troubleshooting](#troubleshooting)

## Debian 13: hardware, VM or container

A bare Debian 13 (trixie) install is enough, for example a netinst with only *standard system utilities*, or a Debian LXC container. Debian 12, Ubuntu and other systemd-based distributions work too. As root (`su -`, or prefix `sudo` when you have it), run:

```sh
curl -fsSL https://raw.githubusercontent.com/rhisijmuiden/iptv-portal/main/install.sh | bash
```

Without curl, use wget, which a standard Debian has:

```sh
wget -qO- https://raw.githubusercontent.com/rhisijmuiden/iptv-portal/main/install.sh | bash
```

The installer:

1. installs what a bare system may lack with apt (curl, ca-certificates),
2. downloads the latest release for your CPU and checks its checksum,
3. creates the system user `iptv-portal`,
4. installs the program in `/opt/iptv-portal` and the data in `/var/lib/iptv-portal`,
5. installs and starts the systemd service `iptv-portal`,
6. shows the login, and nothing else:

```
==> IPTV Portal v0.7.0 is installed and running

    Admin panel:        http://192.168.1.20:8080/
    Username:           admin
    One-time password:  x7KpQ2mWv9RtZh3c
    You must change this password after your first login.
```

Open the address, log in and choose a new password. All further configuration (portal name, public URL, ports, mail server, backups, channels) is done in the admin panel.

The same login is shown on the console before you log in (the login banner) and every time you open a root shell, so you can find it again. Once you have changed the password, only the admin panel address is shown.

If port 80 or 8080 is already taken (for example by a reverse proxy on the same machine), the installer picks 8081 or 8090 instead and says so. You can also choose the ports yourself:

```sh
curl -fsSL https://raw.githubusercontent.com/rhisijmuiden/iptv-portal/main/install.sh | bash -s -- --portal-port 8081 --admin-port 8080
```

| Option | Meaning |
|---|---|
| `--version vX.Y.Z` | Install a specific release instead of the latest stable one, also a pre-release such as `v0.4.0-rc.1` |
| `--admin-port PORT` | Admin panel port (default 8080) |
| `--portal-port PORT` | Portal port for TV boxes (default 80) |
| `--archive FILE` | Install from a downloaded `iptv-portal_linux_*.tar.gz` instead of downloading it (offline installs) |
| `--reset-admin` | Print a new one-time admin password |
| `--template` | Prepare a container template: install everything, but create the admin on the first boot and start nothing. This is how the Proxmox CT template is built. |
| `--uninstall` | Remove the service and program, keep the data |
| `--purge` | With `--uninstall`, also delete the data |

Both ports can be changed later under **Settings** in the admin panel.

## Proxmox CT template

Every release has a ready-made Proxmox VE container template: the official Proxmox *debian-13-standard* template with IPTV Portal installed. Nothing has to run on the Proxmox host.

1. In the Proxmox web interface, select a storage that holds container templates (usually *local*), open **CT Templates** and click **Download from URL**.
2. Paste the URL and click **Query URL**:

   ```
   https://github.com/rhisijmuiden/iptv-portal/releases/latest/download/debian-13-iptv-portal_amd64.tar.zst
   ```

   For a specific version, use `https://github.com/rhisijmuiden/iptv-portal/releases/download/vX.Y.Z/debian-13-iptv-portal_amd64.tar.zst` (the test channel's pre-releases have one too). To verify the download, choose *SHA-256* as hash algorithm and paste the checksum of the file from the release's `checksums.txt`.
3. Click **Download**.
4. Click **Create CT**, pick the template under *Template*, and set a root password or SSH key, the disk (2 GB is enough), CPU (1 core), memory (512 MB) and the network (DHCP or a static IP). Leave *Unprivileged container* on.
5. Start the container.

On its first boot the container creates the admin account with a one-time password and starts the portal. Open the container's **Console** in Proxmox: above the login prompt it shows

```
IPTV Portal
  Admin panel:        http://192.168.1.21:8080/
  Username:           admin
  One-time password:  x7KpQ2mWv9RtZh3c
  (change it after your first login)
```

The same appears when you run `pct enter <ID>` on the Proxmox host or log in as root over SSH. Open the address, log in and choose a new password; the console then shows only the address. Everything else is configured in the admin panel.

From the shell on the Proxmox host:

```sh
wget -O /var/lib/vz/template/cache/debian-13-iptv-portal_amd64.tar.zst \
  https://github.com/rhisijmuiden/iptv-portal/releases/latest/download/debian-13-iptv-portal_amd64.tar.zst
pct create 120 local:vztmpl/debian-13-iptv-portal_amd64.tar.zst \
  --hostname iptv-portal --unprivileged 1 --features nesting=1 \
  --cores 1 --memory 512 --rootfs local-lvm:2 \
  --net0 name=eth0,bridge=vmbr0,ip=dhcp --password
pct start 120
pct enter 120
```

`pveam`, the template command line tool, only knows the templates of the official Proxmox list, so use the web interface or `wget` as above.

The template is amd64 only, like the other Proxmox templates. Updating works as for any install: **Info → Update now** in the admin panel.

## Proxmox LXC container

Instead of the [CT template](#proxmox-ct-template) you can let a script create the container. On the Proxmox VE host shell, run:

```sh
bash -c "$(curl -fsSL https://raw.githubusercontent.com/rhisijmuiden/iptv-portal/main/ct/iptv-portal.sh)"
```

This uses the [community-scripts](https://community-scripts.github.io/ProxmoxVE/) framework, the same one as its Debian LXC script. You get its usual menu with default and advanced settings. The defaults are an unprivileged Debian 13 container with 1 CPU core, 512 MB RAM and 2 GB disk. Inside the container it runs the installer above.

At the end, the console shows only the admin panel address (IP and port), the username `admin` and a one-time password. You have to change the password after the first login; everything else is configured in the admin panel.

To update later, use **Info → Update now** in the admin panel, run `update` in the container's console, or run the installer command from [Updating](#updating) inside the container.

## Behind a reverse proxy

The portal works behind any reverse proxy (Nginx, Caddy, Nginx Proxy Manager, Traefik). That is the way to give it a domain name and HTTPS.

1. Point the proxy at the **portal port** (80 by default, or 8081 when the proxy runs on the same machine).
2. Pass on `Host`, `X-Forwarded-For` and `X-Forwarded-Proto`.
3. In the admin panel, set **Settings → Public URL** to the address users will use, for example `https://tv.example.com`. Every link sent to TV boxes, including invitation links and playlist URLs, is built from it.

The admin panel does not need to be public, because invitation links are served by the portal. If you do publish it, use a separate hostname and restrict access, for example to your VPN or home IP.

Proxy requests from private and local addresses are trusted for `X-Forwarded-For`, so rate limiting sees the real client address. Run the proxy on the same machine or the same private network.

### Nginx

```nginx
server {
    listen 443 ssl;
    server_name tv.example.com;
    # ssl_certificate / ssl_certificate_key, e.g. from certbot

    location / {
        proxy_pass http://127.0.0.1:8081;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        # Stream On Demand movies straight through instead of buffering
        # them to disk.
        proxy_buffering off;
    }
}
```

### Caddy

Caddy gets a certificate automatically and sets the forwarding headers itself:

```caddyfile
tv.example.com {
    reverse_proxy 127.0.0.1:8081
}
```

### Nginx Proxy Manager

Add a **Proxy Host**:

- Domain name: `tv.example.com`
- Scheme: `http`, forward hostname/IP: the server or container IP, forward port: the portal port
- SSL tab: request a Let's Encrypt certificate and enable *Force SSL*

### Traefik

With the file provider:

```yaml
http:
  routers:
    iptv-portal:
      rule: Host(`tv.example.com`)
      entryPoints: [websecure]
      service: iptv-portal
      tls:
        certResolver: letsencrypt
  services:
    iptv-portal:
      loadBalancer:
        servers:
          - url: http://192.168.1.10:80
```

## Updating

The easiest way is the admin panel: open **Info**, click **Check for updates** and then **Update now**. The portal downloads the new release, verifies its checksum, keeps the current program as `/opt/iptv-portal/iptv-portal.old` and restarts itself. With *Check automatically every 24 hours* switched on, the dashboard tells you when a new version is available.

**Release channel** on the same page chooses which versions are offered. *Stable* (the default) offers normal releases only. *Test* also offers pre-releases such as `v0.4.0-rc.1`, to try a version before it is released to everyone. Switching back to *Stable* does not downgrade: the portal keeps the installed pre-release until a newer stable release comes out.

To roll back to the previous version:

```sh
sudo mv /opt/iptv-portal/iptv-portal.old /opt/iptv-portal/iptv-portal
sudo systemctl restart iptv-portal
```

You can also run the installer again. It downloads the latest release, restarts the service and keeps all settings, channels and users:

```sh
curl -fsSL https://raw.githubusercontent.com/rhisijmuiden/iptv-portal/main/install.sh | sudo bash
```

## Uninstalling

```sh
curl -fsSL https://raw.githubusercontent.com/rhisijmuiden/iptv-portal/main/install.sh | sudo bash -s -- --uninstall
```

Add `--purge` to delete the data in `/var/lib/iptv-portal` as well.

## Troubleshooting

| Problem | Fix |
|---|---|
| Forgot the admin password | `sudo iptv-portal admin-reset` prints a new one-time password |
| Where was the one-time password again? | `sudo iptv-portal login-info`, or open a root shell or the console: both show it until it has been changed |
| Admin panel unreachable after changing its port | `sudo iptv-portal set-port -admin 8080 && sudo systemctl restart iptv-portal` |
| Service does not start | `journalctl -u iptv-portal -e` shows the log |
| A port is taken | Change it under **Settings**, or with `iptv-portal set-port` |

Data lives in `/var/lib/iptv-portal/portal.db` (SQLite). Set up automatic full and incremental backups to FTP, FTPS or SCP under **Backups** in the admin panel (see [Backup and restore](../README.md#backup-and-restore)). By hand, use `sqlite3 /var/lib/iptv-portal/portal.db ".backup portal-backup.db"`, or copy the directory while the service is stopped.
