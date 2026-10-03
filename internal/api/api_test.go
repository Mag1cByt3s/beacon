package api

import (
	"testing"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/focus"
)

func TestTaskRoundTrip(t *testing.T) {
	created := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		task focus.Task
	}{
		{"no due date", focus.Task{UID: "a", Summary: "A", List: "Todo", Priority: 3, Created: created}},
		{"due with time", focus.Task{UID: "b", Due: time.Date(2026, 10, 5, 17, 30, 0, 0, time.UTC)}},
		{"due all day", focus.Task{UID: "c", Due: time.Date(2026, 10, 5, 0, 0, 0, 0, time.Local), DueAllDay: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FromFocus(tt.task).Focus()
			if got.UID != tt.task.UID || got.Summary != tt.task.Summary || got.List != tt.task.List ||
				got.Priority != tt.task.Priority || !got.Created.Equal(tt.task.Created) ||
				!got.Due.Equal(tt.task.Due) || got.DueAllDay != tt.task.DueAllDay {
				t.Errorf("round trip changed the task:\n got  %+v\n want %+v", got, tt.task)
			}
		})
	}
}

func TestAllDayDueIsADate(t *testing.T) {
	task := focus.Task{Due: time.Date(2026, 10, 5, 0, 0, 0, 0, time.Local), DueAllDay: true}
	if got := FromFocus(task).Due; got != "2026-10-05" {
		t.Errorf("Due = %q, want a plain date", got)
	}
}

func TestBadDueIsDropped(t *testing.T) {
	got := Task{UID: "x", Due: "soon", DueAllDay: true}.Focus()
	if !got.Due.IsZero() || got.DueAllDay {
		t.Errorf("Due = %v (all day %v), want none", got.Due, got.DueAllDay)
	}
}
