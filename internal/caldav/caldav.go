// Package caldav reads tasks (VTODOs) from a CalDAV server such as Radicale.
package caldav

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"

	"github.com/Mag1cByt3s/beacon/internal/focus"
)

// ErrUnreachable is matched (with errors.Is) by errors that mean the CalDAV
// server could not be reached at all: no network, server down, timeout.
var ErrUnreachable = errors.New("CalDAV server unreachable")

// unreachableError wraps a network error so that errors.Is(err,
// ErrUnreachable) is true, while keeping the original message.
type unreachableError struct{ err error }

func (e unreachableError) Error() string        { return e.err.Error() }
func (e unreachableError) Unwrap() error        { return e.err }
func (e unreachableError) Is(target error) bool { return target == ErrUnreachable }

// errLogin is returned when the server rejects our credentials.
var errLogin = errors.New("CalDAV login failed: check BEACON_CALDAV_USER and BEACON_CALDAV_PASSWORD_CMD")

// Client reads and writes tasks on a CalDAV server.
type Client struct {
	dav   *caldav.Client
	http  *authClient // for requests go-webdav cannot send (conditional PUT)
	base  *url.URL    // server address; paths from the server are resolved against it
	lists []string
}

// NewClient creates a client for the server at endpoint. If user is empty,
// no authentication is sent. lists are the collection names to read.
func NewClient(endpoint, user, password string, lists []string) (*Client, error) {
	httpClient := &authClient{
		http:     &http.Client{Timeout: 20 * time.Second},
		user:     user,
		password: password,
	}
	base, err := url.Parse(endpoint)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, errors.New("BEACON_CALDAV_URL is not a valid URL (example: https://dav.example.org/)")
	}
	dav, err := caldav.NewClient(httpClient, endpoint)
	if err != nil {
		return nil, fmt.Errorf("BEACON_CALDAV_URL is not a valid URL: %w", err)
	}
	return &Client{dav: dav, http: httpClient, base: base, lists: lists}, nil
}

// OpenTasks returns all open tasks from the configured lists, unordered.
// Completed and cancelled tasks are left out. Recurring tasks are included
// and marked with Recurring.
func (c *Client) OpenTasks(ctx context.Context) ([]focus.Task, error) {
	taskLists, err := c.taskLists(ctx)
	if err != nil {
		return nil, err
	}
	var cals []caldav.Calendar
	for _, name := range c.lists {
		cal, err := findList(taskLists, name)
		if err != nil {
			return nil, fmt.Errorf("%w; check BEACON_LISTS", err)
		}
		cals = append(cals, cal)
	}

	// Ask only for calendar objects that contain a VTODO.
	query := &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{
			Name:     ical.CompCalendar,
			AllProps: true,
			AllComps: true,
		},
		CompFilter: caldav.CompFilter{
			Name:  ical.CompCalendar,
			Comps: []caldav.CompFilter{{Name: ical.CompToDo}},
		},
	}

	var tasks []focus.Task
	for _, cal := range cals {
		objects, err := c.dav.QueryCalendar(ctx, cal.Path, query)
		if err != nil {
			return nil, friendlyError(fmt.Errorf("cannot read list %q: %w", cal.Name, err))
		}
		for _, obj := range objects {
			task, ok := taskFromCalendar(obj.Data, cal.Name)
			if ok {
				task.Path = obj.Path
				task.ETag = obj.ETag
				tasks = append(tasks, task)
			}
		}
	}
	return tasks, nil
}

// taskLists returns all collections on the server that can hold tasks.
// Collections without a display name are named after the last part of
// their URL path.
func (c *Client) taskLists(ctx context.Context) ([]caldav.Calendar, error) {
	principal, err := c.dav.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return nil, friendlyError(fmt.Errorf("cannot reach CalDAV server: %w", err))
	}
	homeSet, err := c.dav.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		return nil, friendlyError(fmt.Errorf("cannot find your CalDAV collections: %w", err))
	}
	all, err := c.dav.FindCalendars(ctx, homeSet)
	if err != nil {
		return nil, friendlyError(fmt.Errorf("cannot list your CalDAV collections: %w", err))
	}

	var taskCals []caldav.Calendar
	for _, cal := range all {
		if supportsTasks(cal) {
			if cal.Name == "" {
				cal.Name = path.Base(strings.TrimSuffix(cal.Path, "/"))
			}
			taskCals = append(taskCals, cal)
		}
	}
	return taskCals, nil
}

// findList picks the collection called name. Matching ignores case and also
// accepts the last part of the URL path.
func findList(cals []caldav.Calendar, name string) (caldav.Calendar, error) {
	for _, cal := range cals {
		base := path.Base(strings.TrimSuffix(cal.Path, "/"))
		if strings.EqualFold(cal.Name, name) || strings.EqualFold(base, name) {
			return cal, nil
		}
	}
	return caldav.Calendar{}, fmt.Errorf("list %q not found on the CalDAV server (task lists there: %s)",
		name, listNames(cals))
}

// supportsTasks reports whether a collection can contain VTODOs. A server
// that does not say which components it supports is assumed to allow all.
func supportsTasks(cal caldav.Calendar) bool {
	if len(cal.SupportedComponentSet) == 0 {
		return true
	}
	return slices.Contains(cal.SupportedComponentSet, ical.CompToDo)
}

func listNames(cals []caldav.Calendar) string {
	if len(cals) == 0 {
		return "none"
	}
	names := make([]string, len(cals))
	for i, cal := range cals {
		names[i] = cal.Name
	}
	return strings.Join(names, ", ")
}

// friendlyError replaces a long wrapped error with errLogin if that was the
// real cause, so the user sees what to fix.
func friendlyError(err error) error {
	if errors.Is(err, errLogin) {
		return errLogin
	}
	return err
}

// authClient adds basic auth to every request and turns HTTP 401 into
// errLogin. It satisfies the webdav.HTTPClient interface, which only needs
// a Do method.
type authClient struct {
	http     *http.Client
	user     string
	password string
}

func (c *authClient) Do(req *http.Request) (*http.Response, error) {
	if c.user != "" {
		req.SetBasicAuth(c.user, c.password)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// http.Client only returns an error when there was no HTTP answer.
		return nil, unreachableError{err}
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		return nil, errLogin
	}
	return resp, nil
}
