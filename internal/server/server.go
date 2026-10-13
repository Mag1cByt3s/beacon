// Package server is beacon's HTTP JSON API (see package api for the
// endpoints). It reads and writes tasks in CalDAV and keeps only the focus
// state (current task, skip order) in its own store.
package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/api"
	"github.com/Mag1cByt3s/beacon/internal/caldav"
	"github.com/Mag1cByt3s/beacon/internal/focus"
	"github.com/Mag1cByt3s/beacon/internal/journal"
	"github.com/Mag1cByt3s/beacon/internal/store"
)

// requestTimeout bounds the CalDAV work behind one API request.
const requestTimeout = 25 * time.Second

// Tasks is the task storage the server needs. *caldav.Client provides it.
type Tasks interface {
	OpenTasks(ctx context.Context) ([]focus.Task, error)
	Create(ctx context.Context, list, uid, summary string) (listName, etag string, err error)
	Complete(ctx context.Context, task focus.Task) error
	Delete(ctx context.Context, task focus.Task) error
	Rename(ctx context.Context, task focus.Task, summary string) (etag string, err error)
}

// Server handles API requests.
type Server struct {
	tasks       Tasks
	store       *store.Store
	clients     []client // who may use the API
	defaultList string
	log         *slog.Logger
	now         func() time.Time // replaceable in tests

	// TrustedProxies are the reverse proxies whose X-Forwarded-For header
	// tells the client's address; see clientIP. Empty: trust no one.
	TrustedProxies []netip.Prefix

	// Clock says when journal days start. New sets 04:00 Europe/Berlin.
	Clock journal.Clock

	// mu makes requests that read and change the focus state run one at a
	// time, so two clients cannot both pick or complete a task at once.
	mu sync.Mutex

	// journalMu makes changes to the journal run one at a time, so two
	// clients changing the same day cannot overwrite each other's fields.
	journalMu sync.Mutex
}

// New creates a server. token is the bearer token clients must send; new
// tasks go to defaultList.
func New(tasks Tasks, st *store.Store, tokens map[string]string, defaultList string, log *slog.Logger) *Server {
	s := &Server{
		tasks:       tasks,
		store:       st,
		defaultList: defaultList,
		log:         log,
		now:         time.Now,
	}
	for name, token := range tokens {
		s.clients = append(s.clients, client{name: name, hash: sha256.Sum256([]byte(token))})
	}
	// The time zone data is built into the binary (package journal), so
	// this cannot fail.
	s.Clock, _ = journal.NewClock("Europe/Berlin", "04:00")
	return s
}

// AWSyncClient is the client that uploads ActivityWatch data. It may use
// only the endpoint for that, and only it may use that endpoint.
const AWSyncClient = "aw-sync"

// client is one API client, known by the hash of its token.
type client struct {
	name string
	hash [sha256.Size]byte
}

// Who may use a route.
var (
	everyClientButSync = func(name string) bool { return name != AWSyncClient }
	onlySync           = func(name string) bool { return name == AWSyncClient }
)

// clientKey is the context key under which requireClient stores the name
// of the client that sent the request.
type clientKey struct{}

// clientName returns the name of the client that sent the request.
func clientName(r *http.Request) string {
	name, _ := r.Context().Value(clientKey{}).(string)
	return name
}

// Handler returns the HTTP handler with all routes, auth and logging.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok\n"))
	})

	// Every API route needs the token. Patterns like "GET /current"
	// (Go 1.22+) match method and path.
	routes := map[string]http.HandlerFunc{
		"GET /current":        s.getCurrent,
		"GET /tasks":          s.getTasks,
		"POST /tasks":         s.addTask,
		"DELETE /tasks/{uid}": s.deleteTask,
		"PATCH /tasks/{uid}":  s.renameTask,
		"POST /current/done":  s.done,
		"POST /current/skip":  s.skip,

		// The journal. "today" works wherever a {date} is expected.
		"GET /api/journal":                  s.listJournal,
		"GET /api/journal/export.csv":       s.exportJournal,
		"GET /api/journal/items/export.csv": s.exportItems,
		"GET /api/journal/{date}":           s.getJournal,
		"PATCH /api/journal/{date}":         s.patchJournal,
		"GET /api/journal/{date}/items":     s.getItems,
		"POST /api/journal/{date}/items":    s.addItem,
		"PUT /api/journal/{date}/items":     s.replaceItems,
	}
	for pattern, handler := range routes {
		mux.Handle(pattern, s.requireClient(everyClientButSync, handler))
	}
	// ActivityWatch data comes only from the aw-sync client.
	mux.Handle("PUT /api/journal/{date}/aw", s.requireClient(onlySync, http.HandlerFunc(s.putAW)))

	// Anything else is not part of the API: 404, without asking for a
	// token, so scanners looking for /wp-login.php don't fill the log with
	// warnings.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Tell logRequests (whose recorder w is) that no route matched.
		if rec, ok := w.(*statusRecorder); ok {
			rec.unknownRoute = true
		}
		writeJSON(w, http.StatusNotFound, api.ErrorResponse{Error: "not found"})
	})

	return s.logRequests(mux)
}

// requireClient lets a request through only with the bearer token of a
// client that allowed accepts. The handler can ask for the client's name
// with clientName.
func (s *Server) requireClient(allowed func(name string) bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := s.authenticate(r)
		if !ok {
			s.log.Warn("rejected request without valid token", "remote", s.clientIP(r), "path", r.URL.Path)
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeJSON(w, http.StatusUnauthorized, api.ErrorResponse{Error: "unauthorized"})
			return
		}
		// Tell logRequests (whose recorder w is) who this is.
		if rec, ok := w.(*statusRecorder); ok {
			rec.client = name
		}
		if !allowed(name) {
			s.log.Warn("client may not use this endpoint", "client", name, "path", r.URL.Path)
			writeJSON(w, http.StatusForbidden, api.ErrorResponse{Error: fmt.Sprintf("client %q may not use this endpoint", name)})
			return
		}
		// context.WithValue makes a copy of the request context that also
		// carries the client name.
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientKey{}, name)))
	})
}

// authenticate returns the name of the client whose token the request
// carries.
func (s *Server) authenticate(r *http.Request) (name string, ok bool) {
	token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !found {
		return "", false
	}
	// Compare hashes, not the tokens: they all have the same length, so
	// ConstantTimeCompare's time reveals nothing, not even the length.
	// Every client is compared, also after a match, for the same reason.
	got := sha256.Sum256([]byte(token))
	for _, c := range s.clients {
		if subtle.ConstantTimeCompare(got[:], c.hash[:]) == 1 {
			name = c.name
		}
	}
	return name, name != ""
}

func (s *Server) getCurrent(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()

	_, order, _, err := s.focusState(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.CurrentResponse{Task: first(order)})
}

func (s *Server) getTasks(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()

	_, order, _, err := s.focusState(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	resp := api.TasksResponse{Tasks: []api.Task{}} // [] rather than null when empty
	for _, task := range order {
		resp.Tasks = append(resp.Tasks, api.FromFocus(task))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) addTask(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var req api.AddRequest
	// MaxBytesReader stops a client from sending an endless body.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Error: "body must be JSON like {\"summary\": \"buy coffee\"}"})
		return
	}
	summary := strings.Join(strings.Fields(req.Summary), " ")
	if summary == "" {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Error: "summary is empty"})
		return
	}

	uid := req.UID
	if uid == "" {
		uid = caldav.NewUID()
	}

	// Adding a task does not touch the focus state, so no lock is needed.
	list, etag, err := s.tasks.Create(ctx, s.defaultList, uid, summary)
	switch {
	case errors.Is(err, caldav.ErrInvalidUID):
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	case errors.Is(err, caldav.ErrExists):
		s.log.Info("task already added", "uid", uid)
		writeJSON(w, http.StatusConflict, api.ErrorResponse{Error: err.Error()})
		return
	case err != nil:
		s.fail(w, caldavError{err})
		return
	}
	s.log.Info("task added", "uid", uid, "list", list)
	writeJSON(w, http.StatusCreated, api.AddResponse{UID: uid, ETag: etag, List: list})
}

// deleteTask removes an open task for good. With an If-Match header, only
// that version of the task is removed.
func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()

	task, rest, cur, ok := s.taskForRequest(ctx, w, r)
	if !ok {
		return
	}

	err := s.tasks.Delete(ctx, task)
	switch {
	case errors.Is(err, caldav.ErrConflict):
		writeJSON(w, http.StatusConflict, changedResponse(task))
		return
	case errors.Is(err, caldav.ErrGone):
		writeJSON(w, http.StatusNotFound, api.ErrorResponse{Error: "no open task with this uid"})
		return
	case err != nil:
		s.fail(w, caldavError{err})
		return
	}
	s.log.Info("task removed", "uid", task.UID)

	// Forget its focus state; if it was the current task, pick the next.
	if err := s.store.Unskip(ctx, task.UID); err != nil {
		s.fail(w, err)
		return
	}
	if cur.UID == task.UID {
		if err := s.store.ClearCurrent(ctx); err != nil {
			s.fail(w, err)
			return
		}
		cur = store.Current{}
	}
	if _, _, err := s.update(ctx, rest, cur); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.DeleteResponse{Deleted: api.TaskPtr(task, true)})
}

// renameTask gives an open task a new title. With an If-Match header, only
// that version of the task is renamed.
func (s *Server) renameTask(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var req api.RenameRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Error: "body must be JSON like {\"summary\": \"buy coffee\"}"})
		return
	}
	summary := strings.Join(strings.Fields(req.Summary), " ")
	if summary == "" {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Error: "summary is empty"})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	task, _, cur, ok := s.taskForRequest(ctx, w, r)
	if !ok {
		return
	}

	etag, err := s.tasks.Rename(ctx, task, summary)
	switch {
	case errors.Is(err, caldav.ErrConflict):
		writeJSON(w, http.StatusConflict, changedResponse(task))
		return
	case errors.Is(err, caldav.ErrRecurring):
		writeJSON(w, http.StatusConflict, api.ErrorResponse{Error: err.Error()})
		return
	case err != nil:
		s.fail(w, caldavError{err})
		return
	}
	s.log.Info("task renamed", "uid", task.UID)

	// The user changed the current task themselves, so its new version is
	// the one to complete later; otherwise "done" would report a conflict.
	if cur.UID == task.UID {
		if err := s.store.SetCurrent(ctx, store.Current{UID: task.UID, ETag: etag}); err != nil {
			s.fail(w, err)
			return
		}
	}

	task.Summary, task.ETag = summary, etag
	writeJSON(w, http.StatusOK, api.TaskResponse{Task: api.TaskPtr(task, true)})
}

// taskForRequest finds the open task named by the {uid} in the request
// path and checks an If-Match header against it. It also returns the other
// open tasks and the current task. If something is wrong it answers the
// request itself and returns ok == false. s.mu must be held.
func (s *Server) taskForRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) (task focus.Task, rest []focus.Task, cur store.Current, ok bool) {
	// PathValue returns the {uid} part of the "/tasks/{uid}" pattern.
	uid := r.PathValue("uid")
	tasks, _, cur, err := s.focusState(ctx)
	if err != nil {
		s.fail(w, err)
		return focus.Task{}, nil, cur, false
	}

	found := false
	for _, t := range tasks {
		if t.UID == uid && !found {
			task, found = t, true
		} else {
			rest = append(rest, t)
		}
	}
	if !found {
		writeJSON(w, http.StatusNotFound, api.ErrorResponse{Error: "no open task with this uid"})
		return focus.Task{}, nil, cur, false
	}
	if task.Recurring {
		writeJSON(w, http.StatusConflict, api.ErrorResponse{Error: caldav.ErrRecurring.Error()})
		return focus.Task{}, nil, cur, false
	}
	if want := unquoteETag(r.Header.Get("If-Match")); want != "" && want != "*" && want != task.ETag {
		writeJSON(w, http.StatusConflict, changedResponse(task))
		return focus.Task{}, nil, cur, false
	}
	return task, rest, cur, true
}

// changedResponse is the 409 answer for a task that changed elsewhere; it
// includes the task as it is now.
func changedResponse(task focus.Task) api.ErrorResponse {
	return api.ErrorResponse{
		Error:   "the task was changed elsewhere, so it was left alone",
		Current: api.TaskPtr(task, true),
	}
}

// unquoteETag turns an If-Match value ("\"abc\"") into a bare ETag (abc).
func unquoteETag(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
		return value[1 : len(value)-1]
	}
	return value
}

func (s *Server) done(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()

	tasks, order, cur, err := s.focusState(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(order) == 0 {
		writeJSON(w, http.StatusOK, api.DoneResponse{})
		return
	}

	task := order[0] // the current task, as listed just now
	listedETag := task.ETag
	// Complete the version that became current, not whatever is there now:
	// if it changed since, Complete refuses with ErrConflict.
	task.ETag = cur.ETag
	err = s.tasks.Complete(ctx, task)

	switch {
	case errors.Is(err, caldav.ErrConflict):
		// Leave the task alone and show it as it is now. Remember this
		// version, so a second "done" completes it as the user now sees it.
		if err := s.store.SetCurrent(ctx, store.Current{UID: task.UID, ETag: listedETag}); err != nil {
			s.fail(w, err)
			return
		}
		s.log.Info("done refused: task changed since it became current", "uid", task.UID)
		writeJSON(w, http.StatusConflict, api.ErrorResponse{
			Error:   "the current task was changed elsewhere since it became current, so it was left alone",
			Current: api.TaskPtr(order[0], true),
		})
		return
	case errors.Is(err, caldav.ErrRecurring):
		writeJSON(w, http.StatusConflict, api.ErrorResponse{Error: err.Error()})
		return
	case err != nil:
		s.fail(w, caldavError{err})
		return
	}
	s.log.Info("task completed", "uid", task.UID)

	// Forget the done task and pick the next one from what is left.
	var rest []focus.Task
	for _, t := range tasks {
		if t.UID != task.UID {
			rest = append(rest, t)
		}
	}
	next, err := s.moveOn(ctx, task.UID, false, rest)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.DoneResponse{Done: api.TaskPtr(order[0], true), Current: first(next)})
}

func (s *Server) skip(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()

	tasks, order, _, err := s.focusState(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(order) == 0 {
		writeJSON(w, http.StatusOK, api.SkipResponse{})
		return
	}

	skipped := order[0]
	s.log.Info("task skipped", "uid", skipped.UID)
	next, err := s.moveOn(ctx, skipped.UID, true, tasks)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.SkipResponse{Skipped: api.TaskPtr(skipped, true), Current: first(next)})
}

// moveOn ends the current task uid, either by skipping it (to the end of
// the skip order) or because it is done (out of the skip order), and picks
// the next current task from tasks. It returns the new focus order.
func (s *Server) moveOn(ctx context.Context, uid string, skip bool, tasks []focus.Task) ([]focus.Task, error) {
	var err error
	if skip {
		err = s.store.Skip(ctx, uid)
	} else {
		err = s.store.Unskip(ctx, uid)
	}
	if err != nil {
		return nil, err
	}
	if err := s.store.ClearCurrent(ctx); err != nil {
		return nil, err
	}
	order, _, err := s.update(ctx, tasks, store.Current{})
	return order, err
}

// focusState reads the open tasks from CalDAV and brings the stored focus
// state up to date. It returns all open tasks, the focus order (current
// task first) and the stored current task. s.mu must be held.
func (s *Server) focusState(ctx context.Context) (tasks, order []focus.Task, cur store.Current, err error) {
	tasks, err = s.tasks.OpenTasks(ctx)
	if err != nil {
		return nil, nil, store.Current{}, caldavError{err}
	}
	cur, err = s.store.Current(ctx)
	if err != nil {
		return nil, nil, store.Current{}, err
	}
	order, cur, err = s.update(ctx, tasks, cur)
	return tasks, order, cur, err
}

// update works out the focus order for tasks and stores its first task as
// the current one if that changed (because the old one was done, skipped,
// or completed or deleted elsewhere). It also forgets skips of tasks that
// are gone. cur is the stored current task; the up-to-date one is returned.
func (s *Server) update(ctx context.Context, tasks []focus.Task, cur store.Current) ([]focus.Task, store.Current, error) {
	skipped, err := s.store.Skipped(ctx)
	if err != nil {
		return nil, cur, err
	}
	open := map[string]bool{}
	for _, t := range tasks {
		open[t.UID] = true
	}
	var stillOpen []string
	for _, uid := range skipped {
		if open[uid] {
			stillOpen = append(stillOpen, uid)
		} else if err := s.store.Unskip(ctx, uid); err != nil {
			return nil, cur, err
		}
	}

	order := focus.Order(tasks, s.now(), cur.UID, stillOpen)
	switch {
	case len(order) == 0:
		if cur.UID != "" {
			if err := s.store.ClearCurrent(ctx); err != nil {
				return nil, cur, err
			}
			s.log.Info("no open tasks left")
		}
		return order, store.Current{}, nil
	case order[0].UID != cur.UID:
		next := store.Current{UID: order[0].UID, ETag: order[0].ETag}
		if err := s.store.SetCurrent(ctx, next); err != nil {
			return nil, cur, err
		}
		s.log.Info("new current task", "uid", next.UID)
		return order, next, nil
	}
	return order, cur, nil
}

// caldavError marks an error that came from the CalDAV server, so it is
// answered with 503 (unreachable) or 502 (any other problem) and its
// (human) message.
type caldavError struct{ err error }

func (e caldavError) Error() string { return e.err.Error() }
func (e caldavError) Unwrap() error { return e.err }

// fail answers with an error. CalDAV problems are passed on to the client;
// anything else is logged and reported only as an internal error.
func (s *Server) fail(w http.ResponseWriter, err error) {
	var cerr caldavError
	if errors.As(err, &cerr) {
		s.log.Error("CalDAV request failed", "err", err)
		status := http.StatusBadGateway
		if errors.Is(err, caldav.ErrUnreachable) {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, api.ErrorResponse{Error: err.Error()})
		return
	}
	s.log.Error("request failed", "err", err)
	writeJSON(w, http.StatusInternalServerError, api.ErrorResponse{Error: "internal server error; see the server log"})
}

// first returns the first task of order for a response, or nil.
func first(order []focus.Task) *api.Task {
	if len(order) == 0 {
		return nil
	}
	return api.TaskPtr(order[0], true)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// logRequests logs one line per request. It never logs headers, so the
// token cannot end up in the log.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		// Health checks and requests for unknown routes (scanners) would
		// drown everything else, so they are only logged at debug level.
		// A 404 from the API itself (an unknown task UID) stays at info.
		level := slog.LevelInfo
		if r.URL.Path == "/healthz" || rec.unknownRoute {
			level = slog.LevelDebug
		}
		s.log.Log(r.Context(), level, "request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"client", rec.client,
			"remote", s.clientIP(r),
			"duration", time.Since(start).Round(time.Millisecond),
		)
	})
}

// clientIP returns the address of whoever sent the request. If it came
// through one of the TrustedProxies, the client's address is taken from
// X-Forwarded-For: each proxy appends the address it got the request from,
// so the list is read from the right, skipping trusted proxies, and the
// first other address is the client. Entries further left could have been
// made up by the client and are never used. From anyone else, the header
// is ignored.
func (s *Server) clientIP(r *http.Request) string {
	remote, ok := parseAddr(r.RemoteAddr)
	if !ok {
		return r.RemoteAddr
	}
	if !s.trusted(remote) {
		return remote.String()
	}

	// The header may be sent more than once; together they form one list.
	var hops []string
	for _, value := range r.Header.Values("X-Forwarded-For") {
		for _, hop := range strings.Split(value, ",") {
			hops = append(hops, strings.TrimSpace(hop))
		}
	}

	client := remote
	for i := len(hops) - 1; i >= 0; i-- {
		addr, ok := parseAddr(hops[i])
		if !ok {
			break // garbage: keep the last address that could be checked
		}
		client = addr
		if !s.trusted(addr) {
			break
		}
	}
	return client.String()
}

func (s *Server) trusted(addr netip.Addr) bool {
	for _, p := range s.TrustedProxies {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// parseAddr reads "10.0.0.4", "10.0.0.4:5678", "::1" or "[::1]:5678".
func parseAddr(s string) (netip.Addr, bool) {
	if addr, err := netip.ParseAddr(s); err == nil {
		return addr.Unmap(), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap(), true
	}
	return netip.Addr{}, false
}

// statusRecorder remembers the status code a handler wrote. Embedding
// http.ResponseWriter passes every other method straight through.
type statusRecorder struct {
	http.ResponseWriter
	status       int
	unknownRoute bool   // set when no API route matched the request
	client       string // set when a client was recognized by its token
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
