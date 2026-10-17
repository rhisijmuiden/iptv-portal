package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
	"github.com/rhisijmuiden/iptv-portal/internal/version"
	"github.com/rhisijmuiden/iptv-portal/internal/web"
)

// The station command implements the Build My TV Station contract
// (https://github.com/rhisijmuiden/BulldMyTVStation/blob/main/docs/station-contract.md):
// the station panel runs it as root after the installer to configure the
// portal as one part of a station.

// stationAdmin is the admin account the station panel uses.
const stationAdmin = "admin"

// stationFile is written to the data directory.
const stationFile = "station.json"

// Replaced in tests.
var (
	hasSystemd     = systemdRunning
	restartService = func() error { return exec.Command("systemctl", "restart", "iptv-portal.service").Run() }
	waitHealthy    = waitForHealth
	lookupEnv      = os.LookupEnv
)

// stationInfo is station.json; InitialPassword is only printed, never
// written to the file.
type stationInfo struct {
	Contract        int            `json:"contract"`
	App             string         `json:"app"`
	Version         string         `json:"version"`
	Port            int            `json:"port"`
	BasePath        string         `json:"basePath"`
	AdminURL        string         `json:"adminUrl"`
	AdminUser       string         `json:"adminUser"`
	APIBase         string         `json:"apiBase"`
	ServiceToken    bool           `json:"serviceToken"`
	Ports           map[string]int `json:"ports"`
	InitialPassword string         `json:"initialPassword,omitempty"`
}

func station(args []string, stdout, stderr io.Writer) error {
	fs, data := newFlags("station")
	reset := fs.Bool("reset-admin", false, "set a new one-time admin password")
	fs.Parse(args)
	defer fixOwnership(*data)

	st, err := store.Open(*data)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()

	// 1. Store the settings. An unset variable keeps the setting, an empty
	// one clears it.
	old, err := st.Settings(ctx)
	if err != nil {
		return err
	}
	old.Validate() // normalise, so an unchanged value compares equal
	next := old
	if v, ok := lookupEnv("STATION_SERVICE_TOKEN"); ok {
		next.ServiceToken = v
	}
	if v, ok := lookupEnv("STATION_BASE_PATH"); ok {
		next.BasePath = v
	}
	if v, ok := lookupEnv("STATION_PUBLIC_URL"); ok {
		next.PublicURL = v
	}
	if v, ok := lookupEnv("STATION_TRUSTED_PROXIES"); ok {
		next.TrustedProxies = []string{v}
	}
	if err := next.Validate(); err != nil {
		return err
	}
	changed := next.ServiceToken != old.ServiceToken || next.BasePath != old.BasePath ||
		next.PublicURL != old.PublicURL || !slices.Equal(next.TrustedProxies, old.TrustedProxies)
	if changed {
		if err := st.SaveSettings(ctx, next); err != nil {
			return err
		}
		fmt.Fprintln(stderr, "station settings saved")
	} else {
		fmt.Fprintln(stderr, "station settings unchanged")
	}

	// 2. A new one-time password for the admin.
	info := stationInfo{
		Contract: web.StationContract, App: web.App, Version: version.Version,
		Port: next.AdminPort, BasePath: next.BasePath, AdminURL: adminURL(next.AdminPort, next.BasePath),
		AdminUser: stationAdmin, APIBase: next.BasePath + "/api/v1", ServiceToken: next.ServiceToken != "",
		Ports: map[string]int{"portal": next.PortalPort},
	}
	var password string
	if *reset {
		if password, err = resetAdmin(ctx, st, stationAdmin); err != nil {
			return err
		}
		fmt.Fprintln(stderr, "new one-time password set for", stationAdmin)
	}
	st.Close()
	fixOwnership(*data)

	// 3. Restart and wait for the health check.
	if hasSystemd() {
		if changed {
			fmt.Fprintln(stderr, "restarting iptv-portal.service")
			if err := restartService(); err != nil {
				return fmt.Errorf("restarting iptv-portal.service: %w", err)
			}
		}
		url := "http://127.0.0.1:" + strconv.Itoa(next.AdminPort) + next.BasePath + "/healthz"
		if err := waitHealthy(url, 60*time.Second); err != nil {
			return err
		}
	} else if changed {
		fmt.Fprintln(stderr, "systemd not found; restart the portal yourself to apply the settings")
	}

	// 4. Write station.json and print it.
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(*data, stationFile), append(b, '\n'), 0o600); err != nil {
		return err
	}
	info.InitialPassword = password
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(info)
}

// systemdRunning reports whether this machine runs systemd.
func systemdRunning() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	_, err := os.Stat("/run/systemd/system")
	return err == nil
}

// waitForHealth polls url until it answers 200 or timeout passes.
func waitForHealth(url string, timeout time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			err = errors.New(resp.Status)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not answer within %s: %w", url, timeout, err)
		}
		time.Sleep(time.Second)
	}
}

// writeFileAtomic replaces name with data, so readers never see a partly
// written file.
func writeFileAtomic(name string, data []byte, perm os.FileMode) error {
	tmp := name + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, name); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
