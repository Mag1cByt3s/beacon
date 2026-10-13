package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/journal"
)

// Errors for focus sessions.
var (
	ErrSessionRunning = errors.New("a focus session is already running")
	ErrNoSession      = errors.New("no focus session is running")
)

// timeLayout is how times are stored: RFC 3339 in UTC, which also sorts
// correctly as text.
const timeLayout = time.RFC3339Nano

func formatTime(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

func parseTime(s string) (time.Time, error) {
	return time.Parse(timeLayout, s)
}

// scanner is what *sql.Row and *sql.Rows have in common.
type scanner interface {
	Scan(dest ...any) error
}

// ---- Entries ----

const entryColumns = `date, sleep_quality, sleep_hours, productivity, productive_hours,
	productive_windows, aw_productive_minutes, aw_windows, sport, sport_type,
	sport_minutes, steps, note, updated_at, updated_by`

// Entry returns the journal entry for date. found is false if nothing
// has been saved for that day yet.
func (s *Store) Entry(ctx context.Context, date string) (e journal.Entry, found bool, err error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+entryColumns+` FROM journal_entries WHERE date = ?`, date)
	e, err = scanEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return journal.Entry{Date: date}, false, nil
	}
	if err != nil {
		return journal.Entry{}, false, err
	}
	return e, true, nil
}

// Entries returns the saved entries from one date to another (both
// included), oldest first.
func (s *Store) Entries(ctx context.Context, from, to string) ([]journal.Entry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+entryColumns+` FROM journal_entries WHERE date BETWEEN ? AND ? ORDER BY date`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := []journal.Entry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// SaveEntry stores e, replacing what was saved for its date. The caller
// sets UpdatedAt and UpdatedBy.
func (s *Store) SaveEntry(ctx context.Context, e journal.Entry) error {
	productive, err := windowsJSON(e.ProductiveWindows)
	if err != nil {
		return err
	}
	aw, err := windowsJSON(e.AWWindows)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO journal_entries (`+entryColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (date) DO UPDATE SET
			sleep_quality = excluded.sleep_quality,
			sleep_hours = excluded.sleep_hours,
			productivity = excluded.productivity,
			productive_hours = excluded.productive_hours,
			productive_windows = excluded.productive_windows,
			aw_productive_minutes = excluded.aw_productive_minutes,
			aw_windows = excluded.aw_windows,
			sport = excluded.sport,
			sport_type = excluded.sport_type,
			sport_minutes = excluded.sport_minutes,
			steps = excluded.steps,
			note = excluded.note,
			updated_at = excluded.updated_at,
			updated_by = excluded.updated_by`,
		e.Date, e.SleepQuality, e.SleepHours, e.Productivity, e.ProductiveHours,
		productive, e.AWProductiveMinutes, aw, e.Sport, e.SportType,
		e.SportMinutes, e.Steps, e.Note, formatTime(e.UpdatedAt), e.UpdatedBy)
	return err
}

func scanEntry(row scanner) (journal.Entry, error) {
	var (
		e               journal.Entry
		productive, aw  sql.NullString
		sleepQuality    sql.NullInt64
		productivity    sql.NullInt64
		awMinutes       sql.NullInt64
		sport           sql.NullBool
		sportMinutes    sql.NullInt64
		steps           sql.NullInt64
		sleepHours      sql.NullFloat64
		productiveHours sql.NullFloat64
		sportType, note sql.NullString
		updatedAt       string
	)
	err := row.Scan(&e.Date, &sleepQuality, &sleepHours, &productivity, &productiveHours,
		&productive, &awMinutes, &aw, &sport, &sportType,
		&sportMinutes, &steps, &note, &updatedAt, &e.UpdatedBy)
	if err != nil {
		return journal.Entry{}, err
	}

	e.SleepQuality = intPtr(sleepQuality)
	e.Productivity = intPtr(productivity)
	e.AWProductiveMinutes = intPtr(awMinutes)
	e.SportMinutes = intPtr(sportMinutes)
	e.Steps = intPtr(steps)
	e.SleepHours = floatPtr(sleepHours)
	e.ProductiveHours = floatPtr(productiveHours)
	e.SportType = stringPtr(sportType)
	e.Note = stringPtr(note)
	if sport.Valid {
		e.Sport = &sport.Bool
	}
	if e.ProductiveWindows, err = parseWindows(productive); err != nil {
		return journal.Entry{}, err
	}
	if e.AWWindows, err = parseWindows(aw); err != nil {
		return journal.Entry{}, err
	}
	if e.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return journal.Entry{}, err
	}
	return e, nil
}

// windowsJSON turns windows into the JSON stored in the database; no
// windows (nil) is stored as NULL.
func windowsJSON(w []journal.Window) (any, error) {
	if w == nil {
		return nil, nil
	}
	data, err := json.Marshal(w)
	return string(data), err
}

func parseWindows(s sql.NullString) ([]journal.Window, error) {
	if !s.Valid {
		return nil, nil
	}
	w := []journal.Window{}
	if err := json.Unmarshal([]byte(s.String), &w); err != nil {
		return nil, fmt.Errorf("stored windows are damaged: %w", err)
	}
	return w, nil
}

// The sql.Null* types say whether a column was NULL; these turn them into
// pointers, which is how package journal marks "unknown".
func intPtr(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

func floatPtr(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	return &n.Float64
}

func stringPtr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	return &n.String
}

// ---- Items ----

const itemColumns = `id, date, title, minutes, kind, source, ref_id, created_at`

// Items returns the items from one date to another (both included), in
// the order they were added.
func (s *Store) Items(ctx context.Context, from, to string) ([]journal.Item, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+itemColumns+` FROM journal_items WHERE date BETWEEN ? AND ? ORDER BY date, id`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []journal.Item{}
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// ItemByRef returns the item that came from source with this reference
// (a task UID or a session ID), if there is one.
func (s *Store) ItemByRef(ctx context.Context, source, ref string) (journal.Item, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+itemColumns+` FROM journal_items WHERE source = ? AND ref_id = ? ORDER BY id LIMIT 1`, source, ref)
	it, err := scanItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return journal.Item{}, false, nil
	}
	return it, err == nil, err
}

// AddItem stores a new item and returns it with its ID. If CreatedAt is
// zero, it is set to now.
func (s *Store) AddItem(ctx context.Context, it journal.Item) (journal.Item, error) {
	return addItem(ctx, s.db, it)
}

// ReplaceItems makes items the complete list for date: all other items of
// that day are deleted. It returns the stored items with their IDs.
func (s *Store) ReplaceItems(ctx context.Context, date string, items []journal.Item) ([]journal.Item, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	// Rollback after a successful Commit does nothing, so this is a safe
	// way to undo everything on any early return.
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM journal_items WHERE date = ?`, date); err != nil {
		return nil, err
	}
	stored := []journal.Item{}
	for _, it := range items {
		it.Date = date
		it, err := addItem(ctx, tx, it)
		if err != nil {
			return nil, err
		}
		stored = append(stored, it)
	}
	return stored, tx.Commit()
}

// execer is what *sql.DB and *sql.Tx have in common.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func addItem(ctx context.Context, db execer, it journal.Item) (journal.Item, error) {
	if it.CreatedAt.IsZero() {
		it.CreatedAt = time.Now()
	}
	var ref any // NULL for no reference
	if it.RefID != "" {
		ref = it.RefID
	}
	res, err := db.ExecContext(ctx, `
		INSERT INTO journal_items (date, title, minutes, kind, source, ref_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		it.Date, it.Title, it.Minutes, it.Kind, it.Source, ref, formatTime(it.CreatedAt))
	if err != nil {
		return journal.Item{}, err
	}
	it.ID, err = res.LastInsertId()
	return it, err
}

func scanItem(row scanner) (journal.Item, error) {
	var (
		it        journal.Item
		minutes   sql.NullInt64
		ref       sql.NullString
		createdAt string
	)
	err := row.Scan(&it.ID, &it.Date, &it.Title, &minutes, &it.Kind, &it.Source, &ref, &createdAt)
	if err != nil {
		return journal.Item{}, err
	}
	it.Minutes = intPtr(minutes)
	it.RefID = ref.String
	it.CreatedAt, err = parseTime(createdAt)
	return it, err
}

// ---- Focus sessions ----

const sessionColumns = `id, date, started_at, ended_at, label, kind, source, task_uid`

// StartSession stores a new running session and returns it with its ID.
// It fails with ErrSessionRunning if one is running already.
func (s *Store) StartSession(ctx context.Context, sess journal.Session) (journal.Session, error) {
	if _, running, err := s.RunningSession(ctx); err != nil {
		return journal.Session{}, err
	} else if running {
		return journal.Session{}, ErrSessionRunning
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO focus_sessions (date, started_at, ended_at, label, kind, source, task_uid)
		VALUES (?, ?, NULL, ?, ?, ?, ?)`,
		sess.Date, formatTime(sess.Start), sess.Label, sess.Kind, sess.Source, sess.TaskUID)
	if err != nil {
		return journal.Session{}, err
	}
	sess.End = nil
	sess.ID, err = res.LastInsertId()
	return sess, err
}

// RunningSession returns the session that is running, if any.
func (s *Store) RunningSession(ctx context.Context) (journal.Session, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM focus_sessions WHERE ended_at IS NULL`)
	sess, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return journal.Session{}, false, nil
	}
	return sess, err == nil, err
}

// StopSession ends the running session at end and returns it. It fails
// with ErrNoSession if none is running.
func (s *Store) StopSession(ctx context.Context, end time.Time) (journal.Session, error) {
	sess, running, err := s.RunningSession(ctx)
	if err != nil {
		return journal.Session{}, err
	}
	if !running {
		return journal.Session{}, ErrNoSession
	}
	if end.Before(sess.Start) {
		end = sess.Start // a clock that went backwards: a session of 0 minutes
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE focus_sessions SET ended_at = ? WHERE id = ?`,
		formatTime(end), sess.ID); err != nil {
		return journal.Session{}, err
	}
	sess.End = &end
	return sess, nil
}

// Sessions returns the sessions that started from one date to another
// (both included), oldest first.
func (s *Store) Sessions(ctx context.Context, from, to string) ([]journal.Session, error) {
	return s.querySessions(ctx, `WHERE date BETWEEN ? AND ? ORDER BY started_at`, from, to)
}

// SessionsForTask returns the sessions that were for the task with this
// UID, oldest first.
func (s *Store) SessionsForTask(ctx context.Context, taskUID string) ([]journal.Session, error) {
	return s.querySessions(ctx, `WHERE task_uid = ? ORDER BY started_at`, taskUID)
}

func (s *Store) querySessions(ctx context.Context, where string, args ...any) ([]journal.Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sessionColumns+` FROM focus_sessions `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessions := []journal.Session{}
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, sess)
	}
	return sessions, rows.Err()
}

func scanSession(row scanner) (journal.Session, error) {
	var (
		sess                 journal.Session
		started              string
		ended, label, source sql.NullString
		taskUID              sql.NullString
	)
	err := row.Scan(&sess.ID, &sess.Date, &started, &ended, &label, &sess.Kind, &source, &taskUID)
	if err != nil {
		return journal.Session{}, err
	}
	if sess.Start, err = parseTime(started); err != nil {
		return journal.Session{}, err
	}
	if ended.Valid {
		end, err := parseTime(ended.String)
		if err != nil {
			return journal.Session{}, err
		}
		sess.End = &end
	}
	sess.Label, sess.Source, sess.TaskUID = label.String, source.String, taskUID.String
	return sess, nil
}
