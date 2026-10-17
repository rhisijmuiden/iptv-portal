package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"
)

// Category groups channels, e.g. "Noord-Holland" or "Regionaal".
type Category struct {
	ID           int64
	Name         string
	SortOrder    int
	ChannelCount int
}

// Channel is a single stream. Clients connect to StreamURL directly; the
// portal only indexes it.
type Channel struct {
	ID         int64
	Name       string
	StreamURL  string
	LogoURL    string
	EPGID      string // tvg-id used to match the channel in an XMLTV guide
	CategoryID int64  // 0 means no category
	Category   string // category name, filled by queries
	SortOrder  int
	Enabled    bool
}

// Validate checks and normalises a channel before it is stored.
func (c *Channel) Validate() error {
	c.Name = strings.TrimSpace(c.Name)
	c.StreamURL = strings.TrimSpace(c.StreamURL)
	c.LogoURL = strings.TrimSpace(c.LogoURL)
	c.EPGID = strings.TrimSpace(c.EPGID)
	if c.Name == "" {
		return errors.New("naam mag niet leeg zijn")
	}
	if !isHTTPURL(c.StreamURL) {
		return errors.New("stream-URL moet beginnen met http:// of https://")
	}
	if c.LogoURL != "" && !isHTTPURL(c.LogoURL) {
		return errors.New("logo-URL moet beginnen met http:// of https://")
	}
	return nil
}

func isHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// Categories returns all categories with their channel counts.
func (s *Store) Categories(ctx context.Context) ([]Category, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.id, c.name, c.sort_order, COUNT(ch.id) FROM categories c
		 LEFT JOIN channels ch ON ch.category_id = c.id
		 GROUP BY c.id ORDER BY c.sort_order, c.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name, &c.SortOrder, &c.ChannelCount); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SaveCategory creates (ID 0) or renames a category.
func (s *Store) SaveCategory(ctx context.Context, c Category) (int64, error) {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return 0, errors.New("naam mag niet leeg zijn")
	}
	if c.ID == 0 {
		res, err := s.db.ExecContext(ctx,
			`INSERT INTO categories(name, sort_order) VALUES(?, ?)`, c.Name, c.SortOrder)
		if err != nil {
			return 0, uniqueErr(err, "categorie bestaat al")
		}
		return res.LastInsertId()
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE categories SET name = ?, sort_order = ? WHERE id = ?`, c.Name, c.SortOrder, c.ID)
	if err != nil {
		return 0, uniqueErr(err, "categorie bestaat al")
	}
	return c.ID, mustAffect(res)
}

// DeleteCategory removes a category; its channels become uncategorised.
func (s *Store) DeleteCategory(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM categories WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

// categoryID returns the id of the named category, creating it when needed.
func categoryID(ctx context.Context, tx *sql.Tx, name string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, nil
	}
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM categories WHERE name = ?`, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO categories(name) VALUES(?)`, name)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const channelSelect = `SELECT ch.id, ch.name, ch.stream_url, ch.logo_url, ch.epg_id,
	COALESCE(ch.category_id, 0), COALESCE(c.name, ''), ch.sort_order, ch.enabled
	FROM channels ch LEFT JOIN categories c ON c.id = ch.category_id`

func scanChannel(sc interface{ Scan(...any) error }) (Channel, error) {
	var c Channel
	err := sc.Scan(&c.ID, &c.Name, &c.StreamURL, &c.LogoURL, &c.EPGID,
		&c.CategoryID, &c.Category, &c.SortOrder, &c.Enabled)
	return c, err
}

// ChannelFilter narrows a channel listing.
type ChannelFilter struct {
	CategoryID  int64 // 0 = all, -1 = uncategorised
	Search      string
	EnabledOnly bool
}

// Channels lists channels ordered by category and sort order.
func (s *Store) Channels(ctx context.Context, f ChannelFilter) ([]Channel, error) {
	q := channelSelect + ` WHERE 1=1`
	var args []any
	switch {
	case f.CategoryID > 0:
		q += ` AND ch.category_id = ?`
		args = append(args, f.CategoryID)
	case f.CategoryID < 0:
		q += ` AND ch.category_id IS NULL`
	}
	if s := strings.TrimSpace(f.Search); s != "" {
		q += ` AND (ch.name LIKE ? ESCAPE '\' OR ch.stream_url LIKE ? ESCAPE '\')`
		like := "%" + escapeLike(s) + "%"
		args = append(args, like, like)
	}
	if f.EnabledOnly {
		q += ` AND ch.enabled = 1`
	}
	q += ` ORDER BY COALESCE(c.sort_order, 1e9), c.name, ch.sort_order, ch.name`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// Channel returns a single channel.
func (s *Store) Channel(ctx context.Context, id int64) (Channel, error) {
	c, err := scanChannel(s.db.QueryRowContext(ctx, channelSelect+` WHERE ch.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Channel{}, ErrNotFound
	}
	return c, err
}

// SaveChannel creates (ID 0) or updates a channel.
func (s *Store) SaveChannel(ctx context.Context, c Channel) (int64, error) {
	if err := c.Validate(); err != nil {
		return 0, err
	}
	if c.ID == 0 {
		res, err := s.db.ExecContext(ctx,
			`INSERT INTO channels(name, stream_url, logo_url, epg_id, category_id, sort_order, enabled)
			 VALUES(?, ?, ?, ?, ?, ?, ?)`,
			c.Name, c.StreamURL, c.LogoURL, c.EPGID, nullID(c.CategoryID), c.SortOrder, c.Enabled)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE channels SET name = ?, stream_url = ?, logo_url = ?, epg_id = ?,
		 category_id = ?, sort_order = ?, enabled = ? WHERE id = ?`,
		c.Name, c.StreamURL, c.LogoURL, c.EPGID, nullID(c.CategoryID), c.SortOrder, c.Enabled, c.ID)
	if err != nil {
		return 0, err
	}
	return c.ID, mustAffect(res)
}

// SetChannelEnabled switches a channel on or off.
func (s *Store) SetChannelEnabled(ctx context.Context, id int64, enabled bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE channels SET enabled = ? WHERE id = ?`, enabled, id)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

// DeleteChannel removes a channel.
func (s *Store) DeleteChannel(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM channels WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

// ImportEntry is one channel read from an M3U playlist.
type ImportEntry struct {
	Name      string
	StreamURL string
	LogoURL   string
	EPGID     string
	Group     string
}

// ImportResult reports what an import did.
type ImportResult struct {
	Added   int
	Updated int
	Skipped []string // human readable reasons
}

// ImportChannels adds channels from a playlist. A channel whose stream URL
// already exists is updated instead of duplicated. Categories are created
// from the group names when they do not exist yet.
func (s *Store) ImportChannels(ctx context.Context, entries []ImportEntry) (ImportResult, error) {
	var r ImportResult
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		for _, e := range entries {
			c := Channel{Name: e.Name, StreamURL: e.StreamURL, LogoURL: e.LogoURL, EPGID: e.EPGID, Enabled: true}
			if err := c.Validate(); err != nil {
				r.Skipped = append(r.Skipped, e.Name+": "+err.Error())
				continue
			}
			cat, err := categoryID(ctx, tx, e.Group)
			if err != nil {
				return err
			}
			var id int64
			err = tx.QueryRowContext(ctx, `SELECT id FROM channels WHERE stream_url = ?`, c.StreamURL).Scan(&id)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO channels(name, stream_url, logo_url, epg_id, category_id, sort_order)
					 VALUES(?, ?, ?, ?, ?, (SELECT COALESCE(MAX(sort_order), 0) + 1 FROM channels))`,
					c.Name, c.StreamURL, c.LogoURL, c.EPGID, nullID(cat)); err != nil {
					return err
				}
				r.Added++
			case err != nil:
				return err
			default:
				if _, err := tx.ExecContext(ctx,
					`UPDATE channels SET name = ?,
					 logo_url = CASE WHEN ? <> '' THEN ? ELSE logo_url END,
					 epg_id = CASE WHEN ? <> '' THEN ? ELSE epg_id END,
					 category_id = COALESCE(?, category_id)
					 WHERE id = ?`,
					c.Name, c.LogoURL, c.LogoURL, c.EPGID, c.EPGID, nullID(cat), id); err != nil {
					return err
				}
				r.Updated++
			}
		}
		return nil
	})
	return r, err
}

func mustAffect(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func uniqueErr(err error, msg string) error {
	if strings.Contains(err.Error(), "UNIQUE") {
		return errors.New(msg)
	}
	return err
}
