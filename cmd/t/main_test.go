package main

import (
	"testing"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/focus"
)

func TestFormatTask(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) // a Saturday
	date := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC) }

	tests := []struct {
		name     string
		task     focus.Task
		showList bool
		want     string
	}{
		{"no due date", focus.Task{Summary: "Read"}, false, "Read"},
		{"overdue", focus.Task{Summary: "Pay rent", Due: now.Add(-time.Hour)}, false, "Pay rent (overdue)"},
		{"due today all day", focus.Task{Summary: "Call", Due: date(10, 3), DueAllDay: true}, false, "Call (due today)"},
		{"due today with time", focus.Task{Summary: "Call", Due: now.Add(3 * time.Hour)}, false, "Call (due today 15:00)"},
		{"due tomorrow", focus.Task{Summary: "Bin", Due: date(10, 4), DueAllDay: true}, false, "Bin (due tomorrow)"},
		{"due later", focus.Task{Summary: "Tax", Due: date(10, 12), DueAllDay: true}, false, "Tax (due Mon 12 Oct)"},
		{"due next year", focus.Task{Summary: "Tax", Due: time.Date(2027, 1, 4, 0, 0, 0, 0, time.UTC), DueAllDay: true}, false, "Tax (due Mon 4 Jan 2027)"},
		{"with list", focus.Task{Summary: "Milk", List: "Groceries"}, true, "Milk [Groceries]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatTask(tt.task, now, tt.showList); got != tt.want {
				t.Errorf("formatTask = %q, want %q", got, tt.want)
			}
		})
	}
}
