package server

import (
	"encoding/csv"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"

	"github.com/Mag1cByt3s/beacon/internal/api"
	"github.com/Mag1cByt3s/beacon/internal/caldav/caldavtest"
	"github.com/Mag1cByt3s/beacon/internal/journal"
)

// setNow fixes the server's clock at a Berlin wall-clock time.
func (e *env) setNow(local string) {
	e.t.Helper()
	tm, err := time.ParseInLocation("2006-01-02 15:04", local, e.srv.Clock.Loc)
	if err != nil {
		e.t.Fatal(err)
	}
	e.srv.now = func() time.Time { return tm }
}

// raw sends a request and returns the status and body as text.
func (e *env) raw(method, path, auth, body string) (int, string) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.url+path, strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

func TestJournalToday(t *testing.T) {
	e := newEnv(t)
	tests := []struct {
		now, want string
	}{
		{"2026-10-13 03:59", "2026-10-12"}, // still the evening before
		{"2026-10-13 04:00", "2026-10-13"},
		{"2026-10-13 00:40", "2026-10-12"},
	}
	for _, tt := range tests {
		e.setNow(tt.now)
		var resp api.JournalResponse
		if got := e.do("GET", "/api/journal/today", "", &resp); got != http.StatusOK {
			t.Fatalf("status %d", got)
		}
		if resp.Entry.Date != tt.want || !slices.Equal(resp.Missing, []string{"morning", "evening"}) {
			t.Errorf("at %s: date %s, missing %q; want %s and both parts", tt.now, resp.Entry.Date, resp.Missing, tt.want)
		}
	}
}

func TestJournalPartialUpdates(t *testing.T) {
	e := newEnv(t)
	e.setNow("2026-10-13 09:30")

	// Morning: only the sleep.
	var resp api.JournalResponse
	if got := e.do("PATCH", "/api/journal/today", `{"sleep_quality": 6, "sleep_hours": 7.25}`, &resp); got != http.StatusOK {
		t.Fatalf("status %d", got)
	}
	if !slices.Equal(resp.Missing, []string{"evening"}) || resp.Entry.UpdatedBy != "laptop" {
		t.Errorf("after the morning: missing %q, updated by %q", resp.Missing, resp.Entry.UpdatedBy)
	}

	// Evening, by date: the morning stays, null clears a field.
	e.do("PATCH", "/api/journal/2026-10-13", `{"note": "test"}`, nil)
	e.setNow("2026-10-13 23:30")
	resp = api.JournalResponse{}
	e.do("PATCH", "/api/journal/2026-10-13", `{"productivity": 8, "sport": true, "sport_type": "Laufen", "sport_minutes": 30, "note": null,
		"productive_windows": [{"start": "09:15", "end": "11:40", "kind": "computer"}]}`, &resp)
	en := resp.Entry
	if *en.SleepQuality != 6 || *en.SleepHours != 7.25 || *en.Productivity != 8 || !*en.Sport || en.Note != nil {
		t.Errorf("after the evening: %+v", en)
	}
	if len(resp.Missing) != 0 || len(en.ProductiveWindows) != 1 {
		t.Errorf("missing %q, windows %+v", resp.Missing, en.ProductiveWindows)
	}
	// The body cannot move the entry to another day or fake who changed it.
	resp = api.JournalResponse{}
	e.do("PATCH", "/api/journal/2026-10-13", `{"date": "2020-01-01", "updated_by": "someone"}`, &resp)
	if resp.Entry.Date != "2026-10-13" || resp.Entry.UpdatedBy != "laptop" {
		t.Errorf("body changed date or author: %+v", resp.Entry)
	}
}

func TestJournalValidation(t *testing.T) {
	e := newEnv(t)
	tests := []struct {
		path, body, want string
	}{
		{"/api/journal/today", `{"sleep_quality": 0}`, "sleep_quality must be 1 to 10"},
		{"/api/journal/today", `{"productivity": 11}`, "productivity must be 1 to 10"},
		{"/api/journal/today", `{"sleep_qualty": 5}`, "unknown field"},
		{"/api/journal/today", `{"productive_windows": [{"start": "9 Uhr", "end": "10:00"}]}`, "not a time"},
		{"/api/journal/today", `not json`, "not valid JSON"},
		{"/api/journal/13.10.2026", `{}`, "not a date"},
	}
	for _, tt := range tests {
		status, body := e.raw("PATCH", tt.path, "Bearer "+testToken, tt.body)
		if status != http.StatusBadRequest || !strings.Contains(body, tt.want) {
			t.Errorf("PATCH %s %s: %d %s; want 400 mentioning %q", tt.path, tt.body, status, body, tt.want)
		}
	}
	// Nothing was stored.
	var resp api.JournalResponse
	e.do("GET", "/api/journal/today", "", &resp)
	if !resp.Entry.UpdatedAt.IsZero() {
		t.Errorf("a refused update was stored: %+v", resp.Entry)
	}
}

func TestJournalItems(t *testing.T) {
	e := newEnv(t)
	e.setNow("2026-10-13 21:00")

	var added journal.Item
	if got := e.do("POST", "/api/journal/today/items", `{"title": "  Writeup  schreiben ", "minutes": 90}`, &added); got != http.StatusCreated {
		t.Fatalf("POST status %d", got)
	}
	if added.ID == 0 || added.Title != "Writeup schreiben" || added.Kind != "computer" || added.Source != "manual" || added.Date != "2026-10-13" {
		t.Errorf("added = %+v", added)
	}
	e.do("POST", "/api/journal/2026-10-13/items", `{"title": "Wäsche", "kind": "offline"}`, nil)

	var list api.ItemsResponse
	e.do("GET", "/api/journal/today/items", "", &list)
	if len(list.Items) != 2 || list.Items[1].Minutes != nil || list.Items[1].Kind != "offline" {
		t.Errorf("GET items = %+v", list.Items)
	}

	// PUT replaces the list; a bad item refuses the whole request.
	status, body := e.raw("PUT", "/api/journal/today/items", "Bearer "+testToken,
		`{"items": [{"title": "ok"}, {"title": "bad", "kind": "sofa"}]}`)
	if status != http.StatusBadRequest || !strings.Contains(body, "item 2") {
		t.Errorf("bad PUT: %d %s", status, body)
	}
	list = api.ItemsResponse{}
	e.do("PUT", "/api/journal/today/items", `{"items": [{"title": "Nur das", "minutes": 15, "kind": "offline"}]}`, &list)
	if len(list.Items) != 1 || list.Items[0].Title != "Nur das" || list.Items[0].ID == 0 {
		t.Errorf("PUT items = %+v", list.Items)
	}
	list = api.ItemsResponse{}
	e.do("GET", "/api/journal/today/items", "", &list)
	if len(list.Items) != 1 {
		t.Errorf("after PUT: %+v", list.Items)
	}

	for _, bad := range []string{`{"title": ""}`, `{"title": "x", "minutes": -1}`, `{"title": "x", "source": "magic"}`} {
		if status, body := e.raw("POST", "/api/journal/today/items", "Bearer "+testToken, bad); status != http.StatusBadRequest {
			t.Errorf("POST %s: %d %s, want 400", bad, status, body)
		}
	}
}

func TestJournalAWOnlyForAWSync(t *testing.T) {
	e := newEnv(t)
	e.setNow("2026-10-13 23:15")
	body := `{"minutes": 210, "windows": [{"start": "09:00", "end": "12:30"}]}`

	if status, _ := e.raw("PUT", "/api/journal/today/aw", "Bearer "+testToken, body); status != http.StatusForbidden {
		t.Errorf("laptop PUT /aw: %d, want 403", status)
	}
	if status, _ := e.raw("GET", "/api/journal/today", "Bearer "+testAWToken, ""); status != http.StatusForbidden {
		t.Errorf("aw-sync GET journal: %d, want 403", status)
	}

	var resp api.JournalResponse
	if got := e.doWithAuth("PUT", "/api/journal/today/aw", "Bearer "+testAWToken, body, &resp); got != http.StatusOK {
		t.Fatalf("aw-sync PUT /aw: %d", got)
	}
	en := resp.Entry
	if en.AWProductiveMinutes == nil || *en.AWProductiveMinutes != 210 || len(en.AWWindows) != 1 ||
		en.AWWindows[0].Kind != "aw" || en.UpdatedBy != "aw-sync" {
		t.Errorf("after /aw: %+v", en)
	}
	// It does not count as filling in the day.
	if len(resp.Missing) != 2 {
		t.Errorf("missing = %q, want both parts still open", resp.Missing)
	}
}

func TestJournalRange(t *testing.T) {
	e := newEnv(t)
	for _, day := range []string{"2026-10-01", "2026-10-05", "2026-10-13"} {
		e.do("PATCH", "/api/journal/"+day, `{"sleep_quality": 5}`, nil)
	}
	e.setNow("2026-10-13 12:00")

	var resp api.JournalListResponse
	e.do("GET", "/api/journal?from=2026-10-02&to=today", "", &resp)
	var dates []string
	for _, en := range resp.Entries {
		dates = append(dates, en.Entry.Date)
	}
	if resp.From != "2026-10-02" || resp.To != "2026-10-13" || !slices.Equal(dates, []string{"2026-10-05", "2026-10-13"}) {
		t.Errorf("range = %s..%s %q", resp.From, resp.To, dates)
	}
	resp = api.JournalListResponse{}
	e.do("GET", "/api/journal", "", &resp)
	if resp.From != "2026-09-14" || len(resp.Entries) != 3 {
		t.Errorf("default range = %s..%s with %d entries, want the last 30 days", resp.From, resp.To, len(resp.Entries))
	}
	if status, _ := e.raw("GET", "/api/journal?from=2026-10-13&to=2026-10-01", "Bearer "+testToken, ""); status != http.StatusBadRequest {
		t.Errorf("from after to: %d, want 400", status)
	}
}

func TestJournalCSVExport(t *testing.T) {
	e := newEnv(t)
	e.do("PATCH", "/api/journal/2026-10-12", `{"sleep_quality": 7, "note": "müde, aber \"ok\""}`, nil)
	e.do("PATCH", "/api/journal/2026-10-13", `{"productivity": 8, "productive_windows": [{"start": "09:15", "end": "11:40", "kind": "computer"}]}`, nil)
	e.do("POST", "/api/journal/2026-10-13/items", `{"title": "Box, user flag", "minutes": 120}`, nil)

	req, _ := http.NewRequest("GET", e.url+"/api/journal/export.csv?from=2026-10-01&to=2026-10-31", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("Content-Type = %q", ct)
	}
	rows, err := csv.NewReader(resp.Body).ReadAll()
	if err != nil {
		t.Fatalf("not valid CSV: %v", err)
	}
	if len(rows) != 3 || rows[0][0] != "date" || rows[1][0] != "2026-10-12" || rows[1][1] != "7" || rows[1][12] != `müde, aber "ok"` {
		t.Errorf("entries CSV = %q", rows)
	}
	if rows[2][5] != "09:15-11:40 computer" || rows[2][14] != "laptop" {
		t.Errorf("row 2 = %q", rows[2])
	}

	_, body := e.raw("GET", "/api/journal/items/export.csv?from=2026-10-13&to=2026-10-13", "Bearer "+testToken, "")
	items, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil || len(items) != 2 || items[1][1] != "Box, user flag" || items[1][2] != "120" {
		t.Errorf("items CSV = %q, %v", items, err)
	}
}

func TestJournalSuggestion(t *testing.T) {
	e := newEnv(t)

	// A focus session and a manual item via the API.
	e.setNow("2026-10-13 09:00")
	e.do("POST", "/api/focus/start", `{"label": "HTB"}`, nil)
	e.setNow("2026-10-13 10:00")
	e.do("POST", "/api/focus/stop", `{"result": "User-Flag"}`, nil)
	e.do("POST", "/api/journal/today/items", `{"title": "Keller", "minutes": 30, "kind": "offline"}`, nil)
	// A task completed on the phone in the afternoon (14:00 UTC is 16:00 in Berlin).
	todo := e.caldav.Todo(caldavtest.TodoPath + "1.ics")
	todo.Props.SetText(ical.PropStatus, "COMPLETED")
	todo.Props.SetDateTime(ical.PropCompleted, time.Date(2026, 10, 13, 14, 0, 0, 0, time.UTC))

	e.setNow("2026-10-13 23:30")
	var sg journal.Suggestion
	if got := e.do("GET", "/api/journal/today/suggestion", "", &sg); got != http.StatusOK {
		t.Fatalf("status %d", got)
	}
	if sg.Date != "2026-10-13" || sg.Source != "sessions" || sg.ProductiveMinutes != 90 ||
		sg.ComputerMinutes != 60 || sg.OfflineMinutes != 30 || len(sg.ProductiveWindows) != 1 || len(sg.Warnings) != 0 {
		t.Errorf("suggestion = %+v", sg)
	}
	var titles []string
	for _, it := range sg.Items {
		titles = append(titles, it.Title)
	}
	if !slices.Equal(titles, []string{"User-Flag", "Keller", "Open task"}) {
		t.Errorf("items = %q, want the two recorded and the task from the phone", titles)
	}

	// Without CalDAV the suggestion still comes, with a warning.
	e.davStop()
	sg = journal.Suggestion{}
	if got := e.do("GET", "/api/journal/today/suggestion", "", &sg); got != http.StatusOK {
		t.Fatalf("status without CalDAV: %d", got)
	}
	if len(sg.Warnings) != 1 || sg.ProductiveMinutes != 90 || len(sg.Items) != 2 {
		t.Errorf("suggestion without CalDAV = %+v", sg)
	}
}
