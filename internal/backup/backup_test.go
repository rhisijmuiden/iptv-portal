package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// dirTarget keeps backups in a local directory.
type dirTarget struct{ dir string }

func (d dirTarget) Upload(_ context.Context, name string, r io.Reader, _ int64) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d.dir, name), b, 0o600)
}

func (d dirTarget) Download(_ context.Context, name string, w io.Writer) error {
	f, err := os.Open(filepath.Join(d.dir, name))
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

func (d dirTarget) List(context.Context) ([]File, error) {
	es, err := os.ReadDir(d.dir)
	if err != nil {
		return nil, err
	}
	var out []File
	for _, e := range es {
		fi, _ := e.Info()
		out = append(out, File{Name: e.Name(), Size: fi.Size()})
	}
	return out, nil
}

func (d dirTarget) Delete(_ context.Context, name string) error {
	return os.Remove(filepath.Join(d.dir, name))
}

func (d dirTarget) Close() error { return nil }

func useDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := dialer
	dialer = func(context.Context, Location) (Target, error) { return dirTarget{dir}, nil }
	t.Cleanup(func() { dialer = old })
	return dir
}

func setup(t *testing.T, passphrase string) (*store.Store, *Service, *time.Time) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c := DefaultConfig
	c.Location = Location{Protocol: FTP, Host: "backup.example", Dir: "iptv"}
	c.Passphrase = passphrase
	if err := SaveConfig(ctx, st, c); err != nil {
		t.Fatal(err)
	}
	s := New(st, nil)
	now := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	return st, s, &now
}

func addCategories(t *testing.T, st *store.Store, prefix string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := st.SaveCategory(context.Background(), store.Category{Name: fmt.Sprintf("%s %d %s", prefix, i, strings.Repeat("x", 500))}); err != nil {
			t.Fatal(err)
		}
	}
}

func categoryCount(t *testing.T, dataDir string) int {
	t.Helper()
	st, err := store.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cats, err := st.Categories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return len(cats)
}

func TestFullIncrementalRestore(t *testing.T) {
	for _, pass := range []string{"", "correct horse"} {
		t.Run(fmt.Sprintf("encrypted=%v", pass != ""), func(t *testing.T) {
			ctx := context.Background()
			remote := useDir(t)
			st, s, now := setup(t, pass)
			addCategories(t, st, "a", 300)

			full, err := s.Run(ctx, false)
			if err != nil {
				t.Fatal(err)
			}
			if full.Kind != Full || full.Encrypted != (pass != "") {
				t.Fatalf("first backup = %+v", full)
			}
			*now = now.Add(time.Hour)
			addCategories(t, st, "b", 5)
			inc1, err := s.Run(ctx, false)
			if err != nil {
				t.Fatal(err)
			}
			if inc1.Kind != Incremental || inc1.Size >= full.Size {
				t.Fatalf("second backup = %+v, full = %+v", inc1, full)
			}
			*now = now.Add(time.Hour)
			addCategories(t, st, "c", 5)
			if _, err := s.Run(ctx, false); err != nil {
				t.Fatal(err)
			}
			if got := s.Status(); got.Error != "" || got.Kind != Incremental {
				t.Fatalf("status = %+v", got)
			}
			files, err := s.Remote(ctx)
			if err != nil || len(files) != 3 {
				t.Fatalf("remote = %+v, %v", files, err)
			}

			// Restore the middle backup: 305 categories.
			m, err := s.Restore(ctx, inc1.Name)
			if err != nil {
				t.Fatal(err)
			}
			if m.Name != inc1.Name || m.Kind != Incremental {
				t.Fatalf("manifest = %+v", m)
			}
			if _, err := os.Stat(filepath.Join(st.Dir(), store.PendingRestore)); err != nil {
				t.Fatal(err)
			}
			st.Close()
			if n := categoryCount(t, st.Dir()); n != 305 {
				t.Fatalf("restored %d categories, want 305", n)
			}
			if _, err := os.Stat(filepath.Join(st.Dir(), store.BeforeRestore)); err != nil {
				t.Fatal("old database not kept:", err)
			}
			if pass != "" {
				b, _ := os.ReadFile(filepath.Join(remote, full.Name))
				if !bytes.HasPrefix(b, []byte(ageHeader)) {
					t.Fatal("backup not encrypted")
				}
			}
		})
	}
}

func TestRestoreWrongPassphrase(t *testing.T) {
	ctx := context.Background()
	useDir(t)
	st, s, _ := setup(t, "one")
	f, err := s.Run(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := LoadConfig(ctx, st)
	c.Passphrase = "two"
	SaveConfig(ctx, st, c)
	if _, err := s.Restore(ctx, f.Name); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("restore with wrong passphrase: %v", err)
	}
	// A new passphrase starts a new chain.
	if f, err := s.Run(ctx, false); err != nil || f.Kind != Full {
		t.Fatalf("backup after passphrase change = %+v, %v", f, err)
	}
}

func TestFullEveryAndPrune(t *testing.T) {
	ctx := context.Background()
	remote := useDir(t)
	st, s, now := setup(t, "")
	c, _ := LoadConfig(ctx, st)
	c.FullEvery, c.Keep = 2, 2
	if err := SaveConfig(ctx, st, c); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for day := 0; day < 7; day++ {
		addCategories(t, st, fmt.Sprintf("d%d", day), 1)
		f, err := s.Run(ctx, false)
		if err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, f.Kind[:1])
		*now = now.Add(24 * time.Hour)
	}
	if got := strings.Join(kinds, ""); got != "fififif" {
		t.Fatalf("kinds = %s", got)
	}
	es, _ := os.ReadDir(remote)
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	// Two full backups kept: day 4 (with day 5) and day 6.
	if len(names) != 3 || !strings.Contains(names[0], "20261005") || !strings.Contains(names[0], "full") {
		t.Fatalf("remote after pruning = %v", names)
	}
	// A missing parent forces a full backup.
	for _, n := range names {
		os.Remove(filepath.Join(remote, n))
	}
	if f, err := s.Run(ctx, false); err != nil || f.Kind != Full {
		t.Fatalf("backup after remote was emptied = %+v, %v", f, err)
	}
}

func TestNext(t *testing.T) {
	last := time.Date(2026, 10, 20, 10, 30, 0, 0, time.Local) // a Tuesday
	cases := []struct {
		c    Config
		want time.Time
	}{
		{Config{Schedule: Off}, time.Time{}},
		{Config{Schedule: Hourly}, last.Add(time.Hour)},
		{Config{Schedule: Every6}, last.Add(6 * time.Hour)},
		{Config{Schedule: Daily, Hour: 3}, time.Date(2026, 10, 21, 3, 0, 0, 0, time.Local)},
		{Config{Schedule: Daily, Hour: 23}, time.Date(2026, 10, 20, 23, 0, 0, 0, time.Local)},
		{Config{Schedule: Weekly, Hour: 3}, time.Date(2026, 10, 25, 3, 0, 0, 0, time.Local)},
	}
	for _, tc := range cases {
		if got := tc.c.Next(last); !got.Equal(tc.want) {
			t.Errorf("%s: next = %v, want %v", tc.c.Schedule, got, tc.want)
		}
	}
	if got := (Config{Schedule: Daily}).Next(time.Time{}); !got.IsZero() {
		t.Errorf("never run: next = %v, want now", got)
	}
}

func TestParseName(t *testing.T) {
	n := fileName(Incremental, time.Date(2026, 10, 20, 1, 2, 3, 0, time.UTC), true)
	if n != "iptv-portal-20261020-010203-incr.tar.gz.age" {
		t.Fatal(n)
	}
	f, ok := parseName(n)
	if !ok || f.Kind != Incremental || !f.Encrypted || f.Created.Hour() != 1 {
		t.Fatalf("parse = %+v, %v", f, ok)
	}
	for _, bad := range []string{"portal.db", n + ".part", "iptv-portal-2026-full.tar.gz"} {
		if _, ok := parseName(bad); ok {
			t.Errorf("%q parsed as a backup", bad)
		}
	}
}

func TestLocationValidate(t *testing.T) {
	priv, pub, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := PublicKey(priv, ""); err != nil || got != pub {
		t.Fatalf("public key = %q, %v; want %q", got, err, pub)
	}
	ok := []Location{
		{Protocol: FTP, Host: "nas"},
		{Protocol: SCP, Host: "nas", Username: "u", PrivateKey: priv, Dir: "/backups/"},
		{Protocol: FTPS, Host: "nas", Fingerprint: strings.Repeat("ab", 32)},
	}
	for i := range ok {
		if err := ok[i].Validate(); err != nil {
			t.Errorf("%+v: %v", ok[i], err)
		}
	}
	if ok[1].Dir != "/backups" || !strings.HasPrefix(ok[2].Fingerprint, "AB:AB:") {
		t.Errorf("not normalised: %q %q", ok[1].Dir, ok[2].Fingerprint)
	}
	bad := []Location{
		{Protocol: "sftp", Host: "nas"},
		{Protocol: FTP},
		{Protocol: SCP, Host: "nas", Username: "u"},
		{Protocol: SCP, Host: "nas", Password: "p"},
		{Protocol: FTP, Host: "nas", Dir: "it's"},
		{Protocol: SCP, Host: "nas", Username: "u", PrivateKey: "nonsense"},
	}
	for _, l := range bad {
		if err := l.Validate(); err == nil {
			t.Errorf("%+v accepted", l)
		}
	}
}
