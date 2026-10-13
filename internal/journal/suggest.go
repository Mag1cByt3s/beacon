package journal

import (
	"math"
	"time"
)

// SuspiciousAfter is how long a session may run before it is probably one
// that was forgotten.
const SuspiciousAfter = 4 * time.Hour

// Where a suggestion's productive time comes from.
const (
	FromSessions = "sessions" // focus sessions and manual items
	FromAW       = "aw"       // nothing recorded: ActivityWatch's measurement
	FromNothing  = "none"     // no data at all
)

// CompletedTask is a task completed on the day, as found in CalDAV (by
// any client, also the phone).
type CompletedTask struct {
	UID   string
	Title string
}

// SuggestInput is everything known about a day.
type SuggestInput struct {
	Date      string
	Clock     Clock
	Now       time.Time
	Entry     Entry           // for the ActivityWatch values
	Sessions  []Session       // sessions started on Date, stopped or running
	Items     []Item          // items already recorded for Date
	Completed []CompletedTask // tasks completed on Date
}

// Suggestion is the proposal for the evening part of a day.
type Suggestion struct {
	Date              string   `json:"date"`
	Source            string   `json:"source"` // sessions, aw or none
	ProductiveMinutes int      `json:"productive_minutes"`
	ProductiveHours   float64  `json:"productive_hours"`
	ProductiveWindows []Window `json:"productive_windows"`

	// The productive time split by kind, and what ActivityWatch measured,
	// to compare with the computer part.
	ComputerMinutes int  `json:"computer_minutes"`
	OfflineMinutes  int  `json:"offline_minutes"`
	AWMinutes       *int `json:"aw_minutes"`

	// Sessions over SuspiciousAfter, probably forgotten to stop. They are
	// counted all the same; correct them if they are wrong.
	Suspicious []Session `json:"suspicious"`
	// A session still running is not counted yet.
	Running *Session `json:"running,omitempty"`

	// Items: the recorded ones (with an ID) and, without an ID, completed
	// tasks that are not recorded yet.
	Items []Item `json:"items"`

	// Problems while collecting the data, for example CalDAV not
	// answering; the suggestion is made from what was available.
	Warnings []string `json:"warnings,omitempty"`
}

// Suggest makes the evening proposal for a day:
//
//   - productive time = all stopped sessions (computer and offline) plus
//     the minutes of manual items, which have no session;
//   - one productive window per stopped session, marked with its kind;
//   - if nothing at all was recorded: ActivityWatch's minutes and windows;
//   - sessions over 4 hours are listed as suspicious (also a running one);
//   - items = the recorded items plus completed tasks not recorded yet.
func Suggest(in SuggestInput) Suggestion {
	sg := Suggestion{
		Date:              in.Date,
		ProductiveWindows: []Window{},
		AWMinutes:         in.Entry.AWProductiveMinutes,
		Suspicious:        []Session{},
		Items:             []Item{},
	}

	for _, sess := range in.Sessions {
		if sess.End == nil {
			running := sess
			sg.Running = &running
			if in.Now.Sub(sess.Start) > SuspiciousAfter {
				sg.Suspicious = append(sg.Suspicious, sess)
			}
			continue
		}
		minutes := sess.Minutes(in.Now)
		if sess.Kind == KindOffline {
			sg.OfflineMinutes += minutes
		} else {
			sg.ComputerMinutes += minutes
		}
		sg.ProductiveWindows = append(sg.ProductiveWindows, Window{
			Start: sess.Start.In(in.Clock.Loc).Format("15:04"),
			End:   sess.End.In(in.Clock.Loc).Format("15:04"),
			Kind:  sess.Kind,
		})
		if sess.End.Sub(sess.Start) > SuspiciousAfter {
			sg.Suspicious = append(sg.Suspicious, sess)
		}
	}

	// Manual items add their minutes; items from sessions and tasks are
	// already counted through the sessions.
	recordedTasks := map[string]bool{}
	for _, it := range in.Items {
		sg.Items = append(sg.Items, it)
		if it.Source == SourceTask {
			recordedTasks[it.RefID] = true
		}
		if it.Source != SourceManual || it.Minutes == nil {
			continue
		}
		if it.Kind == KindOffline {
			sg.OfflineMinutes += *it.Minutes
		} else {
			sg.ComputerMinutes += *it.Minutes
		}
	}

	// Completed tasks not recorded yet, with the minutes of their sessions.
	for _, task := range in.Completed {
		if recordedTasks[task.UID] {
			continue
		}
		recordedTasks[task.UID] = true // a task listed twice is proposed once
		it := Item{Date: in.Date, Title: task.Title, Kind: KindComputer, Source: SourceTask, RefID: task.UID}
		minutes, kind := 0, ""
		for _, sess := range in.Sessions {
			if sess.TaskUID == task.UID && sess.End != nil {
				minutes += sess.Minutes(in.Now)
				kind = sess.Kind
			}
		}
		if kind != "" {
			it.Minutes, it.Kind = &minutes, kind
		}
		sg.Items = append(sg.Items, it)
	}

	sg.ProductiveMinutes = sg.ComputerMinutes + sg.OfflineMinutes
	switch {
	case sg.ProductiveMinutes > 0 || len(sg.ProductiveWindows) > 0:
		sg.Source = FromSessions
	case in.Entry.AWProductiveMinutes != nil:
		sg.Source = FromAW
		sg.ProductiveMinutes = *in.Entry.AWProductiveMinutes
		for _, w := range in.Entry.AWWindows {
			w.Kind = KindAW
			sg.ProductiveWindows = append(sg.ProductiveWindows, w)
		}
	default:
		sg.Source = FromNothing
	}
	// Hours with two decimals: 95 minutes are 1.58 hours.
	sg.ProductiveHours = math.Round(float64(sg.ProductiveMinutes)/60*100) / 100
	return sg
}
