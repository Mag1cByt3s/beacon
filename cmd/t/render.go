package main

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/Mag1cByt3s/beacon/internal/focus"
)

// maxListWidth caps how wide t list draws, so on a wide window the due
// date on the right stays close to its title.
const maxListWidth = 80

// terminal describes where t prints. The zero value means "not a
// terminal" (a pipe or a file): then t prints plain lines that scripts can
// read.
type terminal struct {
	width int  // columns to use; 0 if not a terminal
	color bool // whether to use ANSI colours
}

// detectTerminal reports whether f is a terminal, how wide to draw and
// whether colours are wanted.
func detectTerminal(f *os.File) terminal {
	fd := int(f.Fd())
	if !term.IsTerminal(fd) {
		return terminal{}
	}
	width, _, err := term.GetSize(fd)
	if err != nil || width <= 0 {
		width = maxListWidth
	}
	return terminal{width: min(width, maxListWidth), color: useColor(os.Getenv)}
}

// useColor follows the NO_COLOR convention (https://no-color.org) and
// leaves dumb terminals alone. getenv is os.Getenv, or a stand-in in tests.
func useColor(getenv func(string) string) bool {
	return getenv("NO_COLOR") == "" && getenv("TERM") != "dumb"
}

// ANSI escape codes for the few styles t uses.
const (
	ansiBold  = "\x1b[1m"
	ansiDim   = "\x1b[2m"
	ansiRed   = "\x1b[31m"
	ansiReset = "\x1b[0m"
)

// style wraps text in an ANSI code, if colours are on.
func (t terminal) style(code, text string) string {
	if !t.color || text == "" {
		return text
	}
	return code + text + ansiReset
}

// renderList draws tasks as a checklist for the terminal:
//
//	Todo · 2 open
//
//	 ● Call the bank
//	 ○ buy coffee                         due tomorrow
//
// The first task is the current one. Long titles wrap with an indent, and
// the due date and list name go on the right of a title's last line.
func renderList(tasks []focus.Task, now time.Time, defaultList string, t terminal) string {
	var b strings.Builder
	b.WriteString(t.style(ansiBold, listHeader(tasks)) + "\n\n")

	const indent = "   " // lines up with the text after " ● "
	textWidth := max(t.width-len(indent), 10)

	for i, task := range tasks {
		marker, title := "○", oneLine(task.Summary)
		if i == 0 {
			marker = "●"
		}
		lines := wrap(title, textWidth)

		// What goes on the right: "due tomorrow · Groceries".
		var meta []string
		if due := describeDue(task, now); due != "" {
			meta = append(meta, due)
		}
		if task.List != "" && !strings.EqualFold(task.List, defaultList) {
			meta = append(meta, oneLine(task.List))
		}
		right := strings.Join(meta, " · ")
		metaCode := ansiDim
		if task.Overdue(now) {
			metaCode = ansiRed
		}

		for j, line := range lines {
			prefix := indent
			if j == 0 {
				if i == 0 {
					prefix = " " + t.style(ansiBold, marker) + " "
				} else {
					prefix = " " + t.style(ansiDim, marker) + " "
				}
			}
			if i == 0 {
				line = t.style(ansiBold, line)
			}
			b.WriteString(prefix + line)

			// The due date and list go after the last line, right-aligned,
			// or on a line of their own if they don't fit.
			if j == len(lines)-1 && right != "" {
				used := len(indent) + utf8.RuneCountInString(lines[j])
				gap := t.width - used - utf8.RuneCountInString(right)
				if gap < 2 {
					b.WriteString("\n")
					gap = t.width - utf8.RuneCountInString(right)
				}
				b.WriteString(strings.Repeat(" ", max(gap, 0)) + t.style(metaCode, right))
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// listHeader says which lists the tasks come from and how many there are,
// for example "Todo · 6 open" or "Todo, Groceries · 8 open".
func listHeader(tasks []focus.Task) string {
	var names []string
	seen := map[string]bool{}
	for _, task := range tasks {
		if task.List != "" && !seen[task.List] {
			seen[task.List] = true
			names = append(names, oneLine(task.List))
		}
	}
	count := fmt.Sprintf("%d open", len(tasks))
	if len(names) == 0 {
		return count
	}
	return strings.Join(names, ", ") + " · " + count
}

// wrap breaks text into lines of at most width characters, at spaces.
// A word longer than a whole line is cut.
func wrap(text string, width int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		// Cut words that would not fit even on a line of their own.
		for utf8.RuneCountInString(word) > width {
			if line != "" {
				lines = append(lines, line)
				line = ""
			}
			r := []rune(word)
			lines = append(lines, string(r[:width]))
			word = string(r[width:])
		}
		switch {
		case line == "":
			line = word
		case utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" || len(lines) == 0 {
		lines = append(lines, line)
	}
	return lines
}
