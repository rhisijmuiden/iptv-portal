package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
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
}

// DefaultSettings are used for every key that has not been stored yet.
var DefaultSettings = Settings{
	PortalName: "IPTV Portal",
	AdminPort:  8080,
	PortalPort: 80,
}

const (
	keyPortalName = "portal_name"
	keyPublicURL  = "public_url"
	keyAdminPort  = "admin_port"
	keyPortalPort = "portal_port"
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
	return nil
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
