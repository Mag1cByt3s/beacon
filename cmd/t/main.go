// Command t is beacon's command line: one task at a time.
//
//	t                 show the next task (same as t focus)
//	t buy coffee      add a task
//	t list            open tasks, best first
//	t focus           only the next task, on one line
//	t done            complete the next task and show the one after it
//	t add <words>     add a task that starts with a command word
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/caldav"
	"github.com/Mag1cByt3s/beacon/internal/config"
	"github.com/Mag1cByt3s/beacon/internal/focus"
)

const usage = `usage:
  t                 show the next task
  t <words...>      add a task, e.g. t buy coffee
  t list            show open tasks, best first
  t focus           show only the next task
  t done            complete the next task
  t add <words...>  add a task that starts with a command word
`

// Each network call gets at most this long, so t never hangs the terminal.
const timeout = 30 * time.Second

// command is what the user asked for, decided from the arguments alone.
type command struct {
	name    string // "list", "focus", "done", "add" or "help"
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

	switch cmd.name {
	case "help":
		fmt.Fprint(out, usage)
		return nil
	case "list":
		return list(out)
	case "focus":
		return showFocus(out)
	case "done":
		return done(out)
	case "add":
		return add(out, cmd.summary)
	}
	return fmt.Errorf("unknown command %q", cmd.name) // not reached
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
	case "list", "focus", "done", "help":
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

func list(out io.Writer) error {
	_, tasks, showList, err := loadTasks()
	if err != nil {
		return err
	}
	now := time.Now()
	queue := focus.Queue(tasks, now)
	if len(queue) == 0 {
		fmt.Fprintln(out, "No open tasks.")
		return nil
	}
	for _, task := range queue {
		fmt.Fprintln(out, formatTask(task, now, showList))
	}
	return nil
}

func showFocus(out io.Writer) error {
	_, tasks, showList, err := loadTasks()
	if err != nil {
		return err
	}
	printNext(out, tasks, showList)
	return nil
}

// done completes the task t focus would show, then shows the next one.
func done(out io.Writer) error {
	client, tasks, showList, err := loadTasks()
	if err != nil {
		return err
	}
	current, ok := focus.Next(tasks, time.Now())
	if !ok {
		fmt.Fprintln(out, "No open tasks.")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := client.Complete(ctx, current); err != nil {
		return err
	}

	// Show the next task from what was already read, without asking the
	// server again.
	var rest []focus.Task
	for _, task := range tasks {
		if task.UID != current.UID || task.Path != current.Path {
			rest = append(rest, task)
		}
	}
	printNext(out, rest, showList)
	return nil
}

func add(out io.Writer, summary string) error {
	cfg, client, err := connect()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	listName, err := client.Create(ctx, cfg.DefaultList, summary)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Added to %s.\n", listName)
	return nil
}

func printNext(out io.Writer, tasks []focus.Task, showList bool) {
	now := time.Now()
	next, ok := focus.Next(tasks, now)
	if !ok {
		fmt.Fprintln(out, "No open tasks.")
		return
	}
	fmt.Fprintln(out, formatTask(next, now, showList))
}

// connect reads the configuration and creates a CalDAV client.
func connect() (config.Config, *caldav.Client, error) {
	cfg := config.Load()
	if err := cfg.CheckCalDAV(); err != nil {
		return config.Config{}, nil, err
	}

	var err error
	password := ""
	if cfg.User != "" {
		password, err = cfg.Password()
		if err != nil {
			return config.Config{}, nil, err
		}
	}

	client, err := caldav.NewClient(cfg.CalDAVURL, cfg.User, password, cfg.Lists)
	if err != nil {
		return config.Config{}, nil, err
	}
	return cfg, client, nil
}

// loadTasks connects and fetches open tasks from the focus lists.
// showList is true when more than one list is configured, so the output
// can say where each task comes from.
func loadTasks() (client *caldav.Client, tasks []focus.Task, showList bool, err error) {
	cfg, client, err := connect()
	if err != nil {
		return nil, nil, false, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel() // defer runs when loadTasks returns

	tasks, err = client.OpenTasks(ctx)
	if err != nil {
		return nil, nil, false, err
	}
	return client, tasks, len(cfg.Lists) > 1, nil
}

// formatTask renders a task as one calm line, for example
// "Pay rent (due tomorrow)" or "Milk [Groceries]".
func formatTask(task focus.Task, now time.Time, showList bool) string {
	line := task.Summary
	if due := describeDue(task, now); due != "" {
		line += " (" + due + ")"
	}
	if showList {
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
