// Package journal holds the daily sleep and productivity journal: what a
// journal day is, the data kept for each day, and the rules it must
// follow. Like package focus it does no I/O; package store keeps the data
// and package server offers it over HTTP.
package journal

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// DateLayout is how journal days are written: "2006-01-02".
const DateLayout = time.DateOnly

// Clock says which journal day a moment belongs to. A journal day starts
// at Hour:Minute local time, not at midnight, so an entry made at 00:40
// still belongs to the evening before.
type Clock struct {
	Loc    *time.Location
	Hour   int // the day starts at Hour:Minute
	Minute int
}

// NewClock returns a Clock for the time zone name (like "Europe/Berlin")
// whose days start at dayStart ("04:00").
func NewClock(zone, dayStart string) (Clock, error) {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return Clock{}, fmt.Errorf("unknown time zone %q (example: Europe/Berlin)", zone)
	}
	t, err := time.Parse("15:04", dayStart)
	if err != nil {
		return Clock{}, fmt.Errorf("day start %q is not a time like 04:00", dayStart)
	}
	return Clock{Loc: loc, Hour: t.Hour(), Minute: t.Minute()}, nil
}

// Day returns the journal day that t belongs to, as "2006-01-02".
func (c Clock) Day(t time.Time) string {
	local := t.In(c.Loc)
	y, m, d := local.Date()
	// time.Date with the wall-clock time gets daylight saving right: on
	// the days the clocks change, 04:00 is still 04:00.
	if local.Before(time.Date(y, m, d, c.Hour, c.Minute, 0, 0, c.Loc)) {
		d-- // before the day start: still the previous journal day
	}
	return time.Date(y, m, d, 12, 0, 0, 0, c.Loc).Format(DateLayout)
}

// Bounds returns when the journal day date starts and when the next one
// starts.
func (c Clock) Bounds(date string) (start, end time.Time, err error) {
	day, err := time.Parse(DateLayout, date)
	if err != nil {
		return time.Time{}, time.Time{}, errBadDate(date)
	}
	y, m, d := day.Date()
	start = time.Date(y, m, d, c.Hour, c.Minute, 0, 0, c.Loc)
	end = time.Date(y, m, d+1, c.Hour, c.Minute, 0, 0, c.Loc)
	return start, end, nil
}

// Resolve turns "today" into the current journal day and checks that
// anything else is a date like 2026-10-13.
func (c Clock) Resolve(date string, now time.Time) (string, error) {
	if strings.EqualFold(date, "today") {
		return c.Day(now), nil
	}
	if _, err := time.Parse(DateLayout, date); err != nil {
		return "", errBadDate(date)
	}
	return date, nil
}

func errBadDate(date string) error {
	return fmt.Errorf("%q is not a date like 2026-10-13 or \"today\"", date)
}

// Kinds of productive time.
const (
	KindComputer = "computer"
	KindOffline  = "offline"
	KindAW       = "aw" // measured by ActivityWatch; only in windows
)

// Sources of a journal item.
const (
	SourceFocus  = "focus"  // a stopped focus session
	SourceTask   = "task"   // a completed task
	SourceManual = "manual" // typed in by hand
)

// Entry is one journal day. Every field except Date may be unknown (nil),
// so a day can be filled in bit by bit: the morning part in the morning,
// the evening part in the evening.
//
// The JSON form doubles as the body of a partial update: unmarshalling it
// onto an existing Entry changes only the fields the JSON mentions, and a
// field set to null is cleared.
type Entry struct {
	Date string `json:"date"`

	SleepQuality *int     `json:"sleep_quality"` // 1-10, the night before this day
	SleepHours   *float64 `json:"sleep_hours"`

	Productivity      *int     `json:"productivity"` // 1-10
	ProductiveHours   *float64 `json:"productive_hours"`
	ProductiveWindows []Window `json:"productive_windows"`

	AWProductiveMinutes *int     `json:"aw_productive_minutes"` // from ActivityWatch
	AWWindows           []Window `json:"aw_windows"`

	Sport        *bool   `json:"sport"`
	SportType    *string `json:"sport_type"`
	SportMinutes *int    `json:"sport_minutes"`
	Steps        *int    `json:"steps"`
	Note         *string `json:"note"`

	// omitzero (Go 1.24+) leaves out the zero time, i.e. "never saved".
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	UpdatedBy string    `json:"updated_by,omitempty"` // client that saved it last
}

// Window is a stretch of time within a journal day, as local wall-clock
// times "09:15" to "11:40". End may be smaller than Start when the window
// runs past midnight.
type Window struct {
	Start string `json:"start"`
	End   string `json:"end"`
	Kind  string `json:"kind,omitempty"` // computer, offline or aw
}

// The two parts of a day that can be missing.
const (
	PartMorning = "morning" // sleep quality not entered yet
	PartEvening = "evening" // productivity not entered yet
)

// Missing returns the parts of the day not filled in yet: "morning" until
// the sleep quality is set, "evening" until the productivity is set.
func (e Entry) Missing() []string {
	missing := []string{}
	if e.SleepQuality == nil {
		missing = append(missing, PartMorning)
	}
	if e.Productivity == nil {
		missing = append(missing, PartEvening)
	}
	return missing
}

// Validate checks that every field that is set has a sensible value.
func (e Entry) Validate() error {
	var errs []error
	check := func(ok bool, format string, args ...any) {
		if !ok {
			errs = append(errs, fmt.Errorf(format, args...))
		}
	}
	if e.SleepQuality != nil {
		check(*e.SleepQuality >= 1 && *e.SleepQuality <= 10, "sleep_quality must be 1 to 10")
	}
	if e.Productivity != nil {
		check(*e.Productivity >= 1 && *e.Productivity <= 10, "productivity must be 1 to 10")
	}
	if e.SleepHours != nil {
		check(*e.SleepHours >= 0 && *e.SleepHours <= 24, "sleep_hours must be 0 to 24")
	}
	if e.ProductiveHours != nil {
		check(*e.ProductiveHours >= 0 && *e.ProductiveHours <= 24, "productive_hours must be 0 to 24")
	}
	if e.AWProductiveMinutes != nil {
		check(*e.AWProductiveMinutes >= 0 && *e.AWProductiveMinutes <= 24*60, "aw_productive_minutes must be 0 to 1440")
	}
	if e.SportMinutes != nil {
		check(*e.SportMinutes >= 0 && *e.SportMinutes <= 24*60, "sport_minutes must be 0 to 1440")
	}
	if e.Steps != nil {
		check(*e.Steps >= 0, "steps must not be negative")
	}
	for _, w := range e.ProductiveWindows {
		if err := w.validate(); err != nil {
			errs = append(errs, fmt.Errorf("productive_windows: %w", err))
		}
	}
	for _, w := range e.AWWindows {
		if err := w.validate(); err != nil {
			errs = append(errs, fmt.Errorf("aw_windows: %w", err))
		}
	}
	// errors.Join puts all problems into one error, one per line.
	return errors.Join(errs...)
}

func (w Window) validate() error {
	for _, t := range []string{w.Start, w.End} {
		if _, err := time.Parse("15:04", t); err != nil {
			return fmt.Errorf("%q is not a time like 09:15", t)
		}
	}
	switch w.Kind {
	case "", KindComputer, KindOffline, KindAW:
		return nil
	}
	return fmt.Errorf("kind %q must be computer, offline or aw", w.Kind)
}

// Item is one thing done on a journal day, with how long it took if
// known.
type Item struct {
	ID        int64     `json:"id"`
	Date      string    `json:"date"`
	Title     string    `json:"title"`
	Minutes   *int      `json:"minutes"`
	Kind      string    `json:"kind"`   // computer or offline
	Source    string    `json:"source"` // focus, task or manual
	RefID     string    `json:"ref_id,omitempty"`
	CreatedAt time.Time `json:"created_at,omitzero"`
}

// Validate checks an item. An empty kind or source is fine; Normalize
// fills in the defaults.
func (it Item) Validate() error {
	var errs []error
	if strings.TrimSpace(it.Title) == "" {
		errs = append(errs, errors.New("title is empty"))
	}
	if it.Minutes != nil && (*it.Minutes < 0 || *it.Minutes > 24*60) {
		errs = append(errs, errors.New("minutes must be 0 to 1440"))
	}
	switch it.Kind {
	case "", KindComputer, KindOffline:
	default:
		errs = append(errs, fmt.Errorf("kind %q must be computer or offline", it.Kind))
	}
	switch it.Source {
	case "", SourceFocus, SourceTask, SourceManual:
	default:
		errs = append(errs, fmt.Errorf("source %q must be focus, task or manual", it.Source))
	}
	return errors.Join(errs...)
}

// Normalize tidies an item: single spaces in the title, and "computer"
// and "manual" when kind and source are not given.
func (it Item) Normalize() Item {
	it.Title = strings.Join(strings.Fields(it.Title), " ")
	if it.Kind == "" {
		it.Kind = KindComputer
	}
	if it.Source == "" {
		it.Source = SourceManual
	}
	return it
}

// Session is a stretch of focused work, measured with start and stop. It
// belongs to the journal day it started on.
type Session struct {
	ID      int64      `json:"id"`
	Date    string     `json:"date"`
	Start   time.Time  `json:"start"`
	End     *time.Time `json:"end"` // nil while it is running
	Label   string     `json:"label,omitempty"`
	Kind    string     `json:"kind"`               // computer or offline
	Source  string     `json:"source,omitempty"`   // the client that started it
	TaskUID string     `json:"task_uid,omitempty"` // the task it was for, if any
}

// Minutes is how long a stopped session lasted, rounded to whole minutes.
// A running session counts up to now.
func (s Session) Minutes(now time.Time) int {
	end := now
	if s.End != nil {
		end = *s.End
	}
	return int(end.Sub(s.Start).Round(time.Minute).Minutes())
}
