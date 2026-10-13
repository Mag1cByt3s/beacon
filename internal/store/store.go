// Package store keeps beacon's own data in SQLite: the focus state (which
// task is current, which were skipped) and the journal (see package
// journal). Tasks are referred to by their VTODO UID only; task data
// itself always stays in CalDAV.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	// The blank import registers the "sqlite" driver with database/sql.
	// modernc.org/sqlite is pure Go, so no C compiler is needed.
	_ "modernc.org/sqlite"
)

// migrations lists the schema changes in order: migrations[i] brings the
// database from version i to version i+1. The version is kept in SQLite's
// user_version. Never change a step that has been released; add a new one.
var migrations = []string{
	// 1: the focus state. Databases from before versioning already have
	// these tables, hence IF NOT EXISTS.
	`
	CREATE TABLE IF NOT EXISTS current (
		id   INTEGER PRIMARY KEY CHECK (id = 1), -- at most one row
		uid  TEXT NOT NULL,
		etag TEXT NOT NULL                       -- version when it became current
	);
	CREATE TABLE IF NOT EXISTS skips (
		uid TEXT PRIMARY KEY,
		seq INTEGER NOT NULL                     -- higher = skipped more recently
	);`,

	// 2: the journal. Times are RFC 3339 in UTC; dates are journal days.
	`
	CREATE TABLE journal_entries (
		date                  TEXT PRIMARY KEY,
		sleep_quality         INTEGER CHECK (sleep_quality BETWEEN 1 AND 10),
		sleep_hours           REAL,
		productivity          INTEGER CHECK (productivity BETWEEN 1 AND 10),
		productive_hours      REAL,
		productive_windows    TEXT,    -- JSON list of windows
		aw_productive_minutes INTEGER,
		aw_windows            TEXT,    -- JSON list of windows
		sport                 INTEGER, -- 0 or 1
		sport_type            TEXT,
		sport_minutes         INTEGER,
		steps                 INTEGER,
		note                  TEXT,
		updated_at            TEXT NOT NULL,
		updated_by            TEXT NOT NULL
	);
	CREATE TABLE journal_items (
		id         INTEGER PRIMARY KEY,
		date       TEXT NOT NULL,
		title      TEXT NOT NULL,
		minutes    INTEGER,
		kind       TEXT NOT NULL CHECK (kind IN ('computer', 'offline')),
		source     TEXT NOT NULL CHECK (source IN ('focus', 'task', 'manual')),
		ref_id     TEXT,
		created_at TEXT NOT NULL
	);
	CREATE INDEX journal_items_by_date ON journal_items (date);
	CREATE TABLE focus_sessions (
		id         INTEGER PRIMARY KEY,
		date       TEXT NOT NULL,          -- journal day the session started on
		started_at TEXT NOT NULL,
		ended_at   TEXT,                   -- NULL while it is running
		label      TEXT,
		kind       TEXT NOT NULL CHECK (kind IN ('computer', 'offline')),
		source     TEXT,
		task_uid   TEXT
	);
	CREATE INDEX focus_sessions_by_date ON focus_sessions (date);
	-- At most one running session: all running rows would share the
	-- value 1 in this partial index, which must be unique.
	CREATE UNIQUE INDEX focus_sessions_one_running ON focus_sessions ((1)) WHERE ended_at IS NULL;`,
}

// migrate brings the database schema up to date.
func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("the database is from a newer beacon (schema version %d, this one knows %d)", version, len(migrations))
	}
	for v := version; v < len(migrations); v++ {
		// Each step and its new version number are saved together or not
		// at all.
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("schema step %d: %w", v+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Store is an open beacon database.
type Store struct {
	db *sql.DB
}

// Current is the task focus mode is showing, and the ETag it had when it
// became current. The zero value means there is no current task.
type Current struct {
	UID  string
	ETag string
}

// Open opens (or creates) the database file at path.
func Open(path string) (*Store, error) {
	// busy_timeout makes SQLite wait for a lock instead of failing at once.
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("cannot open database %s: %w", path, err)
	}
	// One connection is plenty for one person and avoids lock contention.
	db.SetMaxOpenConns(1)

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("cannot set up database %s (check BEACON_DB): %w", path, err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// Current returns the current task, or the zero Current if there is none.
func (s *Store) Current(ctx context.Context) (Current, error) {
	var c Current
	err := s.db.QueryRowContext(ctx, `SELECT uid, etag FROM current WHERE id = 1`).Scan(&c.UID, &c.ETag)
	if errors.Is(err, sql.ErrNoRows) {
		return Current{}, nil
	}
	return c, err
}

// SetCurrent makes c the current task, replacing any previous one.
func (s *Store) SetCurrent(ctx context.Context, c Current) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO current (id, uid, etag) VALUES (1, ?, ?)
		ON CONFLICT (id) DO UPDATE SET uid = excluded.uid, etag = excluded.etag`,
		c.UID, c.ETag)
	return err
}

// ClearCurrent forgets the current task.
func (s *Store) ClearCurrent(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM current`)
	return err
}

// Skipped returns the skipped UIDs, the one skipped longest ago first.
func (s *Store) Skipped(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT uid FROM skips ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var uids []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		uids = append(uids, uid)
	}
	return uids, rows.Err()
}

// Skip puts uid at the end of the skip order, also if it was skipped before.
func (s *Store) Skip(ctx context.Context, uid string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO skips (uid, seq) VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM skips))
		ON CONFLICT (uid) DO UPDATE SET seq = excluded.seq`,
		uid)
	return err
}

// Unskip removes uid from the skip order, for example once it is done.
func (s *Store) Unskip(ctx context.Context, uid string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM skips WHERE uid = ?`, uid)
	return err
}
