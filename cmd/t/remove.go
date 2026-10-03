package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/Mag1cByt3s/beacon/internal/api"
	"github.com/Mag1cByt3s/beacon/internal/caldav"
	"github.com/Mag1cByt3s/beacon/internal/focus"
	"github.com/Mag1cByt3s/beacon/internal/queue"
)

// minQuery is the shortest search t rm accepts for a part of a title, so a
// stray letter cannot pick some task by chance.
const minQuery = 3

// rm removes the one open task whose title matches query. Several matches
// are listed and nothing is removed.
func (a *app) rm(ctx context.Context, query string) error {
	tasks, err := a.b.Tasks(ctx)
	if err != nil {
		return err
	}

	matches := matchTasks(tasks, query)
	switch {
	case len(matches) == 0 && utf8.RuneCountInString(query) < minQuery:
		return fmt.Errorf("%q is too short to search for; type at least %d letters or the whole title", query, minQuery)
	case len(matches) == 0:
		return fmt.Errorf("no open task matches %q (see t list)", query)
	case len(matches) > 1:
		titles := make([]string, len(matches))
		for i, task := range matches {
			titles[i] = "  " + oneLine(task.Summary)
		}
		return fmt.Errorf("%d tasks match %q; be more specific:\n%s", len(matches), query, strings.Join(titles, "\n"))
	}

	task := matches[0]
	_, err = a.b.Remove(ctx, task.UID, task.ETag)
	switch {
	case changedElsewhere(err):
		return fmt.Errorf("%q was changed elsewhere just now, so it was left alone; check it with t list", oneLine(task.Summary))
	case notOpen(err):
		return fmt.Errorf("%q is no longer an open task", oneLine(task.Summary))
	case err != nil:
		return err
	}

	if last, ok := a.loadLast(); ok && last.UID == task.UID {
		a.clearLast()
	}
	fmt.Fprintf(a.out, "Removed %q.\n", oneLine(task.Summary))
	return nil
}

// undo removes the task added last from this computer: from the offline
// queue if it was not sent yet, otherwise from the server, but only if it
// was not changed since.
func (a *app) undo(ctx context.Context) error {
	last, ok := a.loadLast()
	if !ok {
		fmt.Fprintln(a.out, "Nothing to undo.")
		return nil
	}
	title := oneLine(last.Summary)

	if last.Queued && a.queue != nil {
		found, err := a.queue.Remove(last.UID)
		if err != nil {
			return err
		}
		if found {
			a.clearLast()
			fmt.Fprintf(a.out, "Removed %q (it was not sent yet).\n", title)
			return nil
		}
		// Not in the queue any more: it was sent meanwhile. Remove it
		// from the server like any other capture.
	}

	etag := last.ETag
	if etag == "" {
		// No version from when it was added (it went through the offline
		// queue). Make sure the title is still what was typed instead.
		task, err := findTask(ctx, a.b, last.UID)
		if err != nil {
			a.clearLastIf(notOpen(err))
			return undoError(title, err)
		}
		if task.Summary != last.Summary {
			a.clearLast()
			return undoError(title, caldav.ErrConflict)
		}
		etag = task.ETag
	}

	_, err := a.b.Remove(ctx, last.UID, etag)
	if err != nil {
		// Keep it for another try only if the server could not be reached.
		a.clearLastIf(changedElsewhere(err) || notOpen(err))
		return undoError(title, err)
	}
	a.clearLast()
	fmt.Fprintf(a.out, "Removed %q.\n", title)
	return nil
}

// undoError explains why t undo did not remove the task.
func undoError(title string, err error) error {
	switch {
	case changedElsewhere(err):
		return fmt.Errorf("%q was changed elsewhere since you added it, so it was left alone", title)
	case notOpen(err):
		return fmt.Errorf("%q is no longer an open task, so there is nothing to undo", title)
	}
	return err
}

// findTask returns the open task with this UID, or caldav.ErrGone.
func findTask(ctx context.Context, b backend, uid string) (focus.Task, error) {
	tasks, err := b.Tasks(ctx)
	if err != nil {
		return focus.Task{}, err
	}
	for _, task := range tasks {
		if task.UID == uid {
			return task, nil
		}
	}
	return focus.Task{}, caldav.ErrGone
}

// matchTasks returns the tasks whose title is query (ignoring case and
// extra spaces). If there are none, it returns the tasks whose title
// contains query, provided query has at least minQuery letters.
func matchTasks(tasks []focus.Task, query string) []focus.Task {
	q := strings.ToLower(oneLine(query))
	var exact, partial []focus.Task
	for _, task := range tasks {
		title := strings.ToLower(oneLine(task.Summary))
		switch {
		case title == q:
			exact = append(exact, task)
		case utf8.RuneCountInString(q) >= minQuery && strings.Contains(title, q):
			partial = append(partial, task)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return partial
}

// changedElsewhere reports whether err means the task was changed by
// another client, directly (CalDAV) or as told by the server.
func changedElsewhere(err error) bool {
	var conflict *api.ConflictError
	return errors.Is(err, caldav.ErrConflict) || errors.As(err, &conflict)
}

// notOpen reports whether err means there is no such open task (any more).
func notOpen(err error) bool {
	return errors.Is(err, caldav.ErrGone) || errors.Is(err, api.ErrNotFound)
}

// lastCapture is what t undo needs to know about the task added last.
type lastCapture struct {
	UID     string `json:"uid"`
	Summary string `json:"summary"`
	ETag    string `json:"etag,omitempty"`   // version when it was added, if known
	Queued  bool   `json:"queued,omitempty"` // saved offline instead of sent
}

// lastCapturePath returns the file next to the offline queue, or "" if
// there is no place for it.
func lastCapturePath() string {
	path, err := queue.DefaultPath()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(path), "last-capture.json")
}

// saveLast remembers c for t undo. Failing to save only means undo is not
// available, so errors are ignored.
func (a *app) saveLast(c lastCapture) {
	if a.lastPath == "" {
		return
	}
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(a.lastPath), 0o700)
	os.WriteFile(a.lastPath, data, 0o600)
}

func (a *app) loadLast() (lastCapture, bool) {
	if a.lastPath == "" {
		return lastCapture{}, false
	}
	data, err := os.ReadFile(a.lastPath)
	if err != nil {
		return lastCapture{}, false
	}
	var c lastCapture
	if err := json.Unmarshal(data, &c); err != nil || c.UID == "" {
		return lastCapture{}, false
	}
	return c, true
}

func (a *app) clearLast() {
	if a.lastPath != "" {
		os.Remove(a.lastPath)
	}
}

func (a *app) clearLastIf(cond bool) {
	if cond {
		a.clearLast()
	}
}
