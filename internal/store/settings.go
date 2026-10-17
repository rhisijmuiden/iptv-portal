package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/rhisijmuiden/iptv-portal/internal/mail"
)

// Settings are the values an admin can change from the admin panel.
type Settings struct {
	// PortalName is shown in the admin panel and sent to clients.
	PortalName string
	// PublicURL is the address clients use to reach the portal, e.g.
	// "https://tv.example.com". It is used to build every link handed to
	// clients, so it must be the address behind any reverse proxy. Empty
	// means "derive from the incoming request".
	PublicURL string
	// AdminPort is the port the admin panel listens on.
	AdminPort int
	// PortalPort is the port the client portal (M3U / Xtream API) listens on.
	PortalPort int
	// EPGURLs are XMLTV guide sources (plain or gzipped) that are merged
	// into the guide served to players.
	EPGURLs []string
	// AutoUpdateCheck makes the portal look for new releases once a day.
	// It only checks; installing is always started by an admin.
	AutoUpdateCheck bool
	// Mail is the SMTP server used to email users their login details and
	// invitations. An empty host means mail is off.
	Mail mail.Config
}

// DefaultSettings are used for every key that has not been stored yet.
var DefaultSettings = Settings{
	PortalName:      "IPTV Portal",
	AdminPort:       8080,
	PortalPort:      80,
	AutoUpdateCheck: true,
	Mail:            mail.Config{Port: 587, Security: mail.StartTLS},
}

const (
	keyPortalName = "portal_name"
	keyPublicURL  = "public_url"
	keyAdminPort  = "admin_port"
	keyPortalPort = "portal_port"
	keyEPGURLs    = "epg_urls"
	keyAutoUpdate = "auto_update_check"

	keySMTPHost     = "smtp_host"
	keySMTPPort     = "smtp_port"
	keySMTPSecurity = "smtp_security"
	keySMTPUser     = "smtp_username"
	keySMTPPassword = "smtp_password"
	keySMTPFrom     = "smtp_from"
	keySMTPFromName = "smtp_from_name"
)

// Settings returns the current settings, falling back to defaults.
func (s *Store) Settings(ctx context.Context) (Settings, error) {
	out := DefaultSettings
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return out, err
		}
		switch k {
		case keyPortalName:
			out.PortalName = v
		case keyPublicURL:
			out.PublicURL = v
		case keyAdminPort:
			if p, err := strconv.Atoi(v); err == nil {
				out.AdminPort = p
			}
		case keyPortalPort:
			if p, err := strconv.Atoi(v); err == nil {
				out.PortalPort = p
			}
		case keyEPGURLs:
			out.EPGURLs = strings.Fields(v)
		case keyAutoUpdate:
			out.AutoUpdateCheck = v == "1"
		case keySMTPHost:
			out.Mail.Host = v
		case keySMTPPort:
			if p, err := strconv.Atoi(v); err == nil {
				out.Mail.Port = p
			}
		case keySMTPSecurity:
			out.Mail.Security = mail.Security(v)
		case keySMTPUser:
			out.Mail.Username = v
		case keySMTPPassword:
			out.Mail.Password = v
		case keySMTPFrom:
			out.Mail.From = v
		case keySMTPFromName:
			out.Mail.FromName = v
		}
	}
	return out, rows.Err()
}

// Validate checks and normalises the settings.
func (st *Settings) Validate() error {
	st.PortalName = strings.TrimSpace(st.PortalName)
	if st.PortalName == "" {
		return errors.New("name must not be empty")
	}
	st.PublicURL = strings.TrimRight(strings.TrimSpace(st.PublicURL), "/")
	if st.PublicURL != "" {
		u, err := url.Parse(st.PublicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("public URL must start with http:// or https://")
		}
	}
	for _, p := range []int{st.AdminPort, st.PortalPort} {
		if p < 1 || p > 65535 {
			return fmt.Errorf("port %d is invalid (1-65535)", p)
		}
	}
	if st.AdminPort == st.PortalPort {
		return errors.New("admin panel and portal must use different ports")
	}
	var urls []string
	seen := map[string]bool{}
	for _, u := range st.EPGURLs {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			continue
		}
		if !isHTTPURL(u) {
			return fmt.Errorf("guide URL %q must start with http:// or https://", u)
		}
		seen[u] = true
		urls = append(urls, u)
	}
	st.EPGURLs = urls
	return st.Mail.Validate()
}

// SaveSettings validates and stores the settings.
func (s *Store) SaveSettings(ctx context.Context, st Settings) error {
	if err := st.Validate(); err != nil {
		return err
	}
	values := map[string]string{
		keyPortalName: st.PortalName,
		keyPublicURL:  st.PublicURL,
		keyAdminPort:  strconv.Itoa(st.AdminPort),
		keyPortalPort: strconv.Itoa(st.PortalPort),
		keyEPGURLs:    strings.Join(st.EPGURLs, "\n"),
		keyAutoUpdate: boolString(st.AutoUpdateCheck),

		keySMTPHost:     st.Mail.Host,
		keySMTPPort:     strconv.Itoa(st.Mail.Port),
		keySMTPSecurity: string(st.Mail.Security),
		keySMTPUser:     st.Mail.Username,
		keySMTPPassword: st.Mail.Password,
		keySMTPFrom:     st.Mail.From,
		keySMTPFromName: st.Mail.FromName,
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for k, v := range values {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO settings(key, value) VALUES(?, ?)
				 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// SetEPGURLs replaces the guide sources.
func (s *Store) SetEPGURLs(ctx context.Context, urls []string) error {
	st, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	st.EPGURLs = urls
	return s.SaveSettings(ctx, st)
}

// AddEPGURL adds a guide source unless it is already configured. It
// reports whether the source was added.
func (s *Store) AddEPGURL(ctx context.Context, u string) (bool, error) {
	st, err := s.Settings(ctx)
	if err != nil {
		return false, err
	}
	for _, have := range st.EPGURLs {
		if have == u {
			return false, nil
		}
	}
	st.EPGURLs = append(st.EPGURLs, u)
	return true, s.SaveSettings(ctx, st)
}

func boolString(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// SetAutoUpdateCheck switches the daily update check on or off.
func (s *Store) SetAutoUpdateCheck(ctx context.Context, on bool) error {
	st, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	st.AutoUpdateCheck = on
	return s.SaveSettings(ctx, st)
}

// SetMail replaces the mail server settings.
func (s *Store) SetMail(ctx context.Context, m mail.Config) error {
	st, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	st.Mail = m
	return s.SaveSettings(ctx, st)
}
