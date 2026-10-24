package admin

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/backup"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

func TestBackupPages(t *testing.T) {
	h := &Handler{}
	if err := h.parseTemplates(); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	c := backup.DefaultConfig
	c.Location = backup.Location{Protocol: backup.SCP, Host: "nas", Username: "u", Password: "p"}
	c.Schedule, c.Passphrase = backup.Daily, "x"
	render := func(name string, data any) string {
		t.Helper()
		var b strings.Builder
		if err := h.pages["en"][name].ExecuteTemplate(io.Writer(&b), "layout.html",
			page{Title: name, Nav: "backups", Session: &store.Session{}, Data: data}); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	out := render("backups", backupsData{Config: c, Now: now, Next: now.Add(time.Hour), PublicKey: "ssh-ed25519 AAAA iptv-portal-backup",
		Status: backup.Status{LastSuccess: now.Add(-3 * time.Minute), File: "f", Kind: backup.Incremental, Size: 2500}})
	for _, want := range []string{"3 min ago", "incremental, 2.5 kB", "daily at 03:00", "ssh-ed25519 AAAA", `placeholder="unchanged"`, "Restore"} {
		if !strings.Contains(out, want) {
			t.Errorf("backups page is missing %q", want)
		}
	}
	files := []remoteBackup{
		{File: backup.File{Name: "iptv-portal-20261020-010000-incr.tar.gz", Kind: backup.Incremental, Created: now}, Steps: 3},
		{File: backup.File{Name: "iptv-portal-20261019-010000-full.tar.gz", Kind: backup.Full, Created: now}, Steps: 1},
	}
	out = render("backuprestore", map[string]any{"Files": files, "Config": c, "Loaded": true})
	for _, want := range []string{"the full backup and 2 incremental", "this backup", `value="iptv-portal-20261019-010000-full.tar.gz"`, "scp://u@nas:22/"} {
		if !strings.Contains(out, want) {
			t.Errorf("restore page is missing %q", want)
		}
	}
	out = render("restoring", map[string]any{"Manifest": backup.Manifest{Created: now, Version: "v1.0.0"}, "Restart": true})
	if !strings.Contains(out, "IPTV Portal v1.0.0") {
		t.Error("restoring page does not name the version")
	}
}
