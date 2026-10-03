// Command t is beacon's command line: one task at a time.
//
//	t list    open tasks, best first
//	t focus   only the next task, on one line
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/caldav"
	"github.com/Mag1cByt3s/beacon/internal/config"
	"github.com/Mag1cByt3s/beacon/internal/focus"
)

const usage = `usage:
  t list    show open tasks, best first
  t focus   show only the next task
`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "t:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(out, usage)
		return nil
	}

	switch args[0] {
	case "list":
		return list(out)
	case "focus":
		return showFocus(out)
	case "help", "-h", "--help":
		fmt.Fprint(out, usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q (try \"t list\" or \"t focus\")", args[0])
	}
}

func list(out io.Writer) error {
	tasks, showList, err := loadTasks()
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
	tasks, showList, err := loadTasks()
	if err != nil {
		return err
	}
	now := time.Now()
	next, ok := focus.Next(tasks, now)
	if !ok {
		fmt.Fprintln(out, "No open tasks.")
		return nil
	}
	fmt.Fprintln(out, formatTask(next, now, showList))
	return nil
}

// loadTasks reads the configuration and fetches open tasks from CalDAV.
// showList is true when more than one list is configured, so the output
// can say where each task comes from.
func loadTasks() (tasks []focus.Task, showList bool, err error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, false, err
	}

	password := ""
	if cfg.User != "" {
		password, err = cfg.Password()
		if err != nil {
			return nil, false, err
		}
	}

	client, err := caldav.NewClient(cfg.CalDAVURL, cfg.User, password, cfg.Lists)
	if err != nil {
		return nil, false, err
	}

	// Give up after a while instead of hanging the terminal.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel() // defer runs when loadTasks returns

	tasks, err = client.OpenTasks(ctx)
	if err != nil {
		return nil, false, err
	}
	return tasks, len(cfg.Lists) > 1, nil
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
