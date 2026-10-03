// Package focus decides which task to work on next.
//
// Everything here is pure: no network, no files, no clock. The current time
// is passed in, which keeps the logic easy to test.
package focus

import (
	"cmp"
	"slices"
	"time"
)

// Task is an open to-do item, independent of where it is stored.
type Task struct {
	UID     string
	Summary string
	List    string // name of the collection the task lives in

	// Due is the zero time.Time when the task has no due date.
	// Go has no "null" for structs, so IsZero() is the usual check.
	Due       time.Time
	DueAllDay bool // Due is a date without a time of day

	// Priority follows iCalendar: 1 is highest, 9 lowest, 0 means none.
	Priority int

	// Created is the zero time.Time when unknown.
	Created time.Time

	// Recurring tasks (RRULE) are handled on the phone, not in focus mode.
	Recurring bool
}

// Overdue reports whether the task's due date has passed at time now.
// A task due on a date (without a time) is overdue only once that day is over.
func (t Task) Overdue(now time.Time) bool {
	if t.Due.IsZero() {
		return false
	}
	deadline := t.Due
	if t.DueAllDay {
		deadline = t.Due.AddDate(0, 0, 1)
	}
	return !now.Before(deadline)
}

// Queue returns the tasks focus mode works through, best first.
// Recurring tasks are left out. The input slice is not modified.
//
// Order: overdue first, then by due date (tasks without one last), then by
// priority (1 first, none last), then oldest created first.
func Queue(tasks []Task, now time.Time) []Task {
	queue := make([]Task, 0, len(tasks))
	for _, t := range tasks {
		if !t.Recurring {
			queue = append(queue, t)
		}
	}

	// SortStableFunc keeps the input order for tasks that compare equal.
	slices.SortStableFunc(queue, func(a, b Task) int {
		return compare(a, b, now)
	})
	return queue
}

// Next returns the single task to focus on. ok is false when there is none.
func Next(tasks []Task, now time.Time) (next Task, ok bool) {
	queue := Queue(tasks, now)
	if len(queue) == 0 {
		return Task{}, false
	}
	return queue[0], true
}

// compare returns a negative number if a comes before b, a positive number
// if b comes first, and 0 if they are equal (the convention used by slices
// and cmp).
func compare(a, b Task, now time.Time) int {
	// cmp.Or returns the first non-zero comparison.
	return cmp.Or(
		trueFirst(a.Overdue(now), b.Overdue(now)),
		trueFirst(!a.Due.IsZero(), !b.Due.IsZero()),
		a.Due.Compare(b.Due),
		cmp.Compare(priorityRank(a.Priority), priorityRank(b.Priority)),
		trueFirst(!a.Created.IsZero(), !b.Created.IsZero()),
		a.Created.Compare(b.Created),
		cmp.Compare(a.UID, b.UID),
	)
}

// trueFirst sorts true before false.
func trueFirst(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return -1
	default:
		return 1
	}
}

// priorityRank maps iCalendar priorities so that a smaller rank sorts first.
// 0 ("no priority") and invalid values rank below 9, the lowest real priority.
func priorityRank(p int) int {
	if p < 1 || p > 9 {
		return 10
	}
	return p
}
