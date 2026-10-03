// Command t is beacon's command line: one task at a time.
//
//	t                 show the current task (same as t focus)
//	t buy coffee      add a task
//	t list            open tasks, current first
//	t focus           only the current task, on one line
//	t done            complete the current task and show the next one
//	t skip            skip the current task for now (server only)
//	t add <words>     add a task that starts with a command word
//
// With BEACON_SERVER_URL set, t talks to the beacon server; otherwise it
// talks to the CalDAV server directly.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/api"
	"github.com/Mag1cByt3s/beacon/internal/config"
	"github.com/Mag1cByt3s/beacon/internal/focus"
	"github.com/Mag1cByt3s/beacon/internal/queue"
)

const usage = `usage:
  t                 show the current task
  t <words...>      add a task, e.g. t buy coffee
  t list            show open tasks, current first
  t focus           show only the current task
  t done            complete the current task
  t skip            skip the current task for now
  t add <words...>  add a task that starts with a command word
`

// Time limits, so t never hangs the terminal.
const (
	timeout      = 30 * time.Second // one command
	addTimeout   = 5 * time.Second  // one capture; after that it is saved offline
	flushTimeout = 2 * time.Second  // sending saved captures before a command
)

// command is what the user asked for, decided from the arguments alone.
type command struct {
	name    string // "list", "focus", "done", "skip", "add" or "help"
	summary string // title of the new task, only for "add"
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "t:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	cmd, err := parseArgs(args)
	if err != nil {
		return err
	}
	if cmd.name == "help" {
		fmt.Fprint(out, usage)
		return nil
	}

	cfg := config.Load()
	b, err := newBackend(context.Background(), cfg, os.Stderr)
	if err != nil {
		return err
	}
	a := &app{b: b, out: out, defaultList: cfg.DefaultList, queue: openQueue()}

	// First send captures saved while offline. This never stops the
	// command itself.
	a.flush()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel() // defer runs when run returns
	return a.execute(ctx, cmd)
}

// app is what a command works with.
type app struct {
	b           backend
	out         io.Writer
	defaultList string       // tasks from other lists are marked with their list name
	queue       *queue.Queue // captures saved while offline; nil if there is none
	offline     bool         // the last flush could not reach the server
}

// parseArgs decides what to do. A first word that is a command runs that
// command; any other words become a new task. Commands take no extra words,
// so "t done with dishes" is an error instead of completing a task by accident.
func parseArgs(args []string) (command, error) {
	if len(args) == 0 {
		return command{name: "focus"}, nil
	}

	first := args[0]
	switch first {
	case "list", "focus", "done", "skip", "help":
		if len(args) > 1 {
			return command{}, fmt.Errorf("%q takes no extra words; to add this as a task, use: t add %s",
				first, strings.Join(args, " "))
		}
		return command{name: first}, nil
	case "-h", "--help":
		return command{name: "help"}, nil
	case "add":
		args = args[1:]
	default:
		if strings.HasPrefix(first, "-") {
			return command{}, fmt.Errorf("unknown option %q (see t help)", first)
		}
	}

	// strings.Fields also collapses extra spaces inside quoted arguments.
	summary := strings.Join(strings.Fields(strings.Join(args, " ")), " ")
	if summary == "" {
		return command{}, errors.New("nothing to add; usage: t add <words...>")
	}
	return command{name: "add", summary: summary}, nil
}

// execute runs cmd and prints the result.
func (a *app) execute(ctx context.Context, cmd command) error {
	b, out := a.b, a.out
	now := time.Now()
	show := func(task focus.Task, ok bool) {
		if !ok {
			fmt.Fprintln(out, "No open tasks.")
			return
		}
		fmt.Fprintln(out, formatTask(task, now, a.defaultList))
	}

	switch cmd.name {
	case "list":
		tasks, err := b.Tasks(ctx)
		if err != nil {
			return err
		}
		if len(tasks) == 0 {
			show(focus.Task{}, false)
		}
		for _, task := range tasks {
			show(task, true)
		}

	case "focus":
		task, ok, err := b.Current(ctx)
		if err != nil {
			return err
		}
		show(task, ok)

	case "done":
		next, ok, err := b.Done(ctx)
		// errors.As checks whether err is (or wraps) a *api.ConflictError.
		var conflict *api.ConflictError
		if errors.As(err, &conflict) && conflict.Current != nil {
			return fmt.Errorf("%q was changed elsewhere since it became current, so it was left alone; run t done again to complete it as it is now",
				conflict.Current.Summary)
		}
		if err != nil {
			return err
		}
		show(next, ok)

	case "skip":
		next, ok, err := b.Skip(ctx)
		if err != nil {
			return err
		}
		show(next, ok)

	case "add":
		return a.add(ctx, cmd.summary)

	default:
		return fmt.Errorf("unknown command %q", cmd.name)
	}
	return nil
}

// formatTask renders a task as one calm line, for example
// "Pay rent (due tomorrow)" or "Milk [Groceries]".
func formatTask(task focus.Task, now time.Time, defaultList string) string {
	line := task.Summary
	if due := describeDue(task, now); due != "" {
		line += " (" + due + ")"
	}
	if task.List != "" && !strings.EqualFold(task.List, defaultList) {
		line += " [" + task.List + "]"
	}
	return line
}

// describeDue says when a task is due in words: "overdue", "due today",
// "due tomorrow 09:00", "due Mon 5 Oct". It returns "" without a due date.
func describeDue(task focus.Task, now time.Time) string {
	if task.Due.IsZero() {
		return ""
	}
	if task.Overdue(now) {
		return "overdue"
	}

	due := task.Due.In(now.Location())
	clock := ""
	if !task.DueAllDay {
		clock = " " + due.Format("15:04")
	}

	today := startOfDay(now)
	switch startOfDay(due) {
	case today:
		return "due today" + clock
	case today.AddDate(0, 0, 1):
		return "due tomorrow" + clock
	}

	// Go formats dates by example: "Mon 2 Jan" is the layout.
	layout := "Mon 2 Jan"
	if due.Year() != now.Year() {
		layout = "Mon 2 Jan 2006"
	}
	return "due " + due.Format(layout) + clock
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
