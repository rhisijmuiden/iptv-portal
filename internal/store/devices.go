package store

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"strings"
	"time"
)

// MaxUserDevices is how many devices a user can register.
const MaxUserDevices = 10

// Device is a TV box or other device a user registered with its MAC
// address, so the admin knows which devices belong to the account.
type Device struct {
	ID        int64
	UserID    int64
	Name      string
	MAC       string // upper case, separated by colons
	CreatedAt time.Time
}

// NormalizeMAC checks a MAC address and writes it as 00:1A:2B:3C:4D:5E.
// Colons, dashes, dots and no separators at all are accepted.
func NormalizeMAC(s string) (string, error) {
	s = strings.TrimSpace(s)
	hex := strings.NewReplacer(":", "", "-", "", ".", "", " ", "").Replace(s)
	if len(hex) == 12 {
		var b strings.Builder
		for i := 0; i < 12; i += 2 {
			if i > 0 {
				b.WriteByte(':')
			}
			b.WriteString(hex[i : i+2])
		}
		s = b.String()
	}
	hw, err := net.ParseMAC(s)
	if err != nil || len(hw) != 6 {
		return "", errors.New("enter a MAC address of 12 hexadecimal digits, such as 00:1A:79:12:34:56")
	}
	if hw.String() == "00:00:00:00:00:00" {
		return "", errors.New("enter a MAC address of 12 hexadecimal digits, such as 00:1A:79:12:34:56")
	}
	return strings.ToUpper(hw.String()), nil
}

// UserDevices returns the devices a user registered, oldest first.
func (s *Store) UserDevices(ctx context.Context, userID int64) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, name, mac, created_at FROM user_devices WHERE user_id = ? ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.UserID, &d.Name, &d.MAC, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// AddUserDevice registers a device for a user.
func (s *Store) AddUserDevice(ctx context.Context, userID int64, name, mac string) (Device, error) {
	d := Device{UserID: userID, Name: strings.TrimSpace(name), CreatedAt: time.Now().UTC()}
	if d.Name == "" {
		return d, errors.New("enter a name for the device")
	}
	if len([]rune(d.Name)) > 60 {
		return d, errors.New("the name can be at most 60 characters")
	}
	var err error
	if d.MAC, err = NormalizeMAC(mac); err != nil {
		return d, err
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_devices WHERE user_id = ?`, userID).Scan(&n); err != nil {
		return d, err
	}
	if n >= MaxUserDevices {
		return d, errors.New("you can register at most 10 devices; remove one first")
	}
	// A MAC address logs in on the MAG / Stalker portal, so it can belong
	// to one account only.
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_devices WHERE mac = ? AND user_id <> ?`, d.MAC, userID).Scan(&n); err != nil {
		return d, err
	}
	if n > 0 {
		return d, errors.New("this MAC address is registered to another account")
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO user_devices(user_id, name, mac, created_at) VALUES(?, ?, ?, ?)`,
		userID, d.Name, d.MAC, d.CreatedAt)
	if err != nil {
		return d, uniqueErr(err, "this MAC address is already registered")
	}
	d.ID, _ = res.LastInsertId()
	return d, nil
}

// UserDevice returns one device by id.
func (s *Store) UserDevice(ctx context.Context, id int64) (Device, error) {
	var d Device
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, name, mac, created_at FROM user_devices WHERE id = ?`, id).
		Scan(&d.ID, &d.UserID, &d.Name, &d.MAC, &d.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// AuthenticateDevice finds the user a MAC address is registered to, for
// players that log in with the MAC address of the box (MAG / Stalker
// portal). It returns ErrNotFound for an unknown address, or one that
// several accounts registered before addresses had to be unique, and
// ErrUserInactive with the user when the account is disabled or expired.
func (s *Store) AuthenticateDevice(ctx context.Context, mac string) (Device, User, error) {
	mac, err := NormalizeMAC(mac)
	if err != nil {
		return Device{}, User{}, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, name, mac, created_at FROM user_devices WHERE mac = ? LIMIT 2`, mac)
	if err != nil {
		return Device{}, User{}, err
	}
	var found []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.UserID, &d.Name, &d.MAC, &d.CreatedAt); err != nil {
			rows.Close()
			return Device{}, User{}, err
		}
		found = append(found, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Device{}, User{}, err
	}
	if len(found) != 1 {
		return Device{}, User{}, ErrNotFound
	}
	d := found[0]
	u, err := s.User(ctx, d.UserID)
	if err != nil {
		return d, u, err
	}
	if !u.Active() {
		return d, u, ErrUserInactive
	}
	if u.LastSeenAt == nil || time.Since(*u.LastSeenAt) > time.Minute {
		s.db.ExecContext(ctx, `UPDATE users SET last_seen_at = ? WHERE id = ?`, time.Now().UTC(), u.ID)
	}
	return d, u, nil
}

// DeleteUserDevice removes a device of a user. Another user's device is
// not found.
func (s *Store) DeleteUserDevice(ctx context.Context, userID, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM user_devices WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

// UsedDevice is one player (app on an IP address) a user watched with.
type UsedDevice struct {
	IP        string
	UserAgent string
	Views     int
	Time      time.Duration
	First     time.Time
	Last      time.Time
	// LastChannel is what was last opened with it.
	LastChannel string
}

// UsedDevices returns the players that opened channels for a user since
// the given time, most recently used first.
func (s *Store) UsedDevices(ctx context.Context, userID int64, since time.Time, limit int) ([]UsedDevice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT v.ip, v.user_agent, COUNT(*), SUM(v.ended_at - v.started_at),
		MIN(v.started_at), MAX(v.started_at),
		(SELECT l.channel FROM views l WHERE l.user_id = v.user_id AND l.ip = v.ip AND l.user_agent = v.user_agent
		 ORDER BY l.started_at DESC LIMIT 1)
		FROM views v WHERE v.user_id = ? AND v.started_at >= ?
		GROUP BY v.ip, v.user_agent ORDER BY MAX(v.started_at) DESC LIMIT ?`, userID, since.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsedDevice
	for rows.Next() {
		var d UsedDevice
		var secs, first, last int64
		if err := rows.Scan(&d.IP, &d.UserAgent, &d.Views, &secs, &first, &last, &d.LastChannel); err != nil {
			return nil, err
		}
		d.Time, d.First, d.Last = time.Duration(secs)*time.Second, time.Unix(first, 0), time.Unix(last, 0)
		out = append(out, d)
	}
	return out, rows.Err()
}
