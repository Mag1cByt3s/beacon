package main

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/focus"
)

func TestWrap(t *testing.T) {
	tests := []struct {
		text  string
		width int
		want  []string
	}{
		{"buy coffee", 20, []string{"buy coffee"}},
		{"Einen Sport finden der wetterunabhängig ist", 20,
			[]string{"Einen Sport finden", "der wetterunabhängig", "ist"}}, // 20 letters fit exactly
		{"exactly ten", 11, []string{"exactly ten"}},
		{"a verylongwordthatdoesnotfit b", 8, []string{"a", "verylong", "wordthat", "doesnotf", "it b"}},
		{"", 10, []string{""}},
	}
	for _, tt := range tests {
		if got := wrap(tt.text, tt.width); !slices.Equal(got, tt.want) {
			t.Errorf("wrap(%q, %d) = %q, want %q", tt.text, tt.width, got, tt.want)
		}
	}
}

func TestRenderList(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	tasks := []focus.Task{
		{Summary: "Offsec Proving Grounds machine notes in meine knowledge base aufnehmen", List: "Todo"},
		{Summary: "buy coffee", List: "Todo", Due: time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC), DueAllDay: true},
		{Summary: "Milk", List: "Groceries"},
	}
	got := renderList(tasks, now, "Todo", terminal{width: 50})
	want := "" +
		"Todo, Groceries · 3 open\n" +
		"\n" +
		" ● Offsec Proving Grounds machine notes in meine\n" +
		"   knowledge base aufnehmen\n" +
		" ○ buy coffee                         due tomorrow\n" +
		" ○ Milk                                  Groceries\n"
	if got != want {
		t.Errorf("renderList:\n%s\nwant:\n%s", got, want)
	}

	// Every line fits the width.
	for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if n := len([]rune(line)); n > 50 {
			t.Errorf("line is %d wide: %q", n, line)
		}
	}
}

func TestRenderListMetaOnItsOwnLine(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	tasks := []focus.Task{{Summary: "a title that fills the line", Due: now.Add(-time.Hour)}}
	got := renderList(tasks, now, "Todo", terminal{width: 30})
	want := "1 open\n\n ● a title that fills the line\n                       overdue\n"
	if got != want {
		t.Errorf("renderList:\n%q\nwant:\n%q", got, want)
	}
}

func TestRenderListColors(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	tasks := []focus.Task{
		{Summary: "current", List: "Todo"},
		{Summary: "late", List: "Todo", Due: now.Add(-time.Hour)},
	}
	got := renderList(tasks, now, "Todo", terminal{width: 40, color: true})
	for _, want := range []string{
		ansiBold + "Todo · 2 open" + ansiReset, // header
		ansiBold + "●" + ansiReset,             // current marker
		ansiBold + "current" + ansiReset,       // current title
		ansiDim + "○" + ansiReset,              // other markers
		ansiRed + "overdue" + ansiReset,        // overdue in red
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%q", want, got)
		}
	}
	if plain := renderList(tasks, now, "Todo", terminal{width: 40}); strings.Contains(plain, "\x1b") {
		t.Errorf("colours used although off: %q", plain)
	}
}

func TestUseColor(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(name string) string { return vars[name] }
	}
	if !useColor(env(map[string]string{"TERM": "xterm-256color"})) {
		t.Error("no colours in a normal terminal")
	}
	if useColor(env(map[string]string{"TERM": "xterm-256color", "NO_COLOR": "1"})) {
		t.Error("colours despite NO_COLOR")
	}
	if useColor(env(map[string]string{"TERM": "dumb"})) {
		t.Error("colours in a dumb terminal")
	}
}

func TestDetectTerminalOnAFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if got := detectTerminal(f); got != (terminal{}) {
		t.Errorf("detectTerminal on a file = %+v, want not a terminal", got)
	}
}

func TestListPlainWhenNotATerminal(t *testing.T) {
	var out strings.Builder
	a := &app{b: &fakeBackend{tasks: []focus.Task{{Summary: "a", List: "Todo"}, {Summary: "b", List: "Todo"}}}, out: &out, defaultList: "Todo"}
	if err := a.execute(t.Context(), command{name: "list"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "a\nb\n" {
		t.Errorf("output = %q, want plain lines for scripts", out.String())
	}
}
