package caldav

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/Mag1cByt3s/beacon/internal/focus"
)

// ErrConflict means the task was changed on the server (for example on the
// phone) after beacon read it. Nothing was written.
var ErrConflict = errors.New("the task was changed elsewhere since it was read, so it was left alone; run t focus to see it again")

// ErrRecurring means beacon refused to complete a recurring task.
var ErrRecurring = errors.New("recurring tasks are completed on the phone, not with beacon")

// Create adds a new task with the given title to the list called list.
// It returns the list's name as shown on the server.
func (c *Client) Create(ctx context.Context, list, summary string) (string, error) {
	taskLists, err := c.taskLists(ctx)
	if err != nil {
		return "", err
	}
	cal, err := findList(taskLists, list)
	if err != nil {
		return "", fmt.Errorf("%w; check BEACON_DEFAULT_LIST", err)
	}

	uid, err := newUID()
	if err != nil {
		return "", err
	}
	objectPath := strings.TrimSuffix(cal.Path, "/") + "/" + uid + ".ics"

	// "If-None-Match: *" tells the server to refuse if the file already
	// exists, so a new task can never overwrite another one.
	err = c.put(ctx, objectPath, newTodo(uid, summary, time.Now()), "If-None-Match", "*")
	if err != nil {
		return "", friendlyError(fmt.Errorf("cannot add the task: %w", err))
	}
	return cal.Name, nil
}

// Complete marks task as done on the server. task must come from OpenTasks,
// because its Path and ETag say which version was read.
//
// Only the completion fields change; every other property, including
// unknown X- ones, is written back as it was. If the task changed on the
// server since it was read, nothing is written and ErrConflict is returned.
func (c *Client) Complete(ctx context.Context, task focus.Task) error {
	if task.Recurring {
		return ErrRecurring
	}
	if task.Path == "" || task.ETag == "" {
		return errors.New("cannot complete a task that was not read from the server")
	}

	// Fetch the full, current version of the task.
	obj, err := c.dav.GetCalendarObject(ctx, task.Path)
	if err != nil {
		return friendlyError(fmt.Errorf("cannot read the task: %w", err))
	}
	if obj.ETag != task.ETag {
		return ErrConflict
	}

	if err := markCompleted(obj.Data, time.Now()); err != nil {
		return err
	}

	// "If-Match" makes the server refuse the write if the task changed
	// between our read and this write.
	err = c.put(ctx, task.Path, obj.Data, "If-Match", quoteETag(task.ETag))
	if err != nil {
		return friendlyError(fmt.Errorf("cannot complete the task: %w", err))
	}
	return nil
}

// newTodo builds a calendar object holding one new VTODO with only the
// properties a new task needs.
func newTodo(uid, summary string, now time.Time) *ical.Calendar {
	now = now.UTC()

	todo := ical.NewComponent(ical.CompToDo)
	todo.Props.SetText(ical.PropUID, uid)
	todo.Props.SetText(ical.PropSummary, summary)
	todo.Props.SetText(ical.PropStatus, "NEEDS-ACTION")
	todo.Props.SetDateTime(ical.PropCreated, now)
	todo.Props.SetDateTime(ical.PropDateTimeStamp, now)
	todo.Props.SetDateTime(ical.PropLastModified, now)

	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//beacon//beacon//EN")
	cal.Children = append(cal.Children, todo)
	return cal
}

// markCompleted sets the completion fields on the VTODO in cal and leaves
// everything else untouched. It refuses recurring tasks.
func markCompleted(cal *ical.Calendar, now time.Time) error {
	now = now.UTC()

	var todos []*ical.Component
	for _, comp := range cal.Children {
		if comp.Name == ical.CompToDo {
			todos = append(todos, comp)
		}
	}
	if len(todos) == 0 {
		return errors.New("the task is no longer a to-do on the server")
	}
	// More than one VTODO means a recurring task with overrides.
	if len(todos) > 1 {
		return ErrRecurring
	}

	props := todos[0].Props
	if props.Get(ical.PropRecurrenceRule) != nil || props.Get(ical.PropRecurrenceID) != nil {
		return ErrRecurring
	}

	props.SetText(ical.PropStatus, "COMPLETED")
	props.SetDateTime(ical.PropCompleted, now)
	percent := ical.NewProp(ical.PropPercentComplete)
	percent.SetValueType(ical.ValueInt)
	percent.Value = "100"
	props.Set(percent)
	props.SetDateTime(ical.PropLastModified, now)
	props.SetDateTime(ical.PropDateTimeStamp, now)
	return nil
}

// put uploads cal to objectPath with one conditional header (If-Match or
// If-None-Match). go-webdav's own PutCalendarObject cannot send those yet.
func (c *Client) put(ctx context.Context, objectPath string, cal *ical.Calendar, condition, value string) error {
	var body bytes.Buffer
	if err := ical.NewEncoder(&body).Encode(cal); err != nil {
		return err
	}

	// Paths from the server are absolute ("/pascal/todo/x.ics"), so they
	// replace the path of the base URL.
	target := c.base.ResolveReference(&url.URL{Path: objectPath})
	// NewRequestWithContext ties the request to ctx, so it is cancelled
	// when ctx times out.
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target.String(), &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", ical.MIMEType+"; charset=utf-8")
	req.Header.Set(condition, value)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusPreconditionFailed:
		return ErrConflict
	case resp.StatusCode/100 != 2:
		// Include a little of the server's explanation, if any.
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("server answered %s %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// quoteETag turns an ETag back into its header form. go-webdav hands out
// ETags without the surrounding quotes, but If-Match needs them.
func quoteETag(etag string) string {
	if strings.HasPrefix(etag, `"`) || strings.HasPrefix(etag, `W/"`) {
		return etag
	}
	return `"` + etag + `"`
}

// newUID returns a random UUID (version 4), the usual form for VTODO UIDs.
func newUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("cannot create a task ID: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%X-%X-%X-%X-%X", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
