package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Admin is an account that can log in to the admin panel.
type Admin struct {
	ID       int64
	Username string
	// MustChangePassword is set for accounts created with a one-time
	// password; the admin panel only allows changing the password until
	// it is cleared.
	MustChangePassword bool
}

// Session is a logged-in admin panel session.
type Session struct {
	Token string
	CSRF  string
	Admin Admin
}

// SessionTTL is how long an admin stays logged in without activity.
const SessionTTL = 7 * 24 * time.Hour

var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy-password"), bcrypt.DefaultCost)

// ErrInvalidLogin is returned for a wrong username or password.
var ErrInvalidLogin = errors.New("onjuiste gebruikersnaam of wachtwoord")

// AdminCount returns the number of admin accounts.
func (s *Store) AdminCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&n)
	return n, err
}

func validateCredentials(username, password string) error {
	if strings.TrimSpace(username) == "" {
		return errors.New("gebruikersnaam mag niet leeg zijn")
	}
	if len(password) < 8 {
		return errors.New("wachtwoord moet minimaal 8 tekens zijn")
	}
	return nil
}

// CreateAdmin adds an admin account. With mustChange set the admin has to
// pick a new password after logging in.
func (s *Store) CreateAdmin(ctx context.Context, username, password string, mustChange bool) (Admin, error) {
	username = strings.TrimSpace(username)
	if err := validateCredentials(username, password); err != nil {
		return Admin{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return Admin{}, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO admins(username, password_hash, must_change) VALUES(?, ?, ?)`,
		username, string(hash), mustChange)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Admin{}, errors.New("gebruikersnaam bestaat al")
		}
		return Admin{}, err
	}
	id, _ := res.LastInsertId()
	return Admin{ID: id, Username: username, MustChangePassword: mustChange}, nil
}

// SetAdminPassword changes the password of an existing admin and logs out
// all of its sessions. With mustChange set the new password is one-time.
func (s *Store) SetAdminPassword(ctx context.Context, username, password string, mustChange bool) error {
	if err := validateCredentials(username, password); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var id int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM admins WHERE username = ?`, username).Scan(&id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE admins SET password_hash = ?, must_change = ? WHERE id = ?`,
			string(hash), mustChange, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE admin_id = ?`, id)
		return err
	})
}

// Authenticate checks a username and password.
func (s *Store) Authenticate(ctx context.Context, username, password string) (Admin, error) {
	var a Admin
	var hash string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, must_change FROM admins WHERE username = ?`,
		strings.TrimSpace(username)).Scan(&a.ID, &a.Username, &hash, &a.MustChangePassword)
	if errors.Is(err, sql.ErrNoRows) {
		// Spend comparable time to avoid revealing which usernames exist.
		bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return Admin{}, ErrInvalidLogin
	}
	if err != nil {
		return Admin{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return Admin{}, ErrInvalidLogin
	}
	return a, nil
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// CreateSession starts a new session for an admin.
func (s *Store) CreateSession(ctx context.Context, a Admin) (Session, error) {
	sess := Session{Token: randomToken(), CSRF: randomToken(), Admin: a}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions(token, admin_id, csrf, expires_at) VALUES(?, ?, ?, ?)`,
		sess.Token, a.ID, sess.CSRF, time.Now().Add(SessionTTL).UTC())
	return sess, err
}

// Session looks up a valid session and extends its lifetime.
func (s *Store) Session(ctx context.Context, token string) (Session, error) {
	sess := Session{Token: token}
	err := s.db.QueryRowContext(ctx,
		`SELECT s.csrf, a.id, a.username, a.must_change FROM sessions s
		 JOIN admins a ON a.id = s.admin_id
		 WHERE s.token = ? AND s.expires_at > ?`, token, time.Now().UTC()).
		Scan(&sess.CSRF, &sess.Admin.ID, &sess.Admin.Username, &sess.Admin.MustChangePassword)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE sessions SET expires_at = ? WHERE token = ?`,
		time.Now().Add(SessionTTL).UTC(), token)
	return sess, err
}

// DeleteSession logs a session out.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, token)
	return err
}

// PurgeExpiredSessions removes sessions that are past their expiry.
func (s *Store) PurgeExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, time.Now().UTC())
	return err
}

// ChangeOwnPassword lets a logged-in admin set a new password. The current
// session stays valid; all other sessions of the admin are logged out.
func (s *Store) ChangeOwnPassword(ctx context.Context, sess Session, current, password string) error {
	if _, err := s.Authenticate(ctx, sess.Admin.Username, current); err != nil {
		return errors.New("huidig wachtwoord is onjuist")
	}
	if current == password {
		return errors.New("kies een ander wachtwoord dan het huidige")
	}
	if err := validateCredentials(sess.Admin.Username, password); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE admins SET password_hash = ?, must_change = 0 WHERE id = ?`,
			string(hash), sess.Admin.ID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`DELETE FROM sessions WHERE admin_id = ? AND token <> ?`, sess.Admin.ID, sess.Token)
		return err
	})
}

// GeneratePassword returns a random password that is easy to type over
// from a console (no ambiguous characters).
func GeneratePassword() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 16)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			panic(err)
		}
		b[i] = alphabet[n.Int64()]
	}
	return string(b)
}
