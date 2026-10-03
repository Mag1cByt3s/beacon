package caldav

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

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
