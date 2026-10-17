package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/rhisijmuiden/iptv-portal/internal/mail"
)

// Package is a set of channels that can be given to users: either every
// channel, or the channels of the selected categories. OnDemand adds the
// movies and series of the media servers.
type Package struct {
	ID          int64
	Name        string
	AllChannels bool
	CategoryIDs []int64
	OnDemand    bool
	UserCount   int
}

// User is an account an IPTV player logs in with.
type User struct {
	ID         int64
	Username   string
	Note       string
	Email      string // optional; where login details are mailed to
	Enabled    bool
	ExpiresAt  *time.Time // nil = never
	CreatedAt  time.Time
	LastSeenAt *time.Time
	PackageIDs []int64
	Packages   []string // package names, filled by queries
}

// Expired reports whether the account is past its expiry date.
func (u User) Expired() bool {
	return u.ExpiresAt != nil && !u.ExpiresAt.After(time.Now())
}

// Active reports whether the user may use the portal.
func (u User) Active() bool { return u.Enabled && !u.Expired() }

// ---- packages ----

// Packages returns all packages with their categories and user counts.
func (s *Store) Packages(ctx context.Context) ([]Package, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT p.id, p.name, p.all_channels, p.on_demand,
		 (SELECT COUNT(*) FROM user_packages up WHERE up.package_id = p.id)
		 FROM packages p ORDER BY p.name`)
	if err != nil {
		return nil, err
	}
	var out []Package
	for rows.Next() {
		var p Package
		if err := rows.Scan(&p.ID, &p.Name, &p.AllChannels, &p.OnDemand, &p.UserCount); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].CategoryIDs, err = s.ids(ctx,
			`SELECT category_id FROM package_categories WHERE package_id = ?`, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Package returns one package.
func (s *Store) Package(ctx context.Context, id int64) (Package, error) {
	var p Package
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, all_channels, on_demand FROM packages WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &p.AllChannels, &p.OnDemand)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.CategoryIDs, err = s.ids(ctx, `SELECT category_id FROM package_categories WHERE package_id = ?`, id)
	return p, err
}

// SavePackage creates (ID 0) or updates a package.
func (s *Store) SavePackage(ctx context.Context, p Package) (int64, error) {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return 0, errors.New("name must not be empty")
	}
	if !p.AllChannels && len(p.CategoryIDs) == 0 && !p.OnDemand {
		return 0, errors.New("select at least one category, all channels or On Demand")
	}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if p.ID == 0 {
			res, err := tx.ExecContext(ctx, `INSERT INTO packages(name, all_channels, on_demand) VALUES(?, ?, ?)`,
				p.Name, p.AllChannels, p.OnDemand)
			if err != nil {
				return uniqueErr(err, "package already exists")
			}
			p.ID, _ = res.LastInsertId()
		} else {
			res, err := tx.ExecContext(ctx, `UPDATE packages SET name = ?, all_channels = ?, on_demand = ? WHERE id = ?`,
				p.Name, p.AllChannels, p.OnDemand, p.ID)
			if err != nil {
				return uniqueErr(err, "package already exists")
			}
			if err := mustAffect(res); err != nil {
				return err
			}
		}
		return replaceLinks(ctx, tx, "package_categories", "package_id", "category_id", p.ID, p.CategoryIDs)
	})
	return p.ID, err
}

// DeletePackage removes a package; users keep their other packages.
func (s *Store) DeletePackage(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM packages WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

// replaceLinks sets the rows of a many-to-many table for one owner.
// Table and column names are constants from this package, never input.
func replaceLinks(ctx context.Context, tx *sql.Tx, table, ownerCol, otherCol string, owner int64, ids []int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE `+ownerCol+` = ?`, owner); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO `+table+`(`+ownerCol+`, `+otherCol+`) VALUES(?, ?)`, owner, id); err != nil {
			if strings.Contains(err.Error(), "FOREIGN KEY") {
				return errors.New("selected item no longer exists, reload the page")
			}
			return err
		}
	}
	return nil
}

func (s *Store) ids(ctx context.Context, q string, args ...any) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ---- users ----

var usernameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{3,32}$`)

// ValidateUserCredentials checks a player username and password. Both end
// up in stream URLs (/live/user/pass/1.m3u8), so characters that have a
// meaning in URLs are not allowed.
func ValidateUserCredentials(username, password string) error {
	if !usernameRE.MatchString(username) {
		return errors.New("username must be 3-32 characters: letters, digits, dot, dash or underscore")
	}
	if len(password) < 6 || len(password) > 64 {
		return errors.New("password must be 6-64 characters")
	}
	if strings.ContainsAny(password, "/?#%&\\ \t\"'") {
		return errors.New(`password must not contain spaces or any of / ? # % & \ " '`)
	}
	return nil
}

// normalizeEmail trims an optional email address and checks it.
func normalizeEmail(s string) (string, error) {
	if strings.TrimSpace(s) == "" {
		return "", nil
	}
	a, err := mail.ParseAddress(s)
	if err != nil {
		return "", errors.New("email address is not valid")
	}
	return a, nil
}

const userSelect = `SELECT u.id, u.username, u.note, u.email, u.enabled, u.expires_at, u.created_at, u.last_seen_at
	FROM users u`

func scanUser(sc interface{ Scan(...any) error }) (User, error) {
	var u User
	var exp, seen sql.NullTime
	err := sc.Scan(&u.ID, &u.Username, &u.Note, &u.Email, &u.Enabled, &exp, &u.CreatedAt, &seen)
	if exp.Valid {
		t := exp.Time
		u.ExpiresAt = &t
	}
	if seen.Valid {
		t := seen.Time
		u.LastSeenAt = &t
	}
	return u, err
}

func (s *Store) fillUserPackages(ctx context.Context, u *User) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT p.id, p.name FROM user_packages up JOIN packages p ON p.id = up.package_id
		 WHERE up.user_id = ? ORDER BY p.name`, u.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	u.PackageIDs, u.Packages = nil, nil
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		u.PackageIDs = append(u.PackageIDs, id)
		u.Packages = append(u.Packages, name)
	}
	return rows.Err()
}

// Users lists all users, optionally filtered by a search on username/note.
func (s *Store) Users(ctx context.Context, search string) ([]User, error) {
	q := userSelect
	var args []any
	if search = strings.TrimSpace(search); search != "" {
		like := "%" + escapeLike(search) + "%"
		q += ` WHERE u.username LIKE ? ESCAPE '\' OR u.note LIKE ? ESCAPE '\' OR u.email LIKE ? ESCAPE '\'`
		args = append(args, like, like, like)
	}
	q += ` ORDER BY u.username`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := s.fillUserPackages(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// User returns one user.
func (s *Store) User(ctx context.Context, id int64) (User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, userSelect+` WHERE u.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	if err != nil {
		return u, err
	}
	return u, s.fillUserPackages(ctx, &u)
}

// SaveUser creates (ID 0) or updates a user. The password is only changed
// when it is not empty; it is required for a new user.
func (s *Store) SaveUser(ctx context.Context, u User, password string) (int64, error) {
	u.Username = strings.TrimSpace(u.Username)
	u.Note = strings.TrimSpace(u.Note)
	check := password
	if u.ID != 0 && password == "" {
		check = "unchanged" // only validate the username
	}
	if err := ValidateUserCredentials(u.Username, check); err != nil {
		return 0, err
	}
	var err error
	if u.Email, err = normalizeEmail(u.Email); err != nil {
		return 0, err
	}
	var hash []byte
	if password != "" {
		if hash, err = bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost); err != nil {
			return 0, err
		}
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if u.ID == 0 {
			res, err := tx.ExecContext(ctx,
				`INSERT INTO users(username, password_hash, note, email, enabled, expires_at) VALUES(?, ?, ?, ?, ?, ?)`,
				u.Username, string(hash), u.Note, u.Email, u.Enabled, utcPtr(u.ExpiresAt))
			if err != nil {
				return uniqueErr(err, "username already exists")
			}
			u.ID, _ = res.LastInsertId()
		} else {
			res, err := tx.ExecContext(ctx,
				`UPDATE users SET username = ?, note = ?, email = ?, enabled = ?, expires_at = ? WHERE id = ?`,
				u.Username, u.Note, u.Email, u.Enabled, utcPtr(u.ExpiresAt), u.ID)
			if err != nil {
				return uniqueErr(err, "username already exists")
			}
			if err := mustAffect(res); err != nil {
				return err
			}
			if hash != nil {
				if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, string(hash), u.ID); err != nil {
					return err
				}
			}
		}
		return replaceLinks(ctx, tx, "user_packages", "user_id", "package_id", u.ID, u.PackageIDs)
	})
	if err == nil {
		s.authCache.forget(u.ID)
	}
	return u.ID, err
}

// SetUserEnabled switches a user on or off.
func (s *Store) SetUserEnabled(ctx context.Context, id int64, enabled bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET enabled = ? WHERE id = ?`, enabled, id)
	if err != nil {
		return err
	}
	s.authCache.forget(id)
	return mustAffect(res)
}

// DeleteUser removes a user.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	s.authCache.forget(id)
	return mustAffect(res)
}

func utcPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}

// ErrUserInactive is returned when a user logs in correctly but the account
// is disabled or expired.
var ErrUserInactive = errors.New("account is disabled or expired")

// AuthenticateUser checks player credentials. IPTV players send them on
// every request, so successful checks are cached for a few minutes to avoid
// running bcrypt on each channel switch.
func (s *Store) AuthenticateUser(ctx context.Context, username, password string) (User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, userSelect+` WHERE u.username = ?`, username))
	if errors.Is(err, sql.ErrNoRows) {
		bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return User{}, ErrInvalidLogin
	}
	if err != nil {
		return User{}, err
	}
	if !s.authCache.ok(u.ID, password) {
		var hash string
		if err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = ?`, u.ID).Scan(&hash); err != nil {
			return User{}, err
		}
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
			return User{}, ErrInvalidLogin
		}
		s.authCache.remember(u.ID, password)
	}
	if !u.Active() {
		return u, ErrUserInactive
	}
	if err := s.fillUserPackages(ctx, &u); err != nil {
		return User{}, err
	}
	// Record activity at most once a minute per user.
	if u.LastSeenAt == nil || time.Since(*u.LastSeenAt) > time.Minute {
		s.db.ExecContext(ctx, `UPDATE users SET last_seen_at = ? WHERE id = ?`, time.Now().UTC(), u.ID)
	}
	return u, nil
}

// UserChannels returns the enabled channels a user can watch.
func (s *Store) UserChannels(ctx context.Context, userID int64) ([]Channel, error) {
	q := channelSelect + ` WHERE ch.enabled = 1 AND (
		EXISTS (SELECT 1 FROM user_packages up JOIN packages p ON p.id = up.package_id
		        WHERE up.user_id = ? AND p.all_channels = 1)
		OR ch.category_id IN (SELECT pc.category_id FROM user_packages up
		        JOIN package_categories pc ON pc.package_id = up.package_id WHERE up.user_id = ?))
		ORDER BY COALESCE(c.sort_order, 1e9), c.name, ch.sort_order, ch.name`
	rows, err := s.db.QueryContext(ctx, q, userID, userID)
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

// UserOnDemand reports whether one of the user's packages includes On
// Demand.
func (s *Store) UserOnDemand(ctx context.Context, userID int64) (bool, error) {
	var ok bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM user_packages up JOIN packages p ON p.id = up.package_id
		 WHERE up.user_id = ? AND p.on_demand = 1)`, userID).Scan(&ok)
	return ok, err
}

// authCache remembers recently verified (user, password) pairs.
type authCache struct {
	mu      sync.Mutex
	entries map[int64]authEntry
}

type authEntry struct {
	sum     [32]byte
	expires time.Time
}

const authCacheTTL = 5 * time.Minute

func (c *authCache) ok(id int64, password string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, found := c.entries[id]
	return found && time.Now().Before(e.expires) && e.sum == sha256.Sum256([]byte(password))
}

func (c *authCache) remember(id int64, password string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[int64]authEntry{}
	}
	now := time.Now()
	for k, e := range c.entries {
		if now.After(e.expires) {
			delete(c.entries, k)
		}
	}
	c.entries[id] = authEntry{sum: sha256.Sum256([]byte(password)), expires: now.Add(authCacheTTL)}
}

func (c *authCache) forget(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, id)
}

// ---- invites ----

// Invite is a link that lets someone create their own player account.
type Invite struct {
	ID         int64
	Token      string
	Note       string
	MaxUses    int // 0 = unlimited
	Uses       int
	ValidDays  int        // account validity after sign-up; 0 = no expiry
	ExpiresAt  *time.Time // when the link stops working; nil = never
	Revoked    bool
	CreatedAt  time.Time
	PackageIDs []int64
	Packages   []string
}

// Usable reports whether the invite can still be redeemed.
func (i Invite) Usable() bool {
	return !i.Revoked && (i.MaxUses == 0 || i.Uses < i.MaxUses) &&
		(i.ExpiresAt == nil || i.ExpiresAt.After(time.Now()))
}

// Status is a short description of the invite state for the admin panel.
func (i Invite) Status() string {
	switch {
	case i.Revoked:
		return "revoked"
	case i.MaxUses != 0 && i.Uses >= i.MaxUses:
		return "used"
	case i.ExpiresAt != nil && !i.ExpiresAt.After(time.Now()):
		return "expired"
	}
	return "open"
}

const inviteSelect = `SELECT id, token, note, max_uses, uses, valid_days, expires_at, revoked, created_at FROM invites`

func scanInvite(sc interface{ Scan(...any) error }) (Invite, error) {
	var i Invite
	var exp sql.NullTime
	err := sc.Scan(&i.ID, &i.Token, &i.Note, &i.MaxUses, &i.Uses, &i.ValidDays, &exp, &i.Revoked, &i.CreatedAt)
	if exp.Valid {
		t := exp.Time
		i.ExpiresAt = &t
	}
	return i, err
}

func (s *Store) fillInvitePackages(ctx context.Context, i *Invite) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT p.id, p.name FROM invite_packages ip JOIN packages p ON p.id = ip.package_id
		 WHERE ip.invite_id = ? ORDER BY p.name`, i.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		i.PackageIDs = append(i.PackageIDs, id)
		i.Packages = append(i.Packages, name)
	}
	return rows.Err()
}

// Invites lists all invites, newest first.
func (s *Store) Invites(ctx context.Context) ([]Invite, error) {
	rows, err := s.db.QueryContext(ctx, inviteSelect+` ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	var out []Invite
	for rows.Next() {
		i, err := scanInvite(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, i)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for k := range out {
		if err := s.fillInvitePackages(ctx, &out[k]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Invite returns one invite.
func (s *Store) Invite(ctx context.Context, id int64) (Invite, error) {
	i, err := scanInvite(s.db.QueryRowContext(ctx, inviteSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return i, ErrNotFound
	}
	if err != nil {
		return i, err
	}
	return i, s.fillInvitePackages(ctx, &i)
}

// InviteByToken returns the invite for a link token.
func (s *Store) InviteByToken(ctx context.Context, token string) (Invite, error) {
	i, err := scanInvite(s.db.QueryRowContext(ctx, inviteSelect+` WHERE token = ?`, token))
	if errors.Is(err, sql.ErrNoRows) {
		return i, ErrNotFound
	}
	if err != nil {
		return i, err
	}
	return i, s.fillInvitePackages(ctx, &i)
}

// CreateInvite stores a new invite with a random token.
func (s *Store) CreateInvite(ctx context.Context, i Invite) (Invite, error) {
	i.Note = strings.TrimSpace(i.Note)
	if i.MaxUses < 0 || i.ValidDays < 0 {
		return i, errors.New("numbers must not be negative")
	}
	if len(i.PackageIDs) == 0 {
		return i, errors.New("select at least one package")
	}
	i.Token = randomToken()[:32]
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO invites(token, note, max_uses, valid_days, expires_at) VALUES(?, ?, ?, ?, ?)`,
			i.Token, i.Note, i.MaxUses, i.ValidDays, utcPtr(i.ExpiresAt))
		if err != nil {
			return err
		}
		i.ID, _ = res.LastInsertId()
		return replaceLinks(ctx, tx, "invite_packages", "invite_id", "package_id", i.ID, i.PackageIDs)
	})
	return i, err
}

// RevokeInvite stops an invite link from working.
func (s *Store) RevokeInvite(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE invites SET revoked = 1 WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

// DeleteInvite removes an invite; accounts created with it are kept.
func (s *Store) DeleteInvite(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM invites WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

// ErrInviteUnusable is returned for a revoked, used up or expired invite.
var ErrInviteUnusable = errors.New("this invitation is no longer valid")

// RedeemInvite creates a user account from an invite.
// The email address is optional.
func (s *Store) RedeemInvite(ctx context.Context, token, username, password, email string) (User, error) {
	username = strings.TrimSpace(username)
	if err := ValidateUserCredentials(username, password); err != nil {
		return User{}, err
	}
	email, err := normalizeEmail(email)
	if err != nil {
		return User{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	var u User
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		inv, err := scanInvite(tx.QueryRowContext(ctx, inviteSelect+` WHERE token = ?`, token))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInviteUnusable
		}
		if err != nil {
			return err
		}
		if !inv.Usable() {
			return ErrInviteUnusable
		}
		var exp *time.Time
		if inv.ValidDays > 0 {
			t := time.Now().AddDate(0, 0, inv.ValidDays).UTC()
			exp = &t
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO users(username, password_hash, enabled, expires_at, invite_id, note, email)
			 VALUES(?, ?, 1, ?, ?, ?, ?)`, username, string(hash), utcPtr(exp), inv.ID, inv.Note, email)
		if err != nil {
			return uniqueErr(err, "this username is already taken")
		}
		u.ID, _ = res.LastInsertId()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO user_packages(user_id, package_id)
			 SELECT ?, package_id FROM invite_packages WHERE invite_id = ?`, u.ID, inv.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE invites SET uses = uses + 1 WHERE id = ?`, inv.ID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return User{}, err
	}
	return s.User(ctx, u.ID)
}

// CountActiveUsers returns the number of enabled, unexpired users.
func (s *Store) CountActiveUsers(ctx context.Context) (active, total int, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(enabled = 1 AND (expires_at IS NULL OR expires_at > ?)), 0) FROM users`,
		time.Now().UTC()).Scan(&total, &active)
	if err != nil {
		return 0, 0, fmt.Errorf("counting users: %w", err)
	}
	return active, total, nil
}

// ResetUserPassword gives a user a new random password and returns it.
func (s *Store) ResetUserPassword(ctx context.Context, id int64) (string, error) {
	password := GeneratePassword()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, string(hash), id)
	if err != nil {
		return "", err
	}
	s.authCache.forget(id)
	return password, mustAffect(res)
}
