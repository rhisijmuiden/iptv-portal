package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// AlertSent reports whether the alert or reminder with this key was sent.
func (s *Store) AlertSent(ctx context.Context, key string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM alerts_sent WHERE key = ?`, key).Scan(&n)
	return n > 0, err
}

// AlertKeys returns the keys of sent alerts that start with prefix.
func (s *Store) AlertKeys(ctx context.Context, prefix string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key FROM alerts_sent WHERE substr(key, 1, ?) = ? ORDER BY key`,
		len(prefix), prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// MarkAlertsSent remembers that the alerts with these keys were sent.
func (s *Store) MarkAlertsSent(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	now := time.Now().UTC()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for _, k := range keys {
			if _, err := tx.ExecContext(ctx, `INSERT INTO alerts_sent(key, sent_at) VALUES(?, ?)
				ON CONFLICT(key) DO UPDATE SET sent_at = excluded.sent_at`, k, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// ForgetAlerts removes sent alerts, so they are sent again when the
// problem comes back.
func (s *Store) ForgetAlerts(ctx context.Context, keys ...string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for _, k := range keys {
			if _, err := tx.ExecContext(ctx, `DELETE FROM alerts_sent WHERE key = ?`, k); err != nil {
				return err
			}
		}
		return nil
	})
}

// PruneAlerts removes sent alerts and log entries older than before.
func (s *Store) PruneAlerts(ctx context.Context, before time.Time) error {
	before = before.UTC()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM alerts_sent WHERE sent_at < ?`, before); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM alert_log WHERE at < ?`, before)
	return err
}

// AlertLogEntry is one alert as it was sent to the admins.
type AlertLogEntry struct {
	ID     int64
	At     time.Time
	Kind   string
	Title  string
	Detail string
	// Channels are where it was sent to, e.g. "email", "telegram".
	Channels []string
	// Error says what went wrong when sending; empty when it went out.
	Error string
}

// LogAlert stores a sent alert.
func (s *Store) LogAlert(ctx context.Context, e AlertLogEntry) error {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO alert_log(at, kind, title, detail, channels, error) VALUES(?, ?, ?, ?, ?, ?)`,
		e.At.UTC(), e.Kind, e.Title, e.Detail, strings.Join(e.Channels, " "), e.Error)
	return err
}

// AlertLog returns the most recent alerts, newest first.
func (s *Store) AlertLog(ctx context.Context, limit int) ([]AlertLogEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, at, kind, title, detail, channels, error FROM alert_log
		ORDER BY at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertLogEntry
	for rows.Next() {
		var e AlertLogEntry
		var ch string
		if err := rows.Scan(&e.ID, &e.At, &e.Kind, &e.Title, &e.Detail, &ch, &e.Error); err != nil {
			return nil, err
		}
		e.Channels = strings.Fields(ch)
		out = append(out, e)
	}
	return out, rows.Err()
}
