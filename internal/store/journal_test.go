package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/journal"
)

func ptr[T any](v T) *T { return &v }

// TestMigrateExistingDatabase starts from a database as beacon created it
// before schema versions existed (like the one on the server) and checks
// that its focus state survives the upgrade.
func TestMigrateExistingDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "beacon.db")

	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = old.Exec(`
		CREATE TABLE current (id INTEGER PRIMARY KEY CHECK (id = 1), uid TEXT NOT NULL, etag TEXT NOT NULL);
		CREATE TABLE skips (uid TEXT PRIMARY KEY, seq INTEGER NOT NULL);
		INSERT INTO current VALUES (1, 'task-a', 'etag-1');
		INSERT INTO skips VALUES ('task-b', 1);`)
	old.Close()
	if err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if c, _ := s.Current(ctx); c != (Current{UID: "task-a", ETag: "etag-1"}) {
		t.Errorf("Current after upgrade = %+v", c)
	}
	if skipped, _ := s.Skipped(ctx); !reflect.DeepEqual(skipped, []string{"task-b"}) {
		t.Errorf("Skipped after upgrade = %q", skipped)
	}
	var version int
	s.db.QueryRow(`PRAGMA user_version`).Scan(&version)
	if version != len(migrations) {
		t.Errorf("schema version = %d, want %d", version, len(migrations))
	}
	// The journal tables exist now.
	if _, _, err := s.Entry(ctx, "2026-10-12"); err != nil {
		t.Errorf("Entry after upgrade: %v", err)
	}
}

func TestOpenRefusesNewerDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.db")
	db, _ := sql.Open("sqlite", path)
	db.Exec(`PRAGMA user_version = 99`)
	db.Close()
	if s, err := Open(path); err == nil {
		s.Close()
		t.Error("Open accepted a database from a newer beacon")
	}
}

func TestEntryRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)

	e, found, err := s.Entry(ctx, "2026-10-12")
	if err != nil || found || e.Date != "2026-10-12" || e.SleepQuality != nil {
		t.Fatalf("empty day: %+v, found %v, err %v", e, found, err)
	}

	full := journal.Entry{
		Date:                "2026-10-12",
		SleepQuality:        ptr(7),
		SleepHours:          ptr(7.5),
		Productivity:        ptr(6),
		ProductiveHours:     ptr(4.25),
		ProductiveWindows:   []journal.Window{{Start: "09:15", End: "11:40", Kind: "computer"}},
		AWProductiveMinutes: ptr(210),
		AWWindows:           []journal.Window{},
		Sport:               ptr(false),
		SportType:           ptr("Laufen"),
		SportMinutes:        ptr(30),
		Steps:               ptr(8123),
		Note:                ptr("müde, aber ok"),
		UpdatedAt:           time.Date(2026, 10, 12, 21, 30, 0, 0, time.UTC),
		UpdatedBy:           "iphone",
	}
	if err := s.SaveEntry(ctx, full); err != nil {
		t.Fatal(err)
	}

	// Read it back, also after reopening the file.
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, found, err := s2.Entry(ctx, "2026-10-12")
	if err != nil || !found {
		t.Fatalf("Entry: found %v, err %v", found, err)
	}
	if !reflect.DeepEqual(got, full) {
		t.Errorf("round trip changed the entry:\n got  %+v\n want %+v", got, full)
	}

	// Unknown fields stay unknown (nil), and saving again replaces the row.
	sparse := journal.Entry{Date: "2026-10-12", SleepQuality: ptr(3), UpdatedAt: full.UpdatedAt, UpdatedBy: "laptop"}
	s2.SaveEntry(ctx, sparse)
	got, _, _ = s2.Entry(ctx, "2026-10-12")
	if !reflect.DeepEqual(got, sparse) {
		t.Errorf("sparse entry:\n got  %+v\n want %+v", got, sparse)
	}
}

func TestEntriesRange(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	for _, d := range []string{"2026-10-12", "2026-10-10", "2026-10-11", "2026-09-30"} {
		s.SaveEntry(ctx, journal.Entry{Date: d, UpdatedBy: "test", UpdatedAt: time.Now()})
	}
	entries, err := s.Entries(ctx, "2026-10-01", "2026-10-11")
	if err != nil {
		t.Fatal(err)
	}
	var dates []string
	for _, e := range entries {
		dates = append(dates, e.Date)
	}
	if !reflect.DeepEqual(dates, []string{"2026-10-10", "2026-10-11"}) {
		t.Errorf("Entries = %q, want 10th and 11th in order", dates)
	}
}

func TestItems(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)

	a, err := s.AddItem(ctx, journal.Item{Date: "2026-10-12", Title: "Writeup", Minutes: ptr(60), Kind: "computer", Source: "manual"})
	if err != nil || a.ID == 0 || a.CreatedAt.IsZero() {
		t.Fatalf("AddItem = %+v, %v", a, err)
	}
	s.AddItem(ctx, journal.Item{Date: "2026-10-12", Title: "Wäsche", Kind: "offline", Source: "task", RefID: "uid-1"})
	s.AddItem(ctx, journal.Item{Date: "2026-10-13", Title: "Morgen", Kind: "computer", Source: "manual"})

	items, _ := s.Items(ctx, "2026-10-12", "2026-10-12")
	if len(items) != 2 || items[0].Title != "Writeup" || items[1].Minutes != nil || items[1].RefID != "uid-1" {
		t.Errorf("Items = %+v", items)
	}

	if it, found, _ := s.ItemByRef(ctx, "task", "uid-1"); !found || it.Title != "Wäsche" {
		t.Errorf("ItemByRef = %+v, %v", it, found)
	}
	if _, found, _ := s.ItemByRef(ctx, "task", "uid-2"); found {
		t.Error("ItemByRef found an unknown reference")
	}

	// Replacing the list of the 12th leaves the 13th alone.
	stored, err := s.ReplaceItems(ctx, "2026-10-12", []journal.Item{
		{Title: "Nur noch das", Kind: "offline", Source: "manual"},
	})
	if err != nil || len(stored) != 1 || stored[0].Date != "2026-10-12" || stored[0].ID == 0 {
		t.Fatalf("ReplaceItems = %+v, %v", stored, err)
	}
	items, _ = s.Items(ctx, "2026-10-12", "2026-10-13")
	if len(items) != 2 || items[0].Title != "Nur noch das" || items[1].Title != "Morgen" {
		t.Errorf("after replace: %+v", items)
	}

	// A bad item makes the whole replacement fail; nothing changes.
	_, err = s.ReplaceItems(ctx, "2026-10-12", []journal.Item{
		{Title: "ok", Kind: "computer", Source: "manual"},
		{Title: "bad", Kind: "sofa", Source: "manual"}, // fails the CHECK
	})
	if err == nil {
		t.Fatal("ReplaceItems accepted a bad kind")
	}
	items, _ = s.Items(ctx, "2026-10-12", "2026-10-12")
	if len(items) != 1 || items[0].Title != "Nur noch das" {
		t.Errorf("a failed replacement changed the list: %+v", items)
	}
}

func TestSessions(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	start := time.Date(2026, 10, 12, 7, 0, 0, 0, time.UTC)

	if _, running, _ := s.RunningSession(ctx); running {
		t.Fatal("a session is running in a new database")
	}
	if _, err := s.StopSession(ctx, start); err != ErrNoSession {
		t.Errorf("StopSession with none running: %v, want ErrNoSession", err)
	}

	sess, err := s.StartSession(ctx, journal.Session{Date: "2026-10-12", Start: start, Label: "HTB", Kind: "computer", Source: "laptop", TaskUID: "uid-1"})
	if err != nil || sess.ID == 0 {
		t.Fatalf("StartSession = %+v, %v", sess, err)
	}
	if _, err := s.StartSession(ctx, journal.Session{Date: "2026-10-12", Start: start, Kind: "computer"}); err != ErrSessionRunning {
		t.Errorf("second start: %v, want ErrSessionRunning", err)
	}
	if running, ok, _ := s.RunningSession(ctx); !ok || running.ID != sess.ID || running.End != nil {
		t.Errorf("RunningSession = %+v, %v", running, ok)
	}

	stopped, err := s.StopSession(ctx, start.Add(90*time.Minute))
	if err != nil || stopped.End == nil || stopped.Minutes(time.Time{}) != 90 {
		t.Fatalf("StopSession = %+v, %v", stopped, err)
	}

	// A clock that went backwards gives 0 minutes, not a negative time.
	s.StartSession(ctx, journal.Session{Date: "2026-10-12", Start: start.Add(3 * time.Hour), Kind: "offline"})
	short, _ := s.StopSession(ctx, start)
	if short.Minutes(time.Time{}) != 0 {
		t.Errorf("backwards session: %d minutes", short.Minutes(time.Time{}))
	}

	all, _ := s.Sessions(ctx, "2026-10-12", "2026-10-12")
	if len(all) != 2 || all[0].Label != "HTB" || all[0].TaskUID != "uid-1" || all[0].Source != "laptop" {
		t.Errorf("Sessions = %+v", all)
	}
	forTask, _ := s.SessionsForTask(ctx, "uid-1")
	if len(forTask) != 1 || forTask[0].ID != sess.ID {
		t.Errorf("SessionsForTask = %+v", forTask)
	}
}

// TestOneRunningSessionInTheDatabase checks the database itself refuses a
// second running session, even if two requests race past the check.
func TestOneRunningSessionInTheDatabase(t *testing.T) {
	s, _ := openTemp(t)
	insert := `INSERT INTO focus_sessions (date, started_at, kind) VALUES ('2026-10-12', '2026-10-12T07:00:00Z', 'computer')`
	if _, err := s.db.Exec(insert); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(insert); err == nil {
		t.Error("the database accepted a second running session")
	}
}
