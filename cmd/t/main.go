// Command t is beacon's command line: one task at a time.
//
//	t                 show the current task (same as t focus)
//	t buy coffee      add a task
//	t list            open tasks, current first
//	t focus           only the current task, on one line
//	t done            complete the current task and show the next one
//	t skip            skip the current task for now (server only)
//	t add <words>     add a task that starts with a command word
//	t rm <words>      remove the task whose title matches, for good
//	t undo            remove the task added last, for good
//	t edit            full-screen editor: rename, add and remove tasks
//	t prompt          the current task or nothing, fast (for the shell hook)
//	t hook zsh|bash   print the shell hook
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
	"unicode"

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
  t rm <words...>   remove the task whose title matches
  t undo            remove the task you added last
  t edit            rename, add and remove tasks in a full-screen editor
  t prompt          show the current task quickly, or nothing
  t hook zsh|bash   print a snippet for your shell rc file
`

// Time limits, so t never hangs the terminal.
const (
	timeout      = 30 * time.Second // one command
	addTimeout   = 5 * time.Second  // one capture; after that it is saved offline
	flushTimeout = 2 * time.Second  // sending saved captures before a command
)

// command is what the user asked for, decided from the arguments alone.
type command struct {
	name    string // "list", "focus", "done", "skip", "add", "rm", "undo", "edit", "prompt", "hook" or "help"
	summary string // title of the new task, only for "add"
	query   string // words to look for in titles, only for "rm"
	shell   string // "zsh" or "bash", only for "hook"
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
	switch cmd.name {
	case "help":
		fmt.Fprint(out, usage)
		return nil
	case "hook":
		fmt.Fprint(out, hooks[cmd.shell])
		return nil
	case "prompt":
		runPrompt(out, config.Load())
		return nil // always exit code 0
	}

	cfg := config.Load()
	b, err := newBackend(context.Background(), cfg, os.Stderr)
	if err != nil {
		return err
	}
	a := &app{b: b, out: out, defaultList: cfg.DefaultList, queue: openQueue(), lastPath: lastCapturePath()}
	// out.(*os.File) asks whether out is a real file such as os.Stdout;
	// only then can it be a terminal.
	if f, ok := out.(*os.File); ok {
		a.tty = detectTerminal(f)
	}

	// First send captures saved while offline. This never stops the
	// command itself. Not for undo: a capture still waiting in the queue
	// is simply taken out of it instead of being sent and then deleted.
	if cmd.name != "undo" {
		a.flush()
	}

	// The editor runs as long as the user likes; each of its changes gets
	// its own time limit instead.
	if cmd.name == "edit" {
		return a.edit()
	}

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
	lastPath    string       // file remembering the last capture for t undo; "" for none
	tty         terminal     // the terminal t prints to; zero value for pipes and files
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
	case "list", "focus", "done", "skip", "undo", "edit", "prompt", "help":
		if len(args) > 1 {
			return command{}, fmt.Errorf("%q takes no extra words; to add this as a task, use: t add %s",
				first, strings.Join(args, " "))
		}
		return command{name: first}, nil
	case "-h", "--help":
		return command{name: "help"}, nil
	case "hook":
		if len(args) != 2 || hooks[args[1]] == "" {
			return command{}, errors.New("usage: t hook zsh|bash")
		}
		return command{name: "hook", shell: args[1]}, nil
	case "rm":
		query := strings.Join(strings.Fields(strings.Join(args[1:], " ")), " ")
		if query == "" {
			return command{}, errors.New("usage: t rm <words of the title>")
		}
		return command{name: "rm", query: query}, nil
	case "add":
		args = args[1:]
	default:
		if strings.HasPrefix(first, "-") {
			return command{}, fmt.Errorf("unknown option %q (see t help)", first)
		}
		// A single word that is a typo of a command ("t lsit") is not
		// captured; "t add lsit" still adds it.
		if len(args) == 1 {
			if meant, ok := likelyTypo(first); ok {
				return command{}, fmt.Errorf("did you mean \"t %s\"? To add %q as a task, use: t add %s", meant, first, first)
			}
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
		switch {
		case len(tasks) == 0:
			show(focus.Task{}, false)
		case a.tty.width > 0:
			// A terminal gets a checklist; pipes and scripts get plain lines.
			fmt.Fprint(out, renderList(tasks, now, a.defaultList, a.tty))
		default:
			for _, task := range tasks {
				show(task, true)
			}
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

	case "rm":
		return a.rm(ctx, cmd.query)

	case "undo":
		return a.undo(ctx)

	default:
		return fmt.Errorf("unknown command %q", cmd.name)
	}
	return nil
}

// formatTask renders a task as one calm line, for example
// "Pay rent (due tomorrow)" or "Milk [Groceries]".
func formatTask(task focus.Task, now time.Time, defaultList string) string {
	line := oneLine(task.Summary)
	if due := describeDue(task, now); due != "" {
		line += " (" + due + ")"
	}
	if task.List != "" && !strings.EqualFold(task.List, defaultList) {
		line += " [" + oneLine(task.List) + "]"
	}
	return line
}

// oneLine makes text safe to print as one line in a terminal. Titles come
// from other devices and may contain line breaks or control characters
// (which could even change the terminal's state); those become spaces.
func oneLine(text string) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	return strings.Join(strings.Fields(clean), " ")
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
