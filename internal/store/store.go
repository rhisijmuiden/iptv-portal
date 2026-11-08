// Package store holds all persistent state of the portal in a single SQLite
// database: settings, admin accounts, sessions, categories, channels,
// users and their account sessions, media servers, stream health and
// viewing statistics.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps the SQLite database.
type Store struct {
	db        *sql.DB
	authCache authCache
	dir       string
}

// Open opens (and creates when needed) the database in dataDir and applies
// any pending migrations.
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, fmt.Errorf("creating data directory: %w", err)
	}
	if err := applyPendingRestore(dataDir); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.Join(dataDir, dbFile) +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_time_format=sqlite"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite handles one writer at a time; a single connection avoids
	// "database is locked" errors and is plenty for this workload.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, dir: dataDir}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// migrations are applied in order; the index+1 is the schema version.
var migrations = []string{
	`CREATE TABLE settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);
	CREATE TABLE admins (
		id            INTEGER PRIMARY KEY,
		username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
		password_hash TEXT NOT NULL,
		must_change   INTEGER NOT NULL DEFAULT 0,
		created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE sessions (
		token      TEXT PRIMARY KEY,
		admin_id   INTEGER NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
		csrf       TEXT NOT NULL,
		expires_at DATETIME NOT NULL
	);
	CREATE TABLE categories (
		id         INTEGER PRIMARY KEY,
		name       TEXT NOT NULL UNIQUE COLLATE NOCASE,
		sort_order INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE channels (
		id          INTEGER PRIMARY KEY,
		name        TEXT NOT NULL,
		stream_url  TEXT NOT NULL,
		logo_url    TEXT NOT NULL DEFAULT '',
		epg_id      TEXT NOT NULL DEFAULT '',
		category_id INTEGER REFERENCES categories(id) ON DELETE SET NULL,
		sort_order  INTEGER NOT NULL DEFAULT 0,
		enabled     INTEGER NOT NULL DEFAULT 1,
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX channels_category ON channels(category_id);`,

	`CREATE TABLE packages (
		id           INTEGER PRIMARY KEY,
		name         TEXT NOT NULL UNIQUE COLLATE NOCASE,
		all_channels INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE package_categories (
		package_id  INTEGER NOT NULL REFERENCES packages(id) ON DELETE CASCADE,
		category_id INTEGER NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
		PRIMARY KEY (package_id, category_id)
	);
	CREATE TABLE users (
		id            INTEGER PRIMARY KEY,
		username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
		password_hash TEXT NOT NULL,
		note          TEXT NOT NULL DEFAULT '',
		enabled       INTEGER NOT NULL DEFAULT 1,
		expires_at    DATETIME,
		invite_id     INTEGER REFERENCES invites(id) ON DELETE SET NULL,
		created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_seen_at  DATETIME
	);
	CREATE TABLE user_packages (
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		package_id INTEGER NOT NULL REFERENCES packages(id) ON DELETE CASCADE,
		PRIMARY KEY (user_id, package_id)
	);
	CREATE TABLE invites (
		id          INTEGER PRIMARY KEY,
		token       TEXT NOT NULL UNIQUE,
		note        TEXT NOT NULL DEFAULT '',
		max_uses    INTEGER NOT NULL DEFAULT 1,
		uses        INTEGER NOT NULL DEFAULT 0,
		valid_days  INTEGER NOT NULL DEFAULT 0,
		expires_at  DATETIME,
		revoked     INTEGER NOT NULL DEFAULT 0,
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE invite_packages (
		invite_id  INTEGER NOT NULL REFERENCES invites(id) ON DELETE CASCADE,
		package_id INTEGER NOT NULL REFERENCES packages(id) ON DELETE CASCADE,
		PRIMARY KEY (invite_id, package_id)
	);`,

	`ALTER TABLE users ADD COLUMN email TEXT NOT NULL DEFAULT '';`,

	`ALTER TABLE packages ADD COLUMN on_demand INTEGER NOT NULL DEFAULT 0;
	CREATE TABLE media_servers (
		id         INTEGER PRIMARY KEY,
		name       TEXT NOT NULL UNIQUE COLLATE NOCASE,
		kind       TEXT NOT NULL,
		url        TEXT NOT NULL,
		token      TEXT NOT NULL,
		libraries  TEXT NOT NULL DEFAULT '',
		direct     INTEGER NOT NULL DEFAULT 0,
		enabled    INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE media_ids (
		id        INTEGER PRIMARY KEY,
		server_id INTEGER NOT NULL REFERENCES media_servers(id) ON DELETE CASCADE,
		key       TEXT NOT NULL,
		UNIQUE (server_id, key)
	);`,

	`ALTER TABLE users ADD COLUMN max_connections INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE packages ADD COLUMN max_connections INTEGER NOT NULL DEFAULT 0;`,

	`CREATE TABLE channel_health (
		channel_id    INTEGER PRIMARY KEY REFERENCES channels(id) ON DELETE CASCADE,
		ok            INTEGER NOT NULL,
		message       TEXT NOT NULL DEFAULT '',
		failures      INTEGER NOT NULL DEFAULT 0,
		checked_at    DATETIME NOT NULL,
		last_ok_at    DATETIME,
		auto_disabled INTEGER NOT NULL DEFAULT 0
	);`,

	`ALTER TABLE channels ADD COLUMN kind TEXT NOT NULL DEFAULT 'tv';
	ALTER TABLE channels ADD COLUMN source_id TEXT NOT NULL DEFAULT '';
	ALTER TABLE categories ADD COLUMN kind TEXT NOT NULL DEFAULT 'tv';
	CREATE INDEX channels_source ON channels(source_id) WHERE source_id <> '';`,

	// Times are Unix seconds: the statistics add up and group durations.
	`CREATE TABLE views (
		id         INTEGER PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		username   TEXT NOT NULL,
		channel_id INTEGER REFERENCES channels(id) ON DELETE SET NULL,
		channel    TEXT NOT NULL,
		kind       TEXT NOT NULL,
		ip         TEXT NOT NULL DEFAULT '',
		user_agent TEXT NOT NULL DEFAULT '',
		started_at INTEGER NOT NULL,
		ended_at   INTEGER NOT NULL
	);
	CREATE INDEX views_started ON views(started_at);
	CREATE INDEX views_channel ON views(channel_id, started_at);
	CREATE INDEX views_user ON views(user_id, started_at);`,

	// Replacement streams found for failing channels. Dismissed ones are
	// kept so they are not suggested again.
	`CREATE TABLE stream_suggestions (
		id         INTEGER PRIMARY KEY,
		channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
		stream_url TEXT NOT NULL,
		name       TEXT NOT NULL DEFAULT '',
		source     TEXT NOT NULL,
		source_id  TEXT NOT NULL DEFAULT '',
		logo_url   TEXT NOT NULL DEFAULT '',
		epg_id     TEXT NOT NULL DEFAULT '',
		match      TEXT NOT NULL DEFAULT '',
		detail     TEXT NOT NULL DEFAULT '',
		found_at   DATETIME NOT NULL,
		dismissed  INTEGER NOT NULL DEFAULT 0,
		UNIQUE(channel_id, stream_url)
	);
	ALTER TABLE channel_health ADD COLUMN searched_at DATETIME;`,

	`CREATE TABLE user_sessions (
		token      TEXT PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		csrf       TEXT NOT NULL,
		expires_at DATETIME NOT NULL
	);
	CREATE INDEX user_sessions_user ON user_sessions(user_id);
	CREATE TABLE password_resets (
		token_hash TEXT PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		expires_at DATETIME NOT NULL
	);
	CREATE INDEX password_resets_user ON password_resets(user_id);`,

	// On Demand is grouped by genre unless a server says otherwise.
	`ALTER TABLE media_servers ADD COLUMN categories TEXT NOT NULL DEFAULT 'genres';`,

	// Setup guides, devices users register, and package requests from the
	// account pages. The default guides are added by seedGuides.
	`CREATE TABLE guides (
		id          INTEGER PRIMARY KEY,
		builtin     TEXT NOT NULL DEFAULT '',
		icon        TEXT NOT NULL DEFAULT 'tv',
		title       TEXT NOT NULL,
		body        TEXT NOT NULL,
		title_nl    TEXT NOT NULL DEFAULT '',
		body_nl     TEXT NOT NULL DEFAULT '',
		sort_order  INTEGER NOT NULL DEFAULT 0,
		enabled     INTEGER NOT NULL DEFAULT 1
	);
	CREATE TABLE user_devices (
		id         INTEGER PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		name       TEXT NOT NULL,
		mac        TEXT NOT NULL,
		created_at DATETIME NOT NULL,
		UNIQUE (user_id, mac)
	);
	CREATE TABLE package_requests (
		id         INTEGER PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		package_id INTEGER NOT NULL REFERENCES packages(id) ON DELETE CASCADE,
		message    TEXT NOT NULL DEFAULT '',
		status     TEXT NOT NULL DEFAULT 'pending',
		reply      TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL,
		decided_at DATETIME
	);
	CREATE INDEX package_requests_user ON package_requests(user_id);
	ALTER TABLE packages ADD COLUMN description TEXT NOT NULL DEFAULT '';
	ALTER TABLE packages ADD COLUMN price TEXT NOT NULL DEFAULT '';
	ALTER TABLE packages ADD COLUMN public INTEGER NOT NULL DEFAULT 1;`,

	// Alerts to admins and reminders to users: what was already sent, so
	// nothing is sent twice, and a log of the alerts.
	`CREATE TABLE alerts_sent (
		key     TEXT PRIMARY KEY,
		sent_at DATETIME NOT NULL
	);
	CREATE TABLE alert_log (
		id       INTEGER PRIMARY KEY,
		at       DATETIME NOT NULL,
		kind     TEXT NOT NULL,
		title    TEXT NOT NULL,
		detail   TEXT NOT NULL DEFAULT '',
		channels TEXT NOT NULL DEFAULT '',
		error    TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX alert_log_at ON alert_log(at);`,

	// What users set for their own player on the account pages:
	// favourites, their own order of the channel groups, hidden groups and
	// parental controls.
	`CREATE TABLE user_preferences (
		user_id          INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
		favourites_group INTEGER NOT NULL DEFAULT 1,
		pin_hash         TEXT NOT NULL DEFAULT '',
		max_age          INTEGER NOT NULL DEFAULT 0,
		unlocked_until   DATETIME
	);
	CREATE TABLE user_favourites (
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
		position   INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (user_id, channel_id)
	);
	CREATE TABLE user_categories (
		user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		category_id INTEGER NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
		position    INTEGER NOT NULL DEFAULT 0,
		hidden      INTEGER NOT NULL DEFAULT 0,
		locked      INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (user_id, category_id)
	);
	CREATE TABLE user_locked_media (
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		kind    TEXT NOT NULL,
		name    TEXT NOT NULL COLLATE NOCASE,
		PRIMARY KEY (user_id, kind, name)
	);
	ALTER TABLE user_sessions ADD COLUMN pin_until DATETIME;`,
}

// afterMigration runs Go code in the transaction of a migration, keyed by
// schema version.
var afterMigration = map[int]func(context.Context, *sql.Tx) error{
	12: seedGuides,
}

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if fn := afterMigration[i+1]; fn != nil {
			if err := fn(ctx, tx); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration %d: %w", i+1, err)
			}
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
