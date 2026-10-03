package caldav

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"

	"github.com/Mag1cByt3s/beacon/internal/focus"
)

func newTestClient(t *testing.T) (*Client, *fakeBackend) {
	t.Helper()
	srv, backend := newTestServer(t)
	c, err := NewClient(srv.URL+"/", "alice", "test-password", []string{"Todo"})
	if err != nil {
		t.Fatal(err)
	}
	return c, backend
}

// openTask reads the open tasks and returns the one with the given UID.
func openTask(t *testing.T, c *Client, uid string) focus.Task {
	t.Helper()
	tasks, err := c.OpenTasks(context.Background())
	if err != nil {
		t.Fatalf("OpenTasks: %v", err)
	}
	for _, task := range tasks {
		if task.UID == uid {
			return task
		}
	}
	t.Fatalf("task %q not found", uid)
	return focus.Task{}
}

func TestCreate(t *testing.T) {
	c, backend := newTestClient(t)

	name, err := c.Create(context.Background(), "todo", "Buy coffee, beans; fresh")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if name != "Todo" {
		t.Errorf("list name = %q, want Todo", name)
	}

	objects := backend.objects["/alice/calendars/todo/"]
	if len(objects) != 3 {
		t.Fatalf("todo list has %d objects, want 3", len(objects))
	}
	obj := objects[2]
	todo := obj.Data.Children[0]

	uid := text(todo.Props, ical.PropUID)
	if obj.Path != "/alice/calendars/todo/"+uid+".ics" {
		t.Errorf("path = %q, want it named after UID %q", obj.Path, uid)
	}
	if got := text(todo.Props, ical.PropSummary); got != "Buy coffee, beans; fresh" {
		t.Errorf("SUMMARY = %q", got)
	}
	if got := text(todo.Props, ical.PropStatus); got != "NEEDS-ACTION" {
		t.Errorf("STATUS = %q, want NEEDS-ACTION", got)
	}

	// Exactly these properties, nothing else.
	var names []string
	for name := range todo.Props {
		names = append(names, name)
	}
	slices.Sort(names)
	want := []string{"CREATED", "DTSTAMP", "LAST-MODIFIED", "STATUS", "SUMMARY", "UID"}
	if !slices.Equal(names, want) {
		t.Errorf("properties = %q, want %q", names, want)
	}

	// The new task shows up as open.
	task := openTask(t, c, uid)
	if task.Created.IsZero() {
		t.Error("CREATED was not readable")
	}
}

func TestCreateUnknownList(t *testing.T) {
	c, _ := newTestClient(t)
	_, err := c.Create(context.Background(), "Nope", "x")
	if err == nil || !strings.Contains(err.Error(), "BEACON_DEFAULT_LIST") {
		t.Errorf("err = %v, want a hint about BEACON_DEFAULT_LIST", err)
	}
}

func TestCreateNeverOverwrites(t *testing.T) {
	c, _ := newTestClient(t)
	// Try to "create" on top of an existing object, as a UID collision would.
	err := c.put(context.Background(), "/alice/calendars/todo/1.ics",
		newTodo("open", "x", time.Now()), "If-None-Match", "*")
	if !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

func TestComplete(t *testing.T) {
	c, backend := newTestClient(t)

	// A task as an iPhone might store it, with properties beacon does not know.
	backend.objects["/alice/calendars/todo/"] = append(backend.objects["/alice/calendars/todo/"],
		object(t, "/alice/calendars/todo/rich.ics",
			"BEGIN:VTODO",
			"UID:rich",
			"DTSTAMP:20261001T100000Z",
			"CREATED:20260901T100000Z",
			"LAST-MODIFIED:20261001T100000Z",
			"SUMMARY:Call the bank",
			"DESCRIPTION:About the card",
			"DUE;VALUE=DATE:20261010",
			"PRIORITY:5",
			"STATUS:NEEDS-ACTION",
			"X-APPLE-SORT-ORDER:123456",
			"X-CUSTOM;X-PARAM=keep:me",
			"BEGIN:VALARM",
			"ACTION:DISPLAY",
			"DESCRIPTION:Reminder",
			"TRIGGER;VALUE=DATE-TIME:20261010T080000Z",
			"END:VALARM",
			"END:VTODO",
		))
	before := backend.objects["/alice/calendars/todo/"][2].Data.Children[0]
	beforeProps := ical.Props{}
	for name, values := range before.Props {
		beforeProps[name] = slices.Clone(values)
	}

	task := openTask(t, c, "rich")
	if err := c.Complete(context.Background(), task); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	after, _, _ := backend.find("/alice/calendars/todo/rich.ics")
	todo := after.Data.Children[0]

	if got := text(todo.Props, ical.PropStatus); got != "COMPLETED" {
		t.Errorf("STATUS = %q, want COMPLETED", got)
	}
	if p := todo.Props.Get(ical.PropPercentComplete); p == nil || p.Value != "100" {
		t.Errorf("PERCENT-COMPLETE = %v, want 100", p)
	}
	for _, name := range []string{ical.PropCompleted, ical.PropLastModified, ical.PropDateTimeStamp} {
		got, err := todo.Props.DateTime(name, nil)
		if err != nil || time.Since(got) > time.Minute {
			t.Errorf("%s = %v (%v), want about now", name, got, err)
		}
	}

	// Every other property is exactly as before.
	changed := []string{ical.PropStatus, ical.PropCompleted, ical.PropPercentComplete,
		ical.PropLastModified, ical.PropDateTimeStamp}
	for name, values := range beforeProps {
		if slices.Contains(changed, name) {
			continue
		}
		if !reflect.DeepEqual(todo.Props[name], values) {
			t.Errorf("%s changed: %v -> %v", name, values, todo.Props[name])
		}
	}
	for name := range todo.Props {
		if _, existed := beforeProps[name]; !existed && !slices.Contains(changed, name) {
			t.Errorf("unexpected new property %s", name)
		}
	}
	if len(todo.Children) != 1 || todo.Children[0].Name != ical.CompAlarm {
		t.Errorf("VALARM was lost: children = %v", todo.Children)
	}

	// The task is no longer open.
	tasks, _ := c.OpenTasks(context.Background())
	for _, task := range tasks {
		if task.UID == "rich" {
			t.Error("completed task is still listed as open")
		}
	}
}

func TestCompleteChangedSinceRead(t *testing.T) {
	c, backend := newTestClient(t)
	task := openTask(t, c, "open")

	// Someone edits the task on the phone after beacon listed it.
	editOnPhone(t, backend, "/alice/calendars/todo/1.ics", "Open task, edited")

	err := c.Complete(context.Background(), task)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	assertUntouched(t, backend, "/alice/calendars/todo/1.ics", "Open task, edited")
}

func TestCompleteChangedDuringWrite(t *testing.T) {
	c, backend := newTestClient(t)
	task := openTask(t, c, "open")

	// The edit lands between beacon's GET and its PUT, so only If-Match
	// can catch it.
	backend.afterGet = func(path string) {
		backend.afterGet = nil
		editOnPhone(t, backend, path, "Edited at the worst moment")
	}

	err := c.Complete(context.Background(), task)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	assertUntouched(t, backend, "/alice/calendars/todo/1.ics", "Edited at the worst moment")
}

func TestCompleteRefusesRecurring(t *testing.T) {
	c, backend := newTestClient(t)
	backend.objects["/alice/calendars/todo/"] = append(backend.objects["/alice/calendars/todo/"],
		object(t, "/alice/calendars/todo/weekly.ics",
			"BEGIN:VTODO", "UID:weekly", "DTSTAMP:20261001T100000Z", "SUMMARY:Water plants",
			"RRULE:FREQ=WEEKLY", "END:VTODO"))

	// Refused straight away when the task is known to be recurring ...
	task := openTask(t, c, "weekly")
	if err := c.Complete(context.Background(), task); !errors.Is(err, ErrRecurring) {
		t.Errorf("err = %v, want ErrRecurring", err)
	}

	// ... and also when it only became recurring on the server since.
	task.Recurring = false
	if err := c.Complete(context.Background(), task); !errors.Is(err, ErrRecurring) {
		t.Errorf("err = %v, want ErrRecurring", err)
	}
	if obj, _, _ := backend.find("/alice/calendars/todo/weekly.ics"); obj.ETag != task.ETag {
		t.Error("recurring task was written")
	}
}

func TestNewUID(t *testing.T) {
	a, err := newUID()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newUID()
	if a == b || len(a) != 36 || a[14] != '4' {
		t.Errorf("newUID gave %q and %q", a, b)
	}
}

// editOnPhone replaces an object's summary and gives it a new ETag, as a
// change from another client would.
func editOnPhone(t *testing.T, b *fakeBackend, path, summary string) {
	t.Helper()
	obj, i, ok := b.find(path)
	if !ok {
		t.Fatalf("%s not found", path)
	}
	obj.Data.Children[0].Props.SetText(ical.PropSummary, summary)
	b.version++
	obj.ETag = fmt.Sprintf("phone-edit-%d", b.version)
	calPath := path[:strings.LastIndex(path, "/")+1]
	b.objects[calPath][i] = obj
}

func assertUntouched(t *testing.T, b *fakeBackend, path, wantSummary string) {
	t.Helper()
	obj, _, _ := b.find(path)
	todo := obj.Data.Children[0]
	if got := text(todo.Props, ical.PropSummary); got != wantSummary {
		t.Errorf("SUMMARY = %q, want the phone's edit %q", got, wantSummary)
	}
	if got := text(todo.Props, ical.PropStatus); got == "COMPLETED" {
		t.Error("the task was completed despite the conflict")
	}
}

// Make sure the fake backend still satisfies go-webdav's interface.
var _ caldav.Backend = (*fakeBackend)(nil)
