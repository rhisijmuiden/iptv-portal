package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"
)

// Kinds of channels and categories.
const (
	KindTV    = "tv"
	KindRadio = "radio"
)

// CleanKind returns k when it is a known kind, otherwise KindTV.
func CleanKind(k string) string {
	if k == KindRadio {
		return KindRadio
	}
	return KindTV
}

// Category groups channels, e.g. "Noord-Holland" or "Regionaal". A
// category holds either TV channels or radio stations.
type Category struct {
	ID           int64
	Name         string
	Kind         string // KindTV or KindRadio
	SortOrder    int
	ChannelCount int
}

// Channel is a single stream: a TV channel or a radio station. Clients
// connect to StreamURL directly; the portal only indexes it.
type Channel struct {
	ID         int64
	Name       string
	Kind       string // KindTV or KindRadio; set when the channel is created
	StreamURL  string
	LogoURL    string
	EPGID      string // tvg-id used to match the channel in an XMLTV guide
	CategoryID int64  // 0 means no category
	Category   string // category name, filled by queries
	SortOrder  int
	Enabled    bool
	// SourceID identifies the channel in the database it was imported
	// from, e.g. the station UUID of Radio Browser; empty when added by
	// hand.
	SourceID string
}

// IsRadio reports whether the channel is a radio station.
func (c Channel) IsRadio() bool { return c.Kind == KindRadio }

// Validate checks and normalises a channel before it is stored.
func (c *Channel) Validate() error {
	c.Kind = CleanKind(c.Kind)
	c.Name = strings.TrimSpace(c.Name)
	c.StreamURL = strings.TrimSpace(c.StreamURL)
	c.LogoURL = strings.TrimSpace(c.LogoURL)
	c.EPGID = strings.TrimSpace(c.EPGID)
	if c.Name == "" {
		return errors.New("name must not be empty")
	}
	if !isHTTPURL(c.StreamURL) {
		return errors.New("stream URL must start with http:// or https://")
	}
	if c.LogoURL != "" && !isHTTPURL(c.LogoURL) {
		return errors.New("logo URL must start with http:// or https://")
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
	return s.categories(ctx, "")
}

// CategoriesOf returns the categories of one kind with their channel
// counts.
func (s *Store) CategoriesOf(ctx context.Context, kind string) ([]Category, error) {
	return s.categories(ctx, CleanKind(kind))
}

func (s *Store) categories(ctx context.Context, kind string) ([]Category, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.id, c.name, c.kind, c.sort_order, COUNT(ch.id) FROM categories c
		 LEFT JOIN channels ch ON ch.category_id = c.id
		 WHERE ? = '' OR c.kind = ?
		 GROUP BY c.id ORDER BY c.sort_order, c.name`, kind, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.SortOrder, &c.ChannelCount); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SaveCategory creates (ID 0) or renames a category. The kind is set when
// the category is created and kept afterwards.
func (s *Store) SaveCategory(ctx context.Context, c Category) (int64, error) {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return 0, errors.New("name must not be empty")
	}
	if c.ID == 0 {
		res, err := s.db.ExecContext(ctx,
			`INSERT INTO categories(name, kind, sort_order) VALUES(?, ?, ?)`, c.Name, CleanKind(c.Kind), c.SortOrder)
		if err != nil {
			return 0, uniqueErr(err, "category already exists")
		}
		return res.LastInsertId()
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE categories SET name = ?, sort_order = ? WHERE id = ?`, c.Name, c.SortOrder, c.ID)
	if err != nil {
		return 0, uniqueErr(err, "category already exists")
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

// EnsureCategory returns the id of the named category of the given kind,
// creating it when it does not exist yet. An empty name returns 0 (no
// category).
func (s *Store) EnsureCategory(ctx context.Context, kind, name string) (int64, error) {
	var id int64
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = categoryID(ctx, tx, kind, name)
		return err
	})
	return id, err
}

// categoryID returns the id of the named category of the given kind,
// creating it when needed. Category names are unique over both kinds, so
// when a category of the other kind has the name, " (Radio)" or " (TV)" is
// added to it.
func categoryID(ctx context.Context, tx *sql.Tx, kind, name string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, nil
	}
	kind = CleanKind(kind)
	suffix := " (TV)"
	if kind == KindRadio {
		suffix = " (Radio)"
	}
	for _, n := range []string{name, name + suffix} {
		var id int64
		var k string
		err := tx.QueryRowContext(ctx, `SELECT id, kind FROM categories WHERE name = ?`, n).Scan(&id, &k)
		if err == nil {
			if k == kind {
				return id, nil
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO categories(name, kind) VALUES(?, ?)`, n, kind)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	return 0, errors.New("category " + name + " already exists")
}

const channelSelect = `SELECT ch.id, ch.name, ch.kind, ch.stream_url, ch.logo_url, ch.epg_id,
	COALESCE(ch.category_id, 0), COALESCE(c.name, ''), ch.sort_order, ch.enabled, ch.source_id
	FROM channels ch LEFT JOIN categories c ON c.id = ch.category_id`

func scanChannel(sc interface{ Scan(...any) error }) (Channel, error) {
	var c Channel
	err := sc.Scan(&c.ID, &c.Name, &c.Kind, &c.StreamURL, &c.LogoURL, &c.EPGID,
		&c.CategoryID, &c.Category, &c.SortOrder, &c.Enabled, &c.SourceID)
	return c, err
}

// ChannelFilter narrows a channel listing.
type ChannelFilter struct {
	Kind        string // KindTV, KindRadio or "" for both
	CategoryID  int64  // 0 = all, -1 = uncategorised
	Search      string
	EnabledOnly bool
}

// Channels lists channels ordered by category and sort order.
func (s *Store) Channels(ctx context.Context, f ChannelFilter) ([]Channel, error) {
	q := channelSelect + ` WHERE 1=1`
	var args []any
	if f.Kind != "" {
		q += ` AND ch.kind = ?`
		args = append(args, CleanKind(f.Kind))
	}
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
			`INSERT INTO channels(name, kind, stream_url, logo_url, epg_id, category_id, sort_order, enabled, source_id)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.Name, c.Kind, c.StreamURL, c.LogoURL, c.EPGID, nullID(c.CategoryID), c.SortOrder, c.Enabled, c.SourceID)
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

// inIDs returns " IN (?, ?, …)" for ids and the ids as query arguments.
func inIDs(ids []int64) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return " IN (" + strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ") + ")", args
}

// DeleteChannels removes the channels of kind with the given ids and
// returns how many were removed. Ids of the other kind are left alone.
func (s *Store) DeleteChannels(ctx context.Context, kind string, ids []int64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	in, args := inIDs(ids)
	res, err := s.db.ExecContext(ctx, `DELETE FROM channels WHERE kind = ? AND id`+in,
		append([]any{CleanKind(kind)}, args...)...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// SetChannelsCategory moves the channels of kind with the given ids to a
// category of the same kind; 0 leaves them without a category.
func (s *Store) SetChannelsCategory(ctx context.Context, kind string, ids []int64, categoryID int64) (int, error) {
	kind = CleanKind(kind)
	if categoryID != 0 {
		var catKind string
		err := s.db.QueryRowContext(ctx, `SELECT kind FROM categories WHERE id = ?`, categoryID).Scan(&catKind)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && catKind != kind) {
			return 0, errors.New("choose an existing category")
		}
		if err != nil {
			return 0, err
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}
	in, args := inIDs(ids)
	res, err := s.db.ExecContext(ctx, `UPDATE channels SET category_id = ? WHERE kind = ? AND id`+in,
		append([]any{nullID(categoryID), kind}, args...)...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// SetChannelsEnabled switches the channels of kind with the given ids on
// or off. They were switched by hand, so the stream check no longer
// switches them back.
func (s *Store) SetChannelsEnabled(ctx context.Context, kind string, ids []int64, enabled bool) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	in, args := inIDs(ids)
	var n int64
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE channels SET enabled = ? WHERE kind = ? AND id`+in,
			append([]any{enabled, CleanKind(kind)}, args...)...)
		if err != nil {
			return err
		}
		if n, err = res.RowsAffected(); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE channel_health SET auto_disabled = 0
			WHERE channel_id IN (SELECT id FROM channels WHERE kind = ? AND id`+in+`)`,
			append([]any{CleanKind(kind)}, args...)...)
		return err
	})
	return int(n), err
}

// ImportEntry is one channel read from an M3U playlist or picked from a
// public database.
type ImportEntry struct {
	Kind      string // KindTV (the default) or KindRadio
	Name      string
	StreamURL string
	LogoURL   string
	EPGID     string
	Group     string
	// SourceID identifies the entry in the database it comes from; an
	// entry with a known SourceID updates that channel.
	SourceID string
}

// ImportResult reports what an import did.
type ImportResult struct {
	Added   int
	Updated int
	Skipped []string // human readable reasons
}

// ImportChannels adds channels from a playlist or database. A channel
// with the same source id or, failing that, the same stream URL is updated
// instead of duplicated. Categories are created from the group names when
// they do not exist yet.
func (s *Store) ImportChannels(ctx context.Context, entries []ImportEntry) (ImportResult, error) {
	var r ImportResult
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		for _, e := range entries {
			c := Channel{Kind: e.Kind, Name: e.Name, StreamURL: e.StreamURL, LogoURL: e.LogoURL, EPGID: e.EPGID,
				SourceID: strings.TrimSpace(e.SourceID), Enabled: true}
			if err := c.Validate(); err != nil {
				r.Skipped = append(r.Skipped, e.Name+": "+err.Error())
				continue
			}
			cat, err := categoryID(ctx, tx, c.Kind, e.Group)
			if err != nil {
				return err
			}
			var id int64
			err = sql.ErrNoRows
			if c.SourceID != "" {
				err = tx.QueryRowContext(ctx, `SELECT id FROM channels WHERE source_id = ? AND kind = ?`,
					c.SourceID, c.Kind).Scan(&id)
			}
			if errors.Is(err, sql.ErrNoRows) {
				err = tx.QueryRowContext(ctx, `SELECT id FROM channels WHERE stream_url = ?`, c.StreamURL).Scan(&id)
			}
			switch {
			case errors.Is(err, sql.ErrNoRows):
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO channels(name, kind, stream_url, logo_url, epg_id, category_id, source_id, sort_order)
					 VALUES(?, ?, ?, ?, ?, ?, ?, (SELECT COALESCE(MAX(sort_order), 0) + 1 FROM channels))`,
					c.Name, c.Kind, c.StreamURL, c.LogoURL, c.EPGID, nullID(cat), c.SourceID); err != nil {
					return err
				}
				r.Added++
			case err != nil:
				return err
			default:
				if _, err := tx.ExecContext(ctx,
					`UPDATE channels SET name = ?, stream_url = ?,
					 logo_url = CASE WHEN ? <> '' THEN ? ELSE logo_url END,
					 epg_id = CASE WHEN ? <> '' THEN ? ELSE epg_id END,
					 source_id = CASE WHEN ? <> '' THEN ? ELSE source_id END,
					 category_id = COALESCE(?, category_id)
					 WHERE id = ?`,
					c.Name, c.StreamURL, c.LogoURL, c.LogoURL, c.EPGID, c.EPGID, c.SourceID, c.SourceID,
					nullID(cat), id); err != nil {
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

// StreamURLs returns the stream URLs of all channels, to tell which
// streams are already in the portal.
func (s *Store) StreamURLs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT stream_url FROM channels`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out[u] = true
	}
	return out, rows.Err()
}

// SourceIDs returns the source ids of the channels of one kind, to tell
// which entries of a public database are already in the portal.
func (s *Store) SourceIDs(ctx context.Context, kind string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source_id FROM channels WHERE kind = ? AND source_id <> ''`, CleanKind(kind))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// AddCategoriesToPackages adds the named categories of the given kind
// (created when needed) to the given packages, keeping what the packages
// already contain.
func (s *Store) AddCategoriesToPackages(ctx context.Context, packageIDs []int64, kind string, categories []string) error {
	if len(packageIDs) == 0 {
		return nil
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for _, name := range categories {
			cat, err := categoryID(ctx, tx, kind, name)
			if err != nil {
				return err
			}
			if cat == 0 {
				continue
			}
			for _, p := range packageIDs {
				if _, err := tx.ExecContext(ctx,
					`INSERT OR IGNORE INTO package_categories(package_id, category_id)
					 SELECT id, ? FROM packages WHERE id = ?`, cat, p); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
