// Package caldav reads tasks (VTODOs) from a CalDAV server such as Radicale.
package caldav

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"

	"github.com/Mag1cByt3s/beacon/internal/focus"
)

// errLogin is returned when the server rejects our credentials.
var errLogin = errors.New("CalDAV login failed: check BEACON_CALDAV_USER and BEACON_CALDAV_PASSWORD_CMD")

// Client reads tasks from the configured lists on a CalDAV server.
type Client struct {
	dav   *caldav.Client
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
	dav, err := caldav.NewClient(httpClient, endpoint)
	if err != nil {
		return nil, fmt.Errorf("BEACON_CALDAV_URL is not a valid URL: %w", err)
	}
	return &Client{dav: dav, lists: lists}, nil
}

// OpenTasks returns all open tasks from the configured lists, unordered.
// Completed and cancelled tasks are left out. Recurring tasks are included
// and marked with Recurring.
func (c *Client) OpenTasks(ctx context.Context) ([]focus.Task, error) {
	cals, err := c.findLists(ctx)
	if err != nil {
		return nil, err
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
				tasks = append(tasks, task)
			}
		}
	}
	return tasks, nil
}

// findLists returns the collections whose name matches one of c.lists.
// Matching ignores case and also accepts the last part of the URL path,
// because some collections have no display name.
func (c *Client) findLists(ctx context.Context) ([]caldav.Calendar, error) {
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

	// Only collections that can hold tasks are relevant.
	var taskCals []caldav.Calendar
	for _, cal := range all {
		if supportsTasks(cal) {
			if cal.Name == "" {
				cal.Name = path.Base(strings.TrimSuffix(cal.Path, "/"))
			}
			taskCals = append(taskCals, cal)
		}
	}

	var found []caldav.Calendar
	for _, want := range c.lists {
		match := false
		for _, cal := range taskCals {
			base := path.Base(strings.TrimSuffix(cal.Path, "/"))
			if strings.EqualFold(cal.Name, want) || strings.EqualFold(base, want) {
				found = append(found, cal)
				match = true
			}
		}
		if !match {
			return nil, fmt.Errorf("list %q not found on the CalDAV server (task lists there: %s); check BEACON_LISTS",
				want, listNames(taskCals))
		}
	}
	return found, nil
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
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		return nil, errLogin
	}
	return resp, nil
}
