package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"golang.org/x/term"

	"github.com/Mag1cByt3s/beacon/internal/focus"
)

// t edit is a full-screen editor for the open tasks, built with Bubble Tea.
// Bubble Tea follows "the Elm architecture": a model holds the state,
// Update turns a message (a key press, a server answer) into a new state,
// and View draws the state. Work that takes time, such as talking to the
// server, runs as a tea.Cmd: a function whose result comes back to Update
// as another message, so the screen never freezes.
//
// Every change is sent right away, with the version that was read, so a
// task changed elsewhere (on the phone) is never overwritten. After each
// change the list is read again.

// editMode says what the editor is doing right now.
type editMode int

const (
	browsing   editMode = iota // moving through the list
	renaming                   // typing a new title for the selected task
	adding                     // typing the title of a new task
	confirming                 // asking whether to remove the selected task
)

// editModel is the editor's whole state.
type editModel struct {
	a      *app
	tasks  []focus.Task
	cursor int // index of the selected task
	offset int // index of the first task on screen, for scrolling

	mode    editMode
	input   textinput.Model
	busy    bool   // waiting for the server
	loaded  bool   // the list has been read at least once
	status  string // last message, shown at the bottom
	failed  bool   // whether status is an error
	keepUID string // select this task again after the list is reloaded

	width, height int
}

// Messages that come back from commands.
type (
	// tasksLoaded carries the freshly read list.
	tasksLoaded struct {
		tasks []focus.Task
		err   error
	}
	// changeDone reports the outcome of a rename, add or remove.
	changeDone struct {
		status string
		err    error
	}
)

// edit runs the editor until the user quits.
func (a *app) edit() error {
	// Bubble Tea reads keys from stdin and draws on stdout; both must be
	// a terminal.
	if a.tty.width == 0 || !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("t edit needs a terminal; use t list, t add and t rm in scripts")
	}
	_, err := tea.NewProgram(newEditModel(a)).Run()
	return err
}

func newEditModel(a *app) editModel {
	input := textinput.New()
	input.Prompt = ""
	input.CharLimit = 500
	return editModel{a: a, input: input, width: maxListWidth, height: 24}
}

// Init starts by reading the list.
func (m editModel) Init() tea.Cmd {
	return m.load()
}

// load reads the open tasks in the background.
func (m editModel) load() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		tasks, err := m.a.b.Tasks(ctx)
		return tasksLoaded{tasks: tasks, err: err}
	}
}

// change runs one change (rename, add, remove) in the background.
func (m editModel) change(do func(ctx context.Context) (string, error)) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		status, err := do(ctx)
		return changeDone{status: status, err: err}
	}
}

// Update handles one message and returns the new state.
func (m editModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = min(msg.Width, maxListWidth), msg.Height
		m.input.SetWidth(m.width - 12)
		return m, nil

	case tasksLoaded:
		m.busy, m.loaded = false, true
		if msg.err != nil {
			m.setStatus(msg.err.Error(), true)
			return m, nil
		}
		m.tasks = msg.tasks
		m.selectAfterReload()
		return m, nil

	case changeDone:
		if msg.err != nil {
			m.setStatus(msg.err.Error(), true)
		} else {
			m.setStatus(msg.status, false)
		}
		// Read the list again, so it shows what is really stored now.
		m.busy = true
		return m, m.load()

	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		switch m.mode {
		case renaming, adding:
			return m.updateTyping(msg)
		case confirming:
			return m.updateConfirming(msg)
		default:
			return m.updateBrowsing(msg)
		}
	}
	return m, nil
}

// updateBrowsing handles keys while moving through the list.
func (m editModel) updateBrowsing(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.tasks)-1 {
			m.cursor++
		}
	case "r":
		if !m.busy {
			m.busy = true
			return m, m.load()
		}
	}
	if m.busy {
		return m, nil // no new change until the last one is done
	}

	switch msg.String() {
	case "enter", "e":
		if task, ok := m.selected(); ok {
			m.mode = renaming
			m.input.SetValue(oneLine(task.Summary))
			m.input.CursorEnd()
			return m, m.input.Focus()
		}
	case "a", "n":
		m.mode = adding
		m.input.Reset()
		return m, m.input.Focus()
	case "d", "delete":
		if _, ok := m.selected(); ok {
			m.mode = confirming
		}
	}
	return m, nil
}

// updateTyping handles keys while a title is being typed.
func (m editModel) updateTyping(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = browsing
		m.input.Blur()
		return m, nil
	case "enter":
		title := strings.Join(strings.Fields(m.input.Value()), " ")
		mode := m.mode
		m.mode = browsing
		m.input.Blur()
		if title == "" {
			return m, nil // nothing typed: same as cancelling
		}
		if mode == adding {
			m.busy = true
			m.keepUID = ""
			return m, m.change(func(ctx context.Context) (string, error) {
				return m.a.capture(ctx, title)
			})
		}
		task, _ := m.selected()
		if title == oneLine(task.Summary) {
			return m, nil // unchanged
		}
		m.busy = true
		m.keepUID = task.UID
		return m, m.change(func(ctx context.Context) (string, error) {
			if err := m.a.renameTask(ctx, task, title); err != nil {
				return "", err
			}
			return fmt.Sprintf("Renamed to %q.", title), nil
		})
	}

	// Any other key edits the text.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// updateConfirming handles the answer to "remove this task?".
func (m editModel) updateConfirming(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.mode = browsing
	task, ok := m.selected()
	if msg.String() != "y" || !ok {
		return m, nil // anything but y keeps the task
	}
	m.busy = true
	m.keepUID = ""
	return m, m.change(func(ctx context.Context) (string, error) {
		if err := m.a.removeTask(ctx, task); err != nil {
			return "", err
		}
		return fmt.Sprintf("Removed %q.", oneLine(task.Summary)), nil
	})
}

func (m *editModel) setStatus(text string, failed bool) {
	m.status, m.failed = text, failed
}

func (m editModel) selected() (focus.Task, bool) {
	if m.cursor < 0 || m.cursor >= len(m.tasks) {
		return focus.Task{}, false
	}
	return m.tasks[m.cursor], true
}

// selectAfterReload keeps the cursor on the same task if it still exists,
// otherwise at the same position.
func (m *editModel) selectAfterReload() {
	if m.keepUID != "" {
		for i, task := range m.tasks {
			if task.UID == m.keepUID {
				m.cursor = i
				break
			}
		}
		m.keepUID = ""
	}
	m.cursor = max(min(m.cursor, len(m.tasks)-1), 0)
}

// listRows is how many tasks fit on the screen between header and footer.
func (m editModel) listRows() int {
	return max(m.height-7, 1)
}

// View draws the screen.
func (m editModel) View() tea.View {
	t := m.a.tty
	var b strings.Builder

	b.WriteString(" " + t.style(ansiBold, "t edit · "+listHeader(m.tasks)) + "\n\n")

	switch {
	case !m.loaded:
		b.WriteString("   Loading…\n")
	case len(m.tasks) == 0:
		b.WriteString("   No open tasks. Press a to add one.\n")
	}

	// Scroll so the selected task is on screen.
	rows := m.listRows()
	offset := m.offset
	if m.cursor < offset {
		offset = m.cursor
	}
	if m.cursor >= offset+rows {
		offset = m.cursor - rows + 1
	}
	for i := offset; i < len(m.tasks) && i < offset+rows; i++ {
		pointer, marker := "  ", "○"
		if i == m.cursor {
			pointer = t.style(ansiBold, "❯ ")
		}
		if i == 0 {
			marker = "●" // the current task
		}
		title := truncate(oneLine(m.tasks[i].Summary), m.width-6)
		if i == m.cursor {
			title = t.style(ansiBold, title)
		}
		b.WriteString(" " + pointer + t.style(ansiDim, marker) + " " + title + "\n")
	}
	b.WriteString("\n")

	switch m.mode {
	case renaming:
		b.WriteString(" Rename: " + m.input.View() + "\n")
		b.WriteString(t.style(ansiDim, " enter save · esc cancel") + "\n")
	case adding:
		b.WriteString(" New task: " + m.input.View() + "\n")
		b.WriteString(t.style(ansiDim, " enter add · esc cancel") + "\n")
	case confirming:
		task, _ := m.selected()
		b.WriteString(fmt.Sprintf(" Remove %q for good? y/n\n", truncate(oneLine(task.Summary), m.width-30)))
		b.WriteString("\n")
	default:
		status := m.status
		if m.busy {
			status = "Saving…"
		}
		if m.failed && !m.busy {
			b.WriteString(" " + t.style(ansiRed, status) + "\n")
		} else {
			b.WriteString(" " + t.style(ansiDim, status) + "\n")
		}
		b.WriteString(t.style(ansiDim, " ↑↓ move · enter rename · a add · d remove · r reload · q quit") + "\n")
	}

	v := tea.NewView(b.String())
	v.AltScreen = true // use the whole window, and restore it on exit
	return v
}

// truncate shortens text to width characters, ending with "…".
func truncate(text string, width int) string {
	if width < 2 || utf8.RuneCountInString(text) <= width {
		return text
	}
	r := []rune(text)
	return string(r[:width-1]) + "…"
}
