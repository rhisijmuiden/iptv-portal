package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/backup"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// ---- backups ----

type backupsData struct {
	Config backup.Config
	Status backup.Status
	Next   time.Time
	// PublicKey is the authorized_keys line of the stored SSH key.
	PublicKey string
	Hours     []int
	Now       time.Time
}

func (h *Handler) backupRoutes(auth func(string, func(http.ResponseWriter, *http.Request, store.Session))) {
	auth("GET /backups", h.backupsPage)
	auth("POST /backups/location", h.backupsLocation)
	auth("POST /backups/key", h.backupsKey)
	auth("POST /backups/schedule", h.backupsSchedule)
	auth("POST /backups/run", h.backupsRun)
	auth("GET /backups/restore", h.backupsRemote)
	auth("POST /backups/restore", h.backupsRestore)
}

func (h *Handler) backupsPage(w http.ResponseWriter, r *http.Request, sess store.Session) {
	c, err := backup.LoadConfig(r.Context(), h.store)
	if err != nil {
		h.serverError(w, err)
		return
	}
	h.renderBackups(w, r, sess, c, "")
}

func (h *Handler) renderBackups(w http.ResponseWriter, r *http.Request, sess store.Session, c backup.Config, errMsg string) {
	d := backupsData{Config: c, Status: h.backups.Status(), Next: h.backups.Next(c), Now: time.Now()}
	if c.Location.PrivateKey != "" {
		d.PublicKey, _ = backup.PublicKey(c.Location.PrivateKey, c.Location.Password)
	}
	for i := 0; i < 24; i++ {
		d.Hours = append(d.Hours, i)
	}
	if errMsg == "" && d.Status.Running == "" {
		errMsg = d.Status.Error
		if errMsg != "" {
			errMsg = tr(r, "The last backup failed: %s", errMsg)
		}
	}
	h.render(w, r, "backups", page{Title: "Backups", Nav: "backups", Session: &sess, Error: errMsg, Data: d})
}

func (h *Handler) backupsLocation(w http.ResponseWriter, r *http.Request, sess store.Session) {
	ctx := r.Context()
	c, err := backup.LoadConfig(ctx, h.store)
	if err != nil {
		h.serverError(w, err)
		return
	}
	old := c.Location
	l := backup.Location{
		Protocol:    r.FormValue("protocol"),
		Host:        strings.TrimSpace(r.FormValue("host")),
		Username:    r.FormValue("username"),
		Password:    r.FormValue("password"),
		Dir:         r.FormValue("dir"),
		PrivateKey:  r.FormValue("private_key"),
		HostKey:     r.FormValue("host_key"),
		Fingerprint: r.FormValue("fingerprint"),
	}
	if v := strings.TrimSpace(r.FormValue("port")); v != "" {
		if l.Port, err = strconv.Atoi(v); err != nil {
			l.Port = -1 // reported by Validate
		}
	}
	// Stored secrets are never sent to the browser: empty fields keep them.
	if l.Password == "" && r.FormValue("clear_password") != "on" {
		l.Password = old.Password
	}
	if strings.TrimSpace(l.PrivateKey) == "" && r.FormValue("clear_key") != "on" {
		l.PrivateKey = old.PrivateKey
	}
	// Another server has another host key.
	if l.Host != old.Host || l.Port != old.Port {
		if l.HostKey == old.HostKey {
			l.HostKey = ""
		}
		if l.Fingerprint == old.Fingerprint {
			l.Fingerprint = ""
		}
	}
	if l.Protocol != backup.SCP {
		l.PrivateKey, l.HostKey = "", ""
	}
	c.Location = l
	if l.Host == "" {
		c.Location = backup.Location{Protocol: l.Protocol}
		c.Schedule = backup.Off
	}
	if err := backup.SaveConfig(ctx, h.store, c); err != nil {
		h.renderBackups(w, r, sess, c, err.Error())
		return
	}
	if r.FormValue("action") != "test" || !c.Location.Configured() {
		redirectFlash(w, r, "/backups", "Remote location saved.")
		return
	}
	tctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	hostKey, err := backup.Test(tctx, c.Location)
	msg := tr(r, "Connection works: the portal can write to %s.", c.Location.String())
	if hostKey != "" && c.Location.HostKey == "" {
		// Trust on first use: store the key the server presented.
		c.Location.HostKey = hostKey
		if serr := backup.SaveConfig(ctx, h.store, c); serr != nil {
			h.serverError(w, serr)
			return
		}
		msg += " " + tr(r, "The server's host key %s is now trusted.", hostKey)
	}
	if err != nil {
		h.renderBackups(w, r, sess, c, tr(r, "Saved, but the connection test failed: %s", err.Error()))
		return
	}
	redirectFlash(w, r, "/backups", msg)
}

func (h *Handler) backupsKey(w http.ResponseWriter, r *http.Request, sess store.Session) {
	ctx := r.Context()
	c, err := backup.LoadConfig(ctx, h.store)
	if err != nil {
		h.serverError(w, err)
		return
	}
	priv, _, err := backup.GenerateKey()
	if err != nil {
		h.serverError(w, err)
		return
	}
	c.Location.Protocol = backup.SCP
	c.Location.PrivateKey = priv
	err = backup.SaveConfig(ctx, h.store, c)
	if err != nil {
		h.renderBackups(w, r, sess, c, err.Error())
		return
	}
	redirectFlash(w, r, "/backups", "A new SSH key was created. Add its public key to ~/.ssh/authorized_keys on the backup server.")
}

func (h *Handler) backupsSchedule(w http.ResponseWriter, r *http.Request, sess store.Session) {
	ctx := r.Context()
	c, err := backup.LoadConfig(ctx, h.store)
	if err != nil {
		h.serverError(w, err)
		return
	}
	c.Schedule = r.FormValue("schedule")
	c.Hour, _ = strconv.Atoi(r.FormValue("hour"))
	c.FullEvery, _ = strconv.Atoi(r.FormValue("full_every"))
	c.Keep, _ = strconv.Atoi(r.FormValue("keep"))
	if p := r.FormValue("passphrase"); p != "" {
		if p != r.FormValue("passphrase_confirm") {
			h.renderBackups(w, r, sess, c, "The passphrases do not match.")
			return
		}
		c.Passphrase = p
	} else if r.FormValue("clear_passphrase") == "on" {
		c.Passphrase = ""
	}
	if err := backup.SaveConfig(ctx, h.store, c); err != nil {
		h.renderBackups(w, r, sess, c, err.Error())
		return
	}
	msg := "Schedule saved. Automatic backups are off."
	if c.Schedule != backup.Off {
		msg = tr(r, "Schedule saved. Backups run %s.", scheduleName(r, c))
	}
	redirectFlash(w, r, "/backups", msg)
}

func (h *Handler) backupsRun(w http.ResponseWriter, r *http.Request, sess store.Session) {
	full := r.FormValue("kind") == backup.Full
	if err := h.backups.Start(full); err != nil {
		c, _ := backup.LoadConfig(r.Context(), h.store)
		h.renderBackups(w, r, sess, c, err.Error())
		return
	}
	msg := "Backup started."
	if full {
		msg = "Full backup started."
	}
	redirectFlash(w, r, "/backups", msg)
}

type remoteBackup struct {
	backup.File
	// Steps is the number of backups a restore of this one applies.
	Steps int
}

// Incrementals is the number of incremental backups a restore applies.
func (b remoteBackup) Incrementals() int { return b.Steps - 1 }

func (h *Handler) backupsRemote(w http.ResponseWriter, r *http.Request, sess store.Session) {
	h.renderRemote(w, r, sess, "")
}

func (h *Handler) renderRemote(w http.ResponseWriter, r *http.Request, sess store.Session, errMsg string) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	c, err := backup.LoadConfig(ctx, h.store)
	if err != nil {
		h.serverError(w, err)
		return
	}
	var list []remoteBackup
	files, err := h.backups.Remote(ctx)
	if err != nil && errMsg == "" {
		errMsg = tr(r, "Could not read the remote location: %s", err.Error())
	}
	// files is newest first; a restore applies the last full backup at or
	// before a file and every incremental backup up to it.
	steps := 0
	for i := len(files) - 1; i >= 0; i-- {
		if files[i].Kind == backup.Full {
			steps = 0
		}
		steps++
		list = append([]remoteBackup{{File: files[i], Steps: steps}}, list...)
	}
	h.render(w, r, "backuprestore", page{Title: "Restore a backup", Nav: "backups", Session: &sess, Error: errMsg,
		Data: map[string]any{"Files": list, "Config": c, "Loaded": err == nil}})
}

func (h *Handler) backupsRestore(w http.ResponseWriter, r *http.Request, sess store.Session) {
	name := r.FormValue("name")
	if r.FormValue("confirm") != "on" {
		h.renderRemote(w, r, sess, "Confirm the restore to replace the current data.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	m, err := h.backups.Restore(ctx, name)
	if err != nil {
		msg := tr(r, "The restore failed: %s", err.Error())
		if errors.Is(err, backup.ErrPassphrase) {
			msg += ". " + tr(r, "Enter the passphrase the backup was made with under Schedule and encryption first.")
		}
		h.renderRemote(w, r, sess, msg)
		return
	}
	h.log.Info("backup restored, restarting", "file", name)
	h.render(w, r, "restoring", page{Title: "Restoring", Nav: "backups", Session: &sess,
		Data: map[string]any{"Manifest": m, "Restart": h.restart != nil}})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	if h.restart != nil {
		go func() {
			time.Sleep(time.Second)
			h.restart()
		}()
	}
}

// formatSize writes a byte count for people, e.g. "1.4 MB".
func formatSize(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d B", n)
	case n < 1000*1000:
		return fmt.Sprintf("%.1f kB", float64(n)/1000)
	case n < 1000*1000*1000:
		return fmt.Sprintf("%.1f MB", float64(n)/1000/1000)
	}
	return fmt.Sprintf("%.1f GB", float64(n)/1000/1000/1000)
}

// scheduleName describes when backups run, like Config.ScheduleName but in
// the language of the request.
func scheduleName(r *http.Request, c backup.Config) string {
	switch c.Schedule {
	case backup.Daily:
		return tr(r, "daily at %02d:00", c.Hour)
	case backup.Weekly:
		return tr(r, "every Sunday at %02d:00", c.Hour)
	}
	return tr(r, c.ScheduleName())
}
