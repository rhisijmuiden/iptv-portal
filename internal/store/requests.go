package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"
)

// Status of a package request.
const (
	RequestPending  = "pending"
	RequestApproved = "approved"
	RequestRejected = "rejected"
	RequestCanceled = "canceled"
)

// maxPendingRequests is how many open package requests a user can have.
const maxPendingRequests = 5

// PackageRequest is a user asking, on their account page, for a package.
// The admin approves or rejects it.
type PackageRequest struct {
	ID        int64
	UserID    int64
	Username  string
	Email     string
	PackageID int64
	Package   string
	Message   string // from the user
	Status    string
	Reply     string // from the admin
	CreatedAt time.Time
	DecidedAt *time.Time
	// Current are the user's packages when the request was read.
	Current []string
}

// Pending reports whether the admin has not decided yet.
func (r PackageRequest) Pending() bool { return r.Status == RequestPending }

const requestSelect = `SELECT r.id, r.user_id, u.username, u.email, r.package_id, p.name, r.message, r.status, r.reply,
	r.created_at, r.decided_at FROM package_requests r JOIN users u ON u.id = r.user_id JOIN packages p ON p.id = r.package_id`

func scanRequest(sc interface{ Scan(...any) error }) (PackageRequest, error) {
	var r PackageRequest
	var decided sql.NullTime
	err := sc.Scan(&r.ID, &r.UserID, &r.Username, &r.Email, &r.PackageID, &r.Package, &r.Message, &r.Status, &r.Reply,
		&r.CreatedAt, &decided)
	if decided.Valid {
		t := decided.Time
		r.DecidedAt = &t
	}
	return r, err
}

func (s *Store) requests(ctx context.Context, where string, args ...any) ([]PackageRequest, error) {
	rows, err := s.db.QueryContext(ctx, requestSelect+where, args...)
	if err != nil {
		return nil, err
	}
	var out []PackageRequest
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	rows.Close()
	return out, rows.Err()
}

// RequestPackage stores a user's request for a package. Only packages
// shown on the account pages can be requested, and not one the user
// already has or asked for.
func (s *Store) RequestPackage(ctx context.Context, userID, packageID int64, message string) (PackageRequest, error) {
	message = strings.TrimSpace(message)
	if len([]rune(message)) > 500 {
		return PackageRequest{}, errors.New("the message can be at most 500 characters")
	}
	p, err := s.Package(ctx, packageID)
	if errors.Is(err, ErrNotFound) || (err == nil && !p.Public) {
		return PackageRequest{}, errors.New("this package cannot be requested")
	}
	if err != nil {
		return PackageRequest{}, err
	}
	u, err := s.User(ctx, userID)
	if err != nil {
		return PackageRequest{}, err
	}
	if slices.Contains(u.PackageIDs, packageID) {
		return PackageRequest{}, errors.New("you already have this package")
	}
	var id int64
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		var same, open int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FILTER (WHERE package_id = ?), COUNT(*)
			FROM package_requests WHERE user_id = ? AND status = ?`, packageID, userID, RequestPending).Scan(&same, &open); err != nil {
			return err
		}
		if same > 0 {
			return errors.New("you already asked for this package")
		}
		if open >= maxPendingRequests {
			return errors.New("you have too many open requests; wait for an answer first")
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO package_requests(user_id, package_id, message, status, created_at)
			VALUES(?, ?, ?, ?, ?)`, userID, packageID, message, RequestPending, time.Now().UTC())
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		return PackageRequest{}, err
	}
	return s.PackageRequest(ctx, id)
}

// PackageRequest returns one request.
func (s *Store) PackageRequest(ctx context.Context, id int64) (PackageRequest, error) {
	r, err := scanRequest(s.db.QueryRowContext(ctx, requestSelect+` WHERE r.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// UserPackageRequests returns a user's requests, newest first.
func (s *Store) UserPackageRequests(ctx context.Context, userID int64, limit int) ([]PackageRequest, error) {
	return s.requests(ctx, ` WHERE r.user_id = ? ORDER BY r.created_at DESC, r.id DESC LIMIT ?`, userID, limit)
}

// PackageRequests returns the open requests, oldest first, followed by up
// to limit decided ones, newest first. The users' current packages are
// filled in.
func (s *Store) PackageRequests(ctx context.Context, limit int) (open, decided []PackageRequest, err error) {
	if open, err = s.requests(ctx, ` WHERE r.status = ? ORDER BY r.created_at, r.id`, RequestPending); err != nil {
		return nil, nil, err
	}
	for i := range open {
		u, err := s.User(ctx, open[i].UserID)
		if err != nil {
			return nil, nil, err
		}
		open[i].Current = u.Packages
	}
	decided, err = s.requests(ctx, ` WHERE r.status <> ? ORDER BY COALESCE(r.decided_at, r.created_at) DESC, r.id DESC LIMIT ?`,
		RequestPending, limit)
	return open, decided, err
}

// CountPendingRequests returns how many requests wait for the admin.
func (s *Store) CountPendingRequests(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM package_requests WHERE status = ?`, RequestPending).Scan(&n)
	return n, err
}

// CancelPackageRequest withdraws an open request of a user.
func (s *Store) CancelPackageRequest(ctx context.Context, userID, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE package_requests SET status = ?, decided_at = ?
		WHERE id = ? AND user_id = ? AND status = ?`, RequestCanceled, time.Now().UTC(), id, userID, RequestPending)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

// DecidePackageRequest approves or rejects an open request. Approving
// gives the user the package; with replace it takes the place of the
// user's other packages.
func (s *Store) DecidePackageRequest(ctx context.Context, id int64, approve, replace bool, reply string) (PackageRequest, error) {
	reply = strings.TrimSpace(reply)
	if len([]rune(reply)) > 500 {
		return PackageRequest{}, errors.New("the reply can be at most 500 characters")
	}
	r, err := s.PackageRequest(ctx, id)
	if err != nil {
		return r, err
	}
	if !r.Pending() {
		return r, errors.New("this request has already been answered")
	}
	status := RequestRejected
	if approve {
		status = RequestApproved
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE package_requests SET status = ?, reply = ?, decided_at = ? WHERE id = ? AND status = ?`,
			status, reply, time.Now().UTC(), id, RequestPending)
		if err != nil {
			return err
		}
		if err := mustAffect(res); err != nil {
			return errors.New("this request has already been answered")
		}
		if !approve {
			return nil
		}
		if replace {
			if _, err := tx.ExecContext(ctx, `DELETE FROM user_packages WHERE user_id = ?`, r.UserID); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO user_packages(user_id, package_id) VALUES(?, ?)`, r.UserID, r.PackageID)
		return err
	})
	if err != nil {
		return r, err
	}
	s.authCache.forget(r.UserID)
	return s.PackageRequest(ctx, id)
}

// PackageChannels returns the enabled channels a package gives, in the
// order players show them.
func (s *Store) PackageChannels(ctx context.Context, p Package) ([]Channel, error) {
	q := channelSelect + ` WHERE ch.enabled = 1`
	var args []any
	if !p.AllChannels {
		q += ` AND ch.category_id IN (SELECT category_id FROM package_categories WHERE package_id = ?)`
		args = append(args, p.ID)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY COALESCE(c.sort_order, 1e9), c.name, ch.sort_order, ch.name`, args...)
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
