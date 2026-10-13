// Package awsync turns ActivityWatch's window events into productive
// computer time for the journal: each event gets a category from regex
// rules, the time in the productive category is added up, and close
// stretches of it are merged into windows.
package awsync

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/journal"
)

// Event is one ActivityWatch window event: which app and window title
// were active, from Timestamp for Duration seconds.
type Event struct {
	Timestamp time.Time `json:"timestamp"`
	Duration  float64   `json:"duration"`
	Data      struct {
		App   string `json:"app"`
		Title string `json:"title"`
	} `json:"data"`
}

// Rule puts events whose app or window title matches into Category, a
// path like ["Arbeit", "Lernvideos"]. App and Title are regular
// expressions, matched case-insensitively anywhere in the text; an empty
// one matches nothing.
type Rule struct {
	Category []string `json:"category"`
	App      string   `json:"app,omitempty"`
	Title    string   `json:"title,omitempty"`
}

// Config is the content of the categories file.
type Config struct {
	Rules []Rule `json:"rules"`
	// Productive is the top-level category that counts as productive.
	Productive string `json:"productive"`
	// Productive stretches less than MergeGapMinutes apart become one
	// window; windows shorter than MinWindowMinutes are left out.
	MergeGapMinutes  int `json:"merge_gap_minutes"`
	MinWindowMinutes int `json:"min_window_minutes"`
}

// DefaultConfig is used without a categories file.
func DefaultConfig() Config {
	return Config{
		Rules: []Rule{
			{Category: []string{"Arbeit"}, App: "konsole|kitty|alacritty|wezterm|ghostty|codium|code|nvim|burp|ghidra|wireshark|obsidian"},
			{Category: []string{"Arbeit"}, Title: "Hack The Box|Proving Grounds|OffSec|HackTricks|GitHub|NixOS"},
			{Category: []string{"Arbeit", "Lernvideos"}, Title: "IppSec|0xdf"},
			{Category: []string{"Ablenkung"}, Title: "YouTube|Reddit|Twitch|Netflix"},
		},
		Productive:       "Arbeit",
		MergeGapMinutes:  10,
		MinWindowMinutes: 15,
	}
}

// ParseConfig reads a categories file (JSON). Missing settings keep their
// defaults; rules replace the default rules.
func ParseConfig(data []byte) (Config, error) {
	c := DefaultConfig()
	c.Rules = nil
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, err
	}
	if len(c.Rules) == 0 {
		return Config{}, errors.New("the categories file has no rules")
	}
	return c, nil
}

// Categorizer is a Config ready for use.
type Categorizer struct {
	rules      []rule
	productive string
	gap, min   time.Duration
}

type rule struct {
	category   []string
	app, title *regexp.Regexp // nil matches nothing
}

// New checks c and compiles its rules.
func New(c Config) (*Categorizer, error) {
	if c.Productive == "" {
		return nil, errors.New("productive: name the category that counts, e.g. Arbeit")
	}
	if c.MergeGapMinutes < 0 || c.MinWindowMinutes < 0 {
		return nil, errors.New("merge_gap_minutes and min_window_minutes must not be negative")
	}
	cat := &Categorizer{
		productive: c.Productive,
		gap:        time.Duration(c.MergeGapMinutes) * time.Minute,
		min:        time.Duration(c.MinWindowMinutes) * time.Minute,
	}
	for i, r := range c.Rules {
		if len(r.Category) == 0 || slices.Contains(r.Category, "") {
			return nil, fmt.Errorf("rule %d: category must be a path like [\"Arbeit\"]", i+1)
		}
		if r.App == "" && r.Title == "" {
			return nil, fmt.Errorf("rule %d (%v): give an app or a title pattern", i+1, r.Category)
		}
		compiled := rule{category: r.Category}
		var err error
		if compiled.app, err = compile(r.App); err != nil {
			return nil, fmt.Errorf("rule %d (%v): app: %w", i+1, r.Category, err)
		}
		if compiled.title, err = compile(r.Title); err != nil {
			return nil, fmt.Errorf("rule %d (%v): title: %w", i+1, r.Category, err)
		}
		cat.rules = append(cat.rules, compiled)
	}
	return cat, nil
}

func compile(pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, nil
	}
	// (?i) makes the expression ignore case.
	return regexp.Compile("(?i)" + pattern)
}

// Category returns the category of a window, or nil if no rule matches.
// Like ActivityWatch, the deepest matching category wins, so a video by
// IppSec on YouTube is Arbeit > Lernvideos, not Ablenkung. Among equally
// deep ones the first rule wins.
func (c *Categorizer) Category(app, title string) []string {
	var best []string
	for _, r := range c.rules {
		matches := (r.app != nil && r.app.MatchString(app)) || (r.title != nil && r.title.MatchString(title))
		if matches && len(r.category) > len(best) {
			best = r.category
		}
	}
	return best
}

// span is a stretch of time.
type span struct{ start, end time.Time }

// Summarize adds up the productive time of events between from and to
// and merges it into windows (wall-clock times in loc).
func (c *Categorizer) Summarize(events []Event, from, to time.Time, loc *time.Location) (minutes int, windows []journal.Window) {
	var spans []span
	for _, e := range events {
		cat := c.Category(e.Data.App, e.Data.Title)
		if len(cat) == 0 || cat[0] != c.productive {
			continue
		}
		start := e.Timestamp
		end := start.Add(time.Duration(e.Duration * float64(time.Second)))
		// Only the part within the day counts.
		start, end = later(start, from), earlier(end, to)
		if end.After(start) {
			spans = append(spans, span{start, end})
		}
	}

	// Overlapping events (which should not happen) are counted once.
	var total time.Duration
	for _, s := range merge(spans, 0) {
		total += s.end.Sub(s.start)
	}
	minutes = int(math.Round(total.Minutes()))

	windows = []journal.Window{}
	for _, s := range merge(spans, c.gap) {
		if s.end.Sub(s.start) < c.min {
			continue
		}
		windows = append(windows, journal.Window{
			Start: s.start.In(loc).Format("15:04"),
			End:   s.end.In(loc).Format("15:04"),
			Kind:  journal.KindAW,
		})
	}
	return minutes, windows
}

// merge sorts spans and joins those that overlap, touch or are less than
// gap apart.
func merge(spans []span, gap time.Duration) []span {
	sorted := slices.Clone(spans)
	slices.SortFunc(sorted, func(a, b span) int { return a.start.Compare(b.start) })
	var merged []span
	for _, s := range sorted {
		n := len(merged)
		if n > 0 {
			if pause := s.start.Sub(merged[n-1].end); pause <= 0 || pause < gap {
				merged[n-1].end = later(merged[n-1].end, s.end)
				continue
			}
		}
		merged = append(merged, s)
	}
	return merged
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
