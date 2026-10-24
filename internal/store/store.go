// Package store holds all persistent state of the portal in a single SQLite
// database: settings, admin accounts, sessions, categories, channels,
// users, media servers and stream health.
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
