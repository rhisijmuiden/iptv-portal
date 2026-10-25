package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// KindVOD marks a view of an On Demand film or episode. Views of channels
// carry the channel's kind (KindTV or KindRadio).
const KindVOD = "vod"

// View is one time a user opened a channel or an On Demand item. The
// portal does not see the stream itself, so EndedAt is an estimate; see
// package stats.
type View struct {
	ID        int64
	UserID    int64
	Username  string
	ChannelID int64 // 0 for On Demand and for channels deleted since
	Channel   string
	Kind      string
	IP        string
	UserAgent string
	StartedAt time.Time
	EndedAt   time.Time
}

// Duration is how long the view lasted, as far as the portal can tell.
func (v View) Duration() time.Duration { return v.EndedAt.Sub(v.StartedAt) }

// StartView stores a new view and returns its id.
func (s *Store) StartView(ctx context.Context, v View) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO views(user_id, username, channel_id, channel, kind, ip, user_agent, started_at, ended_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		v.UserID, v.Username, nullID(v.ChannelID), v.Channel, v.Kind, v.IP, v.UserAgent,
		v.StartedAt.Unix(), v.EndedAt.Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetViewEnd moves the end of a view.
func (s *Store) SetViewEnd(ctx context.Context, id int64, end time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE views SET ended_at = ? WHERE id = ?`, end.Unix(), id)
	return err
}

// PurgeViews removes views that started before t and returns how many.
func (s *Store) PurgeViews(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM views WHERE started_at < ?`, before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ClearViews removes all viewing statistics.
func (s *Store) ClearViews(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM views`)
	return err
}

// ---- retention ----

const keyViewRetention = "stats_retention_days"

// DefaultViewRetention is how many days of views are kept unless set
// otherwise.
const DefaultViewRetention = 365

// MaxViewRetention is the longest retention that can be set, in days.
const MaxViewRetention = 10 * 365

// ViewRetention returns how many days views are kept; 0 keeps them
// forever.
func (s *Store) ViewRetention(ctx context.Context) (int, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, keyViewRetention).Scan(&v)
	if err == sql.ErrNoRows {
		return DefaultViewRetention, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return DefaultViewRetention, nil
	}
	return n, nil
}

// SetViewRetention sets how many days views are kept; 0 keeps them
// forever.
func (s *Store) SetViewRetention(ctx context.Context, days int) error {
	if days < 0 || days > MaxViewRetention {
		return fmt.Errorf("days to keep statistics must be between 1 and %d, or 0 to keep them forever", MaxViewRetention)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, keyViewRetention, strconv.Itoa(days))
	return err
}

// ---- statistics ----

// ViewFilter selects the views a statistic is made of. Zero fields do not
// filter.
type ViewFilter struct {
	Since     time.Time
	Kind      string
	UserID    int64
	ChannelID int64
}

func (f ViewFilter) where() (string, []any) {
	var conds []string
	var args []any
	if !f.Since.IsZero() {
		conds, args = append(conds, "v.started_at >= ?"), append(args, f.Since.Unix())
	}
	if f.Kind != "" {
		conds, args = append(conds, "v.kind = ?"), append(args, f.Kind)
	}
	if f.UserID != 0 {
		conds, args = append(conds, "v.user_id = ?"), append(args, f.UserID)
	}
	if f.ChannelID != 0 {
		conds, args = append(conds, "v.channel_id = ?"), append(args, f.ChannelID)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ViewSummary adds up the views of a filter.
type ViewSummary struct {
	Views    int
	Viewers  int // distinct users
	Channels int // distinct channels and On Demand titles
	Time     time.Duration
}

// Hours is the viewing time in hours.
func (s ViewSummary) Hours() float64 { return s.Time.Hours() }

// ViewSummary returns the totals of the views matching f.
func (s *Store) ViewSummary(ctx context.Context, f ViewFilter) (ViewSummary, error) {
	where, args := f.where()
	var out ViewSummary
	var secs int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(DISTINCT v.user_id),
		COUNT(DISTINCT COALESCE(CAST(v.channel_id AS TEXT), 'n:' || v.channel)),
		COALESCE(SUM(v.ended_at - v.started_at), 0) FROM views v`+where, args...).
		Scan(&out.Views, &out.Viewers, &out.Channels, &secs)
	out.Time = time.Duration(secs) * time.Second
	return out, err
}

// DayViews counts the views that started on one day.
type DayViews struct {
	Day   time.Time // midnight, local time
	Views int
	Time  time.Duration
}

// ViewsByDay returns the views matching f per local day, from the day of
// f.Since up to and including today; days without views are included.
// f.Since must be set.
func (s *Store) ViewsByDay(ctx context.Context, f ViewFilter, now time.Time) ([]DayViews, error) {
	where, args := f.where()
	rows, err := s.db.QueryContext(ctx, `SELECT date(v.started_at, 'unixepoch', 'localtime') AS day, COUNT(*),
		SUM(v.ended_at - v.started_at) FROM views v`+where+` GROUP BY day`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := map[string]DayViews{}
	for rows.Next() {
		var day string
		var d DayViews
		var secs int64
		if err := rows.Scan(&day, &d.Views, &secs); err != nil {
			return nil, err
		}
		d.Time = time.Duration(secs) * time.Second
		found[day] = d
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []DayViews
	first := f.Since.In(time.Local)
	day := time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, time.Local)
	for !day.After(now) {
		d := found[day.Format("2006-01-02")]
		d.Day = day
		out = append(out, d)
		day = day.AddDate(0, 0, 1)
	}
	return out, nil
}

// ChannelViews adds up the views of one channel or On Demand title.
type ChannelViews struct {
	ChannelID int64 // 0 for On Demand and deleted channels
	Name      string
	Kind      string
	Views     int
	Viewers   int
	Time      time.Duration
	Last      time.Time
}

// ChannelViews returns the views matching f per channel, most watched
// first.
func (s *Store) ChannelViews(ctx context.Context, f ViewFilter) ([]ChannelViews, error) {
	where, args := f.where()
	rows, err := s.db.QueryContext(ctx, `SELECT COALESCE(v.channel_id, 0), COALESCE(MAX(c.name), MAX(v.channel)), v.kind,
		COUNT(*), COUNT(DISTINCT v.user_id), SUM(v.ended_at - v.started_at), MAX(v.started_at)
		FROM views v LEFT JOIN channels c ON c.id = v.channel_id`+where+`
		GROUP BY COALESCE(CAST(v.channel_id AS TEXT), 'n:' || v.channel), v.kind
		ORDER BY SUM(v.ended_at - v.started_at) DESC, COUNT(*) DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChannelViews
	for rows.Next() {
		var c ChannelViews
		var secs, last int64
		if err := rows.Scan(&c.ChannelID, &c.Name, &c.Kind, &c.Views, &c.Viewers, &secs, &last); err != nil {
			return nil, err
		}
		c.Time, c.Last = time.Duration(secs)*time.Second, time.Unix(last, 0)
		out = append(out, c)
	}
	return out, rows.Err()
}

// UserViews adds up the views of one user.
type UserViews struct {
	UserID   int64
	Username string
	Views    int
	Channels int
	Time     time.Duration
	Last     time.Time
	// Favourite is the channel or title the user watched longest.
	Favourite string
}

// UserViews returns the views matching f per user, longest watching first.
func (s *Store) UserViews(ctx context.Context, f ViewFilter) ([]UserViews, error) {
	where, args := f.where()
	rows, err := s.db.QueryContext(ctx, `SELECT v.user_id, COALESCE(MAX(u.username), MAX(v.username)),
		COALESCE(MAX(c.name), MAX(v.channel)), COUNT(*), SUM(v.ended_at - v.started_at), MAX(v.started_at)
		FROM views v LEFT JOIN users u ON u.id = v.user_id LEFT JOIN channels c ON c.id = v.channel_id`+where+`
		GROUP BY v.user_id, COALESCE(CAST(v.channel_id AS TEXT), 'n:' || v.channel)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := map[int64]*UserViews{}
	best := map[int64]int64{}
	for rows.Next() {
		var id, secs, last int64
		var username, channel string
		var n int
		if err := rows.Scan(&id, &username, &channel, &n, &secs, &last); err != nil {
			return nil, err
		}
		u := users[id]
		if u == nil {
			u = &UserViews{UserID: id, Username: username}
			users[id] = u
		}
		u.Views += n
		u.Channels++
		u.Time += time.Duration(secs) * time.Second
		if t := time.Unix(last, 0); t.After(u.Last) {
			u.Last = t
		}
		if secs > best[id] || u.Favourite == "" {
			best[id], u.Favourite = secs, channel
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]UserViews, 0, len(users))
	for _, u := range users {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Time != out[j].Time {
			return out[i].Time > out[j].Time
		}
		return strings.ToLower(out[i].Username) < strings.ToLower(out[j].Username)
	})
	return out, nil
}

// RecentViews returns the latest views matching f, newest first.
func (s *Store) RecentViews(ctx context.Context, f ViewFilter, limit int) ([]View, error) {
	where, args := f.where()
	rows, err := s.db.QueryContext(ctx, `SELECT v.id, v.user_id, COALESCE(u.username, v.username), COALESCE(v.channel_id, 0),
		COALESCE(c.name, v.channel), v.kind, v.ip, v.user_agent, v.started_at, v.ended_at
		FROM views v LEFT JOIN users u ON u.id = v.user_id LEFT JOIN channels c ON c.id = v.channel_id`+where+`
		ORDER BY v.started_at DESC, v.id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []View
	for rows.Next() {
		var v View
		var start, end int64
		if err := rows.Scan(&v.ID, &v.UserID, &v.Username, &v.ChannelID, &v.Channel, &v.Kind, &v.IP, &v.UserAgent,
			&start, &end); err != nil {
			return nil, err
		}
		v.StartedAt, v.EndedAt = time.Unix(start, 0), time.Unix(end, 0)
		out = append(out, v)
	}
	return out, rows.Err()
}

// UnwatchedChannel is an enabled channel nobody opened in a period.
type UnwatchedChannel struct {
	ID       int64
	Name     string
	Kind     string
	Category string
	Added    time.Time
	// LastView is when the channel was last opened at all; nil when never
	// (as far as the statistics go back).
	LastView *time.Time
}

// UnwatchedChannels returns the enabled channels of kind ("" for TV and
// radio) that nobody opened since the given time, longest unwatched first.
func (s *Store) UnwatchedChannels(ctx context.Context, kind string, since time.Time) ([]UnwatchedChannel, error) {
	q := `SELECT c.id, c.name, c.kind, COALESCE(cat.name, ''), c.created_at,
		(SELECT MAX(v.started_at) FROM views v WHERE v.channel_id = c.id)
		FROM channels c LEFT JOIN categories cat ON cat.id = c.category_id
		WHERE c.enabled = 1 AND NOT EXISTS (SELECT 1 FROM views v WHERE v.channel_id = c.id AND v.started_at >= ?)`
	args := []any{since.Unix()}
	if kind != "" {
		q += ` AND c.kind = ?`
		args = append(args, kind)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY c.kind, c.name COLLATE NOCASE`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UnwatchedChannel
	for rows.Next() {
		var c UnwatchedChannel
		var last sql.NullInt64
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.Category, &c.Added, &last); err != nil {
			return nil, err
		}
		if last.Valid {
			t := time.Unix(last.Int64, 0)
			c.LastView = &t
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Never watched first, then the longest ago.
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].LastView, out[j].LastView
		if a == nil || b == nil {
			return a == nil && b != nil
		}
		return a.Before(*b)
	})
	return out, nil
}
