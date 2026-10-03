package caldav

import (
	"testing"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/caldav/caldavtest"
)

func TestTaskFromCalendarOpenState(t *testing.T) {
	tests := []struct {
		name   string
		extra  []string
		wantOK bool
	}{
		{"no status", nil, true},
		{"needs action", []string{"STATUS:NEEDS-ACTION"}, true},
		{"in process", []string{"STATUS:IN-PROCESS"}, true},
		{"completed", []string{"STATUS:COMPLETED", "COMPLETED:20261001T100000Z"}, false},
		{"cancelled", []string{"STATUS:CANCELLED"}, false},
		{"completed timestamp without status", []string{"COMPLETED:20261001T100000Z"}, false},
		{"lowercase status", []string{"STATUS:completed"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := []string{"BEGIN:VTODO", "UID:1", "DTSTAMP:20261001T100000Z", "SUMMARY:x"}
			lines = append(lines, tt.extra...)
			lines = append(lines, "END:VTODO")
			_, ok := taskFromCalendar(caldavtest.Parse(t, lines...), "Todo")
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
		})
	}
}

func TestTaskFromCalendarFields(t *testing.T) {
	cal := caldavtest.Parse(t,
		"BEGIN:VTODO",
		"UID:abc-123",
		"DTSTAMP:20261001T100000Z",
		"CREATED:20260920T080000Z",
		"SUMMARY:Pay rent",
		"DUE:20261005T170000Z",
		"PRIORITY:1",
		"X-APPLE-SORT-ORDER:42",
		"END:VTODO",
	)
	task, ok := taskFromCalendar(cal, "Todo")
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if task.UID != "abc-123" || task.Summary != "Pay rent" || task.List != "Todo" {
		t.Errorf("got UID %q, Summary %q, List %q", task.UID, task.Summary, task.List)
	}
	if want := time.Date(2026, 10, 5, 17, 0, 0, 0, time.UTC); !task.Due.Equal(want) || task.DueAllDay {
		t.Errorf("Due = %v (all-day %v), want %v", task.Due, task.DueAllDay, want)
	}
	if want := time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC); !task.Created.Equal(want) {
		t.Errorf("Created = %v, want %v", task.Created, want)
	}
	if task.Priority != 1 {
		t.Errorf("Priority = %d, want 1", task.Priority)
	}
	if task.Recurring {
		t.Error("Recurring = true, want false")
	}
}

func TestTaskFromCalendarDates(t *testing.T) {
	tests := []struct {
		name       string
		due        string
		wantZero   bool
		wantAllDay bool
	}{
		{"date only", "DUE;VALUE=DATE:20261005", false, true},
		{"date without VALUE param", "DUE:20261005", false, true},
		{"with known time zone", "DUE;TZID=Europe/Berlin:20261005T090000", false, false},
		{"with unknown time zone", "DUE;TZID=W. Europe Standard Time:20261005T090000", false, false},
		{"floating time", "DUE:20261005T090000", false, false},
		{"garbage", "DUE:soon", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cal := caldavtest.Parse(t, "BEGIN:VTODO", "UID:1", "DTSTAMP:20261001T100000Z", "SUMMARY:x", tt.due, "END:VTODO")
			task, ok := taskFromCalendar(cal, "Todo")
			if !ok {
				t.Fatal("ok = false; a bad date must not hide the task")
			}
			if task.Due.IsZero() != tt.wantZero {
				t.Errorf("Due = %v, want zero: %v", task.Due, tt.wantZero)
			}
			if task.DueAllDay != tt.wantAllDay {
				t.Errorf("DueAllDay = %v, want %v", task.DueAllDay, tt.wantAllDay)
			}
		})
	}
}

func TestTaskFromCalendarRecurring(t *testing.T) {
	cal := caldavtest.Parse(t,
		"BEGIN:VTODO", "UID:r", "DTSTAMP:20261001T100000Z", "SUMMARY:Water plants",
		"RRULE:FREQ=WEEKLY", "DUE:20261005T090000Z", "END:VTODO",
	)
	task, ok := taskFromCalendar(cal, "Todo")
	if !ok || !task.Recurring {
		t.Errorf("ok = %v, Recurring = %v; want true, true", ok, task.Recurring)
	}
}

func TestTaskFromCalendarOverridePicksMaster(t *testing.T) {
	// The override comes first on purpose; the master must still win.
	cal := caldavtest.Parse(t,
		"BEGIN:VTODO", "UID:r", "DTSTAMP:20261001T100000Z", "SUMMARY:Override",
		"RECURRENCE-ID:20261005T090000Z", "END:VTODO",
		"BEGIN:VTODO", "UID:r", "DTSTAMP:20261001T100000Z", "SUMMARY:Master",
		"RRULE:FREQ=WEEKLY", "END:VTODO",
	)
	task, ok := taskFromCalendar(cal, "Todo")
	if !ok || task.Summary != "Master" || !task.Recurring {
		t.Errorf("got ok %v, Summary %q, Recurring %v", ok, task.Summary, task.Recurring)
	}
}

func TestTaskFromCalendarNoTodo(t *testing.T) {
	cal := caldavtest.Parse(t, "BEGIN:VEVENT", "UID:e", "DTSTAMP:20261001T100000Z", "DTSTART:20261001T100000Z", "END:VEVENT")
	if _, ok := taskFromCalendar(cal, "Todo"); ok {
		t.Error("ok = true for a calendar without VTODO")
	}
}

func TestTaskFromCalendarEmptySummary(t *testing.T) {
	cal := caldavtest.Parse(t, "BEGIN:VTODO", "UID:1", "DTSTAMP:20261001T100000Z", "END:VTODO")
	task, _ := taskFromCalendar(cal, "Todo")
	if task.Summary != "(no title)" {
		t.Errorf("Summary = %q, want (no title)", task.Summary)
	}
}
