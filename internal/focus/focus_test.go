package focus

import (
	"slices"
	"testing"
	"time"
)

// now is a fixed point in time so the tests never depend on the real clock.
var now = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

func day(offset int) time.Time {
	return time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC).AddDate(0, 0, offset)
}

func hours(offset int) time.Time {
	return now.Add(time.Duration(offset) * time.Hour)
}

func TestOverdue(t *testing.T) {
	tests := []struct {
		name string
		task Task
		want bool
	}{
		{"no due date", Task{}, false},
		{"due in the past", Task{Due: hours(-1)}, true},
		{"due exactly now", Task{Due: now}, true},
		{"due in the future", Task{Due: hours(1)}, false},
		{"all-day due today", Task{Due: day(0), DueAllDay: true}, false},
		{"all-day due yesterday", Task{Due: day(-1), DueAllDay: true}, true},
		{"all-day due tomorrow", Task{Due: day(1), DueAllDay: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.task.Overdue(now); got != tt.want {
				t.Errorf("Overdue = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestQueue(t *testing.T) {
	tests := []struct {
		name  string
		tasks []Task
		want  []string // UIDs in expected order
	}{
		{
			name:  "empty",
			tasks: nil,
			want:  []string{},
		},
		{
			name: "overdue before due later",
			tasks: []Task{
				{UID: "later", Due: hours(2)},
				{UID: "overdue", Due: hours(-2)},
			},
			want: []string{"overdue", "later"},
		},
		{
			name: "overdue beats high priority",
			tasks: []Task{
				{UID: "urgent", Priority: 1},
				{UID: "overdue", Due: day(-3), DueAllDay: true, Priority: 9},
			},
			want: []string{"overdue", "urgent"},
		},
		{
			name: "due dates ascending, no due date last",
			tasks: []Task{
				{UID: "none"},
				{UID: "in-3-days", Due: day(3)},
				{UID: "tomorrow", Due: day(1)},
			},
			want: []string{"tomorrow", "in-3-days", "none"},
		},
		{
			name: "all-day today is not overdue",
			tasks: []Task{
				{UID: "today", Due: day(0), DueAllDay: true},
				{UID: "past-hour", Due: hours(-1)},
			},
			want: []string{"past-hour", "today"},
		},
		{
			name: "priority 1 first, 0 counts as lowest",
			tasks: []Task{
				{UID: "none", Priority: 0},
				{UID: "low", Priority: 9},
				{UID: "high", Priority: 1},
				{UID: "medium", Priority: 5},
			},
			want: []string{"high", "medium", "low", "none"},
		},
		{
			name: "priority only breaks ties on equal due date",
			tasks: []Task{
				{UID: "later-high", Due: day(2), Priority: 1},
				{UID: "sooner-low", Due: day(1), Priority: 9},
				{UID: "sooner-high", Due: day(1), Priority: 1},
			},
			want: []string{"sooner-high", "sooner-low", "later-high"},
		},
		{
			name: "created ascending, unknown created last",
			tasks: []Task{
				{UID: "unknown"},
				{UID: "newer", Created: day(-1)},
				{UID: "older", Created: day(-10)},
			},
			want: []string{"older", "newer", "unknown"},
		},
		{
			name: "recurring tasks are left out",
			tasks: []Task{
				{UID: "weekly", Recurring: true, Due: day(-1)},
				{UID: "normal"},
			},
			want: []string{"normal"},
		},
		{
			name: "full tie falls back to UID",
			tasks: []Task{
				{UID: "b"},
				{UID: "a"},
			},
			want: []string{"a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := []string{}
			for _, task := range Queue(tt.tasks, now) {
				got = append(got, task.UID)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Queue order = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestQueueDoesNotModifyInput(t *testing.T) {
	tasks := []Task{{UID: "b"}, {UID: "a"}}
	Queue(tasks, now)
	if tasks[0].UID != "b" {
		t.Error("Queue reordered its input slice")
	}
}

func TestNext(t *testing.T) {
	if _, ok := Next(nil, now); ok {
		t.Error("Next on no tasks returned ok = true")
	}
	if _, ok := Next([]Task{{UID: "r", Recurring: true}}, now); ok {
		t.Error("Next on only recurring tasks returned ok = true")
	}

	tasks := []Task{{UID: "later", Due: day(5)}, {UID: "soon", Due: day(1)}}
	next, ok := Next(tasks, now)
	if !ok || next.UID != "soon" {
		t.Errorf("Next = %q, %v; want soon, true", next.UID, ok)
	}
}
