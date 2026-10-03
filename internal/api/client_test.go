// This is an external test package (api_test), so it may import the server,
// which itself imports api, without an import cycle.
package api_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mag1cByt3s/beacon/internal/api"
	"github.com/Mag1cByt3s/beacon/internal/caldav"
	"github.com/Mag1cByt3s/beacon/internal/caldav/caldavtest"
	"github.com/Mag1cByt3s/beacon/internal/server"
	"github.com/Mag1cByt3s/beacon/internal/store"
)

const testToken = "test-token-0123456789" // not a secret

// newServer runs a real beacon server on a fake CalDAV server, with the
// lists Todo and Groceries (open tasks: "milk" first, then "open").
func newServer(t *testing.T) (*httptest.Server, *caldavtest.Backend) {
	t.Helper()
	davSrv, backend := caldavtest.NewServer(t)
	dav, err := caldav.NewClient(davSrv.URL+"/", caldavtest.User, caldavtest.Password, []string{"Todo", "Groceries"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "beacon.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(server.New(dav, st, testToken, "Todo", logger).Handler())
	t.Cleanup(srv.Close)
	return srv, backend
}

func newClient(t *testing.T, url, token string) *api.Client {
	t.Helper()
	c, err := api.NewClient(url, token)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestClient(t *testing.T) {
	srv, backend := newServer(t)
	c := newClient(t, srv.URL, testToken)
	ctx := context.Background()

	cur, ok, err := c.Current(ctx)
	if err != nil || !ok || cur.UID != "milk" || cur.List != "Groceries" {
		t.Fatalf("Current = %+v, %v, %v; want milk", cur, ok, err)
	}

	uid := caldav.NewUID()
	list, err := c.Add(ctx, uid, "buy coffee")
	if err != nil || list != "Todo" {
		t.Errorf("Add = %q, %v; want Todo", list, err)
	}
	// Retrying the same capture never creates a duplicate.
	if _, err := c.Add(ctx, uid, "buy coffee"); !errors.Is(err, api.ErrExists) {
		t.Errorf("retry: err = %v, want ErrExists", err)
	}

	tasks, err := c.Tasks(ctx)
	if err != nil || len(tasks) != 3 || tasks[0].UID != "milk" {
		t.Errorf("Tasks = %+v, %v; want 3 tasks, milk first", tasks, err)
	}

	// The new task's UID is an upper-case UUID, which sorts before "open".
	next, ok, err := c.Skip(ctx)
	if err != nil || !ok || next.Summary != "buy coffee" {
		t.Fatalf("Skip = %+v, %v, %v; want buy coffee next", next, ok, err)
	}
	coffeePath := caldavtest.TodoPath + next.UID + ".ics"

	// milk was skipped, so after buy coffee comes open.
	next, ok, err = c.Done(ctx)
	if err != nil || !ok || next.UID != "open" {
		t.Errorf("Done = %+v, %v, %v; want open next", next, ok, err)
	}
	if s, _ := backend.Todo(coffeePath).Props.Text("STATUS"); s != "COMPLETED" {
		t.Errorf("buy coffee STATUS = %q, want COMPLETED", s)
	}
}

func TestClientWrongToken(t *testing.T) {
	srv, _ := newServer(t)
	c := newClient(t, srv.URL, "wrong-token-0123456789")
	if _, _, err := c.Current(context.Background()); !errors.Is(err, api.ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

func TestClientConflict(t *testing.T) {
	srv, backend := newServer(t)
	c := newClient(t, srv.URL, testToken)
	ctx := context.Background()

	c.Current(ctx) // milk becomes current
	backend.Edit(t, caldavtest.GroceriesPath+"1.ics", "Oat milk")

	_, _, err := c.Done(ctx)
	// errors.As finds a *ConflictError anywhere in err's chain.
	var conflict *api.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want a *ConflictError", err)
	}
	if conflict.Current == nil || conflict.Current.Summary != "Oat milk" {
		t.Errorf("Current = %+v, want the edited task", conflict.Current)
	}
}

func TestClientUnreachable(t *testing.T) {
	// A server that is started and stopped again leaves a free address.
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()

	c := newClient(t, srv.URL, testToken)
	_, _, err := c.Current(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cannot reach the beacon server") {
		t.Errorf("err = %v, want a 'cannot reach' message", err)
	}
	if !errors.Is(err, api.ErrUnreachable) {
		t.Errorf("err = %v, want it to match ErrUnreachable", err)
	}
}

func TestClientCalDAVUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"cannot reach CalDAV server"}`))
	}))
	defer srv.Close()

	c := newClient(t, srv.URL, testToken)
	_, err := c.Add(context.Background(), caldav.NewUID(), "x")
	if !errors.Is(err, api.ErrUnreachable) || err.Error() != "beacon server: cannot reach CalDAV server" {
		t.Errorf("err = %v, want ErrUnreachable with the server's message", err)
	}
}

func TestClientServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`{"error":"cannot reach CalDAV server"}`))
	}))
	defer srv.Close()

	c := newClient(t, srv.URL, testToken)
	_, _, err := c.Current(context.Background())
	if err == nil || err.Error() != "beacon server: cannot reach CalDAV server" {
		t.Errorf("err = %v", err)
	}
}

func TestNewClientBadURL(t *testing.T) {
	for _, u := range []string{"", "beacon.example.org", "://x"} {
		if _, err := api.NewClient(u, testToken); err == nil {
			t.Errorf("NewClient(%q) succeeded", u)
		}
	}
}
