package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// UserSession is a user logged in to their account page on the portal.
type UserSession struct {
	Token string
	CSRF  string
	User  User
}

// UserSessionTTL is how long a user stays logged in to their account page
// without activity.
const UserSessionTTL = 30 * 24 * time.Hour

// PasswordResetTTL is how long a password reset link works.
const PasswordResetTTL = time.Hour

// ErrResetInvalid is returned for a password reset link that is unknown,
// used or expired.
var ErrResetInvalid = errors.New("this password reset link is no longer valid")

// LoginUser checks the login of a user's account page. Unlike
// AuthenticateUser it also lets disabled and expired users in, so they can
// see why their player stopped working, and it does not count as player
// activity.
func (s *Store) LoginUser(ctx context.Context, username, password string) (User, error) {
	var id int64
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT id, password_hash FROM users WHERE username = ?`,
		strings.TrimSpace(username)).Scan(&id, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return User{}, ErrInvalidLogin
	}
	if err != nil {
		return User{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return User{}, ErrInvalidLogin
	}
	return s.User(ctx, id)
}

// CreateUserSession logs a user in to their account page.
func (s *Store) CreateUserSession(ctx context.Context, u User) (UserSession, error) {
	sess := UserSession{Token: randomToken(), CSRF: randomToken(), User: u}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_sessions(token, user_id, csrf, expires_at) VALUES(?, ?, ?, ?)`,
		sess.Token, u.ID, sess.CSRF, time.Now().Add(UserSessionTTL).UTC())
	return sess, err
}

// UserSession looks up a valid account session and extends its lifetime.
func (s *Store) UserSession(ctx context.Context, token string) (UserSession, error) {
	sess := UserSession{Token: token}
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT csrf, user_id FROM user_sessions WHERE token = ? AND expires_at > ?`,
		token, time.Now().UTC()).Scan(&sess.CSRF, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return UserSession{}, ErrNotFound
	}
	if err != nil {
		return UserSession{}, err
	}
	if sess.User, err = s.User(ctx, id); err != nil {
		return UserSession{}, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE user_sessions SET expires_at = ? WHERE token = ?`,
		time.Now().Add(UserSessionTTL).UTC(), token)
	return sess, err
}

// DeleteUserSession logs an account session out.
func (s *Store) DeleteUserSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM user_sessions WHERE token = ?`, token)
	return err
}

// setUserPassword stores a new password for a user in tx, ends every
// account session except keep (may be empty) and removes open reset links.
func setUserPassword(ctx context.Context, tx *sql.Tx, id int64, hash []byte, keep string) error {
	res, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, string(hash), id)
	if err != nil {
		return err
	}
	if err := mustAffect(res); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_sessions WHERE user_id = ? AND token <> ?`, id, keep); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM password_resets WHERE user_id = ?`, id)
	return err
}

// ChangeUserPassword lets a user logged in to their account page set a new
// password. The current session stays valid; other sessions are logged out.
func (s *Store) ChangeUserPassword(ctx context.Context, sess UserSession, current, password string) error {
	if _, err := s.LoginUser(ctx, sess.User.Username, current); err != nil {
		return errors.New("current password is incorrect")
	}
	if current == password {
		return errors.New("choose a password different from the current one")
	}
	if err := ValidateUserCredentials(sess.User.Username, password); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error { return setUserPassword(ctx, tx, sess.User.ID, hash, sess.Token) })
	s.authCache.forget(sess.User.ID)
	return err
}

// SetUserEmail changes the email address of a user; empty removes it.
func (s *Store) SetUserEmail(ctx context.Context, id int64, email string) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE users SET email = ? WHERE id = ?`, email, id)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

// PasswordReset is a reset link made for a user.
type PasswordReset struct {
	User  User
	Token string // only known when the link is made; the database keeps a hash
}

func resetHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreatePasswordResets makes a reset link for every account with an email
// address whose username or email address is login. Earlier links of
// those accounts stop working. No accounts is not an error: the caller
// must not reveal which usernames or addresses exist.
func (s *Store) CreatePasswordResets(ctx context.Context, login string) ([]PasswordReset, error) {
	login = strings.TrimSpace(login)
	if login == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM users WHERE email <> '' AND (username = ? OR email = ? COLLATE NOCASE)`,
		login, login)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []PasswordReset
	for _, id := range ids {
		u, err := s.User(ctx, id)
		if err != nil {
			return nil, err
		}
		r := PasswordReset{User: u, Token: randomToken()}
		err = s.withTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `DELETE FROM password_resets WHERE user_id = ?`, id); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO password_resets(token_hash, user_id, expires_at) VALUES(?, ?, ?)`,
				resetHash(r.Token), id, time.Now().Add(PasswordResetTTL).UTC())
			return err
		})
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// PasswordResetUser returns the user a valid reset link belongs to.
func (s *Store) PasswordResetUser(ctx context.Context, token string) (User, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT user_id FROM password_resets WHERE token_hash = ? AND expires_at > ?`,
		resetHash(token), time.Now().UTC()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrResetInvalid
	}
	if err != nil {
		return User{}, err
	}
	return s.User(ctx, id)
}

// ResetUserPasswordWithToken sets a new password through a reset link. The
// link stops working and every account session of the user is logged out.
func (s *Store) ResetUserPasswordWithToken(ctx context.Context, token, password string) (User, error) {
	u, err := s.PasswordResetUser(ctx, token)
	if err != nil {
		return User{}, err
	}
	if err := ValidateUserCredentials(u.Username, password); err != nil {
		return User{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		// Checked again inside the transaction so a link can be used once.
		res, err := tx.ExecContext(ctx, `DELETE FROM password_resets WHERE token_hash = ? AND expires_at > ?`,
			resetHash(token), time.Now().UTC())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrResetInvalid
		}
		return setUserPassword(ctx, tx, u.ID, hash, "")
	})
	s.authCache.forget(u.ID)
	return u, err
}
