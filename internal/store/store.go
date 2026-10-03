// Package store keeps beacon's focus state in SQLite: which task is
// current and which tasks were skipped. Tasks are referred to by their
// VTODO UID only; task data itself always stays in CalDAV.
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

const schema = `
CREATE TABLE IF NOT EXISTS current (
	id   INTEGER PRIMARY KEY CHECK (id = 1), -- at most one row
	uid  TEXT NOT NULL,
	etag TEXT NOT NULL                       -- version when it became current
);
CREATE TABLE IF NOT EXISTS skips (
	uid TEXT PRIMARY KEY,
	seq INTEGER NOT NULL                     -- higher = skipped more recently
);
`

// Store is an open focus-state database.
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

	if _, err := db.Exec(schema); err != nil {
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
