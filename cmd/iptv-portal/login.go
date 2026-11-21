package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// loginInfo prints what an admin needs to log in: the admin panel address
// and, until the admin has chosen a new password, the one-time login. The
// installer shows it after installing and on every root shell login.
func loginInfo(args []string) error {
	fs, data := newFlags("login-info")
	writeBanner := fs.Bool("write-banner", false, "also refresh the console login banner in the data directory")
	fs.Parse(args)
	defer fixOwnership(*data)

	st, err := store.Open(*data)
	if err != nil {
		return err
	}
	defer st.Close()
	settings, err := st.Settings(context.Background())
	if err != nil {
		return err
	}
	creds, ok, err := store.ReadInitialCredentials(*data)
	if err != nil {
		return err
	}
	var pending *store.InitialCredentials
	if ok {
		pending = &creds
	}
	if *writeBanner {
		if err := os.WriteFile(filepath.Join(*data, store.BannerFile), []byte(loginBanner(settings, pending)), 0o644); err != nil {
			return err
		}
	}
	printLoginInfo(os.Stdout, settings, pending)
	return nil
}

func printLoginInfo(w io.Writer, settings store.Settings, creds *store.InitialCredentials) {
	fmt.Fprintf(w, "Admin panel:        %s\n", adminURL(settings.AdminPort, settings.BasePath))
	if creds == nil {
		return
	}
	fmt.Fprintf(w, "Username:           %s\n", creds.Username)
	fmt.Fprintf(w, "One-time password:  %s\n", creds.Password)
	fmt.Fprintln(w, "You must change this password after your first login.")
}

// saveLoginInfo stores the one-time login in the data directory and writes
// the console login banner. The banner uses agetty's \4 escape for the
// address, so it stays right when the IP address changes.
func saveLoginInfo(dir string, creds store.InitialCredentials, settings store.Settings) error {
	if err := store.WriteInitialCredentials(dir, creds); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, store.BannerFile), []byte(loginBanner(settings, &creds)), 0o644)
}

func loginBanner(settings store.Settings, creds *store.InitialCredentials) string {
	esc := strings.NewReplacer(`\`, `\\`).Replace
	var b strings.Builder
	b.WriteString("IPTV Portal\n")
	fmt.Fprintf(&b, "  Admin panel:        %s\n", esc(panelURL("HOST", settings.AdminPort, settings.BasePath)))
	if creds != nil {
		fmt.Fprintf(&b, "  Username:           %s\n", esc(creds.Username))
		fmt.Fprintf(&b, "  One-time password:  %s\n", esc(creds.Password))
		b.WriteString("  (change it after your first login)\n")
	}
	b.WriteString("\n")
	return strings.Replace(b.String(), "HOST", `\4`, 1)
}
