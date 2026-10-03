package caldav

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
)

// fakeBackend is a tiny in-memory CalDAV server backend. go-webdav's own
// caldav.Handler turns it into a real HTTP server for the tests, so the
// client is tested against genuine CalDAV requests and responses.
//
// Paths follow go-webdav's layout:
// /alice/ (principal), /alice/calendars/ (home set), /alice/calendars/<name>/.
//
// Like a real server, it gives every stored version a new ETag and honours
// If-Match and If-None-Match on PUT.
type fakeBackend struct {
	calendars []caldav.Calendar
	objects   map[string][]caldav.CalendarObject // keyed by calendar path
	version   int                                // source of new ETags

	// afterGet, if set, runs after an object has been served by GET.
	// Tests use it to simulate an edit on the phone at the worst moment.
	afterGet func(path string)
}

func (b *fakeBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return "/alice/", nil
}

func (b *fakeBackend) CalendarHomeSetPath(ctx context.Context) (string, error) {
	return "/alice/calendars/", nil
}

func (b *fakeBackend) ListCalendars(ctx context.Context) ([]caldav.Calendar, error) {
	return b.calendars, nil
}

func (b *fakeBackend) GetCalendar(ctx context.Context, path string) (*caldav.Calendar, error) {
	for _, cal := range b.calendars {
		if cal.Path == path {
			return &cal, nil
		}
	}
	return nil, errors.New("not found")
}

func (b *fakeBackend) QueryCalendarObjects(ctx context.Context, path string, query *caldav.CalendarQuery) ([]caldav.CalendarObject, error) {
	// caldav.Filter applies the query's filter like a real server would.
	return caldav.Filter(query, b.objects[path])
}

func (b *fakeBackend) ListCalendarObjects(ctx context.Context, path string, req *caldav.CalendarCompRequest) ([]caldav.CalendarObject, error) {
	return b.objects[path], nil
}

func (b *fakeBackend) GetCalendarObject(ctx context.Context, path string, req *caldav.CalendarCompRequest) (*caldav.CalendarObject, error) {
	obj, _, ok := b.find(path)
	if !ok {
		return nil, webdav.NewHTTPError(http.StatusNotFound, errors.New("no such object"))
	}
	if b.afterGet != nil {
		// obj is already a copy, so the client still gets the old version.
		b.afterGet(path)
	}
	return &obj, nil
}

func (b *fakeBackend) PutCalendarObject(ctx context.Context, path string, cal *ical.Calendar, opts *caldav.PutCalendarObjectOptions) (*caldav.CalendarObject, error) {
	old, i, exists := b.find(path)
	if opts.IfNoneMatch.IsWildcard() && exists {
		return nil, webdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("already exists"))
	}
	if opts.IfMatch.IsSet() {
		ok, err := opts.IfMatch.MatchETag(old.ETag)
		if err != nil || !ok {
			return nil, webdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("etag mismatch"))
		}
	}

	b.version++
	obj := caldav.CalendarObject{Path: path, ETag: fmt.Sprintf("v%d", b.version), Data: cal}
	calPath := path[:strings.LastIndex(path, "/")+1]
	if exists {
		b.objects[calPath][i] = obj
	} else {
		b.objects[calPath] = append(b.objects[calPath], obj)
	}
	return &obj, nil
}

// find returns the object stored at path and its index in its calendar.
func (b *fakeBackend) find(path string) (obj caldav.CalendarObject, index int, ok bool) {
	calPath := path[:strings.LastIndex(path, "/")+1]
	for i, o := range b.objects[calPath] {
		if o.Path == path {
			return o, i, true
		}
	}
	return caldav.CalendarObject{}, 0, false
}

// The methods below are not used by beacon.

func (b *fakeBackend) CreateCalendar(ctx context.Context, calendar *caldav.Calendar) error {
	return errors.New("not implemented")
}

func (b *fakeBackend) DeleteCalendarObject(ctx context.Context, path string) error {
	return errors.New("not implemented")
}

// object builds a calendar object resource from iCalendar lines.
func object(t *testing.T, path string, lines ...string) caldav.CalendarObject {
	return caldav.CalendarObject{Path: path, ETag: "x", Data: parse(t, lines...)}
}

func newTestServer(t *testing.T) (*httptest.Server, *fakeBackend) {
	t.Helper()
	backend := &fakeBackend{
		calendars: []caldav.Calendar{
			{Path: "/alice/calendars/todo/", Name: "Todo", SupportedComponentSet: []string{"VTODO"}},
			{Path: "/alice/calendars/groceries/", Name: "Groceries", SupportedComponentSet: []string{"VTODO"}},
			{Path: "/alice/calendars/cal/", Name: "Calendar", SupportedComponentSet: []string{"VEVENT"}},
		},
		objects: map[string][]caldav.CalendarObject{
			"/alice/calendars/todo/": {
				object(t, "/alice/calendars/todo/1.ics",
					"BEGIN:VTODO", "UID:open", "DTSTAMP:20261001T100000Z", "SUMMARY:Open task", "END:VTODO"),
				object(t, "/alice/calendars/todo/2.ics",
					"BEGIN:VTODO", "UID:done", "DTSTAMP:20261001T100000Z", "SUMMARY:Done task",
					"STATUS:COMPLETED", "END:VTODO"),
			},
			"/alice/calendars/groceries/": {
				object(t, "/alice/calendars/groceries/1.ics",
					"BEGIN:VTODO", "UID:milk", "DTSTAMP:20261001T100000Z", "SUMMARY:Milk", "END:VTODO"),
			},
		},
	}
	handler := &caldav.Handler{Backend: backend}

	// Require basic auth like Radicale does. These are throwaway test values.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "alice" || pass != "test-password" {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, backend
}

func uids(t *testing.T, c *Client) []string {
	t.Helper()
	tasks, err := c.OpenTasks(context.Background())
	if err != nil {
		t.Fatalf("OpenTasks: %v", err)
	}
	var got []string
	for _, task := range tasks {
		got = append(got, task.List+"/"+task.UID)
	}
	slices.Sort(got)
	return got
}

func TestOpenTasks(t *testing.T) {
	srv, _ := newTestServer(t)
	c, err := NewClient(srv.URL+"/", "alice", "test-password", []string{"todo"})
	if err != nil {
		t.Fatal(err)
	}
	got := uids(t, c)
	want := []string{"Todo/open"}
	if !slices.Equal(got, want) {
		t.Errorf("tasks = %q, want %q", got, want)
	}
}

func TestOpenTasksSeveralLists(t *testing.T) {
	srv, _ := newTestServer(t)
	c, err := NewClient(srv.URL+"/", "alice", "test-password", []string{"Todo", "Groceries"})
	if err != nil {
		t.Fatal(err)
	}
	got := uids(t, c)
	want := []string{"Groceries/milk", "Todo/open"}
	if !slices.Equal(got, want) {
		t.Errorf("tasks = %q, want %q", got, want)
	}
}

func TestOpenTasksUnknownList(t *testing.T) {
	srv, _ := newTestServer(t)
	c, _ := NewClient(srv.URL+"/", "alice", "test-password", []string{"Nope"})
	_, err := c.OpenTasks(context.Background())
	if err == nil {
		t.Fatal("OpenTasks succeeded, want an error")
	}
	// The error should name the available task lists, but not the VEVENT calendar.
	msg := err.Error()
	if !strings.Contains(msg, `"Nope"`) || !strings.Contains(msg, "Todo, Groceries") || strings.Contains(msg, "Calendar") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestOpenTasksEventCalendarIsNotATaskList(t *testing.T) {
	srv, _ := newTestServer(t)
	c, _ := NewClient(srv.URL+"/", "alice", "test-password", []string{"Calendar"})
	if _, err := c.OpenTasks(context.Background()); err == nil {
		t.Error("OpenTasks accepted a calendar that cannot hold tasks")
	}
}

func TestOpenTasksWrongPassword(t *testing.T) {
	srv, _ := newTestServer(t)
	c, _ := NewClient(srv.URL+"/", "alice", "wrong", []string{"Todo"})
	_, err := c.OpenTasks(context.Background())
	if !errors.Is(err, errLogin) {
		t.Errorf("err = %v, want errLogin", err)
	}
}
