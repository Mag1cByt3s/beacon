package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/api"
	"github.com/Mag1cByt3s/beacon/internal/caldav"
	"github.com/Mag1cByt3s/beacon/internal/caldav/caldavtest"
	"github.com/Mag1cByt3s/beacon/internal/focus"
	"github.com/Mag1cByt3s/beacon/internal/queue"
	"github.com/Mag1cByt3s/beacon/internal/server"
	"github.com/Mag1cByt3s/beacon/internal/store"
)

func tempQueue(t *testing.T) *queue.Queue {
	t.Helper()
	return queue.New(filepath.Join(t.TempDir(), "queue.jsonl"))
}

func queued(t *testing.T, q *queue.Queue) []queue.Entry {
	t.Helper()
	entries, err := q.Entries()
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestAddOffline(t *testing.T) {
	unreachableErrs := []error{
		unreachableAPI(t),
		unreachableCalDAV(t),
		context.DeadlineExceeded,
	}
	for _, addErr := range unreachableErrs {
		t.Run(addErr.Error(), func(t *testing.T) {
			var out strings.Builder
			q := tempQueue(t)
			b := &fakeBackend{addErr: addErr}
			a := &app{b: b, out: &out, defaultList: "Todo", queue: q}

			if err := a.execute(context.Background(), command{name: "add", summary: "buy coffee"}); err != nil {
				t.Fatalf("err = %v, want nil (exit code 0)", err)
			}
			if out.String() != "Saved offline, will sync later.\n" {
				t.Errorf("output = %q", out.String())
			}
			entries := queued(t, q)
			if len(entries) != 1 || entries[0].Summary != "buy coffee" {
				t.Fatalf("queue = %+v, want the capture", entries)
			}
			// The UID tried online is the one queued, so a late success
			// of the first try cannot lead to a duplicate.
			if entries[0].UID != b.added[0] {
				t.Errorf("queued UID %q, tried %q", entries[0].UID, b.added[0])
			}
		})
	}
}

// unreachableAPI returns a real "server is down" error from the API client.
func unreachableAPI(t *testing.T) error {
	srv := httptest.NewServer(nil)
	srv.Close()
	c, _ := api.NewClient(srv.URL, "token")
	_, _, err := c.Add(context.Background(), caldav.NewUID(), "x")
	if !errors.Is(err, api.ErrUnreachable) {
		t.Fatalf("setup: %v", err)
	}
	return err
}

// unreachableCalDAV returns a real "Radicale is down" error.
func unreachableCalDAV(t *testing.T) error {
	srv := httptest.NewServer(nil)
	srv.Close()
	c, _ := caldav.NewClient(srv.URL+"/", "", "", []string{"Todo"})
	_, _, err := c.Create(context.Background(), "Todo", caldav.NewUID(), "x")
	if !errors.Is(err, caldav.ErrUnreachable) {
		t.Fatalf("setup: %v", err)
	}
	return err
}

func TestAddOfflineWhenProxyReportsBeaconDown(t *testing.T) {
	for _, status := range []int{http.StatusBadGateway, http.StatusGatewayTimeout} {
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status) // what Caddy answers when beacon is down
		}))
		c, _ := api.NewClient(proxy.URL, "token")
		var out strings.Builder
		q := tempQueue(t)
		a := &app{b: c, out: &out, queue: q}

		if err := a.execute(context.Background(), command{name: "add", summary: "buy coffee"}); err != nil {
			t.Errorf("%d: err = %v, want nil", status, err)
		}
		if out.String() != "Saved offline, will sync later.\n" || len(queued(t, q)) != 1 {
			t.Errorf("%d: output %q, %d queued; want the capture saved offline", status, out.String(), len(queued(t, q)))
		}
		proxy.Close()
	}
}

func TestAddOtherErrorsAreNotQueued(t *testing.T) {
	q := tempQueue(t)
	a := &app{b: &fakeBackend{addErr: api.ErrUnauthorized}, out: io.Discard, queue: q}
	err := a.execute(context.Background(), command{name: "add", summary: "x"})
	if !errors.Is(err, api.ErrUnauthorized) {
		t.Errorf("err = %v, want the real error", err)
	}
	if n := len(queued(t, q)); n != 0 {
		t.Errorf("%d entries queued, want none", n)
	}
}

func TestDoneAndSkipAreNeverQueued(t *testing.T) {
	for _, name := range []string{"done", "skip"} {
		q := tempQueue(t)
		a := &app{b: &fakeBackend{err: unreachableAPI(t)}, out: io.Discard, queue: q}
		if err := a.execute(context.Background(), command{name: name}); err == nil {
			t.Errorf("%s: err = nil, want the connection error", name)
		}
		if n := len(queued(t, q)); n != 0 {
			t.Errorf("%s: %d entries queued, want none", name, n)
		}
	}
}

func TestFlushFailureDoesNotBlockTheCommand(t *testing.T) {
	q := tempQueue(t)
	q.Append(queue.Entry{UID: "a", Summary: "queued earlier"})
	var out strings.Builder
	b := &fakeBackend{tasks: []focus.Task{{Summary: "Pay rent"}}, addErr: unreachableAPI(t)}
	a := &app{b: b, out: &out, queue: q}

	start := time.Now()
	a.flush()
	if err := a.execute(context.Background(), command{name: "focus"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Pay rent\n" {
		t.Errorf("output = %q", out.String())
	}
	if !a.offline {
		t.Error("offline = false after the server could not be reached")
	}
	if n := len(queued(t, q)); n != 1 {
		t.Errorf("%d entries queued, want 1 kept for later", n)
	}
	if d := time.Since(start); d > flushTimeout {
		t.Errorf("took %v", d)
	}
}

func TestAddAfterFailedFlushSkipsTheNetwork(t *testing.T) {
	q := tempQueue(t)
	b := &fakeBackend{}
	a := &app{b: b, out: io.Discard, queue: q, offline: true}
	if err := a.execute(context.Background(), command{name: "add", summary: "x"}); err != nil {
		t.Fatal(err)
	}
	if len(b.added) != 0 || len(queued(t, q)) != 1 {
		t.Errorf("Add called %d times, %d queued; want 0 and 1", len(b.added), len(queued(t, q)))
	}
}

// flushAgainst queues three captures, of which the first already reached
// the server (its answer was lost), then flushes into b. Afterwards the
// queue must be empty and every task must exist exactly once.
func flushAgainst(t *testing.T, b backend, dav *caldavtest.Backend) {
	t.Helper()
	q := tempQueue(t)
	uids := []string{caldav.NewUID(), caldav.NewUID(), caldav.NewUID()}
	for i, uid := range uids {
		q.Append(queue.Entry{UID: uid, Summary: "capture " + string(rune('A'+i))})
	}
	// The first capture got through before, but t never heard back.
	if _, _, err := b.Add(context.Background(), uids[0], "capture A"); err != nil {
		t.Fatal(err)
	}

	a := &app{b: b, out: io.Discard, queue: q}
	a.flush()

	if left := queued(t, q); len(left) != 0 {
		t.Errorf("still queued: %+v", left)
	}
	if a.offline {
		t.Error("offline = true after a successful flush")
	}
	var got []string
	for _, obj := range dav.Objects[caldavtest.TodoPath] {
		got = append(got, filepath.Base(obj.Path))
	}
	for _, uid := range uids {
		if n := countOf(got, uid+".ics"); n != 1 {
			t.Errorf("task %s stored %d times, want once", uid, n)
		}
	}
}

func countOf(list []string, s string) int {
	n := 0
	for _, x := range list {
		if x == s {
			n++
		}
	}
	return n
}

func TestFlushDirect(t *testing.T) {
	davSrv, dav := caldavtest.NewServer(t)
	client, err := caldav.NewClient(davSrv.URL+"/", caldavtest.User, caldavtest.Password, []string{"Todo"})
	if err != nil {
		t.Fatal(err)
	}
	flushAgainst(t, direct{client: client, defaultList: "Todo"}, dav)
}

func TestFlushViaServer(t *testing.T) {
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
	flushAgainst(t, apiClient, dav)
}

func TestFlushKeepsOrder(t *testing.T) {
	q := tempQueue(t)
	for _, uid := range []string{"a", "b", "c"} {
		q.Append(queue.Entry{UID: uid, Summary: uid})
	}
	b := &fakeBackend{}
	(&app{b: b, out: io.Discard, queue: q}).flush()
	if !slices.Equal(b.added, []string{"a", "b", "c"}) {
		t.Errorf("sent %q, want oldest first", b.added)
	}
}
