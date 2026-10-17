package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

func TestStation(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"STATION_SERVICE_TOKEN":   "0123456789abcdef0123456789abcdef",
		"STATION_BASE_PATH":       "/portal-admin/",
		"STATION_PUBLIC_URL":      "https://tv.example.com/",
		"STATION_TRUSTED_PROXIES": "10.0.0.1, 10.0.1.0/24",
	}
	origSystemd, origRestart, origWait, origEnv := hasSystemd, restartService, waitHealthy, lookupEnv
	t.Cleanup(func() {
		hasSystemd, restartService, waitHealthy, lookupEnv = origSystemd, origRestart, origWait, origEnv
	})
	restarts := 0
	var waited string
	hasSystemd = func() bool { return true }
	restartService = func() error { restarts++; return nil }
	waitHealthy = func(url string, _ time.Duration) error { waited = url; return nil }
	lookupEnv = func(k string) (string, bool) { v, ok := env[k]; return v, ok }

	run := func(args ...string) (stationInfo, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if err := station(append([]string{"-data", dir}, args...), &stdout, &stderr); err != nil {
			t.Fatalf("station: %v (%s)", err, stderr.String())
		}
		var info stationInfo
		if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
			t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
		}
		return info, stdout.String()
	}

	info, out := run("--reset-admin")
	if info.Contract != 1 || info.App != "iptv-portal" || info.Port != 8080 || info.BasePath != "/portal-admin" ||
		info.APIBase != "/portal-admin/api/v1" || !info.ServiceToken || info.AdminUser != "admin" ||
		info.Ports["portal"] != 80 || info.InitialPassword == "" || !strings.HasSuffix(info.AdminURL, ":8080/portal-admin/") {
		t.Errorf("first run: %+v", info)
	}
	if strings.Contains(out, env["STATION_SERVICE_TOKEN"]) {
		t.Error("stdout holds the token")
	}
	if restarts != 1 || waited != "http://127.0.0.1:8080/portal-admin/healthz" {
		t.Errorf("restarts %d, waited for %q", restarts, waited)
	}

	file := filepath.Join(dir, "station.json")
	fi, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("station.json mode %v", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(file)
	if strings.Contains(string(b), info.InitialPassword) || strings.Contains(string(b), env["STATION_SERVICE_TOKEN"]) ||
		strings.Contains(string(b), "initialPassword") {
		t.Errorf("station.json holds a secret:\n%s", b)
	}

	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, _ := st.Settings(ctx)
	if s.PublicURL != "https://tv.example.com" || strings.Join(s.TrustedProxies, " ") != "10.0.0.1 10.0.1.0/24" {
		t.Errorf("settings %+v", s)
	}
	if _, err := st.Authenticate(ctx, "admin", info.InitialPassword); err != nil {
		t.Errorf("initial password: %v", err)
	}
	st.Close()

	// The same input again: nothing changes, no restart, no password.
	info, _ = run()
	if restarts != 1 || info.InitialPassword != "" || info.BasePath != "/portal-admin" {
		t.Errorf("second run: restarts %d, %+v", restarts, info)
	}

	// Empty clears, unset keeps.
	env = map[string]string{"STATION_BASE_PATH": "", "STATION_SERVICE_TOKEN": ""}
	info, _ = run()
	if restarts != 2 || info.BasePath != "" || info.APIBase != "/api/v1" || info.ServiceToken {
		t.Errorf("clearing: restarts %d, %+v", restarts, info)
	}

	// An invalid value is refused.
	env = map[string]string{"STATION_SERVICE_TOKEN": "short"}
	var stdout, stderr bytes.Buffer
	if err := station([]string{"-data", dir}, &stdout, &stderr); err == nil || stdout.Len() != 0 {
		t.Errorf("short token: err %v, stdout %q", err, stdout.String())
	}
}
