package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Media server kinds.
const (
	MediaJellyfin = "jellyfin"
	MediaPlex     = "plex"
)

// MediaServer is a Jellyfin or Plex server whose movies and series are
// offered to players as On Demand.
type MediaServer struct {
	ID   int64
	Name string
	Kind string // MediaJellyfin or MediaPlex
	// URL is the address the portal uses to reach the server, e.g.
	// "http://192.168.1.10:8096".
	URL string
	// Token is the Jellyfin API key or the Plex X-Plex-Token.
	Token string
	// Libraries are the ids of the libraries that are offered.
	Libraries []string
	// Direct sends players straight to the media server instead of
	// streaming through the portal. The server must then be reachable from
	// the players, and they see the token in the stream address.
	Direct  bool
	Enabled bool
}

// KindName is the display name of the server software.
func (m MediaServer) KindName() string {
	switch m.Kind {
	case MediaJellyfin:
		return "Jellyfin"
	case MediaPlex:
		return "Plex"
	}
	return m.Kind
}

// HasLibrary reports whether the library with the given id is offered.
func (m MediaServer) HasLibrary(id string) bool {
	for _, l := range m.Libraries {
		if l == id {
			return true
		}
	}
	return false
}

// Validate checks and normalises a media server.
func (m *MediaServer) Validate() error {
	m.Name = strings.TrimSpace(m.Name)
	m.URL = strings.TrimRight(strings.TrimSpace(m.URL), "/")
	m.Token = strings.TrimSpace(m.Token)
	if m.Name == "" {
		return errors.New("name must not be empty")
	}
	if m.Kind != MediaJellyfin && m.Kind != MediaPlex {
		return errors.New("choose Jellyfin or Plex")
	}
	if !isHTTPURL(m.URL) {
		return errors.New("server address must start with http:// or https://")
	}
	if m.Token == "" {
		if m.Kind == MediaPlex {
			return errors.New("enter the Plex token")
		}
		return errors.New("enter the Jellyfin API key")
	}
	return nil
}

const mediaServerSelect = `SELECT id, name, kind, url, token, libraries, direct, enabled FROM media_servers`

func scanMediaServer(sc interface{ Scan(...any) error }) (MediaServer, error) {
	var m MediaServer
	var libs string
	err := sc.Scan(&m.ID, &m.Name, &m.Kind, &m.URL, &m.Token, &libs, &m.Direct, &m.Enabled)
	m.Libraries = strings.Fields(libs)
	return m, err
}

// MediaServers returns all media servers.
func (s *Store) MediaServers(ctx context.Context) ([]MediaServer, error) {
	rows, err := s.db.QueryContext(ctx, mediaServerSelect+` ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MediaServer
	for rows.Next() {
		m, err := scanMediaServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MediaServer returns one media server.
func (s *Store) MediaServer(ctx context.Context, id int64) (MediaServer, error) {
	m, err := scanMediaServer(s.db.QueryRowContext(ctx, mediaServerSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

// SaveMediaServer creates (ID 0) or updates a media server. On update an
// empty token keeps the stored one.
func (s *Store) SaveMediaServer(ctx context.Context, m MediaServer) (int64, error) {
	if m.ID != 0 && strings.TrimSpace(m.Token) == "" {
		old, err := s.MediaServer(ctx, m.ID)
		if err != nil {
			return 0, err
		}
		m.Token = old.Token
	}
	if err := m.Validate(); err != nil {
		return 0, err
	}
	libs := strings.Join(m.Libraries, " ")
	if m.ID == 0 {
		res, err := s.db.ExecContext(ctx,
			`INSERT INTO media_servers(name, kind, url, token, libraries, direct, enabled) VALUES(?, ?, ?, ?, ?, ?, ?)`,
			m.Name, m.Kind, m.URL, m.Token, libs, m.Direct, m.Enabled)
		if err != nil {
			return 0, uniqueErr(err, "a media server with this name already exists")
		}
		return res.LastInsertId()
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE media_servers SET name = ?, kind = ?, url = ?, token = ?, libraries = ?, direct = ?, enabled = ?
		 WHERE id = ?`, m.Name, m.Kind, m.URL, m.Token, libs, m.Direct, m.Enabled, m.ID)
	if err != nil {
		return 0, uniqueErr(err, "a media server with this name already exists")
	}
	return m.ID, mustAffect(res)
}

// DeleteMediaServer removes a media server.
func (s *Store) DeleteMediaServer(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM media_servers WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

// MediaIDs returns a stable numeric id for every key (an item id on the
// media server). Players only accept numbers as stream and category ids,
// and keeping them stable keeps favourites and resume points working.
func (s *Store) MediaIDs(ctx context.Context, serverID int64, keys []string) (map[string]int64, error) {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO media_ids(server_id, key) VALUES(?, ?) ON CONFLICT DO NOTHING`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, k := range keys {
			if _, err := stmt.ExecContext(ctx, serverID, k); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT key, id FROM media_ids WHERE server_id = ?`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int64, len(keys))
	for rows.Next() {
		var k string
		var id int64
		if err := rows.Scan(&k, &id); err != nil {
			return nil, err
		}
		out[k] = id
	}
	return out, rows.Err()
}
