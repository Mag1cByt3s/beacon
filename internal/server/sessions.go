package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/api"
	"github.com/Mag1cByt3s/beacon/internal/focus"
	"github.com/Mag1cByt3s/beacon/internal/journal"
	"github.com/Mag1cByt3s/beacon/internal/store"
)

// Focus sessions measure focused work with start and stop. They are kept
// in beacon's database and feed the journal: a stop with a result becomes
// a journal item, and completing a task sums up the sessions for it.
//
// Locking: handlers that need both locks take s.mu (focus state) before
// s.journalMu, always in that order, so they cannot block each other.

// startTaskTimeout bounds how long a start waits for CalDAV to name the
// current task; without an answer the session starts without a label.
const startTaskTimeout = 3 * time.Second

func (s *Server) focusStart(w http.ResponseWriter, r *http.Request) {
	req, ok := readFocusRequest(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.journalMu.Lock()
	defer s.journalMu.Unlock()
	s.startSession(w, r, req)
}

func (s *Server) focusStop(w http.ResponseWriter, r *http.Request) {
	req, ok := readFocusRequest(w, r)
	if !ok {
		return
	}
	s.journalMu.Lock()
	defer s.journalMu.Unlock()
	s.stopSession(w, r, req)
}

// focusToggle stops the running session, or starts one if none runs.
func (s *Server) focusToggle(w http.ResponseWriter, r *http.Request) {
	req, ok := readFocusRequest(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.journalMu.Lock()
	defer s.journalMu.Unlock()

	_, running, err := s.store.RunningSession(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if running {
		s.stopSession(w, r, req)
	} else {
		s.startSession(w, r, req)
	}
}

// startSession starts a session. Both locks must be held.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, req api.FocusRequest) {
	kind := req.Kind
	if kind == "" {
		kind = journal.KindComputer
	}
	if kind != journal.KindComputer && kind != journal.KindOffline {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Error: "kind must be computer or offline"})
		return
	}

	now := s.now()
	sess := journal.Session{
		Date:   s.Clock.Day(now),
		Start:  now,
		Label:  strings.Join(strings.Fields(req.Label), " "),
		Kind:   kind,
		Source: clientName(r),
	}
	if sess.Label == "" {
		// Without a label the session is for the current task.
		if task, ok := s.currentTask(r.Context()); ok {
			sess.Label, sess.TaskUID = task.Summary, task.UID
		}
	}

	sess, err := s.store.StartSession(r.Context(), sess)
	if errors.Is(err, store.ErrSessionRunning) {
		writeJSON(w, http.StatusConflict, api.ErrorResponse{Error: err.Error()})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.log.Info("focus session started", "id", sess.ID, "kind", sess.Kind, "client", sess.Source)
	writeJSON(w, http.StatusOK, api.FocusResponse{Action: "started", Session: sess})
}

// currentTask returns the current task, or ok == false if there is none
// or CalDAV cannot say so quickly. s.mu must be held.
func (s *Server) currentTask(ctx context.Context) (focus.Task, bool) {
	ctx, cancel := context.WithTimeout(ctx, startTaskTimeout)
	defer cancel()
	_, order, _, err := s.focusState(ctx)
	if err != nil {
		s.log.Warn("starting the focus session without a task", "err", err)
		return focus.Task{}, false
	}
	if len(order) == 0 {
		return focus.Task{}, false
	}
	return order[0], true
}

// stopSession stops the running session and turns a result into a
// journal item. s.journalMu must be held.
func (s *Server) stopSession(w http.ResponseWriter, r *http.Request, req api.FocusRequest) {
	sess, err := s.store.StopSession(r.Context(), s.now())
	if errors.Is(err, store.ErrNoSession) {
		writeJSON(w, http.StatusConflict, api.ErrorResponse{Error: err.Error()})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	minutes := sess.Minutes(s.now())
	resp := api.FocusResponse{
		Action:     "stopped",
		Session:    sess,
		Minutes:    minutes,
		Suspicious: sess.End.Sub(sess.Start) > journal.SuspiciousAfter,
	}
	s.log.Info("focus session stopped", "id", sess.ID, "minutes", minutes, "client", clientName(r))

	if result := strings.Join(strings.Fields(req.Result), " "); result != "" {
		it, err := s.store.AddItem(r.Context(), journal.Item{
			Date:    sess.Date,
			Title:   result,
			Minutes: &minutes,
			Kind:    sess.Kind,
			Source:  journal.SourceFocus,
			RefID:   strconv.FormatInt(sess.ID, 10),
		})
		if err != nil {
			s.fail(w, err)
			return
		}
		resp.Item = &it
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) focusStatus(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if date == "" {
		date = "today"
	}
	date, err := s.Clock.Resolve(date, s.now())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	}
	sessions, err := s.store.Sessions(r.Context(), date, date)
	if err != nil {
		s.fail(w, err)
		return
	}
	status := api.FocusStatus{Date: date, Sessions: sessions}
	for _, sess := range sessions {
		if sess.End != nil {
			status.Minutes += sess.Minutes(s.now())
		}
	}
	running, ok, err := s.store.RunningSession(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if ok {
		status.Running = &running
	}
	writeJSON(w, http.StatusOK, status)
}

// readFocusRequest reads the optional body of a focus endpoint.
func readFocusRequest(w http.ResponseWriter, r *http.Request) (api.FocusRequest, bool) {
	var req api.FocusRequest
	body, ok := readBody(w, r)
	if !ok {
		return req, false
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return req, true // no body: all defaults
	}
	if err := decodeStrict(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return req, false
	}
	return req, true
}

// journalTaskDone records a completed task in the journal: it stops a
// session still running for it, and adds an item with the minutes of all
// its sessions (none if it had no sessions). A task already recorded is
// not added twice. Errors are only logged: the task is done either way.
// s.mu must be held; this takes s.journalMu.
func (s *Server) journalTaskDone(ctx context.Context, task focus.Task) {
	s.journalMu.Lock()
	defer s.journalMu.Unlock()

	err := func() error {
		if running, ok, err := s.store.RunningSession(ctx); err != nil {
			return err
		} else if ok && running.TaskUID == task.UID {
			if _, err := s.store.StopSession(ctx, s.now()); err != nil {
				return err
			}
		}
		if _, found, err := s.store.ItemByRef(ctx, journal.SourceTask, task.UID); err != nil || found {
			return err
		}

		sessions, err := s.store.SessionsForTask(ctx, task.UID)
		if err != nil {
			return err
		}
		item := journal.Item{
			Date:   s.Clock.Day(s.now()),
			Title:  task.Summary,
			Kind:   journal.KindComputer,
			Source: journal.SourceTask,
			RefID:  task.UID,
		}
		if len(sessions) > 0 {
			total := 0
			for _, sess := range sessions {
				total += sess.Minutes(s.now())
			}
			item.Minutes = &total
			item.Kind = sessions[len(sessions)-1].Kind
		}
		_, err = s.store.AddItem(ctx, item)
		return err
	}()
	if err != nil {
		s.log.Error("could not record the completed task in the journal", "uid", task.UID, "err", err)
	}
}
