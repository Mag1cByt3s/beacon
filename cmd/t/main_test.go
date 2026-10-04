package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/api"
	"github.com/Mag1cByt3s/beacon/internal/caldav"
	"github.com/Mag1cByt3s/beacon/internal/focus"
)

func TestFormatTask(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) // a Saturday
	date := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC) }

	tests := []struct {
		name string
		task focus.Task
		want string
	}{
		{"no due date", focus.Task{Summary: "Read"}, "Read"},
		{"overdue", focus.Task{Summary: "Pay rent", Due: now.Add(-time.Hour)}, "Pay rent (overdue)"},
		{"due today all day", focus.Task{Summary: "Call", Due: date(10, 3), DueAllDay: true}, "Call (due today)"},
		{"due today with time", focus.Task{Summary: "Call", Due: now.Add(3 * time.Hour)}, "Call (due today 15:00)"},
		{"due tomorrow", focus.Task{Summary: "Bin", Due: date(10, 4), DueAllDay: true}, "Bin (due tomorrow)"},
		{"due later", focus.Task{Summary: "Tax", Due: date(10, 12), DueAllDay: true}, "Tax (due Mon 12 Oct)"},
		{"due next year", focus.Task{Summary: "Tax", Due: time.Date(2027, 1, 4, 0, 0, 0, 0, time.UTC), DueAllDay: true}, "Tax (due Mon 4 Jan 2027)"},
		{"default list is not shown", focus.Task{Summary: "Read", List: "todo"}, "Read"},
		{"other list is shown", focus.Task{Summary: "Milk", List: "Groceries"}, "Milk [Groceries]"},
		{"line breaks become spaces", focus.Task{Summary: "Call\nthe  bank\r\n"}, "Call the bank"},
		{"terminal escapes are removed", focus.Task{Summary: "Pay\x1b[2Jrent\x07"}, "Pay [2Jrent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatTask(tt.task, now, "Todo"); got != tt.want {
				t.Errorf("formatTask = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantName    string
		wantSummary string
		wantErr     bool
	}{
		{name: "no args shows focus", args: nil, wantName: "focus"},
		{name: "list", args: []string{"list"}, wantName: "list"},
		{name: "focus", args: []string{"focus"}, wantName: "focus"},
		{name: "done", args: []string{"done"}, wantName: "done"},
		{name: "skip", args: []string{"skip"}, wantName: "skip"},
		{name: "skip with extra words", args: []string{"skip", "lunch"}, wantErr: true},
		{name: "prompt", args: []string{"prompt"}, wantName: "prompt"},
		{name: "prompt with extra words", args: []string{"prompt", "x"}, wantErr: true},
		{name: "hook zsh", args: []string{"hook", "zsh"}, wantName: "hook"},
		{name: "hook bash", args: []string{"hook", "bash"}, wantName: "hook"},
		{name: "hook without shell", args: []string{"hook"}, wantErr: true},
		{name: "hook unknown shell", args: []string{"hook", "fish"}, wantErr: true},
		{name: "help", args: []string{"help"}, wantName: "help"},
		{name: "--help", args: []string{"--help"}, wantName: "help"},
		{name: "-h", args: []string{"-h"}, wantName: "help"},
		{name: "words become a task", args: []string{"buy", "coffee"}, wantName: "add", wantSummary: "buy coffee"},
		{name: "one quoted argument", args: []string{"buy coffee"}, wantName: "add", wantSummary: "buy coffee"},
		{name: "extra spaces collapse", args: []string{"  buy ", "  coffee  "}, wantName: "add", wantSummary: "buy coffee"},
		{name: "single word", args: []string{"laundry"}, wantName: "add", wantSummary: "laundry"},
		{name: "command word later is fine", args: []string{"make", "a", "list"}, wantName: "add", wantSummary: "make a list"},
		{name: "capitalised command word is a task", args: []string{"Done"}, wantName: "add", wantSummary: "Done"},
		{name: "add with command word", args: []string{"add", "list", "groceries"}, wantName: "add", wantSummary: "list groceries"},
		{name: "add a task called add", args: []string{"add", "add"}, wantName: "add", wantSummary: "add"},
		{name: "add without words", args: []string{"add"}, wantErr: true},
		{name: "add with only spaces", args: []string{"add", "  "}, wantErr: true},
		{name: "empty argument", args: []string{""}, wantErr: true},
		{name: "done with extra words", args: []string{"done", "with", "dishes"}, wantErr: true},
		{name: "list with extra words", args: []string{"list", "all"}, wantErr: true},
		{name: "unknown option", args: []string{"--version"}, wantErr: true},
		{name: "undo", args: []string{"undo"}, wantName: "undo"},
		{name: "undo with extra words", args: []string{"undo", "it"}, wantErr: true},
		{name: "edit", args: []string{"edit"}, wantName: "edit"},
		{name: "typo of edit", args: []string{"eidt"}, wantErr: true},
		{name: "rm", args: []string{"rm", "buy", "coffee"}, wantName: "rm"},
		{name: "rm without words", args: []string{"rm"}, wantErr: true},
		{name: "typo of list", args: []string{"lsit"}, wantErr: true},
		{name: "typo of focus", args: []string{"fcous"}, wantErr: true},
		{name: "typo of done", args: []string{"dnoe"}, wantErr: true},
		{name: "typo of skip", args: []string{"skp"}, wantErr: true},
		{name: "typo of undo", args: []string{"unod"}, wantErr: true},
		{name: "typo can still be added", args: []string{"add", "lsit"}, wantName: "add", wantSummary: "lsit"},
		{name: "capitalised near-command is a task", args: []string{"Lsit"}, wantName: "add", wantSummary: "Lsit"},
		{name: "near-command with more words is a task", args: []string{"lsit", "groceries"}, wantName: "add", wantSummary: "lsit groceries"},
		{name: "ordinary word is a task", args: []string{"coffee"}, wantName: "add", wantSummary: "coffee"},
		{name: "near add is a task", args: []string{"bad"}, wantName: "add", wantSummary: "bad"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseArgs(%q) = %+v, want an error", tt.args, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseArgs(%q): %v", tt.args, err)
			}
			if got.name != tt.wantName || got.summary != tt.wantSummary {
				t.Errorf("parseArgs(%q) = %+v, want name %q summary %q", tt.args, got, tt.wantName, tt.wantSummary)
			}
		})
	}
}

// fakeBackend returns canned answers, so execute can be tested without
// any server.
type fakeBackend struct {
	tasks     []focus.Task
	err       error    // returned by every method
	addErr    error    // returned by Add only
	added     []string // UIDs passed to Add
	removeErr error    // returned by Remove only
	removed   []string // "uid@etag" passed to Remove
	renameErr error    // returned by Rename only
	renamed   []string // "uid@etag=summary" passed to Rename
}

func (f *fakeBackend) Tasks(ctx context.Context) ([]focus.Task, error) { return f.tasks, f.err }

func (f *fakeBackend) Current(ctx context.Context) (focus.Task, bool, error) {
	if len(f.tasks) == 0 {
		return focus.Task{}, false, f.err
	}
	return f.tasks[0], true, f.err
}

func (f *fakeBackend) Add(ctx context.Context, uid, summary string) (string, string, error) {
	f.added = append(f.added, uid)
	if f.addErr != nil {
		return "", "", f.addErr
	}
	return "Todo", "etag-" + uid, f.err
}

func (f *fakeBackend) Done(ctx context.Context) (focus.Task, bool, error) {
	if f.err != nil || len(f.tasks) < 2 {
		return focus.Task{}, false, f.err
	}
	return f.tasks[1], true, nil
}

func (f *fakeBackend) Skip(ctx context.Context) (focus.Task, bool, error) { return f.Done(ctx) }

func (f *fakeBackend) Rename(ctx context.Context, uid, etag, summary string) (focus.Task, error) {
	f.renamed = append(f.renamed, uid+"@"+etag+"="+summary)
	if f.renameErr != nil {
		return focus.Task{}, f.renameErr
	}
	for i, task := range f.tasks {
		if task.UID == uid {
			if etag != "" && etag != task.ETag {
				return task, caldav.ErrConflict
			}
			f.tasks[i].Summary = summary
			f.tasks[i].ETag = etag + "+"
			return f.tasks[i], nil
		}
	}
	return focus.Task{}, caldav.ErrGone
}

func (f *fakeBackend) Remove(ctx context.Context, uid, etag string) (focus.Task, error) {
	f.removed = append(f.removed, uid+"@"+etag)
	if f.removeErr != nil {
		return focus.Task{}, f.removeErr
	}
	for i, task := range f.tasks {
		if task.UID == uid {
			f.tasks = append(f.tasks[:i:i], f.tasks[i+1:]...)
			return task, f.err
		}
	}
	return focus.Task{}, caldav.ErrGone
}

func TestExecute(t *testing.T) {
	two := []focus.Task{{Summary: "Pay rent", List: "Todo"}, {Summary: "Milk", List: "Groceries"}}
	conflict := &api.ConflictError{Message: "changed", Current: &focus.Task{Summary: "Oat milk"}}

	tests := []struct {
		name    string
		cmd     command
		backend *fakeBackend
		want    string // output
		wantErr string // part of the error message
	}{
		{"list", command{name: "list"}, &fakeBackend{tasks: two}, "Pay rent\nMilk [Groceries]\n", ""},
		{"list empty", command{name: "list"}, &fakeBackend{}, "No open tasks.\n", ""},
		{"focus", command{name: "focus"}, &fakeBackend{tasks: two}, "Pay rent\n", ""},
		{"focus empty", command{name: "focus"}, &fakeBackend{}, "No open tasks.\n", ""},
		{"done shows next", command{name: "done"}, &fakeBackend{tasks: two}, "Milk [Groceries]\n", ""},
		{"done last one", command{name: "done"}, &fakeBackend{tasks: two[:1]}, "No open tasks.\n", ""},
		{"skip shows next", command{name: "skip"}, &fakeBackend{tasks: two}, "Milk [Groceries]\n", ""},
		{"add", command{name: "add", summary: "buy coffee"}, &fakeBackend{}, "Added to Todo.\n", ""},
		{"done conflict", command{name: "done"}, &fakeBackend{err: conflict}, "",
			`"Oat milk" was changed elsewhere since it became current, so it was left alone; run t done again`},
		{"unauthorized", command{name: "focus"}, &fakeBackend{err: api.ErrUnauthorized}, "", "check BEACON_TOKEN"},
		{"skip without server", command{name: "skip"}, &fakeBackend{err: errSkipNeedsServer}, "", "BEACON_SERVER_URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out strings.Builder
			a := &app{b: tt.backend, out: &out, defaultList: "Todo"}
			err := a.execute(context.Background(), tt.cmd)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
			}
			if out.String() != tt.want {
				t.Errorf("output = %q, want %q", out.String(), tt.want)
			}
		})
	}
}
