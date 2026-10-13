package journal

import (
	"testing"
	"time"
)

func TestSuggest(t *testing.T) {
	c := berlin(t)
	day := "2026-10-13"
	tm := func(local string) time.Time { return at(t, c, "2026-10-13 "+local+":00") }
	session := func(start, end, kind, taskUID string) Session {
		s := Session{Date: day, Start: tm(start), Kind: kind, TaskUID: taskUID}
		if end != "" {
			e := tm(end)
			s.End = &e
		}
		return s
	}
	now := tm("23:30")

	tests := []struct {
		name string
		in   SuggestInput

		wantSource     string
		wantMinutes    int
		wantHours      float64
		wantComputer   int
		wantOffline    int
		wantWindows    []string // "start-end kind"
		wantSuspicious int
		wantRunning    bool
	}{
		{
			name: "only focus sessions",
			in: SuggestInput{Sessions: []Session{
				session("09:00", "10:00", "computer", ""),
				session("14:00", "14:30", "computer", ""),
			}},
			wantSource: "sessions", wantMinutes: 90, wantHours: 1.5, wantComputer: 90,
			wantWindows: []string{"09:00-10:00 computer", "14:00-14:30 computer"},
		},
		{
			name: "focus, offline and manual",
			in: SuggestInput{
				Sessions: []Session{
					session("09:00", "10:00", "computer", ""),
					session("17:00", "17:45", "offline", ""),
				},
				Items: []Item{
					{ID: 1, Title: "aus Session", Minutes: ptr(60), Kind: "computer", Source: "focus"}, // counted via the session
					{ID: 2, Title: "Keller aufgeräumt", Minutes: ptr(30), Kind: "offline", Source: "manual"},
					{ID: 3, Title: "Mail", Minutes: ptr(10), Kind: "computer", Source: "manual"},
					{ID: 4, Title: "ohne Minuten", Kind: "offline", Source: "manual"},
				},
			},
			wantSource: "sessions", wantMinutes: 145, wantHours: 2.42, wantComputer: 70, wantOffline: 75,
			wantWindows: []string{"09:00-10:00 computer", "17:00-17:45 offline"},
		},
		{
			name: "only ActivityWatch",
			in: SuggestInput{Entry: Entry{
				AWProductiveMinutes: ptr(210),
				AWWindows:           []Window{{Start: "09:10", End: "12:40"}},
			}},
			wantSource: "aw", wantMinutes: 210, wantHours: 3.5,
			wantWindows: []string{"09:10-12:40 aw"},
		},
		{
			name:       "nothing recorded",
			in:         SuggestInput{},
			wantSource: "none",
		},
		{
			name: "session over 4 hours",
			in: SuggestInput{Sessions: []Session{
				session("08:00", "13:30", "computer", ""), // forgotten to stop
				session("15:00", "16:00", "computer", ""),
			}},
			wantSource: "sessions", wantMinutes: 390, wantHours: 6.5, wantComputer: 390,
			wantWindows:    []string{"08:00-13:30 computer", "15:00-16:00 computer"},
			wantSuspicious: 1,
		},
		{
			name: "running session is not counted",
			in: SuggestInput{Sessions: []Session{
				session("09:00", "10:00", "computer", ""),
				session("18:00", "", "computer", ""), // still running at 23:30
			}},
			wantSource: "sessions", wantMinutes: 60, wantHours: 1, wantComputer: 60,
			wantWindows:    []string{"09:00-10:00 computer"},
			wantSuspicious: 1, // running for 5.5 hours
			wantRunning:    true,
		},
		{
			name: "sessions win over ActivityWatch",
			in: SuggestInput{
				Sessions: []Session{session("09:00", "09:45", "computer", "")},
				Entry:    Entry{AWProductiveMinutes: ptr(300)},
			},
			wantSource: "sessions", wantMinutes: 45, wantHours: 0.75, wantComputer: 45,
			wantWindows: []string{"09:00-09:45 computer"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := tt.in
			in.Date, in.Clock, in.Now = day, c, now
			sg := Suggest(in)

			if sg.Source != tt.wantSource || sg.ProductiveMinutes != tt.wantMinutes || sg.ProductiveHours != tt.wantHours {
				t.Errorf("source %s, %d min, %.2f h; want %s, %d min, %.2f h",
					sg.Source, sg.ProductiveMinutes, sg.ProductiveHours, tt.wantSource, tt.wantMinutes, tt.wantHours)
			}
			if sg.ComputerMinutes != tt.wantComputer || sg.OfflineMinutes != tt.wantOffline {
				t.Errorf("computer %d, offline %d; want %d, %d", sg.ComputerMinutes, sg.OfflineMinutes, tt.wantComputer, tt.wantOffline)
			}
			var windows []string
			for _, w := range sg.ProductiveWindows {
				windows = append(windows, w.Start+"-"+w.End+" "+w.Kind)
			}
			if len(windows) != len(tt.wantWindows) {
				t.Fatalf("windows = %q, want %q", windows, tt.wantWindows)
			}
			for i := range windows {
				if windows[i] != tt.wantWindows[i] {
					t.Errorf("windows = %q, want %q", windows, tt.wantWindows)
				}
			}
			if len(sg.Suspicious) != tt.wantSuspicious || (sg.Running != nil) != tt.wantRunning {
				t.Errorf("suspicious %d, running %v; want %d, %v", len(sg.Suspicious), sg.Running != nil, tt.wantSuspicious, tt.wantRunning)
			}
		})
	}
}

func TestSuggestItems(t *testing.T) {
	c := berlin(t)
	start := at(t, c, "2026-10-13 10:00:00")
	end := start.Add(40 * time.Minute)

	sg := Suggest(SuggestInput{
		Date: "2026-10-13", Clock: c, Now: end,
		Sessions: []Session{{Start: start, End: &end, Kind: "offline", TaskUID: "keller"}},
		Items: []Item{
			{ID: 1, Title: "Rent paid", Source: "task", RefID: "rent", Kind: "computer"},
			{ID: 2, Title: "Notizen", Source: "manual", Kind: "computer", Minutes: ptr(20)},
		},
		Completed: []CompletedTask{
			{UID: "rent", Title: "Rent paid"},          // already recorded
			{UID: "keller", Title: "Keller aufräumen"}, // done on the phone, had a session
			{UID: "anruf", Title: "Arzt anrufen"},      // done on the phone, no session
			{UID: "anruf", Title: "Arzt anrufen"},      // listed twice
		},
	})

	if len(sg.Items) != 4 {
		t.Fatalf("items = %+v, want 2 recorded + 2 proposed", sg.Items)
	}
	keller, anruf := sg.Items[2], sg.Items[3]
	if keller.ID != 0 || keller.RefID != "keller" || keller.Minutes == nil || *keller.Minutes != 40 || keller.Kind != "offline" {
		t.Errorf("proposed keller = %+v", keller)
	}
	if anruf.ID != 0 || anruf.RefID != "anruf" || anruf.Minutes != nil || anruf.Source != "task" {
		t.Errorf("proposed anruf = %+v", anruf)
	}
}

func TestSuggestWindowsUseLocalTime(t *testing.T) {
	c := berlin(t)
	// 21:30 to 01:15 UTC is 23:30 to 03:15 in Berlin (summer time).
	start := time.Date(2026, 10, 12, 21, 30, 0, 0, time.UTC)
	end := time.Date(2026, 10, 13, 1, 15, 0, 0, time.UTC)
	sg := Suggest(SuggestInput{Date: "2026-10-12", Clock: c, Now: end,
		Sessions: []Session{{Start: start, End: &end, Kind: "computer"}}})
	if w := sg.ProductiveWindows[0]; w.Start != "23:30" || w.End != "03:15" {
		t.Errorf("window = %+v, want 23:30-03:15", w)
	}
}
