package caldav

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/caldav/caldavtest"
)

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
	srv, _ := caldavtest.NewServer(t)
	c, err := NewClient(srv.URL+"/", caldavtest.User, caldavtest.Password, []string{"todo"})
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
	srv, _ := caldavtest.NewServer(t)
	c, err := NewClient(srv.URL+"/", caldavtest.User, caldavtest.Password, []string{"Todo", "Groceries"})
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
	srv, _ := caldavtest.NewServer(t)
	c, _ := NewClient(srv.URL+"/", caldavtest.User, caldavtest.Password, []string{"Nope"})
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
	srv, _ := caldavtest.NewServer(t)
	c, _ := NewClient(srv.URL+"/", caldavtest.User, caldavtest.Password, []string{"Calendar"})
	if _, err := c.OpenTasks(context.Background()); err == nil {
		t.Error("OpenTasks accepted a calendar that cannot hold tasks")
	}
}

func TestOpenTasksWrongPassword(t *testing.T) {
	srv, _ := caldavtest.NewServer(t)
	c, _ := NewClient(srv.URL+"/", caldavtest.User, "wrong", []string{"Todo"})
	_, err := c.OpenTasks(context.Background())
	if !errors.Is(err, errLogin) {
		t.Errorf("err = %v, want errLogin", err)
	}
}

func TestCompleted(t *testing.T) {
	srv, backend := caldavtest.NewServer(t)
	add := func(name string, lines ...string) {
		all := append([]string{"BEGIN:VTODO", "UID:" + name, "DTSTAMP:20261001T100000Z", "SUMMARY:" + name}, lines...)
		backend.Add(t, caldavtest.TodoPath+name+".ics", append(all, "END:VTODO")...)
	}
	add("in-range", "STATUS:COMPLETED", "COMPLETED:20261013T150000Z")
	add("at-start", "STATUS:COMPLETED", "COMPLETED:20261013T020000Z") // 04:00 Berlin, included
	add("at-end", "STATUS:COMPLETED", "COMPLETED:20261014T020000Z")   // next day starts, excluded
	add("yesterday", "STATUS:COMPLETED", "COMPLETED:20261012T150000Z")
	add("no-status", "COMPLETED:20261013T160000Z") // done by COMPLETED alone
	add("cancelled", "STATUS:CANCELLED", "COMPLETED:20261013T150000Z")
	add("weekly", "STATUS:COMPLETED", "COMPLETED:20261013T150000Z", "RRULE:FREQ=WEEKLY")
	add("open-one", "STATUS:NEEDS-ACTION")
	// "done" from caldavtest is completed without a COMPLETED time: left out.

	c, err := NewClient(srv.URL+"/", caldavtest.User, caldavtest.Password, []string{"Todo"})
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 10, 13, 2, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 14, 2, 0, 0, 0, time.UTC)
	done, err := c.Completed(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	var uids []string
	for _, task := range done {
		uids = append(uids, task.UID)
		if task.List != "Todo" || task.Summary != task.UID {
			t.Errorf("task = %+v", task)
		}
	}
	slices.Sort(uids)
	if want := []string{"at-start", "in-range", "no-status"}; !slices.Equal(uids, want) {
		t.Errorf("Completed = %q, want %q", uids, want)
	}
}
