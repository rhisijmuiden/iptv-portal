// Command iptv-portal runs the IPTV portal: an admin panel to collect
// public m3u8 streams and a client portal that IPTV players connect to.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/activity"
	"github.com/rhisijmuiden/iptv-portal/internal/admin"
	"github.com/rhisijmuiden/iptv-portal/internal/epg"
	"github.com/rhisijmuiden/iptv-portal/internal/iptvorg"
	"github.com/rhisijmuiden/iptv-portal/internal/media"
	"github.com/rhisijmuiden/iptv-portal/internal/portal"
	"github.com/rhisijmuiden/iptv-portal/internal/server"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
	"github.com/rhisijmuiden/iptv-portal/internal/update"
	"github.com/rhisijmuiden/iptv-portal/internal/version"
	"github.com/rhisijmuiden/iptv-portal/internal/web"
)

const usage = `Usage: iptv-portal [command] [options]

Commands:
  serve                       Start the portal (default)
  admin-reset [-username N]   Create an admin or set a new one-time password
  set-port [-admin P] [-portal P]
                              Change the ports (for example when you locked
                              yourself out); restart the service afterwards
  station [--reset-admin]     Apply the Build My TV Station settings from the
                              STATION_* environment, restart the service and
                              print station.json (run as root)
  version                     Print the version

Common option:
  -data DIR   Database directory (default $IPTV_PORTAL_DATA or
              /var/lib/iptv-portal)
`

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve(args)
	case "admin-reset":
		err = adminReset(args)
	case "set-port":
		err = setPort(args)
	case "station":
		err = station(args, os.Stdout, os.Stderr)
	case "version":
		fmt.Println(version.Version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func defaultDataDir() string {
	if d := os.Getenv("IPTV_PORTAL_DATA"); d != "" {
		return d
	}
	return "/var/lib/iptv-portal"
}

func newFlags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	data := fs.String("data", defaultDataDir(), "directory for the database")
	return fs, data
}

func serve(args []string) error {
	fs, data := newFlags("serve")
	fs.Parse(args)

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	st, err := store.Open(*data)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if n, err := st.AdminCount(ctx); err != nil {
		return err
	} else if n == 0 {
		// First start without an installer: create an admin so the panel
		// is reachable, and print the one-time password to the log.
		pw := store.GeneratePassword()
		if _, err := st.CreateAdmin(ctx, "admin", pw, true); err != nil {
			return err
		}
		log.Warn("admin account created with a one-time password; change it after logging in",
			"username", "admin", "password", pw)
	}

	settings, err := st.Settings(ctx)
	if err != nil {
		return err
	}
	var proxies []*net.IPNet
	for _, p := range settings.TrustedProxies {
		n, err := store.ParseProxy(p)
		if err != nil {
			return err
		}
		proxies = append(proxies, n)
	}
	web.SetTrustedProxies(proxies)
	guide := epg.New(log, *data)
	refresh := make(chan struct{}, 1)
	go guide.Loop(ctx, 6*time.Hour, func() []string {
		s, err := st.Settings(ctx)
		if err != nil {
			log.Warn("reading guide sources", "err", err)
			return nil
		}
		return s.EPGURLs
	}, refresh)

	vod := media.NewCatalog(st, log)
	refreshVOD := make(chan struct{}, 1)
	go vod.Loop(ctx, time.Hour, refreshVOD)

	updater := update.New(envOr("IPTV_PORTAL_RELEASES_API", update.DefaultAPI), version.Version)
	iptvOrg := iptvorg.NewClient()
	iptvOrg.BaseURL = envOr("IPTV_PORTAL_IPTVORG_API", iptvorg.DefaultBaseURL)
	go updater.Loop(ctx, 24*time.Hour, func() bool {
		s, err := st.Settings(ctx)
		return err == nil && s.AutoUpdateCheck
	})
	tracker := activity.New()
	restart := false
	var mgr *server.Manager
	adminHandler, err := admin.New(admin.Config{
		Store: st, Listeners: managerRef{&mgr}, Guide: guide, RefreshGuide: refresh,
		Updater: updater, IPTVOrg: iptvOrg, Media: vod, RefreshMedia: refreshVOD,
		Activity: tracker, BasePath: settings.BasePath, Log: log,
		Restart: func() {
			restart = true
			stop()
		},
	})
	if err != nil {
		return err
	}
	portalHandler, err := portal.New(st, guide, tracker, vod, log)
	if err != nil {
		return err
	}
	mgr = server.NewManager(log, adminHandler, portalHandler)
	log.Info("IPTV Portal starting", "version", version.Version, "data", *data)
	if settings.BasePath != "" {
		log.Info("admin panel served under a base path", "path", settings.BasePath)
	}
	mgr.Start(settings.AdminPort, settings.PortalPort)
	go adminHandler.PurgeLoop(ctx)

	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mgr.Shutdown(shutdownCtx)
	if restart {
		return reexec(log, st)
	}
	return nil
}

// reexec replaces this process with the (just updated) binary, keeping the
// PID so systemd sees the same service.
func reexec(log *slog.Logger, st *store.Store) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	st.Close()
	log.Info("restarting with the new version", "binary", exe)
	return syscall.Exec(exe, os.Args, os.Environ())
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// managerRef lets the admin handler be built before the manager that
// serves it.
type managerRef struct{ m **server.Manager }

func (r managerRef) Start(a, p int)               { (*r.m).Start(a, p) }
func (r managerRef) Status() []server.Status      { return (*r.m).Status() }
func (r managerRef) PortAvailable(port int) error { return (*r.m).PortAvailable(port) }

func adminReset(args []string) error {
	fs, data := newFlags("admin-reset")
	username := fs.String("username", "admin", "username")
	fs.Parse(args)
	defer fixOwnership(*data)

	st, err := store.Open(*data)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()

	pw, err := resetAdmin(ctx, st, *username)
	if err != nil {
		return err
	}
	settings, err := st.Settings(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("Admin panel:        %s\n", adminURL(settings.AdminPort, settings.BasePath))
	fmt.Printf("Username:           %s\n", *username)
	fmt.Printf("One-time password:  %s\n", pw)
	fmt.Println("You must change this password after your first login.")
	return nil
}

// resetAdmin gives the admin username a new one-time password, creating
// the account when it does not exist, and returns the password.
func resetAdmin(ctx context.Context, st *store.Store, username string) (string, error) {
	pw := store.GeneratePassword()
	err := st.SetAdminPassword(ctx, username, pw, true)
	if errors.Is(err, store.ErrNotFound) {
		_, err = st.CreateAdmin(ctx, username, pw, true)
	}
	return pw, err
}

func setPort(args []string) error {
	fs, data := newFlags("set-port")
	adminPort := fs.Int("admin", 0, "admin panel port")
	portalPort := fs.Int("portal", 0, "portal port")
	fs.Parse(args)
	defer fixOwnership(*data)
	if *adminPort == 0 && *portalPort == 0 {
		return errors.New("specify -admin and/or -portal")
	}
	st, err := store.Open(*data)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	settings, err := st.Settings(ctx)
	if err != nil {
		return err
	}
	if *adminPort != 0 {
		settings.AdminPort = *adminPort
	}
	if *portalPort != 0 {
		settings.PortalPort = *portalPort
	}
	if err := st.SaveSettings(ctx, settings); err != nil {
		return err
	}
	fmt.Printf("Saved: admin panel on port %d, portal on port %d.\n", settings.AdminPort, settings.PortalPort)
	fmt.Println("Restart the service to apply: systemctl restart iptv-portal")
	return nil
}

// adminURL guesses the admin panel address from the first non-loopback
// IPv4 address of this machine.
func adminURL(port int, basePath string) string {
	host := "localhost"
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
				host = ipn.IP.String()
				break
			}
		}
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + basePath + "/"
}

// fixOwnership gives the database files back to the owner of the data
// directory. Commands like admin-reset are usually run as root, while the
// service runs as its own user; without this SQLite would leave root-owned
// files the service cannot write.
func fixOwnership(dataDir string) {
	if os.Geteuid() != 0 {
		return
	}
	info, err := os.Stat(dataDir)
	if err != nil {
		return
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid == 0 {
		return
	}
	for _, name := range []string{"portal.db", "portal.db-wal", "portal.db-shm"} {
		os.Lchown(filepath.Join(dataDir, name), int(st.Uid), int(st.Gid))
	}
}
