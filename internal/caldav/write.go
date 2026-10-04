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

// ErrInvalidUID means a UID for a new task contains characters that are not
// safe in a file name on the server.
var ErrInvalidUID = errors.New("invalid task uid: use letters, digits, '-', '_', '.' and '@' (at most 128)")

// ErrGone means the task no longer exists on the server (it was deleted
// elsewhere).
var ErrGone = errors.New("the task no longer exists on the server")

// ErrExists means a task with this UID already exists. When a capture is
// retried with the same UID, this means the first attempt got through.
var ErrExists = errors.New("a task with this uid already exists")

// Create adds a new task with the given UID and title to the list called
// list. It returns the list's name as shown on the server and the new
// task's ETag (empty if the server did not send one). Make the UID with
// NewUID. Because the UID decides the file name and existing files are
// never overwritten, retrying with the same UID cannot create a duplicate:
// the retry fails with ErrExists instead.
func (c *Client) Create(ctx context.Context, list, uid, summary string) (listName, etag string, err error) {
	if !ValidUID(uid) {
		return "", "", ErrInvalidUID
	}
	taskLists, err := c.taskLists(ctx)
	if err != nil {
		return "", "", err
	}
	cal, err := findList(taskLists, list)
	if err != nil {
		return "", "", fmt.Errorf("%w; check BEACON_DEFAULT_LIST", err)
	}

	objectPath := strings.TrimSuffix(cal.Path, "/") + "/" + uid + ".ics"

	// "If-None-Match: *" tells the server to refuse if the file already
	// exists, so a new task can never overwrite another one.
	etag, err = c.put(ctx, objectPath, newTodo(uid, summary, time.Now()), "If-None-Match", "*")
	if errors.Is(err, ErrConflict) {
		return "", "", ErrExists
	}
	if err != nil {
		return "", "", friendlyError(fmt.Errorf("cannot add the task: %w", err))
	}
	return cal.Name, etag, nil
}

// Delete removes task from the server for good. task must come from
// OpenTasks; its ETag says which version may be deleted. If the task was
// changed elsewhere since, nothing is deleted and ErrConflict is returned;
// if it is already gone, ErrGone. Recurring tasks are refused.
func (c *Client) Delete(ctx context.Context, task focus.Task) error {
	if task.Recurring {
		return ErrRecurring
	}
	if task.Path == "" || task.ETag == "" {
		return errors.New("cannot delete a task that was not read from the server")
	}

	// "If-Match" makes the server refuse if the task changed since it was
	// read, so a task edited on the phone is never deleted by accident.
	_, err := c.send(ctx, http.MethodDelete, task.Path, nil, "If-Match", quoteETag(task.ETag))
	if err != nil && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrGone) {
		return friendlyError(fmt.Errorf("cannot remove the task: %w", err))
	}
	return err
}

// Complete marks task as done on the server. task must come from OpenTasks,
// because its Path and ETag say which version was read.
//
// Only the completion fields change; every other property, including
// unknown X- ones, is written back as it was. If the task changed on the
// server since it was read, nothing is written and ErrConflict is returned.
func (c *Client) Complete(ctx context.Context, task focus.Task) error {
	_, err := c.change(ctx, task, "complete", func(cal *ical.Calendar) error {
		return markCompleted(cal, time.Now())
	})
	return err
}

// Rename changes the title of task to summary and returns the task's new
// ETag. Like Complete, it changes nothing else (only LAST-MODIFIED and
// DTSTAMP are updated) and refuses with ErrConflict if the task changed on
// the server since it was read.
func (c *Client) Rename(ctx context.Context, task focus.Task, summary string) (string, error) {
	summary = strings.Join(strings.Fields(summary), " ")
	if summary == "" {
		return "", errors.New("the new title is empty")
	}
	return c.change(ctx, task, "rename", func(cal *ical.Calendar) error {
		return renameTodo(cal, summary, time.Now())
	})
}

// change fetches the current version of task, applies edit to it and
// writes it back. It refuses with ErrConflict if the task changed since
// it was read, and returns the new ETag. verb names the action in errors.
func (c *Client) change(ctx context.Context, task focus.Task, verb string, edit func(*ical.Calendar) error) (string, error) {
	if task.Recurring {
		return "", ErrRecurring
	}
	if task.Path == "" || task.ETag == "" {
		return "", fmt.Errorf("cannot %s a task that was not read from the server", verb)
	}

	// Fetch the full, current version of the task.
	obj, err := c.dav.GetCalendarObject(ctx, task.Path)
	if err != nil {
		return "", friendlyError(fmt.Errorf("cannot read the task: %w", err))
	}
	if obj.ETag != task.ETag {
		return "", ErrConflict
	}

	if err := edit(obj.Data); err != nil {
		return "", err
	}

	// "If-Match" makes the server refuse the write if the task changed
	// between our read and this write.
	etag, err := c.put(ctx, task.Path, obj.Data, "If-Match", quoteETag(task.ETag))
	if err != nil {
		return "", friendlyError(fmt.Errorf("cannot %s the task: %w", verb, err))
	}
	return etag, nil
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
	todo, err := soleTodo(cal)
	if err != nil {
		return err
	}
	now = now.UTC()
	props := todo.Props
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

// renameTodo sets the title of the VTODO in cal and leaves everything else
// untouched. It refuses recurring tasks.
func renameTodo(cal *ical.Calendar, summary string, now time.Time) error {
	todo, err := soleTodo(cal)
	if err != nil {
		return err
	}
	now = now.UTC()
	todo.Props.SetText(ical.PropSummary, summary)
	todo.Props.SetDateTime(ical.PropLastModified, now)
	todo.Props.SetDateTime(ical.PropDateTimeStamp, now)
	return nil
}

// soleTodo returns the one VTODO in cal. A recurring task (an RRULE, or
// several VTODOs for its occurrences) gives ErrRecurring.
func soleTodo(cal *ical.Calendar) (*ical.Component, error) {
	var todos []*ical.Component
	for _, comp := range cal.Children {
		if comp.Name == ical.CompToDo {
			todos = append(todos, comp)
		}
	}
	if len(todos) == 0 {
		return nil, errors.New("the task is no longer a to-do on the server")
	}
	// More than one VTODO means a recurring task with overrides.
	if len(todos) > 1 {
		return nil, ErrRecurring
	}
	props := todos[0].Props
	if props.Get(ical.PropRecurrenceRule) != nil || props.Get(ical.PropRecurrenceID) != nil {
		return nil, ErrRecurring
	}
	return todos[0], nil
}

// put uploads cal to objectPath with one conditional header (If-Match or
// If-None-Match) and returns the new ETag. go-webdav's own
// PutCalendarObject cannot send those headers yet.
func (c *Client) put(ctx context.Context, objectPath string, cal *ical.Calendar, condition, value string) (string, error) {
	var body bytes.Buffer
	if err := ical.NewEncoder(&body).Encode(cal); err != nil {
		return "", err
	}
	return c.send(ctx, http.MethodPut, objectPath, &body, condition, value)
}

// send makes one PUT or DELETE request with one conditional header and
// returns the ETag from the answer, if any. A failed condition (412)
// becomes ErrConflict, a missing object on DELETE (404) ErrGone.
func (c *Client) send(ctx context.Context, method, objectPath string, body io.Reader, condition, value string) (string, error) {
	// Paths from the server are absolute ("/pascal/todo/x.ics"), so they
	// replace the path of the base URL.
	target := c.base.ResolveReference(&url.URL{Path: objectPath})
	// NewRequestWithContext ties the request to ctx, so it is cancelled
	// when ctx times out.
	req, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", ical.MIMEType+"; charset=utf-8")
	}
	req.Header.Set(condition, value)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusPreconditionFailed:
		return "", ErrConflict
	case resp.StatusCode == http.StatusNotFound && method == http.MethodDelete:
		return "", ErrGone
	case resp.StatusCode/100 != 2:
		// Include a little of the server's explanation, if any.
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return "", fmt.Errorf("server answered %s %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return unquoteETag(resp.Header.Get("ETag")), nil
}

// unquoteETag turns an ETag header ("\"abc\"") into the form go-webdav
// uses (abc). Weak ETags (W/"abc") are kept as they are.
func unquoteETag(etag string) string {
	if len(etag) >= 2 && strings.HasPrefix(etag, `"`) && strings.HasSuffix(etag, `"`) {
		return etag[1 : len(etag)-1]
	}
	return etag
}

// quoteETag turns an ETag back into its header form. go-webdav hands out
// ETags without the surrounding quotes, but If-Match needs them.
func quoteETag(etag string) string {
	if strings.HasPrefix(etag, `"`) || strings.HasPrefix(etag, `W/"`) {
		return etag
	}
	return `"` + etag + `"`
}

// NewUID returns a random UUID (version 4), the usual form for VTODO UIDs.
func NewUID() string {
	var b [16]byte
	rand.Read(b[:])         // never fails since Go 1.24
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%X-%X-%X-%X-%X", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ValidUID reports whether uid is safe to use as a file name for a new
// task. It must not be empty, start with a dot, or contain a slash.
func ValidUID(uid string) bool {
	if uid == "" || len(uid) > 128 || uid[0] == '.' {
		return false
	}
	for _, r := range uid {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '-' || r == '_' || r == '.' || r == '@'
		if !ok {
			return false
		}
	}
	return true
}
