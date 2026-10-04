// Package api defines the JSON that the beacon server and its clients
// exchange, and a client for the CLI.
//
//	GET  /healthz        "ok", no token needed
//	GET  /current        CurrentResponse
//	GET  /tasks          TasksResponse, in focus order
//	POST /tasks          AddRequest -> 201 AddResponse; 409 if the uid exists
//	DELETE /tasks/{uid}  DeleteResponse; optional If-Match: "<etag>";
//	                     404 if no such open task, 409 if it changed
//	PATCH /tasks/{uid}   RenameRequest -> TaskResponse; optional If-Match;
//	                     404 if no such open task, 409 if it changed
//	POST /current/done   DoneResponse, or 409 ErrorResponse if the task changed
//	POST /current/skip   SkipResponse
//
// Every endpoint except /healthz needs "Authorization: Bearer <token>".
// Errors come back as ErrorResponse. 503 means the CalDAV server could not
// be reached; trying again later may work.
package api

import (
	"time"

	"github.com/Mag1cByt3s/beacon/internal/focus"
)

// Task is a task as the API sends it. Fields beacon does not need are left
// out; the full task always stays in CalDAV.
type Task struct {
	UID     string `json:"uid"`
	Summary string `json:"summary"`
	List    string `json:"list"`

	// Due is "2006-01-02" for a date without a time (DueAllDay), otherwise
	// an RFC 3339 timestamp. Empty when there is no due date.
	Due       string `json:"due,omitempty"`
	DueAllDay bool   `json:"due_all_day,omitempty"`

	Priority int `json:"priority,omitempty"`

	// omitzero (Go 1.24+) leaves out a zero time.Time, i.e. "unknown".
	Created time.Time `json:"created,omitzero"`

	// ETag is the version of the task in CalDAV. Sending it back in
	// If-Match makes sure only that version is changed or deleted.
	ETag string `json:"etag,omitempty"`
}

// CurrentResponse answers GET /current. Task is null when nothing is open.
type CurrentResponse struct {
	Task *Task `json:"task"`
}

// TasksResponse answers GET /tasks.
type TasksResponse struct {
	Tasks []Task `json:"tasks"`
}

// AddRequest is the body of POST /tasks. UID is optional; a client that
// may retry (for example from an offline queue) sends its own, so a retry
// of a capture that already got through answers 409 instead of creating
// a duplicate.
type AddRequest struct {
	UID     string `json:"uid,omitempty"`
	Summary string `json:"summary"`
}

// AddResponse answers POST /tasks with the new task's UID, its ETag (if
// the CalDAV server sent one) and the list it was added to.
type AddResponse struct {
	UID  string `json:"uid"`
	ETag string `json:"etag,omitempty"`
	List string `json:"list"`
}

// RenameRequest is the body of PATCH /tasks/{uid}: the task's new title.
type RenameRequest struct {
	Summary string `json:"summary"`
}

// TaskResponse answers PATCH /tasks/{uid} with the task as it is now,
// including its new ETag.
type TaskResponse struct {
	Task *Task `json:"task"`
}

// DeleteResponse answers DELETE /tasks/{uid} with the removed task.
type DeleteResponse struct {
	Deleted *Task `json:"deleted"`
}

// DoneResponse answers POST /current/done: the completed task and the new
// current one. Both are null if there was nothing to complete.
type DoneResponse struct {
	Done    *Task `json:"done"`
	Current *Task `json:"current"`
}

// SkipResponse answers POST /current/skip: the skipped task and the new
// current one.
type SkipResponse struct {
	Skipped *Task `json:"skipped"`
	Current *Task `json:"current"`
}

// ErrorResponse is the body of every error. For 409 Conflict, Current is
// the current task as it is now.
type ErrorResponse struct {
	Error   string `json:"error"`
	Current *Task  `json:"current,omitempty"`
}

const dateLayout = "2006-01-02"

// FromFocus converts a task for sending.
func FromFocus(t focus.Task) Task {
	out := Task{
		UID:       t.UID,
		Summary:   t.Summary,
		List:      t.List,
		DueAllDay: t.DueAllDay,
		Priority:  t.Priority,
		Created:   t.Created,
		ETag:      t.ETag,
	}
	switch {
	case t.Due.IsZero():
	case t.DueAllDay:
		// Send the calendar date itself, so each client reads it in its
		// own time zone.
		out.Due = t.Due.Format(dateLayout)
	default:
		out.Due = t.Due.Format(time.RFC3339)
	}
	return out
}

// Focus converts a received task back. A due date that cannot be read is
// dropped rather than failing.
func (t Task) Focus() focus.Task {
	out := focus.Task{
		UID:       t.UID,
		Summary:   t.Summary,
		List:      t.List,
		DueAllDay: t.DueAllDay,
		Priority:  t.Priority,
		Created:   t.Created,
		ETag:      t.ETag,
	}
	if t.Due != "" {
		var err error
		if t.DueAllDay {
			out.Due, err = time.ParseInLocation(dateLayout, t.Due, time.Local)
		} else {
			out.Due, err = time.Parse(time.RFC3339, t.Due)
		}
		if err != nil {
			out.Due, out.DueAllDay = time.Time{}, false
		}
	}
	return out
}

// TaskPtr converts an optional task: ok false gives nil, which is sent as
// JSON null.
func TaskPtr(t focus.Task, ok bool) *Task {
	if !ok {
		return nil
	}
	out := FromFocus(t)
	return &out
}
