package main

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Mag1cByt3s/beacon/internal/caldav"
	"github.com/Mag1cByt3s/beacon/internal/focus"
	"github.com/Mag1cByt3s/beacon/internal/queue"
)

// key turns a key name into the message Bubble Tea sends for it.
func key(name string) tea.KeyPressMsg {
	switch name {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	r := []rune(name)[0]
	return tea.KeyPressMsg{Code: r, Text: name}
}

// send feeds messages to the editor as Bubble Tea would, and runs the
// commands that come back (server work) until there are none. Commands
// from the text input (cursor blinking) are not run. It reports whether
// the editor asked to quit.
func send(t *testing.T, m editModel, msgs ...tea.Msg) (editModel, bool) {
	t.Helper()
	for _, msg := range msgs {
		next, cmd := m.Update(msg)
		m = next.(editModel)
		for cmd != nil && m.mode == browsing {
			result := cmd()
			if _, quit := result.(tea.QuitMsg); quit {
				return m, true
			}
			next, cmd = m.Update(result)
			m = next.(editModel)
		}
	}
	return m, false
}

func press(t *testing.T, m editModel, keys ...string) (editModel, bool) {
	t.Helper()
	msgs := make([]tea.Msg, len(keys))
	for i, k := range keys {
		msgs[i] = key(k)
	}
	return send(t, m, msgs...)
}

// newEditor starts an editor on b and loads the list.
func newEditor(t *testing.T, b *fakeBackend) editModel {
	t.Helper()
	dir := t.TempDir()
	a := &app{
		b:           b,
		defaultList: "Todo",
		queue:       queue.New(filepath.Join(dir, "queue.jsonl")),
		lastPath:    filepath.Join(dir, "last-capture.json"),
		tty:         terminal{width: 60},
	}
	m, _ := send(t, newEditModel(a), m0(newEditModel(a).Init()))
	return m
}

// m0 runs a command once and returns its message.
func m0(cmd tea.Cmd) tea.Msg { return cmd() }

func someTasks() []focus.Task {
	return []focus.Task{
		{UID: "1", Summary: "Offsec notes aufnehmen", List: "Todo", ETag: "e1"},
		{UID: "2", Summary: "buy cofee", List: "Todo", ETag: "e2"},
		{UID: "3", Summary: "lsit", List: "Todo", ETag: "e3"},
	}
}

func screen(m editModel) string { return m.View().Content }

func TestEditShowsTheList(t *testing.T) {
	m := newEditor(t, &fakeBackend{tasks: someTasks()})
	view := screen(m)
	for _, want := range []string{"t edit · Todo · 3 open", "❯ ● Offsec notes aufnehmen", "○ buy cofee", "○ lsit", "↑↓ select", "q quit"} {
		if !strings.Contains(view, want) {
			t.Errorf("screen lacks %q:\n%s", want, view)
		}
	}
	if !m.View().AltScreen {
		t.Error("the editor does not use the full window")
	}
}

func TestEditMoves(t *testing.T) {
	m := newEditor(t, &fakeBackend{tasks: someTasks()})
	m, _ = press(t, m, "up")
	if m.cursor != 0 {
		t.Errorf("cursor = %d after up at the top, want 0", m.cursor)
	}
	m, _ = press(t, m, "down", "j", "down")
	if m.cursor != 2 {
		t.Errorf("cursor = %d after three downs, want 2 (the last)", m.cursor)
	}
	m, _ = press(t, m, "k")
	if m.cursor != 1 {
		t.Errorf("cursor = %d after k, want 1", m.cursor)
	}
}

func TestEditRename(t *testing.T) {
	b := &fakeBackend{tasks: someTasks()}
	m := newEditor(t, b)

	m, _ = press(t, m, "down", "enter")
	if m.mode != renaming || m.input.Value() != "buy cofee" {
		t.Fatalf("mode %v, input %q; want renaming with the old title", m.mode, m.input.Value())
	}
	m.input.SetValue("buy coffee")
	m, _ = press(t, m, "enter")

	if want := []string{"2@e2=buy coffee"}; !slices.Equal(b.renamed, want) {
		t.Errorf("renamed %q, want %q (with the version that was read)", b.renamed, want)
	}
	if m.status != `Renamed to "buy coffee".` || m.failed {
		t.Errorf("status = %q (failed %v)", m.status, m.failed)
	}
	if m.cursor != 1 || m.tasks[1].Summary != "buy coffee" {
		t.Errorf("after reload: cursor %d, title %q", m.cursor, m.tasks[1].Summary)
	}
}

func TestEditRenameCancelled(t *testing.T) {
	b := &fakeBackend{tasks: someTasks()}
	m := newEditor(t, b)

	m, _ = press(t, m, "enter")
	m.input.SetValue("something else")
	m, _ = press(t, m, "esc")
	m, _ = press(t, m, "enter", "enter") // open, save unchanged
	m, _ = press(t, m, "enter")
	m.input.SetValue("   ")
	m, _ = press(t, m, "enter") // empty title
	if len(b.renamed) != 0 || m.mode != browsing {
		t.Errorf("renamed %q, mode %v; want nothing renamed", b.renamed, m.mode)
	}
}

func TestEditRenameChangedElsewhere(t *testing.T) {
	b := &fakeBackend{tasks: someTasks(), renameErr: caldav.ErrConflict}
	m := newEditor(t, b)
	m, _ = press(t, m, "enter")
	m.input.SetValue("new title")
	m, _ = press(t, m, "enter")
	if !m.failed || !strings.Contains(m.status, "changed elsewhere") {
		t.Errorf("status = %q (failed %v), want the conflict explained", m.status, m.failed)
	}
	if m.busy {
		t.Error("still busy after the answer")
	}
}

func TestEditAdd(t *testing.T) {
	b := &fakeBackend{tasks: someTasks()}
	m := newEditor(t, b)
	m, _ = press(t, m, "a")
	if m.mode != adding {
		t.Fatalf("mode = %v, want adding", m.mode)
	}
	for _, r := range "milk" {
		m, _ = press(t, m, string(r)) // typed letter by letter
	}
	if m.input.Value() != "milk" {
		t.Fatalf("input = %q, want milk", m.input.Value())
	}
	m, _ = press(t, m, "enter")
	if len(b.added) != 1 || m.status != "Added to Todo." {
		t.Errorf("added %q, status %q", b.added, m.status)
	}
}

func TestEditAddOffline(t *testing.T) {
	b := &fakeBackend{tasks: someTasks(), addErr: errUnreachableForTest()}
	m := newEditor(t, b)
	m, _ = press(t, m, "a")
	m.input.SetValue("milk")
	m, _ = press(t, m, "enter")
	if m.status != "Saved offline, will sync later." {
		t.Errorf("status = %q", m.status)
	}
	if entries, _ := m.a.queue.Entries(); len(entries) != 1 {
		t.Errorf("queue = %+v, want the capture", entries)
	}
}

// errUnreachableForTest is a timed-out request, which counts as "try
// again later".
func errUnreachableForTest() error {
	return fmt.Errorf("request failed: %w", context.DeadlineExceeded)
}

func TestEditRemove(t *testing.T) {
	b := &fakeBackend{tasks: someTasks()}
	m := newEditor(t, b)

	m, _ = press(t, m, "down", "down", "d")
	if m.mode != confirming || !strings.Contains(screen(m), `Remove "lsit" for good? y/n`) {
		t.Fatalf("mode %v, screen:\n%s", m.mode, screen(m))
	}
	m, _ = press(t, m, "n")
	if len(b.removed) != 0 {
		t.Fatal("n removed the task")
	}

	m, _ = press(t, m, "d", "y")
	if want := []string{"3@e3"}; !slices.Equal(b.removed, want) {
		t.Errorf("removed %q, want %q", b.removed, want)
	}
	if len(m.tasks) != 2 || m.cursor != 1 || m.status != `Removed "lsit".` {
		t.Errorf("after reload: %d tasks, cursor %d, status %q", len(m.tasks), m.cursor, m.status)
	}
}

func TestEditQuit(t *testing.T) {
	for _, k := range []string{"q", "esc", "ctrl+c"} {
		m := newEditor(t, &fakeBackend{tasks: someTasks()})
		if _, quit := press(t, m, k); !quit {
			t.Errorf("%s did not quit", k)
		}
	}
	// While typing, q is just a letter.
	m := newEditor(t, &fakeBackend{tasks: someTasks()})
	m, _ = press(t, m, "a")
	if m, quit := press(t, m, "q"); quit || m.input.Value() != "q" {
		t.Errorf("q while typing: quit %v, input %q", quit, m.input.Value())
	}
}

func TestEditEmptyAndScrolling(t *testing.T) {
	m := newEditor(t, &fakeBackend{})
	if !strings.Contains(screen(m), "No open tasks. Press a to add one.") {
		t.Errorf("empty screen:\n%s", screen(m))
	}

	var many []focus.Task
	for i := range 30 {
		many = append(many, focus.Task{UID: string(rune('A' + i)), Summary: "task " + string(rune('A'+i))})
	}
	m = newEditor(t, &fakeBackend{tasks: many})
	m, _ = send(t, m, tea.WindowSizeMsg{Width: 60, Height: 12})
	for range 20 {
		m, _ = press(t, m, "down")
	}
	view := screen(m)
	if !strings.Contains(view, "❯ ○ task U") || strings.Contains(view, "task A\n") {
		t.Errorf("the selected task is not on screen:\n%s", view)
	}
	if lines := strings.Count(view, "\n"); lines > 12 {
		t.Errorf("screen has %d lines, window is 12", lines)
	}
}

func TestEditNeedsATerminal(t *testing.T) {
	a := &app{b: &fakeBackend{}}
	if err := a.edit(); err == nil || !strings.Contains(err.Error(), "needs a terminal") {
		t.Errorf("err = %v", err)
	}
}
