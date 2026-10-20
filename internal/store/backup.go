package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const dbFile = "portal.db"

// PendingRestore is the file name, in the data directory, of a database
// that waits to replace the current one. Open moves it into place.
const PendingRestore = dbFile + ".restore"

// BeforeRestore is the file name the database is kept under when a restore
// replaces it.
const BeforeRestore = dbFile + ".before-restore"

// SchemaVersion is the database schema version this build creates.
func SchemaVersion() int { return len(migrations) }

// Dir is the data directory the database lives in.
func (s *Store) Dir() string { return s.dir }

// Snapshot writes a consistent copy of the database file to w. The copy is
// byte for byte the file on disk, so unchanged pages stay identical between
// snapshots, which keeps incremental backups small.
func (s *Store) Snapshot(ctx context.Context, w io.Writer) error {
	// The pool holds a single connection: while it is taken here, nothing
	// else in this process reads or writes the database.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	path := filepath.Join(s.dir, dbFile)
	for attempt := 0; attempt < 10; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
		// Move everything from the write-ahead log into the database file.
		var busy, frames, done int
		if err := conn.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &frames, &done); err != nil {
			return err
		}
		if busy != 0 {
			continue
		}
		// Hold the write lock so another process (such as admin-reset)
		// cannot change the file while it is copied.
		if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
			continue
		}
		err := copyIfLogEmpty(path, w)
		conn.ExecContext(context.Background(), `ROLLBACK`)
		if errors.Is(err, errLogNotEmpty) {
			continue
		}
		return err
	}
	return errors.New("the database stayed busy, try again later")
}

var errLogNotEmpty = errors.New("write-ahead log not empty")

func copyIfLogEmpty(path string, w io.Writer) error {
	if fi, err := os.Stat(path + "-wal"); err == nil && fi.Size() > 0 {
		return errLogNotEmpty
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// CheckDatabaseFile verifies that path holds an intact portal database
// this build can open, and returns its schema version.
func CheckDatabaseFile(ctx context.Context, path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	header := make([]byte, 100)
	_, err = io.ReadFull(f, header)
	f.Close()
	if err != nil || string(header[:16]) != "SQLite format 3\x00" {
		return 0, errors.New("not an SQLite database")
	}
	version := int(binary.BigEndian.Uint32(header[60:64])) // PRAGMA user_version
	if version > SchemaVersion() {
		return version, fmt.Errorf("the backup was made by a newer version of IPTV Portal (database schema %d, this version supports %d); update first", version, SchemaVersion())
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&immutable=1")
	if err != nil {
		return version, err
	}
	defer db.Close()
	var result string
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&result); err != nil {
		return version, fmt.Errorf("checking the database: %w", err)
	}
	if result != "ok" {
		return version, fmt.Errorf("the database is damaged: %s", result)
	}
	return version, nil
}

// applyPendingRestore replaces the database with a staged restore. The
// current database, with its write-ahead log, is kept as BeforeRestore.
func applyPendingRestore(dataDir string) error {
	staged := filepath.Join(dataDir, PendingRestore)
	if _, err := os.Stat(staged); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	current := filepath.Join(dataDir, dbFile)
	old := filepath.Join(dataDir, BeforeRestore)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		os.Remove(old + suffix)
		// SQLite finds the log by appending -wal to the file name, so the
		// old database stays consistent under its new name.
		if err := os.Rename(current+suffix, old+suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("restoring the database: %w", err)
		}
	}
	if err := os.Rename(staged, current); err != nil {
		return fmt.Errorf("restoring the database: %w", err)
	}
	return nil
}

// Value returns a stored setting that is not part of Settings, such as
// the backup configuration. It returns ErrNotFound when the key is unset.
func (s *Store) Value(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

// SetValue stores a setting read with Value.
func (s *Store) SetValue(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
