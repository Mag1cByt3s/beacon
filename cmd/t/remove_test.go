package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Mag1cByt3s/beacon/internal/api"
	"github.com/Mag1cByt3s/beacon/internal/caldav"
	"github.com/Mag1cByt3s/beacon/internal/caldav/caldavtest"
	"github.com/Mag1cByt3s/beacon/internal/focus"
	"github.com/Mag1cByt3s/beacon/internal/queue"
	"github.com/Mag1cByt3s/beacon/internal/server"
	"github.com/Mag1cByt3s/beacon/internal/store"
)

func TestMatchTasks(t *testing.T) {
	tasks := []focus.Task{
		{UID: "1", Summary: "buy coffee"},
		{UID: "2", Summary: "Coffee filter"},
		{UID: "3", Summary: "lsit"},
		{UID: "4", Summary: "Call  the\nbank"},
		{UID: "5", Summary: "buy milk"},
	}
	tests := []struct {
		query string
		want  []string
	}{
		{"lsit", []string{"3"}},          // exact title
		{"LSIT", []string{"3"}},          // case is ignored
		{"buy coffee", []string{"1"}},    // exact wins over "contains"
		{"coffee", []string{"1", "2"}},   // ambiguous
		{"bank", []string{"4"}},          // part of a title
		{"call the bank", []string{"4"}}, // extra spaces in titles don't matter
		{"buy", []string{"1", "5"}},      // part of two titles
		{"zz", nil},                      // too short for a part
		{"ls", nil},                      // too short for a part
		{"tea", nil},                     // no match
	}
	for _, tt := range tests {
		var got []string
		for _, task := range matchTasks(tasks, tt.query) {
			got = append(got, task.UID)
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("matchTasks(%q) = %q, want %q", tt.query, got, tt.want)
		}
	}
}

func TestEditDistance(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"list", "list", 0},
		{"lsit", "list", 1}, // swapped neighbours
		{"lst", "list", 1},  // missing letter
		{"lisst", "list", 1},
		{"lost", "list", 1},
		{"tsil", "list", 3},
		{"", "done", 4},
	}
	for _, tt := range tests {
		if got := editDistance(tt.a, tt.b); got != tt.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestRm(t *testing.T) {
	tasks := []focus.Task{
		{UID: "1", Summary: "buy coffee", ETag: "e1"},
		{UID: "2", Summary: "Coffee filter", ETag: "e2"},
		{UID: "3", Summary: "lsit", ETag: "e3"},
	}
	conflict := &api.ConflictError{Message: "changed"}

	tests := []struct {
		name        string
		query       string
		removeErr   error
		wantOut     string
		wantErr     string
		wantRemoved []string
	}{
		{"unique", "lsit", nil, "Removed \"lsit\".\n", "", []string{"3@e3"}},
		{"ambiguous", "coffee", nil, "", "2 tasks match \"coffee\"; be more specific:\n  buy coffee\n  Coffee filter", nil},
		{"no match", "tea", nil, "", "no open task matches \"tea\"", nil},
		{"too short", "ls", nil, "", "too short", nil},
		{"changed elsewhere", "lsit", conflict, "", "changed elsewhere just now", []string{"3@e3"}},
		{"already gone", "lsit", api.ErrNotFound, "", "no longer an open task", []string{"3@e3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out strings.Builder
			b := &fakeBackend{tasks: tasks, removeErr: tt.removeErr}
			a := &app{b: b, out: &out}
			err := a.execute(context.Background(), command{name: "rm", query: tt.query})

			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
			}
			if out.String() != tt.wantOut {
				t.Errorf("output = %q, want %q", out.String(), tt.wantOut)
			}
			if !slices.Equal(b.removed, tt.wantRemoved) {
				t.Errorf("removed %q, want %q", b.removed, tt.wantRemoved)
			}
		})
	}
}

func newUndoApp(t *testing.T, b backend) (*app, *strings.Builder) {
	t.Helper()
	dir := t.TempDir()
	out := &strings.Builder{}
	return &app{
		b:        b,
		out:      out,
		queue:    queue.New(filepath.Join(dir, "queue.jsonl")),
		lastPath: filepath.Join(dir, "last-capture.json"),
	}, out
}

func TestUndoNothing(t *testing.T) {
	a, out := newUndoApp(t, &fakeBackend{})
	if err := a.execute(context.Background(), command{name: "undo"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Nothing to undo.\n" {
		t.Errorf("output = %q", out.String())
	}
}

func TestUndoOnline(t *testing.T) {
	b := &fakeBackend{}
	a, out := newUndoApp(t, b)
	ctx := context.Background()

	a.execute(ctx, command{name: "add", summary: "lsit"})
	uid := b.added[0]
	b.tasks = []focus.Task{{UID: uid, Summary: "lsit", ETag: "etag-" + uid}}
	out.Reset()

	if err := a.execute(ctx, command{name: "undo"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Removed \"lsit\".\n" {
		t.Errorf("output = %q", out.String())
	}
	// Removed with the version from when it was added.
	if want := []string{uid + "@etag-" + uid}; !slices.Equal(b.removed, want) {
		t.Errorf("removed %q, want %q", b.removed, want)
	}

	// Only one step of undo.
	out.Reset()
	a.execute(ctx, command{name: "undo"})
	if out.String() != "Nothing to undo.\n" {
		t.Errorf("second undo: output = %q", out.String())
	}
}

func TestUndoQueued(t *testing.T) {
	b := &fakeBackend{addErr: context.DeadlineExceeded}
	a, out := newUndoApp(t, b)
	ctx := context.Background()

	a.execute(ctx, command{name: "add", summary: "lsit"})
	out.Reset()
	if err := a.execute(ctx, command{name: "undo"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Removed \"lsit\" (it was not sent yet).\n" {
		t.Errorf("output = %q", out.String())
	}
	if entries, _ := a.queue.Entries(); len(entries) != 0 {
		t.Errorf("queue still has %+v", entries)
	}
	if len(b.removed) != 0 {
		t.Error("undo of a queued capture used the network")
	}
}

func TestUndoRefusesChangedTask(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"via server", &api.ConflictError{Message: "changed"}},
		{"direct", caldav.ErrConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &fakeBackend{}
			a, _ := newUndoApp(t, b)
			ctx := context.Background()
			a.execute(ctx, command{name: "add", summary: "lsit"})
			b.removeErr = tt.err

			err := a.execute(ctx, command{name: "undo"})
			if err == nil || !strings.Contains(err.Error(), "changed elsewhere since you added it") {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestUndoKeepsCaptureWhenOffline(t *testing.T) {
	b := &fakeBackend{}
	a, _ := newUndoApp(t, b)
	ctx := context.Background()
	a.execute(ctx, command{name: "add", summary: "lsit"})

	b.removeErr = unreachableAPI(t)
	if err := a.execute(ctx, command{name: "undo"}); !errors.Is(err, api.ErrUnreachable) {
		t.Fatalf("err = %v, want the connection error", err)
	}
	// Try again once the server is back.
	b.removeErr = nil
	b.tasks = []focus.Task{{UID: b.added[0], Summary: "lsit"}}
	if err := a.execute(ctx, command{name: "undo"}); err != nil {
		t.Errorf("retry: %v", err)
	}
}

// undoAgainst adds a task through b, then undoes it, both for real against
// the fake CalDAV server.
func undoAgainst(t *testing.T, b backend, dav *caldavtest.Backend) {
	t.Helper()
	a, out := newUndoApp(t, b)
	ctx := context.Background()
	before := len(dav.Objects[caldavtest.TodoPath])

	if err := a.execute(ctx, command{name: "add", summary: "lsit"}); err != nil {
		t.Fatal(err)
	}
	if n := len(dav.Objects[caldavtest.TodoPath]); n != before+1 {
		t.Fatalf("Todo has %d objects after add, want %d", n, before+1)
	}
	out.Reset()
	if err := a.execute(ctx, command{name: "undo"}); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if out.String() != "Removed \"lsit\".\n" {
		t.Errorf("output = %q", out.String())
	}
	if n := len(dav.Objects[caldavtest.TodoPath]); n != before {
		t.Errorf("Todo has %d objects after undo, want %d", n, before)
	}

	// A capture edited on the phone is not undone.
	a.execute(ctx, command{name: "add", summary: "lsit"})
	objects := dav.Objects[caldavtest.TodoPath]
	dav.Edit(t, objects[len(objects)-1].Path, "list of things, edited")
	err := a.execute(ctx, command{name: "undo"})
	if err == nil || !strings.Contains(err.Error(), "changed elsewhere") {
		t.Errorf("undo after edit: err = %v", err)
	}
	if n := len(dav.Objects[caldavtest.TodoPath]); n != before+1 {
		t.Errorf("the edited task was deleted")
	}
}

func TestUndoDirect(t *testing.T) {
	davSrv, dav := caldavtest.NewServer(t)
	client, err := caldav.NewClient(davSrv.URL+"/", caldavtest.User, caldavtest.Password, []string{"Todo"})
	if err != nil {
		t.Fatal(err)
	}
	undoAgainst(t, direct{client: client, defaultList: "Todo"}, dav)
}

func TestUndoViaServer(t *testing.T) {
	davSrv, dav := caldavtest.NewServer(t)
	client, err := caldav.NewClient(davSrv.URL+"/", caldavtest.User, caldavtest.Password, []string{"Todo"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "beacon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(server.New(client, st, "test-token-0123456789", "Todo", logger).Handler())
	defer srv.Close()

	apiClient, _ := api.NewClient(srv.URL, "test-token-0123456789")
	undoAgainst(t, apiClient, dav)
}
