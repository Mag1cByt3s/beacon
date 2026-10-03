// Package caldavtest runs a small in-memory CalDAV server for tests.
//
// go-webdav's own caldav.Handler serves a fake backend, so code under test
// talks real CalDAV over HTTP without ever touching a real server.
//
// Paths follow go-webdav's layout:
// /alice/ (principal), /alice/calendars/ (home set), /alice/calendars/<name>/.
package caldavtest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
)

// Throwaway credentials the test server accepts. Not secrets.
const (
	User     = "alice"
	Password = "test-password"
)

// Paths of the lists the server starts with.
const (
	TodoPath      = "/alice/calendars/todo/"
	GroceriesPath = "/alice/calendars/groceries/"
)

// Backend is the fake server's storage. Tests may read and change it
// between requests (not during one).
//
// Like a real server, it gives every stored version a new ETag and honours
// If-Match and If-None-Match on PUT.
type Backend struct {
	Calendars []caldav.Calendar
	Objects   map[string][]caldav.CalendarObject // keyed by calendar path

	// AfterGet, if set, runs after an object has been read by GET.
	// Tests use it to simulate an edit on the phone at the worst moment.
	AfterGet func(path string)

	version int // source of new ETags
}

// NewServer starts a CalDAV server with three collections:
//
//	Todo       "Open task" (UID open, at TodoPath+"1.ics") and a completed one
//	Groceries  "Milk" (UID milk)
//	Calendar   events only, so not a task list
//
// It requires basic auth with User and Password, like Radicale does.
// The server is shut down when the test ends.
func NewServer(t testing.TB) (*httptest.Server, *Backend) {
	t.Helper()
	b := &Backend{
		Calendars: []caldav.Calendar{
			{Path: TodoPath, Name: "Todo", SupportedComponentSet: []string{"VTODO"}},
			{Path: GroceriesPath, Name: "Groceries", SupportedComponentSet: []string{"VTODO"}},
			{Path: "/alice/calendars/cal/", Name: "Calendar", SupportedComponentSet: []string{"VEVENT"}},
		},
		Objects: map[string][]caldav.CalendarObject{},
	}
	b.Add(t, TodoPath+"1.ics",
		"BEGIN:VTODO", "UID:open", "DTSTAMP:20261001T100000Z", "SUMMARY:Open task", "END:VTODO")
	b.Add(t, TodoPath+"2.ics",
		"BEGIN:VTODO", "UID:done", "DTSTAMP:20261001T100000Z", "SUMMARY:Done task",
		"STATUS:COMPLETED", "END:VTODO")
	b.Add(t, GroceriesPath+"1.ics",
		"BEGIN:VTODO", "UID:milk", "DTSTAMP:20261001T100000Z", "SUMMARY:Milk", "END:VTODO")

	handler := &caldav.Handler{Backend: b}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != User || pass != Password {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, b
}

// Parse decodes iCalendar lines wrapped in a VCALENDAR.
func Parse(t testing.TB, lines ...string) *ical.Calendar {
	t.Helper()
	all := append([]string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//beacon//test//EN"}, lines...)
	all = append(all, "END:VCALENDAR", "")
	cal, err := ical.NewDecoder(strings.NewReader(strings.Join(all, "\r\n"))).Decode()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return cal
}

// Add stores a new object at path, built from iCalendar lines.
func (b *Backend) Add(t testing.TB, path string, lines ...string) {
	t.Helper()
	b.store(caldav.CalendarObject{Path: path, Data: Parse(t, lines...)})
}

// Find returns the object stored at path.
func (b *Backend) Find(path string) (obj caldav.CalendarObject, ok bool) {
	obj, _, ok = b.find(path)
	return obj, ok
}

// Todo returns the first VTODO of the object at path, or nil.
func (b *Backend) Todo(path string) *ical.Component {
	obj, ok := b.Find(path)
	if !ok {
		return nil
	}
	for _, comp := range obj.Data.Children {
		if comp.Name == ical.CompToDo {
			return comp
		}
	}
	return nil
}

// Edit changes the summary of the object at path and gives it a new ETag,
// as an edit on another device would.
func (b *Backend) Edit(t testing.TB, path, summary string) {
	t.Helper()
	todo := b.Todo(path)
	if todo == nil {
		t.Fatalf("%s not found", path)
	}
	todo.Props.SetText(ical.PropSummary, summary)
	obj, _ := b.Find(path)
	b.store(obj)
}

// Remove deletes the object at path, as deleting it on the phone would.
func (b *Backend) Remove(path string) {
	if _, i, ok := b.find(path); ok {
		calPath := calendarOf(path)
		b.Objects[calPath] = append(b.Objects[calPath][:i], b.Objects[calPath][i+1:]...)
	}
}

// store saves obj under its path with a fresh ETag.
func (b *Backend) store(obj caldav.CalendarObject) caldav.CalendarObject {
	b.version++
	obj.ETag = fmt.Sprintf("v%d", b.version)
	calPath := calendarOf(obj.Path)
	if _, i, ok := b.find(obj.Path); ok {
		b.Objects[calPath][i] = obj
	} else {
		b.Objects[calPath] = append(b.Objects[calPath], obj)
	}
	return obj
}

func (b *Backend) find(path string) (obj caldav.CalendarObject, index int, ok bool) {
	for i, o := range b.Objects[calendarOf(path)] {
		if o.Path == path {
			return o, i, true
		}
	}
	return caldav.CalendarObject{}, 0, false
}

// calendarOf turns "/alice/calendars/todo/1.ics" into "/alice/calendars/todo/".
func calendarOf(path string) string {
	return path[:strings.LastIndex(path, "/")+1]
}

// The methods below implement go-webdav's caldav.Backend interface.

func (b *Backend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return "/alice/", nil
}

func (b *Backend) CalendarHomeSetPath(ctx context.Context) (string, error) {
	return "/alice/calendars/", nil
}

func (b *Backend) ListCalendars(ctx context.Context) ([]caldav.Calendar, error) {
	return b.Calendars, nil
}

func (b *Backend) GetCalendar(ctx context.Context, path string) (*caldav.Calendar, error) {
	for _, cal := range b.Calendars {
		if cal.Path == path {
			return &cal, nil
		}
	}
	return nil, webdav.NewHTTPError(http.StatusNotFound, errors.New("no such calendar"))
}

func (b *Backend) GetCalendarObject(ctx context.Context, path string, req *caldav.CalendarCompRequest) (*caldav.CalendarObject, error) {
	obj, ok := b.Find(path)
	if !ok {
		return nil, webdav.NewHTTPError(http.StatusNotFound, errors.New("no such object"))
	}
	if b.AfterGet != nil {
		// obj is already a copy, so the client still gets the old version.
		b.AfterGet(path)
	}
	return &obj, nil
}

func (b *Backend) ListCalendarObjects(ctx context.Context, path string, req *caldav.CalendarCompRequest) ([]caldav.CalendarObject, error) {
	return b.Objects[path], nil
}

func (b *Backend) QueryCalendarObjects(ctx context.Context, path string, query *caldav.CalendarQuery) ([]caldav.CalendarObject, error) {
	// caldav.Filter applies the query's filter like a real server would.
	return caldav.Filter(query, b.Objects[path])
}

func (b *Backend) PutCalendarObject(ctx context.Context, path string, cal *ical.Calendar, opts *caldav.PutCalendarObjectOptions) (*caldav.CalendarObject, error) {
	old, exists := b.Find(path)
	if opts.IfNoneMatch.IsWildcard() && exists {
		return nil, webdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("already exists"))
	}
	if opts.IfMatch.IsSet() {
		ok, err := opts.IfMatch.MatchETag(old.ETag)
		if err != nil || !ok {
			return nil, webdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("etag mismatch"))
		}
	}
	obj := b.store(caldav.CalendarObject{Path: path, Data: cal})
	return &obj, nil
}

func (b *Backend) CreateCalendar(ctx context.Context, calendar *caldav.Calendar) error {
	return errors.New("not implemented")
}

func (b *Backend) DeleteCalendarObject(ctx context.Context, path string) error {
	b.Remove(path)
	return nil
}

// Make sure Backend satisfies go-webdav's interface.
var _ caldav.Backend = (*Backend)(nil)
