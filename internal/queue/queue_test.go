package queue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func newQueue(t *testing.T) *Queue {
	t.Helper()
	return New(filepath.Join(t.TempDir(), "beacon", "queue.jsonl"))
}

func appendAll(t *testing.T, q *Queue, uids ...string) {
	t.Helper()
	for _, uid := range uids {
		if err := q.Append(Entry{UID: uid, Summary: "task " + uid, Queued: time.Now()}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
}

func queuedUIDs(t *testing.T, q *Queue) []string {
	t.Helper()
	entries, err := q.Entries()
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	uids := []string{}
	for _, e := range entries {
		uids = append(uids, e.UID)
	}
	return uids
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	if got, _ := DefaultPath(); got != "/state/beacon/queue.jsonl" {
		t.Errorf("with XDG_STATE_HOME: %q", got)
	}

	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/someone")
	if got, _ := DefaultPath(); got != "/home/someone/.local/state/beacon/queue.jsonl" {
		t.Errorf("fallback: %q", got)
	}
}

func TestAppendAndEntries(t *testing.T) {
	q := newQueue(t)
	if got := queuedUIDs(t, q); len(got) != 0 {
		t.Fatalf("new queue has entries: %q", got)
	}

	appendAll(t, q, "a", "b")
	entries, _ := q.Entries()
	if len(entries) != 2 || entries[0].UID != "a" || entries[0].Summary != "task a" || entries[0].Queued.IsZero() {
		t.Errorf("Entries = %+v", entries)
	}

	// The queue is private to the user.
	info, err := os.Stat(q.Path())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("queue file mode = %o, want 600", perm)
	}
}

func TestFlushAll(t *testing.T) {
	q := newQueue(t)
	appendAll(t, q, "a", "b", "c")

	var sentOrder []string
	sent, err := q.Flush(context.Background(), func(ctx context.Context, e Entry) error {
		sentOrder = append(sentOrder, e.UID)
		return nil
	})
	if err != nil || sent != 3 {
		t.Fatalf("Flush = %d, %v; want 3, nil", sent, err)
	}
	if !slices.Equal(sentOrder, []string{"a", "b", "c"}) {
		t.Errorf("sent in order %q, want oldest first", sentOrder)
	}
	if _, err := os.Stat(q.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("empty queue file still exists (err %v)", err)
	}
}

func TestFlushPartialFailure(t *testing.T) {
	q := newQueue(t)
	appendAll(t, q, "a", "b", "c", "d")
	failure := errors.New("server said no")

	sent, err := q.Flush(context.Background(), func(ctx context.Context, e Entry) error {
		if e.UID == "b" || e.UID == "d" {
			return failure
		}
		return nil
	})
	if sent != 2 || !errors.Is(err, failure) {
		t.Errorf("Flush = %d, %v; want 2 and the first send error", sent, err)
	}
	if got := queuedUIDs(t, q); !slices.Equal(got, []string{"b", "d"}) {
		t.Errorf("left in queue: %q, want [b d] in order", got)
	}
}

func TestFlushStopsWhenTimeIsUp(t *testing.T) {
	q := newQueue(t)
	appendAll(t, q, "a", "b", "c")
	ctx, cancel := context.WithCancel(context.Background())

	sent, err := q.Flush(ctx, func(ctx context.Context, e Entry) error {
		cancel() // time runs out while sending the first one
		return nil
	})
	if sent != 1 || err == nil {
		t.Errorf("Flush = %d, %v; want 1 and an error", sent, err)
	}
	if got := queuedUIDs(t, q); !slices.Equal(got, []string{"b", "c"}) {
		t.Errorf("left in queue: %q, want [b c]", got)
	}
}

func TestAppendDuringFlushIsKept(t *testing.T) {
	q := newQueue(t)
	appendAll(t, q, "a")

	_, err := q.Flush(context.Background(), func(ctx context.Context, e Entry) error {
		// Another t process captures something while this one flushes.
		appendAll(t, q, "new")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := queuedUIDs(t, q); !slices.Equal(got, []string{"new"}) {
		t.Errorf("left in queue: %q, want [new]", got)
	}
}

func TestBrokenLineIsKept(t *testing.T) {
	q := newQueue(t)
	appendAll(t, q, "a")
	f, _ := os.OpenFile(q.Path(), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("{not json\n")
	f.Close()
	appendAll(t, q, "b")

	if got := queuedUIDs(t, q); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("Entries = %q, want [a b]", got)
	}
	q.Flush(context.Background(), func(ctx context.Context, e Entry) error { return nil })

	// Nothing is thrown away that might be someone's task.
	data, _ := os.ReadFile(q.Path())
	if string(data) != "{not json\n" {
		t.Errorf("queue file = %q, want only the broken line left", data)
	}
}

func TestRetryNeverDuplicates(t *testing.T) {
	q := newQueue(t)
	appendAll(t, q, "a", "b")

	// A server that stores tasks by UID and refuses a UID it already has,
	// like CalDAV with If-None-Match. The sender treats "already exists"
	// as success, as t does.
	stored := map[string]int{}
	errExists := errors.New("exists")
	lostAnswer := errors.New("timeout")
	first := true
	send := func(ctx context.Context, e Entry) error {
		if stored[e.UID] > 0 {
			return errExists
		}
		stored[e.UID]++
		if first && e.UID == "a" {
			first = false
			return lostAnswer // stored, but the answer never arrived
		}
		return nil
	}
	sender := func(ctx context.Context, e Entry) error {
		if err := send(ctx, e); err != nil && !errors.Is(err, errExists) {
			return err
		}
		return nil
	}

	q.Flush(context.Background(), sender) // a: lost answer, b: sent
	if got := queuedUIDs(t, q); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("after first flush: %q, want [a]", got)
	}
	q.Flush(context.Background(), sender) // a: already exists -> done
	if got := queuedUIDs(t, q); len(got) != 0 {
		t.Errorf("after second flush: %q, want empty", got)
	}
	if stored["a"] != 1 || stored["b"] != 1 {
		t.Errorf("stored = %v, want each task exactly once", stored)
	}
}

func TestRemove(t *testing.T) {
	q := newQueue(t)
	appendAll(t, q, "a", "b", "c")

	if found, err := q.Remove("b"); err != nil || !found {
		t.Fatalf("Remove(b) = %v, %v; want true", found, err)
	}
	if got := queuedUIDs(t, q); !slices.Equal(got, []string{"a", "c"}) {
		t.Errorf("left in queue: %q, want [a c]", got)
	}
	if found, err := q.Remove("b"); err != nil || found {
		t.Errorf("Remove(b) again = %v, %v; want false", found, err)
	}
	q.Remove("a")
	q.Remove("c")
	if _, err := os.Stat(q.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("empty queue file still exists (err %v)", err)
	}
	// An empty queue is fine too.
	if found, err := newQueue(t).Remove("x"); err != nil || found {
		t.Errorf("Remove on a new queue = %v, %v", found, err)
	}
}
