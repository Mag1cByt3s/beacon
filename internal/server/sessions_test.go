package server

import (
	"net/http"
	"testing"

	"github.com/Mag1cByt3s/beacon/internal/api"
)

func TestFocusStartStop(t *testing.T) {
	e := newEnv(t)
	e.setNow("2026-10-13 09:00")

	// Without a label the session is for the current task (milk).
	var started api.FocusResponse
	if got := e.do("POST", "/api/focus/start", "", &started); got != http.StatusOK {
		t.Fatalf("start: %d", got)
	}
	sess := started.Session
	if started.Action != "started" || sess.Label != "Milk" || sess.TaskUID != "milk" || sess.Kind != "computer" ||
		sess.Source != "laptop" || sess.Date != "2026-10-13" || sess.End != nil {
		t.Errorf("started = %+v", started)
	}
	if got := e.do("POST", "/api/focus/start", `{"label": "noch eine"}`, nil); got != http.StatusConflict {
		t.Errorf("second start: %d, want 409", got)
	}

	var status api.FocusStatus
	e.do("GET", "/api/focus", "", &status)
	if status.Running == nil || status.Running.ID != sess.ID || status.Date != "2026-10-13" {
		t.Errorf("status while running = %+v", status)
	}

	e.setNow("2026-10-13 10:30")
	var stopped api.FocusResponse
	if got := e.do("POST", "/api/focus/stop", `{"result": "  Milch   gekauft "}`, &stopped); got != http.StatusOK {
		t.Fatalf("stop: %d", got)
	}
	if stopped.Action != "stopped" || stopped.Minutes != 90 || stopped.Suspicious || stopped.Item == nil {
		t.Fatalf("stopped = %+v", stopped)
	}
	it := stopped.Item
	if it.Title != "Milch gekauft" || *it.Minutes != 90 || it.Source != "focus" || it.Kind != "computer" ||
		it.RefID == "" || it.Date != "2026-10-13" {
		t.Errorf("item = %+v", it)
	}
	if got := e.do("POST", "/api/focus/stop", "", nil); got != http.StatusConflict {
		t.Errorf("stop with none running: %d, want 409", got)
	}

	status = api.FocusStatus{}
	e.do("GET", "/api/focus?date=2026-10-13", "", &status)
	if status.Running != nil || len(status.Sessions) != 1 || status.Minutes != 90 {
		t.Errorf("status after stop = %+v", status)
	}
}

func TestFocusToggleOfflineAndSuspicious(t *testing.T) {
	e := newEnv(t)
	e.setNow("2026-10-13 08:00")

	var resp api.FocusResponse
	e.do("POST", "/api/focus/toggle", `{"label": "Garten", "kind": "offline"}`, &resp)
	if resp.Action != "started" || resp.Session.Label != "Garten" || resp.Session.Kind != "offline" || resp.Session.TaskUID != "" {
		t.Errorf("toggle 1 = %+v", resp)
	}

	// Forgotten until the evening: more than 4 hours.
	e.setNow("2026-10-13 13:01")
	resp = api.FocusResponse{}
	e.do("POST", "/api/focus/toggle", "", &resp)
	if resp.Action != "stopped" || !resp.Suspicious || resp.Minutes != 301 || resp.Item != nil {
		t.Errorf("toggle 2 = %+v", resp)
	}

	if got := e.do("POST", "/api/focus/start", `{"kind": "sofa"}`, nil); got != http.StatusBadRequest {
		t.Errorf("bad kind: %d, want 400", got)
	}
	if got := e.do("POST", "/api/focus/start", `{"lable": "typo"}`, nil); got != http.StatusBadRequest {
		t.Errorf("unknown field: %d, want 400", got)
	}
	if got := e.doWithAuth("POST", "/api/focus/toggle", "Bearer "+testAWToken, "", nil); got != http.StatusForbidden {
		t.Errorf("aw-sync: %d, want 403", got)
	}
}

func TestFocusStartWithoutCalDAV(t *testing.T) {
	e := newEnv(t)
	e.davStop()
	var resp api.FocusResponse
	if got := e.do("POST", "/api/focus/start", "", &resp); got != http.StatusOK {
		t.Fatalf("start with CalDAV down: %d", got)
	}
	if resp.Session.Label != "" || resp.Session.TaskUID != "" {
		t.Errorf("session = %+v, want one without a task", resp.Session)
	}
}

// TestTaskDoneJournalHook: completing a task stops its running session
// and records the task with the minutes of all its sessions.
func TestTaskDoneJournalHook(t *testing.T) {
	e := newEnv(t)

	// Two sessions for the current task (milk): 30 minutes, then 45 still
	// running when the task is completed.
	e.setNow("2026-10-13 09:00")
	e.do("POST", "/api/focus/start", "", nil)
	e.setNow("2026-10-13 09:30")
	e.do("POST", "/api/focus/stop", "", nil)
	e.setNow("2026-10-13 10:00")
	e.do("POST", "/api/focus/start", `{"kind": "offline"}`, nil)
	e.setNow("2026-10-13 10:45")

	var done api.DoneResponse
	if got := e.do("POST", "/current/done", "", &done); got != http.StatusOK || uidOf(done.Done) != "milk" {
		t.Fatalf("done: %d %+v", got, done)
	}

	var status api.FocusStatus
	e.do("GET", "/api/focus", "", &status)
	if status.Running != nil || status.Minutes != 75 {
		t.Errorf("after done: running %v, %d minutes; want stopped, 75", status.Running, status.Minutes)
	}

	var items api.ItemsResponse
	e.do("GET", "/api/journal/today/items", "", &items)
	if len(items.Items) != 1 {
		t.Fatalf("items = %+v", items.Items)
	}
	it := items.Items[0]
	if it.Title != "Milk" || it.Source != "task" || it.RefID != "milk" || it.Minutes == nil || *it.Minutes != 75 || it.Kind != "offline" {
		t.Errorf("task item = %+v", it)
	}

	// The next task has no sessions: recorded without minutes.
	e.do("POST", "/current/done", "", nil)
	items = api.ItemsResponse{}
	e.do("GET", "/api/journal/today/items", "", &items)
	if len(items.Items) != 2 || items.Items[1].RefID != "open" || items.Items[1].Minutes != nil {
		t.Errorf("items = %+v", items.Items)
	}
}
