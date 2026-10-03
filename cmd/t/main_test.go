package main

import (
	"testing"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/focus"
)

func TestFormatTask(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) // a Saturday
	date := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC) }

	tests := []struct {
		name     string
		task     focus.Task
		showList bool
		want     string
	}{
		{"no due date", focus.Task{Summary: "Read"}, false, "Read"},
		{"overdue", focus.Task{Summary: "Pay rent", Due: now.Add(-time.Hour)}, false, "Pay rent (overdue)"},
		{"due today all day", focus.Task{Summary: "Call", Due: date(10, 3), DueAllDay: true}, false, "Call (due today)"},
		{"due today with time", focus.Task{Summary: "Call", Due: now.Add(3 * time.Hour)}, false, "Call (due today 15:00)"},
		{"due tomorrow", focus.Task{Summary: "Bin", Due: date(10, 4), DueAllDay: true}, false, "Bin (due tomorrow)"},
		{"due later", focus.Task{Summary: "Tax", Due: date(10, 12), DueAllDay: true}, false, "Tax (due Mon 12 Oct)"},
		{"due next year", focus.Task{Summary: "Tax", Due: time.Date(2027, 1, 4, 0, 0, 0, 0, time.UTC), DueAllDay: true}, false, "Tax (due Mon 4 Jan 2027)"},
		{"with list", focus.Task{Summary: "Milk", List: "Groceries"}, true, "Milk [Groceries]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatTask(tt.task, now, tt.showList); got != tt.want {
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
