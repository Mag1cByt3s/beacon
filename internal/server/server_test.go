package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emersion/go-ical"

	"github.com/Mag1cByt3s/beacon/internal/api"
	"github.com/Mag1cByt3s/beacon/internal/caldav"
	"github.com/Mag1cByt3s/beacon/internal/caldav/caldavtest"
	"github.com/Mag1cByt3s/beacon/internal/store"
)

// A throwaway token for tests. Not a secret.
const testToken = "test-token-0123456789"

// env is one running beacon server on top of a fake CalDAV server.
type env struct {
	t       *testing.T
	url     string
	caldav  *caldavtest.Backend
	logs    *bytes.Buffer
	dbPath  string
	davStop func()
}

// newEnv starts beacon with the lists Todo and Groceries. At the start the
// open tasks are "milk" (Groceries) and "open" (Todo); neither has a due
// date or priority, so "milk" comes first by UID.
func newEnv(t *testing.T) *env {
	t.Helper()
	davSrv, backend := caldavtest.NewServer(t)
	client, err := caldav.NewClient(davSrv.URL+"/", caldavtest.User, caldavtest.Password, []string{"Todo", "Groceries"})
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "beacon.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srv := httptest.NewServer(New(client, st, testToken, "Todo", logger).Handler())
	t.Cleanup(srv.Close)

	return &env{t: t, url: srv.URL, caldav: backend, logs: logs, dbPath: dbPath, davStop: davSrv.Close}
}

// do sends a request with the test token and decodes the JSON answer into
// out (if not nil). It returns the status code.
func (e *env) do(method, path, body string, out any) int {
	e.t.Helper()
	return e.doWithAuth(method, path, "Bearer "+testToken, body, out)
}

func (e *env) doWithAuth(method, path, auth, body string, out any) int {
	e.t.Helper()
	req, err := http.NewRequest(method, e.url+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			e.t.Fatalf("%s %s: bad JSON %q: %v", method, path, data, err)
		}
	}
	return resp.StatusCode
}

// current returns the UID of the current task, or "" if there is none.
func (e *env) current() string {
	e.t.Helper()
	var resp api.CurrentResponse
	if status := e.do("GET", "/current", "", &resp); status != http.StatusOK {
		e.t.Fatalf("GET /current: status %d", status)
	}
	return uidOf(resp.Task)
}

// order returns the UIDs from GET /tasks.
func (e *env) order() string {
	e.t.Helper()
	var resp api.TasksResponse
	if status := e.do("GET", "/tasks", "", &resp); status != http.StatusOK {
		e.t.Fatalf("GET /tasks: status %d", status)
	}
	var uids []string
	for _, task := range resp.Tasks {
		uids = append(uids, task.UID)
	}
	return strings.Join(uids, ",")
}

func uidOf(t *api.Task) string {
	if t == nil {
		return ""
	}
	return t.UID
}

func status(todo *ical.Component) string {
	s, _ := todo.Props.Text(ical.PropStatus)
	return s
}

func TestHealthzNeedsNoToken(t *testing.T) {
	e := newEnv(t)
	resp, err := http.Get(e.url + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestAuth(t *testing.T) {
	e := newEnv(t)

	auths := []struct {
		name string
		auth string
		want int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong token", "Bearer wrong-token-0123456789", http.StatusUnauthorized},
		{"token prefix only", "Bearer " + testToken[:10], http.StatusUnauthorized},
		{"token with extra", "Bearer " + testToken + "x", http.StatusUnauthorized},
		{"basic auth", "Basic " + testToken, http.StatusUnauthorized},
		{"bare token", testToken, http.StatusUnauthorized},
		{"right token", "Bearer " + testToken, http.StatusOK},
	}
	endpoints := []struct{ method, path, body string }{
		{"GET", "/current", ""},
		{"GET", "/tasks", ""},
		{"POST", "/tasks", `{"summary":"x"}`},
		{"POST", "/current/done", ""},
		{"POST", "/current/skip", ""},
		{"GET", "/unknown", ""},
	}
	for _, a := range auths {
		for _, ep := range endpoints {
			if a.want == http.StatusOK {
				continue // the endpoints themselves are tested below
			}
			t.Run(a.name+" "+ep.method+" "+ep.path, func(t *testing.T) {
				var resp map[string]any
				got := e.doWithAuth(ep.method, ep.path, a.auth, ep.body, &resp)
				if got != a.want {
					t.Errorf("status = %d, want %d", got, a.want)
				}
				// No details beyond "unauthorized".
				if len(resp) != 1 || resp["error"] != "unauthorized" {
					t.Errorf("body = %v", resp)
				}
			})
		}
		if a.want == http.StatusOK {
			if got := e.doWithAuth("GET", "/current", a.auth, "", nil); got != http.StatusOK {
				t.Errorf("right token: status %d", got)
			}
		}
	}

	// Nothing was changed by the rejected requests.
	if got := len(e.caldav.Objects[caldavtest.TodoPath]); got != 2 {
		t.Errorf("Todo has %d objects, want 2", got)
	}
	if status(e.caldav.Todo(caldavtest.GroceriesPath+"1.ics")) == "COMPLETED" {
		t.Error("a rejected request completed a task")
	}
}

func TestCurrentIsSticky(t *testing.T) {
	e := newEnv(t)
	if got := e.current(); got != "milk" {
		t.Fatalf("current = %q, want milk", got)
	}

	// A more urgent task arrives (overdue, top priority) ...
	e.caldav.Add(t, caldavtest.TodoPath+"urgent.ics",
		"BEGIN:VTODO", "UID:urgent", "DTSTAMP:20261001T100000Z", "SUMMARY:Urgent",
		"DUE;VALUE=DATE:20200101", "PRIORITY:1", "END:VTODO")
	// ... and the current task is edited on the phone.
	e.caldav.Edit(t, caldavtest.GroceriesPath+"1.ics", "Oat milk")

	// The current task stays current; the rest is ordered as usual.
	if got := e.current(); got != "milk" {
		t.Errorf("current = %q, want milk to stay current", got)
	}
	if got := e.order(); got != "milk,urgent,open" {
		t.Errorf("order = %q, want milk,urgent,open", got)
	}
}

func TestCurrentGoneElsewhere(t *testing.T) {
	tests := []struct {
		name   string
		change func(*caldavtest.Backend)
	}{
		{"deleted on the phone", func(b *caldavtest.Backend) {
			b.Remove(caldavtest.GroceriesPath + "1.ics")
		}},
		{"completed on the phone", func(b *caldavtest.Backend) {
			b.Todo(caldavtest.GroceriesPath+"1.ics").Props.SetText(ical.PropStatus, "COMPLETED")
		}},
		{"became recurring", func(b *caldavtest.Backend) {
			b.Todo(caldavtest.GroceriesPath+"1.ics").Props.SetText(ical.PropRecurrenceRule, "FREQ=DAILY")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			if got := e.current(); got != "milk" {
				t.Fatalf("current = %q, want milk", got)
			}
			tt.change(e.caldav)
			if got := e.current(); got != "open" {
				t.Errorf("current = %q, want open", got)
			}
		})
	}
}

func TestDone(t *testing.T) {
	e := newEnv(t)

	var resp api.DoneResponse
	if got := e.do("POST", "/current/done", "", &resp); got != http.StatusOK {
		t.Fatalf("status = %d, want 200", got)
	}
	if uidOf(resp.Done) != "milk" || uidOf(resp.Current) != "open" {
		t.Errorf("done = %q, current = %q; want milk, open", uidOf(resp.Done), uidOf(resp.Current))
	}
	if got := status(e.caldav.Todo(caldavtest.GroceriesPath + "1.ics")); got != "COMPLETED" {
		t.Errorf("milk STATUS = %q, want COMPLETED", got)
	}
	if got := e.current(); got != "open" {
		t.Errorf("current = %q, want open", got)
	}

	// Complete the last one, then there is nothing left.
	resp = api.DoneResponse{}
	e.do("POST", "/current/done", "", &resp)
	if uidOf(resp.Done) != "open" || resp.Current != nil {
		t.Errorf("done = %q, current = %v; want open, null", uidOf(resp.Done), resp.Current)
	}
	resp = api.DoneResponse{}
	if got := e.do("POST", "/current/done", "", &resp); got != http.StatusOK || resp.Done != nil {
		t.Errorf("done with nothing open: status %d, done %v", got, resp.Done)
	}
}

func TestDoneConflict(t *testing.T) {
	e := newEnv(t)
	if got := e.current(); got != "milk" {
		t.Fatalf("current = %q, want milk", got)
	}

	// The current task is edited on the phone after it became current.
	e.caldav.Edit(t, caldavtest.GroceriesPath+"1.ics", "Oat milk")

	var conflict api.ErrorResponse
	if got := e.do("POST", "/current/done", "", &conflict); got != http.StatusConflict {
		t.Fatalf("status = %d, want 409", got)
	}
	if conflict.Error == "" || conflict.Current == nil || conflict.Current.Summary != "Oat milk" {
		t.Errorf("409 body = %+v, want a message and the task as it is now", conflict)
	}
	todo := e.caldav.Todo(caldavtest.GroceriesPath + "1.ics")
	if status(todo) == "COMPLETED" {
		t.Fatal("the changed task was completed")
	}
	if got := e.current(); got != "milk" {
		t.Errorf("current = %q, want milk to stay current", got)
	}

	// The user has now seen the new version; a second done completes it.
	var resp api.DoneResponse
	if got := e.do("POST", "/current/done", "", &resp); got != http.StatusOK {
		t.Fatalf("second done: status %d, want 200", got)
	}
	if got := status(e.caldav.Todo(caldavtest.GroceriesPath + "1.ics")); got != "COMPLETED" {
		t.Errorf("milk STATUS = %q, want COMPLETED after the second done", got)
	}
}

func TestDoneConflictDuringWrite(t *testing.T) {
	e := newEnv(t)
	e.current()

	// The edit lands between beacon's read and its write.
	e.caldav.AfterGet = func(path string) {
		e.caldav.AfterGet = nil
		e.caldav.Edit(t, path, "Edited at the worst moment")
	}
	if got := e.do("POST", "/current/done", "", nil); got != http.StatusConflict {
		t.Fatalf("status = %d, want 409", got)
	}
	if status(e.caldav.Todo(caldavtest.GroceriesPath+"1.ics")) == "COMPLETED" {
		t.Error("the changed task was completed")
	}
}

func TestSkip(t *testing.T) {
	e := newEnv(t)
	e.caldav.Add(t, caldavtest.TodoPath+"zzz.ics",
		"BEGIN:VTODO", "UID:zzz", "DTSTAMP:20261001T100000Z", "SUMMARY:Last", "END:VTODO")
	// Order without focus state: milk, open, zzz.

	skip := func(wantSkipped, wantCurrent string) {
		t.Helper()
		var resp api.SkipResponse
		if got := e.do("POST", "/current/skip", "", &resp); got != http.StatusOK {
			t.Fatalf("skip: status %d", got)
		}
		if uidOf(resp.Skipped) != wantSkipped || uidOf(resp.Current) != wantCurrent {
			t.Errorf("skip: skipped %q, current %q; want %q, %q",
				uidOf(resp.Skipped), uidOf(resp.Current), wantSkipped, wantCurrent)
		}
	}

	skip("milk", "open")
	if got := e.order(); got != "open,zzz,milk" {
		t.Errorf("order = %q, want open,zzz,milk", got)
	}
	skip("open", "zzz")
	// Everything else is skipped now, so skipped tasks come back,
	// the one skipped longest ago first.
	skip("zzz", "milk")
	skip("milk", "open")
	if got := e.order(); got != "open,zzz,milk" {
		t.Errorf("order = %q, want open,zzz,milk", got)
	}

	// Completing a skipped task removes it from the skip order for good.
	e.do("POST", "/current/done", "", nil)
	if got := e.order(); got != "zzz,milk" {
		t.Errorf("order after done = %q, want zzz,milk", got)
	}

	// Nothing to skip when nothing is open.
	e.do("POST", "/current/done", "", nil)
	e.do("POST", "/current/done", "", nil)
	skip("", "")
}

func TestFocusStateSurvivesRestart(t *testing.T) {
	e := newEnv(t)
	e.do("POST", "/current/skip", "", nil) // milk skipped, open current

	// A second server on the same database and CalDAV data.
	st, err := store.Open(e.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if cur, _ := st.Current(t.Context()); cur.UID != "open" {
		t.Errorf("stored current = %q, want open", cur.UID)
	}
	if skipped, _ := st.Skipped(t.Context()); len(skipped) != 1 || skipped[0] != "milk" {
		t.Errorf("stored skips = %q, want [milk]", skipped)
	}
}

func TestAddTask(t *testing.T) {
	e := newEnv(t)
	e.current() // milk becomes current

	var resp api.AddResponse
	if got := e.do("POST", "/tasks", `{"summary":"  buy   coffee "}`, &resp); got != http.StatusCreated {
		t.Fatalf("status = %d, want 201", got)
	}
	if resp.List != "Todo" {
		t.Errorf("list = %q, want Todo", resp.List)
	}
	objects := e.caldav.Objects[caldavtest.TodoPath]
	added := objects[len(objects)-1].Data.Children[0]
	if s, _ := added.Props.Text(ical.PropSummary); s != "buy coffee" {
		t.Errorf("SUMMARY = %q, want %q", s, "buy coffee")
	}
	if got := e.current(); got != "milk" {
		t.Errorf("current = %q; adding a task must not change it", got)
	}

	for _, body := range []string{`{"summary":"   "}`, `{}`, `not json`, ``} {
		var errResp api.ErrorResponse
		if got := e.do("POST", "/tasks", body, &errResp); got != http.StatusBadRequest || errResp.Error == "" {
			t.Errorf("body %q: status %d, error %q; want 400 with a message", body, got, errResp.Error)
		}
	}
}

func TestCalDAVDown(t *testing.T) {
	e := newEnv(t)
	e.davStop()

	var resp api.ErrorResponse
	if got := e.do("GET", "/current", "", &resp); got != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", got)
	}
	if !strings.Contains(resp.Error, "CalDAV") {
		t.Errorf("error = %q, want it to mention CalDAV", resp.Error)
	}
}

func TestLogsHaveNoSecrets(t *testing.T) {
	e := newEnv(t)
	e.do("GET", "/current", "", nil)
	e.do("POST", "/current/skip", "", nil)
	e.do("POST", "/current/done", "", nil)
	e.doWithAuth("GET", "/current", "Bearer wrong-token-0123456789", "", nil)

	logs := e.logs.String()
	for _, secret := range []string{testToken, "wrong-token", caldavtest.Password} {
		if strings.Contains(logs, secret) {
			t.Errorf("log contains %q:\n%s", secret, logs)
		}
	}
	if !strings.Contains(logs, "task completed") || !strings.Contains(logs, "status=401") {
		t.Errorf("log is missing expected lines:\n%s", logs)
	}
}
