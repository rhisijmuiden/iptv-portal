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
	"strconv"
	"syscall"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/admin"
	"github.com/rhisijmuiden/iptv-portal/internal/portal"
	"github.com/rhisijmuiden/iptv-portal/internal/server"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
	"github.com/rhisijmuiden/iptv-portal/internal/version"
)

const usage = `Gebruik: iptv-portal [opdracht] [opties]

Opdrachten:
  serve                       Start de portal (standaard)
  admin-reset [-username N]   Maak een admin aan of zet een eenmalig wachtwoord
  set-port [-admin P] [-portal P]
                              Wijzig de poorten (bijvoorbeeld als je jezelf
                              hebt buitengesloten); herstart daarna de service
  version                     Toon de versie

Algemene optie:
  -data DIR   Map voor de database (standaard $IPTV_PORTAL_DATA of
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
	case "version":
		fmt.Println(version.Version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "onbekende opdracht %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fout:", err)
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
	data := fs.String("data", defaultDataDir(), "map voor de database")
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
		log.Warn("admin-account aangemaakt met eenmalig wachtwoord; wijzig het na het inloggen",
			"gebruiker", "admin", "wachtwoord", pw)
	}

	settings, err := st.Settings(ctx)
	if err != nil {
		return err
	}
	var mgr *server.Manager
	adminHandler, err := admin.New(st, managerRef{&mgr}, log)
	if err != nil {
		return err
	}
	mgr = server.NewManager(log, adminHandler, portal.New(st, log))
	log.Info("IPTV Portal start", "versie", version.Version, "data", *data)
	mgr.Start(settings.AdminPort, settings.PortalPort)
	go adminHandler.PurgeLoop(ctx)

	<-ctx.Done()
	log.Info("stoppen")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mgr.Shutdown(shutdownCtx)
	return nil
}

// managerRef lets the admin handler be built before the manager that
// serves it.
type managerRef struct{ m **server.Manager }

func (r managerRef) Start(a, p int)               { (*r.m).Start(a, p) }
func (r managerRef) Status() []server.Status      { return (*r.m).Status() }
func (r managerRef) PortAvailable(port int) error { return (*r.m).PortAvailable(port) }

func adminReset(args []string) error {
	fs, data := newFlags("admin-reset")
	username := fs.String("username", "admin", "gebruikersnaam")
	fs.Parse(args)

	st, err := store.Open(*data)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()

	pw := store.GeneratePassword()
	err = st.SetAdminPassword(ctx, *username, pw, true)
	if errors.Is(err, store.ErrNotFound) {
		_, err = st.CreateAdmin(ctx, *username, pw, true)
	}
	if err != nil {
		return err
	}
	settings, err := st.Settings(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("Beheerpaneel:        %s\n", adminURL(settings.AdminPort))
	fmt.Printf("Gebruikersnaam:      %s\n", *username)
	fmt.Printf("Eenmalig wachtwoord: %s\n", pw)
	fmt.Println("Je moet dit wachtwoord wijzigen na de eerste keer inloggen.")
	return nil
}

func setPort(args []string) error {
	fs, data := newFlags("set-port")
	adminPort := fs.Int("admin", 0, "poort voor het beheerpaneel")
	portalPort := fs.Int("portal", 0, "poort voor de portal")
	fs.Parse(args)
	if *adminPort == 0 && *portalPort == 0 {
		return errors.New("geef -admin en/of -portal op")
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
	fmt.Printf("Opgeslagen: beheerpaneel op poort %d, portal op poort %d.\n", settings.AdminPort, settings.PortalPort)
	fmt.Println("Herstart de service om dit toe te passen: systemctl restart iptv-portal")
	return nil
}

// adminURL guesses the admin panel address from the first non-loopback
// IPv4 address of this machine.
func adminURL(port int) string {
	host := "localhost"
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
				host = ipn.IP.String()
				break
			}
		}
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/"
}
