package journal

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func berlin(t *testing.T) Clock {
	t.Helper()
	c, err := NewClock("Europe/Berlin", "04:00")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// at returns a moment in Berlin local time.
func at(t *testing.T, c Clock, value string) time.Time {
	t.Helper()
	tm, err := time.ParseInLocation("2006-01-02 15:04:05", value, c.Loc)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func TestDay(t *testing.T) {
	c := berlin(t)
	tests := []struct {
		local string
		want  string
	}{
		{"2026-10-13 03:59:59", "2026-10-12"}, // still the evening before
		{"2026-10-13 04:00:00", "2026-10-13"}, // the new day starts
		{"2026-10-13 00:40:00", "2026-10-12"}, // the example from the spec
		{"2026-10-13 23:59:59", "2026-10-13"},
		{"2026-10-01 01:00:00", "2026-09-30"}, // across a month
		{"2027-01-01 03:00:00", "2026-12-31"}, // across a year
		// Clocks go forward on 2026-03-29 at 02:00: 04:00 is still the start.
		{"2026-03-29 03:59:00", "2026-03-28"},
		{"2026-03-29 04:00:00", "2026-03-29"},
		// Clocks go back on 2026-10-25 at 03:00: 04:00 is still the start.
		{"2026-10-25 03:59:00", "2026-10-24"},
		{"2026-10-25 04:00:00", "2026-10-25"},
	}
	for _, tt := range tests {
		if got := c.Day(at(t, c, tt.local)); got != tt.want {
			t.Errorf("Day(%s) = %s, want %s", tt.local, got, tt.want)
		}
	}

	// The same moment in UTC gives the same day.
	utc := at(t, c, "2026-10-13 03:59:59").UTC()
	if got := c.Day(utc); got != "2026-10-12" {
		t.Errorf("Day(%v UTC) = %s, want 2026-10-12", utc, got)
	}
}

func TestBounds(t *testing.T) {
	c := berlin(t)
	tests := []struct {
		date   string
		length time.Duration
	}{
		{"2026-10-13", 24 * time.Hour},
		// The clocks change at night, before 04:00, so the short and the
		// long journal day are the ones before.
		{"2026-03-28", 23 * time.Hour}, // clocks go forward on the 29th at 02:00
		{"2026-03-29", 24 * time.Hour},
		{"2026-10-24", 25 * time.Hour}, // clocks go back on the 25th at 03:00
		{"2026-10-25", 24 * time.Hour},
	}
	for _, tt := range tests {
		start, end, err := c.Bounds(tt.date)
		if err != nil {
			t.Fatal(err)
		}
		if got := start.In(c.Loc).Format("2006-01-02 15:04"); got != tt.date+" 04:00" {
			t.Errorf("%s starts %s, want 04:00 that day", tt.date, got)
		}
		if got := end.Sub(start); got != tt.length {
			t.Errorf("%s lasts %v, want %v", tt.date, got, tt.length)
		}
		// Bounds and Day agree at both edges.
		if c.Day(start) != tt.date || c.Day(end.Add(-time.Nanosecond)) != tt.date || c.Day(end) == tt.date {
			t.Errorf("%s: Day and Bounds disagree", tt.date)
		}
	}
	if _, _, err := c.Bounds("13.10.2026"); err == nil {
		t.Error("Bounds accepted a German date")
	}
}

func TestResolve(t *testing.T) {
	c := berlin(t)
	now := at(t, c, "2026-10-13 00:40:00")
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{"today", "2026-10-12", false},
		{"TODAY", "2026-10-12", false},
		{"2026-10-01", "2026-10-01", false},
		{"2026-13-01", "", true},
		{"yesterday", "", true},
		{"", "", true},
	}
	for _, tt := range tests {
		got, err := c.Resolve(tt.in, now)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("Resolve(%q) = %q, %v; want %q, error %v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestNewClockErrors(t *testing.T) {
	if _, err := NewClock("Mars/Olympus", "04:00"); err == nil {
		t.Error("unknown zone accepted")
	}
	if _, err := NewClock("Europe/Berlin", "4 Uhr"); err == nil {
		t.Error("bad day start accepted")
	}
	c, err := NewClock("UTC", "05:30")
	if err != nil || c.Hour != 5 || c.Minute != 30 {
		t.Errorf("NewClock = %+v, %v", c, err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestMissing(t *testing.T) {
	tests := []struct {
		e    Entry
		want []string
	}{
		{Entry{}, []string{"morning", "evening"}},
		{Entry{SleepQuality: ptr(7)}, []string{"evening"}},
		{Entry{Productivity: ptr(5)}, []string{"morning"}},
		{Entry{SleepQuality: ptr(7), Productivity: ptr(5)}, []string{}},
	}
	for _, tt := range tests {
		if got := tt.e.Missing(); !slices.Equal(got, tt.want) {
			t.Errorf("Missing = %q, want %q", got, tt.want)
		}
	}
}

func TestEntryValidate(t *testing.T) {
	tests := []struct {
		name    string
		e       Entry
		wantErr string // "" means valid
	}{
		{"empty", Entry{}, ""},
		{"all fine", Entry{
			SleepQuality: ptr(1), Productivity: ptr(10), SleepHours: ptr(7.5),
			ProductiveHours: ptr(4.25), SportMinutes: ptr(45), Steps: ptr(9000),
			ProductiveWindows: []Window{{Start: "09:15", End: "11:40", Kind: "computer"}, {Start: "23:30", End: "01:10"}},
		}, ""},
		{"sleep quality 0", Entry{SleepQuality: ptr(0)}, "sleep_quality must be 1 to 10"},
		{"sleep quality 11", Entry{SleepQuality: ptr(11)}, "sleep_quality must be 1 to 10"},
		{"productivity 11", Entry{Productivity: ptr(11)}, "productivity must be 1 to 10"},
		{"25 hours", Entry{ProductiveHours: ptr(25.0)}, "productive_hours"},
		{"negative steps", Entry{Steps: ptr(-1)}, "steps"},
		{"bad window time", Entry{ProductiveWindows: []Window{{Start: "9 Uhr", End: "10:00"}}}, "not a time"},
		{"bad window kind", Entry{AWWindows: []Window{{Start: "09:00", End: "10:00", Kind: "sofa"}}}, "kind"},
		{"two problems", Entry{SleepQuality: ptr(0), Productivity: ptr(0)}, "productivity"},
	}
	for _, tt := range tests {
		err := tt.e.Validate()
		switch {
		case tt.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error %v", tt.name, err)
		case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
			t.Errorf("%s: err = %v, want one mentioning %q", tt.name, err, tt.wantErr)
		}
	}
}

// TestEntryPartialUpdate shows how a JSON body updates an entry: fields
// it mentions change, null clears, everything else stays.
func TestEntryPartialUpdate(t *testing.T) {
	e := Entry{Date: "2026-10-12", SleepQuality: ptr(7), Note: ptr("gut")}
	if err := json.Unmarshal([]byte(`{"productivity": 6, "note": null}`), &e); err != nil {
		t.Fatal(err)
	}
	if e.SleepQuality == nil || *e.SleepQuality != 7 {
		t.Error("sleep_quality was not kept")
	}
	if e.Productivity == nil || *e.Productivity != 6 {
		t.Error("productivity was not set")
	}
	if e.Note != nil {
		t.Error("note was not cleared by null")
	}
}

func TestItemValidateAndNormalize(t *testing.T) {
	good := Item{Title: "  Writeup   schreiben ", Minutes: ptr(90)}
	if err := good.Validate(); err != nil {
		t.Errorf("valid item: %v", err)
	}
	n := good.Normalize()
	if n.Title != "Writeup schreiben" || n.Kind != KindComputer || n.Source != SourceManual {
		t.Errorf("Normalize = %+v", n)
	}

	for _, bad := range []Item{
		{Title: "  "},
		{Title: "x", Minutes: ptr(-5)},
		{Title: "x", Minutes: ptr(2000)},
		{Title: "x", Kind: "aw"}, // aw is only for windows
		{Title: "x", Source: "magic"},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
}

func TestSessionMinutes(t *testing.T) {
	start := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	end := start.Add(95*time.Minute + 20*time.Second)
	if got := (Session{Start: start, End: &end}).Minutes(time.Time{}); got != 95 {
		t.Errorf("stopped session: %d minutes, want 95", got)
	}
	if got := (Session{Start: start}).Minutes(start.Add(30 * time.Minute)); got != 30 {
		t.Errorf("running session: %d minutes, want 30", got)
	}
}
