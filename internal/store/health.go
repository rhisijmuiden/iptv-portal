package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// HealthSettings control the periodic stream check. They are stored in the
// settings table but kept apart from Settings, because only the health
// checker uses them.
type HealthSettings struct {
	// IntervalMinutes is the time between two checks of every channel;
	// 0 switches the periodic check off.
	IntervalMinutes int
	// DisableAfter switches a channel off after this many failed checks in
	// a row, and back on once it works again; 0 never switches channels.
	DisableAfter int
}

// DefaultHealthSettings check every hour and never switch channels off.
var DefaultHealthSettings = HealthSettings{IntervalMinutes: 60}

// Limits for HealthSettings.
const (
	MinHealthInterval = 5
	MaxHealthInterval = 7 * 24 * 60
	MaxDisableAfter   = 100
)

const (
	keyHealthInterval     = "health_interval_minutes"
	keyHealthDisableAfter = "health_disable_after"
)

// Interval is the check interval; 0 when the periodic check is off.
func (hs HealthSettings) Interval() time.Duration {
	return time.Duration(hs.IntervalMinutes) * time.Minute
}

// Validate checks the health settings.
func (hs HealthSettings) Validate() error {
	if hs.IntervalMinutes != 0 && (hs.IntervalMinutes < MinHealthInterval || hs.IntervalMinutes > MaxHealthInterval) {
		return fmt.Errorf("check interval must be between %d and %d minutes, or 0 to switch it off",
			MinHealthInterval, MaxHealthInterval)
	}
	if hs.DisableAfter < 0 || hs.DisableAfter > MaxDisableAfter {
		return fmt.Errorf("failed checks before disabling must be between 0 and %d", MaxDisableAfter)
	}
	return nil
}

// HealthSettings returns the stream check settings, falling back to
// defaults.
func (s *Store) HealthSettings(ctx context.Context) (HealthSettings, error) {
	out := DefaultHealthSettings
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings WHERE key IN (?, ?)`,
		keyHealthInterval, keyHealthDisableAfter)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return out, err
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			continue
		}
		switch k {
		case keyHealthInterval:
			out.IntervalMinutes = n
		case keyHealthDisableAfter:
			out.DisableAfter = n
		}
	}
	return out, rows.Err()
}

// SaveHealthSettings validates and stores the stream check settings.
func (s *Store) SaveHealthSettings(ctx context.Context, hs HealthSettings) error {
	if err := hs.Validate(); err != nil {
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for k, v := range map[string]int{keyHealthInterval: hs.IntervalMinutes, keyHealthDisableAfter: hs.DisableAfter} {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO settings(key, value) VALUES(?, ?)
				 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, strconv.Itoa(v)); err != nil {
				return err
			}
		}
		return nil
	})
}

// ChannelHealth is the result of the latest check of a channel.
type ChannelHealth struct {
	ChannelID int64
	OK        bool
	// Message says why the last check failed, or what was found when it
	// passed.
	Message string
	// Failures counts the failed checks in a row; 0 after a good check.
	Failures  int
	CheckedAt time.Time
	LastOKAt  *time.Time
	// AutoDisabled is set when the checker switched the channel off; it is
	// switched back on once a check passes again.
	AutoDisabled bool
}

// HealthChange says what RecordHealth did to the channel itself.
type HealthChange int

// Possible HealthChange values.
const (
	HealthUnchanged HealthChange = iota
	HealthDisabled
	HealthReenabled
)

func scanHealth(sc interface{ Scan(...any) error }) (ChannelHealth, error) {
	var h ChannelHealth
	var ok sql.NullTime
	err := sc.Scan(&h.ChannelID, &h.OK, &h.Message, &h.Failures, &h.CheckedAt, &ok, &h.AutoDisabled)
	if ok.Valid {
		h.LastOKAt = &ok.Time
	}
	return h, err
}

const healthSelect = `SELECT channel_id, ok, message, failures, checked_at, last_ok_at, auto_disabled FROM channel_health`

// ChannelHealth returns the latest check result of every checked channel,
// by channel ID.
func (s *Store) ChannelHealth(ctx context.Context) (map[int64]ChannelHealth, error) {
	rows, err := s.db.QueryContext(ctx, healthSelect)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]ChannelHealth{}
	for rows.Next() {
		h, err := scanHealth(rows)
		if err != nil {
			return nil, err
		}
		out[h.ChannelID] = h
	}
	return out, rows.Err()
}

// RecordHealth stores the result of a check. With disableAfter > 0 an
// enabled channel is switched off after that many failures in a row, and a
// channel switched off that way is switched on again when a check passes.
func (s *Store) RecordHealth(ctx context.Context, channelID int64, ok bool, msg string, disableAfter int) (ChannelHealth, HealthChange, error) {
	var h ChannelHealth
	change := HealthUnchanged
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var enabled bool
		err := tx.QueryRowContext(ctx, `SELECT enabled FROM channels WHERE id = ?`, channelID).Scan(&enabled)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound // deleted while it was being checked
		}
		if err != nil {
			return err
		}
		h, err = scanHealth(tx.QueryRowContext(ctx, healthSelect+` WHERE channel_id = ?`, channelID))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		now := time.Now().UTC()
		h.ChannelID, h.OK, h.Message, h.CheckedAt = channelID, ok, msg, now
		if ok {
			h.Failures = 0
			h.LastOKAt = &now
			if h.AutoDisabled {
				h.AutoDisabled = false
				if !enabled {
					change = HealthReenabled
				}
			}
		} else {
			h.Failures++
			if disableAfter > 0 && h.Failures >= disableAfter && enabled {
				h.AutoDisabled = true
				change = HealthDisabled
			}
		}
		switch change {
		case HealthDisabled, HealthReenabled:
			if _, err := tx.ExecContext(ctx, `UPDATE channels SET enabled = ? WHERE id = ?`,
				change == HealthReenabled, channelID); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO channel_health(channel_id, ok, message, failures, checked_at, last_ok_at, auto_disabled)
			 VALUES(?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(channel_id) DO UPDATE SET ok = excluded.ok, message = excluded.message,
			 failures = excluded.failures, checked_at = excluded.checked_at,
			 last_ok_at = excluded.last_ok_at, auto_disabled = excluded.auto_disabled`,
			channelID, h.OK, h.Message, h.Failures, h.CheckedAt, h.LastOKAt, h.AutoDisabled)
		return err
	})
	return h, change, err
}

// ClearAutoDisabled forgets that the checker switched a channel off, so a
// channel an admin switched on or off by hand is left alone.
func (s *Store) ClearAutoDisabled(ctx context.Context, channelID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE channel_health SET auto_disabled = 0 WHERE channel_id = ?`, channelID)
	return err
}
