package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/api"
	"github.com/Mag1cByt3s/beacon/internal/journal"
)

// t timer and t did: focus sessions and things done, for the journal.
// Both need the beacon server.

// timerCmd is what "t timer ..." asked for.
type timerCmd struct {
	action string // "start", "stop", "toggle" or "status"
	text   string // start: the label; stop and toggle: what was done
	kind   string // start: computer or offline
	label  bool   // status: print only the running timer's label, for scripts
}

const timerUsage = "usage: t timer start [label] [--offline] | stop [what was done] | toggle | status [--label]"

func parseTimerArgs(args []string) (timerCmd, error) {
	if len(args) == 0 {
		return timerCmd{}, errors.New(timerUsage)
	}
	tc := timerCmd{action: args[0], kind: journal.KindComputer}
	words, offline := offlineFlag(args[1:])
	tc.text = strings.Join(strings.Fields(strings.Join(words, " ")), " ")
	switch tc.action {
	case "start":
		if offline {
			tc.kind = journal.KindOffline
		}
	case "stop", "toggle":
		if offline {
			return timerCmd{}, errors.New("--offline is for starting: t timer start [label] --offline")
		}
	case "status":
		switch {
		case len(args) == 2 && args[1] == "--label":
			tc.label, tc.text = true, ""
		case len(args) > 1:
			return timerCmd{}, errors.New(timerUsage)
		}
	default:
		return timerCmd{}, errors.New(timerUsage)
	}
	return tc, nil
}

// didCmd is what "t did ..." recorded.
type didCmd struct {
	title   string
	minutes *int
	kind    string
}

const didUsage = `usage: t did <what you did> [minutes] [--offline], e.g. t did "HTB box" 90`

// parseDidArgs reads the title, an optional number of minutes at the end
// ("90" or "90min") and --offline.
func parseDidArgs(args []string) (didCmd, error) {
	words, offline := offlineFlag(args)
	dc := didCmd{kind: journal.KindComputer}
	if offline {
		dc.kind = journal.KindOffline
	}
	// A quoted title may hold several words; split them all up first.
	words = strings.Fields(strings.Join(words, " "))
	if len(words) > 1 {
		last := strings.TrimSuffix(strings.TrimSuffix(words[len(words)-1], "min"), "m")
		if n, err := strconv.Atoi(last); err == nil {
			if n < 0 || n > 24*60 {
				return didCmd{}, errors.New("minutes must be between 0 and 1440")
			}
			dc.minutes = &n
			words = words[:len(words)-1]
		}
	}
	dc.title = strings.Join(words, " ")
	if dc.title == "" {
		return didCmd{}, errors.New(didUsage)
	}
	return dc, nil
}

// offlineFlag takes --offline out of args.
func offlineFlag(args []string) (rest []string, offline bool) {
	for _, arg := range args {
		if arg == "--offline" {
			offline = true
			continue
		}
		rest = append(rest, arg)
	}
	return rest, offline
}

// did records one thing done today. Like a capture, it prints nothing.
func (a *app) did(dc didCmd) error {
	c, err := a.server("t did")
	if err != nil {
		return err
	}
	ctx, cancel := request()
	defer cancel()
	_, err = c.AddItem(ctx, "today", journal.Item{
		Title:   dc.title,
		Minutes: dc.minutes,
		Kind:    dc.kind,
		Source:  journal.SourceManual,
	})
	return err
}

func (a *app) runTimer(tc timerCmd) error {
	c, err := a.server("t timer")
	if err != nil {
		return err
	}
	ctx, cancel := request()
	status, err := c.FocusStatus(ctx, "today")
	cancel()
	if err != nil {
		return err
	}

	switch tc.action {
	case "status":
		return a.timerStatus(status, tc.label)
	case "start":
		if status.Running != nil {
			return fmt.Errorf("a timer is already running (%s); stop it first: t timer stop", sessionLine(*status.Running, a.now()))
		}
		return a.timerStart(c, tc)
	case "stop":
		if status.Running == nil {
			return errors.New("no timer is running; start one with: t timer start [label]")
		}
		return a.timerStop(c, tc, *status.Running)
	default: // toggle
		if status.Running == nil {
			return a.timerStart(c, tc)
		}
		return a.timerStop(c, tc, *status.Running)
	}
}

func (a *app) timerStart(c *api.Client, tc timerCmd) error {
	ctx, cancel := request()
	defer cancel()
	resp, err := c.FocusStart(ctx, api.FocusRequest{Label: tc.text, Kind: tc.kind})
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Timer läuft: %s\n", sessionLine(resp.Session, a.now()))
	return nil
}

// timerStop stops the running session. Without a text and in a terminal,
// it asks what was done, with the session's label as the answer for Enter.
func (a *app) timerStop(c *api.Client, tc timerCmd, running journal.Session) error {
	result := tc.text
	if result == "" && a.interactive {
		label := oneLine(running.Label)
		prompt := "Was geschafft? (Enter = nichts notieren): "
		if label != "" {
			prompt = fmt.Sprintf("Was geschafft? [%s] (- = nichts notieren): ", label)
		}
		answer, err := a.ask(prompt)
		if err != nil {
			return err
		}
		switch answer {
		case "":
			result = label
		case "-":
			result = ""
		default:
			result = answer
		}
	}

	ctx, cancel := request()
	defer cancel()
	resp, err := c.FocusStop(ctx, api.FocusRequest{Result: result})
	if err != nil {
		return err
	}
	line := "Timer gestoppt: " + minutesText(resp.Minutes)
	if resp.Item != nil {
		line += ", notiert: " + oneLine(resp.Item.Title)
	}
	fmt.Fprintln(a.out, line)
	if resp.Suspicious {
		fmt.Fprintln(a.out, "Der Timer lief über 4 Stunden. Stimmt die Zeit nicht, korrigiere sie in t journal.")
	}
	return nil
}

// timerStatus prints the running timer and today's total, or with
// labelOnly just the running timer's label. Like t journal status, the
// exit code tells scripts the answer: 0 if a timer runs, 1 if not.
func (a *app) timerStatus(status api.FocusStatus, labelOnly bool) error {
	if labelOnly {
		if status.Running == nil {
			return exitCode{code: 1}
		}
		fmt.Fprintln(a.out, oneLine(status.Running.Label))
		return nil
	}
	now := a.now()
	total := status.Minutes
	if status.Running != nil {
		fmt.Fprintf(a.out, "Timer läuft: %s\n", sessionLine(*status.Running, now))
		if status.Running.Date == status.Date {
			total += status.Running.Minutes(now)
		}
	} else {
		fmt.Fprintln(a.out, "Kein Timer läuft.")
	}
	if total > 0 {
		fmt.Fprintf(a.out, "Heute: %s\n", minutesText(total))
	}
	if status.Running == nil {
		return exitCode{code: 1}
	}
	return nil
}

// sessionLine describes a session: "HTB box, seit 14:03 (42 min)".
func sessionLine(s journal.Session, now time.Time) string {
	line := fmt.Sprintf("seit %s (%s)", s.Start.Local().Format("15:04"), minutesText(s.Minutes(now)))
	if s.Kind == journal.KindOffline {
		line += ", offline"
	}
	if label := oneLine(s.Label); label != "" {
		line = label + ", " + line
	}
	return line
}

// exitCode ends t with code instead of 1. err is printed if set; without
// it t exits silently, which is what scripts asking a yes/no question want.
type exitCode struct {
	code int
	err  error
}

func (e exitCode) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return "exit status " + strconv.Itoa(e.code)
}

func (e exitCode) Unwrap() error { return e.err }
