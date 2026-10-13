package awsync

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/journal"
)

var berlin, _ = time.LoadLocation("Europe/Berlin")

func defaultCategorizer(t *testing.T) *Categorizer {
	t.Helper()
	c, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCategory(t *testing.T) {
	c := defaultCategorizer(t)
	tests := []struct {
		app, title string
		want       []string
	}{
		{"org.kde.konsole", "~ : zsh", []string{"Arbeit"}},
		{"com.mitchellh.ghostty", "nvim", []string{"Arbeit"}},
		{"VSCodium", "main.go", []string{"Arbeit"}},
		{"firefox", "Hack The Box :: Machines", []string{"Arbeit"}},
		{"firefox", "mag1cbyt3s/beacon - github", []string{"Arbeit"}}, // any case
		{"firefox", "HTB Sau - IppSec - YouTube", []string{"Arbeit", "Lernvideos"}},
		{"firefox", "0xdf hacks stuff", []string{"Arbeit", "Lernvideos"}},
		{"firefox", "Cat videos - YouTube", []string{"Ablenkung"}},
		{"firefox", "r/NixOS - Reddit", []string{"Arbeit"}}, // equally deep: the first rule wins
		{"Spotify", "Some song", nil},
		{"", "", nil},
	}
	for _, tt := range tests {
		if got := c.Category(tt.app, tt.title); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Category(%q, %q) = %v, want %v", tt.app, tt.title, got, tt.want)
		}
	}
}

// event is a window event from start (Berlin time on 2026-10-13, or the
// next day for hours below 4) lasting minutes.
func event(start string, minutes float64, app, title string) Event {
	tm, _ := time.ParseInLocation("2006-01-02 15:04", "2026-10-13 "+start, berlin)
	if tm.Hour() < 4 {
		tm = tm.AddDate(0, 0, 1)
	}
	e := Event{Timestamp: tm.UTC(), Duration: minutes * 60}
	e.Data.App, e.Data.Title = app, title
	return e
}

func TestSummarize(t *testing.T) {
	c := defaultCategorizer(t)
	clock, _ := journal.NewClock("Europe/Berlin", "04:00")
	from, to, _ := clock.Bounds("2026-10-13")
	early := event("03:50", 30, "konsole", "") // starts on the day before: 04:00-04:20 counts
	early.Timestamp = early.Timestamp.AddDate(0, 0, -1)

	events := []Event{
		event("09:25", 15, "konsole", ""), // out of order on purpose
		early,
		event("09:00", 20, "kitty", ""),
		event("09:30", 5, "kitty", ""),          // overlaps the one at 09:25: counted once
		event("10:00", 60, "firefox", "Twitch"), // distraction
		event("11:00", 10, "codium", ""),        // too short for a window, still counted
		event("11:20", 10, "codium", ""),        // exactly 10 minutes later: not merged
		event("14:00", 10, "firefox", "IppSec - YouTube"),
		event("14:05", 12, "firefox", "GitHub"), // overlaps: 14:00-14:17
		event("23:50", 30, "nvim", ""),          // over midnight
		event("03:55", 10, "nvim", ""),          // ends after the day: 03:55-04:00 counts
	}
	minutes, windows := c.Summarize(events, from, to, berlin)

	// 20 (04:00-04:20) + 35 (09:00-09:20, 09:25-09:40) + 10 + 10 + 17 + 30 + 5
	if minutes != 127 {
		t.Errorf("minutes = %d, want 127", minutes)
	}
	want := []journal.Window{
		{Start: "04:00", End: "04:20", Kind: "aw"},
		{Start: "09:00", End: "09:40", Kind: "aw"},
		{Start: "14:00", End: "14:17", Kind: "aw"},
		{Start: "23:50", End: "00:20", Kind: "aw"},
	}
	if !reflect.DeepEqual(windows, want) {
		t.Errorf("windows = %+v\nwant %+v", windows, want)
	}
}

func TestSummarizeNothing(t *testing.T) {
	c := defaultCategorizer(t)
	minutes, windows := c.Summarize(nil, time.Now().Add(-time.Hour), time.Now(), berlin)
	if minutes != 0 || windows == nil || len(windows) != 0 {
		t.Errorf("Summarize(nil) = %d, %#v; want 0 and an empty list", minutes, windows)
	}
}

func TestMergeGapSetting(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MergeGapMinutes, cfg.MinWindowMinutes = 30, 0
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	from := event("04:00", 0, "", "").Timestamp
	_, windows := c.Summarize([]Event{
		event("09:00", 5, "konsole", ""),
		event("09:25", 5, "konsole", ""),
		event("11:00", 1, "konsole", ""),
	}, from, from.Add(24*time.Hour), berlin)
	if len(windows) != 2 || windows[0].End != "09:30" || windows[1].Start != "11:00" {
		t.Errorf("windows = %+v", windows)
	}
}

func TestParseConfig(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{"rules": [{"category": ["Work"], "title": "jira"}], "productive": "Work"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Rules) != 1 || cfg.Productive != "Work" || cfg.MergeGapMinutes != 10 || cfg.MinWindowMinutes != 15 {
		t.Errorf("config = %+v; want one rule and the default gaps", cfg)
	}
	for _, bad := range []string{`{}`, `{"rules": []}`, `not json`, `{"rules": [{"category": ["x"], "app": "y"}], "productive": 1}`} {
		if _, err := ParseConfig([]byte(bad)); err == nil {
			t.Errorf("ParseConfig(%s) was accepted", bad)
		}
	}
}

func TestNewRefuses(t *testing.T) {
	rule := func(r Rule) Config {
		c := DefaultConfig()
		c.Rules = []Rule{r}
		return c
	}
	noProductive := DefaultConfig()
	noProductive.Productive = ""
	negative := DefaultConfig()
	negative.MinWindowMinutes = -1
	for name, cfg := range map[string]Config{
		"bad regex":      rule(Rule{Category: []string{"Arbeit"}, App: "("}),
		"no category":    rule(Rule{App: "x"}),
		"empty part":     rule(Rule{Category: []string{"Arbeit", ""}, App: "x"}),
		"no pattern":     rule(Rule{Category: []string{"Arbeit"}}),
		"no productive":  noProductive,
		"negative times": negative,
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestEvents(t *testing.T) {
	from := time.Date(2026, 10, 13, 4, 0, 0, 0, berlin)
	to := from.Add(24 * time.Hour)
	var gotBody map[string]any
	aw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/0/query/" {
			http.Error(w, "wrong request "+r.Method+" "+r.URL.Path, http.StatusNotFound)
			return
		}
		data, _ := io.ReadAll(r.Body)
		json.Unmarshal(data, &gotBody)
		io.WriteString(w, `[[{"id": 1, "timestamp": "2026-10-13T07:00:00.123+00:00", "duration": 90.5,
			"data": {"app": "konsole", "title": "zsh"}}]]`)
	}))
	defer aw.Close()

	events, err := Events(context.Background(), aw.URL, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Data.App != "konsole" || events[0].Duration != 90.5 ||
		!events[0].Timestamp.Equal(time.Date(2026, 10, 13, 7, 0, 0, 123e6, time.UTC)) {
		t.Errorf("events = %+v", events)
	}
	periods, _ := gotBody["timeperiods"].([]any)
	if len(periods) != 1 || periods[0] != "2026-10-13T04:00:00+02:00/2026-10-14T04:00:00+02:00" {
		t.Errorf("timeperiods = %v", gotBody["timeperiods"])
	}
	if q, _ := gotBody["query"].([]any); len(q) == 0 || !strings.Contains(q[len(q)-1].(string), "RETURN") {
		t.Errorf("query = %v", gotBody["query"])
	}
}

func TestEventsErrors(t *testing.T) {
	now := time.Now()
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message": "There's no bucket named aw-watcher-afk_"}`, http.StatusInternalServerError)
	}))
	defer failing.Close()
	if _, err := Events(context.Background(), failing.URL, now, now); err == nil || !strings.Contains(err.Error(), "no bucket") {
		t.Errorf("server error: err = %v, want its message", err)
	}

	gone := httptest.NewServer(nil)
	gone.Close()
	if _, err := Events(context.Background(), gone.URL, now, now); err == nil || !strings.Contains(err.Error(), "is it running") {
		t.Errorf("unreachable: err = %v", err)
	}
	if _, err := Events(context.Background(), "localhost:5600", now, now); err == nil {
		t.Error("a URL without a scheme was accepted")
	}
}
