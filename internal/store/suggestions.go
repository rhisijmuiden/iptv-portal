package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Sources of suggested replacement streams.
const (
	SourceIPTVOrg      = "iptv-org"
	SourceRadioBrowser = "radio-browser"
)

// Reasons a stream was suggested for a channel, from strongest to weakest.
const (
	MatchSource = "source" // the station or channel the channel was imported from
	MatchEPG    = "epg"    // the channel's EPG ID
	MatchURL    = "url"    // the database lists the dead stream for this channel
	MatchName   = "name"   // the same name
)

// Suggestion is a working stream found for a failing channel. Nothing
// changes until an admin accepts it.
type Suggestion struct {
	ID        int64
	ChannelID int64
	StreamURL string
	Name      string // the name the source gives the stream
	Source    string // SourceIPTVOrg or SourceRadioBrowser
	SourceID  string // channel id or station UUID in the source
	LogoURL   string
	EPGID     string
	Match     string // one of the Match constants
	Detail    string // what the test of the stream found, e.g. "HLS, segments load"
	FoundAt   time.Time
	Dismissed bool
}

const suggestionSelect = `SELECT id, channel_id, stream_url, name, source, source_id, logo_url, epg_id,
	match, detail, found_at, dismissed FROM stream_suggestions`

func scanSuggestion(sc interface{ Scan(...any) error }) (Suggestion, error) {
	var s Suggestion
	err := sc.Scan(&s.ID, &s.ChannelID, &s.StreamURL, &s.Name, &s.Source, &s.SourceID, &s.LogoURL, &s.EPGID,
		&s.Match, &s.Detail, &s.FoundAt, &s.Dismissed)
	return s, err
}

// SaveSuggestions stores the suggestions found for a channel and notes
// that it was searched. A stream that was dismissed before stays
// dismissed; one that is suggested already gets the new details. It
// returns how many suggestions are new.
func (s *Store) SaveSuggestions(ctx context.Context, channelID int64, list []Suggestion) (int, error) {
	added := 0
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC()
		for _, sg := range list {
			res, err := tx.ExecContext(ctx,
				`INSERT INTO stream_suggestions(channel_id, stream_url, name, source, source_id, logo_url, epg_id, match, detail, found_at)
				 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				 ON CONFLICT(channel_id, stream_url) DO NOTHING`,
				channelID, sg.StreamURL, sg.Name, sg.Source, sg.SourceID, sg.LogoURL, sg.EPGID, sg.Match, sg.Detail, now)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				added++
				continue
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE stream_suggestions SET name = ?, source_id = ?, logo_url = ?, epg_id = ?, match = ?, detail = ?
				 WHERE channel_id = ? AND stream_url = ? AND dismissed = 0`,
				sg.Name, sg.SourceID, sg.LogoURL, sg.EPGID, sg.Match, sg.Detail, channelID, sg.StreamURL); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE channel_health SET searched_at = ? WHERE channel_id = ?`, now, channelID)
		return err
	})
	return added, err
}

// SearchedAt returns when a replacement was last looked for, by channel
// ID, for the channels searched since their stream failed.
func (s *Store) SearchedAt(ctx context.Context) (map[int64]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT channel_id, searched_at FROM channel_health WHERE searched_at IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]time.Time{}
	for rows.Next() {
		var id int64
		var t time.Time
		if err := rows.Scan(&id, &t); err != nil {
			return nil, err
		}
		out[id] = t
	}
	return out, rows.Err()
}

// Suggestions returns the open suggestions by channel ID, in the order
// they were found.
func (s *Store) Suggestions(ctx context.Context) (map[int64][]Suggestion, error) {
	rows, err := s.db.QueryContext(ctx, suggestionSelect+` WHERE dismissed = 0 ORDER BY channel_id, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]Suggestion{}
	for rows.Next() {
		sg, err := scanSuggestion(rows)
		if err != nil {
			return nil, err
		}
		out[sg.ChannelID] = append(out[sg.ChannelID], sg)
	}
	return out, rows.Err()
}

// DismissedStreams returns the stream URLs dismissed for a channel, so
// they are not tested again.
func (s *Store) DismissedStreams(ctx context.Context, channelID int64) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT stream_url FROM stream_suggestions WHERE channel_id = ? AND dismissed = 1`, channelID)
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

// CountSuggestions returns how many failing channels have an open
// suggestion.
func (s *Store) CountSuggestions(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT channel_id) FROM stream_suggestions WHERE dismissed = 0`).Scan(&n)
	return n, err
}

// DismissSuggestion hides a suggestion for good.
func (s *Store) DismissSuggestion(ctx context.Context, id int64) (Suggestion, error) {
	sg, err := scanSuggestion(s.db.QueryRowContext(ctx, suggestionSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return sg, ErrNotFound
	}
	if err != nil {
		return sg, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE stream_suggestions SET dismissed = 1 WHERE id = ?`, id)
	return sg, err
}

// AcceptSuggestion switches the channel to the suggested stream. The logo
// and EPG ID of the source are filled in when the channel has none, and a
// radio station takes over the Radio Browser UUID so it is recognised in
// later searches. The channel's other open suggestions are removed.
func (s *Store) AcceptSuggestion(ctx context.Context, id int64) (Channel, error) {
	var c Channel
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		sg, err := scanSuggestion(tx.QueryRowContext(ctx, suggestionSelect+` WHERE id = ? AND dismissed = 0`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		c, err = scanChannel(tx.QueryRowContext(ctx, channelSelect+` WHERE ch.id = ?`, sg.ChannelID))
		if err != nil {
			return err
		}
		c.StreamURL = sg.StreamURL
		if c.LogoURL == "" && isHTTPURL(sg.LogoURL) {
			c.LogoURL = sg.LogoURL
		}
		if c.EPGID == "" {
			c.EPGID = sg.EPGID
		}
		if c.IsRadio() && sg.Source == SourceRadioBrowser && sg.SourceID != "" && sg.SourceID != c.SourceID {
			var taken int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM channels WHERE source_id = ? AND kind = ? AND id <> ?`,
				sg.SourceID, c.Kind, c.ID).Scan(&taken); err != nil {
				return err
			}
			if taken == 0 {
				c.SourceID = sg.SourceID
			}
		}
		if err := c.Validate(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE channels SET stream_url = ?, logo_url = ?, epg_id = ?, source_id = ? WHERE id = ?`,
			c.StreamURL, c.LogoURL, c.EPGID, c.SourceID, c.ID); err != nil {
			return err
		}
		// The stream is in use now: suggesting it elsewhere makes no sense.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM stream_suggestions WHERE dismissed = 0 AND (channel_id = ? OR stream_url = ?)`,
			c.ID, c.StreamURL); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE channel_health SET searched_at = NULL WHERE channel_id = ?`, c.ID)
		return err
	})
	return c, err
}
