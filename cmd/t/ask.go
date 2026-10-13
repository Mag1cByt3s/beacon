package main

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// The questions of t journal and t timer. They read whole lines from
// a.in, so they work in any terminal and are easy to test with prepared
// input.

// errStopped means the input ended (Ctrl-D) in the middle of the
// questions; nothing is saved then.
var errStopped = errors.New("abgebrochen, nichts gespeichert")

// ask prints prompt and returns the answer without surrounding spaces.
func (a *app) ask(prompt string) (string, error) {
	fmt.Fprint(a.out, prompt)
	line, err := a.in.ReadString('\n')
	if err != nil && (line == "" || !errors.Is(err, io.EOF)) {
		fmt.Fprintln(a.out)
		return "", errStopped
	}
	return strings.TrimSpace(line), nil
}

// askInt asks for a whole number from min to max. An empty answer gives
// def, which may be nil for "skip".
func (a *app) askInt(prompt string, min, max int, def *int) (*int, error) {
	for {
		answer, err := a.ask(prompt)
		if err != nil {
			return nil, err
		}
		if answer == "" {
			return def, nil
		}
		n, err := strconv.Atoi(answer)
		if err == nil && n >= min && n <= max {
			return &n, nil
		}
		fmt.Fprintf(a.out, "  Bitte eine Zahl von %d bis %d.\n", min, max)
	}
}

// askRequiredInt is askInt without a way to skip.
func (a *app) askRequiredInt(prompt string, min, max int) (int, error) {
	for {
		n, err := a.askInt(prompt, min, max, nil)
		if err != nil {
			return 0, err
		}
		if n != nil {
			return *n, nil
		}
		fmt.Fprintf(a.out, "  Bitte eine Zahl von %d bis %d.\n", min, max)
	}
}

// askHours asks for a number of hours, "7.5" or "7,5". An empty answer
// gives def, which may be nil for "skip".
func (a *app) askHours(prompt string, def *float64) (*float64, error) {
	for {
		answer, err := a.ask(prompt)
		if err != nil {
			return nil, err
		}
		if answer == "" {
			return def, nil
		}
		// German keyboards write 7,5.
		h, err := strconv.ParseFloat(strings.Replace(answer, ",", ".", 1), 64)
		if err == nil && h >= 0 && h <= 24 {
			return &h, nil
		}
		fmt.Fprintln(a.out, "  Bitte eine Zahl von 0 bis 24, z. B. 7,5.")
	}
}

// askYesNo asks a yes/no question; an empty answer gives def.
func (a *app) askYesNo(prompt string, def bool) (bool, error) {
	for {
		answer, err := a.ask(prompt)
		if err != nil {
			return false, err
		}
		switch strings.ToLower(answer) {
		case "":
			return def, nil
		case "j", "ja", "y", "yes":
			return true, nil
		case "n", "nein", "no":
			return false, nil
		}
		fmt.Fprintln(a.out, "  Bitte j oder n.")
	}
}

// hours formats hours the German way: 2,5 h.
func hours(h float64) string {
	return strings.Replace(strconv.FormatFloat(h, 'f', -1, 64), ".", ",", 1) + " h"
}

// minutesText formats a duration in minutes: "45 min" or "1 h 30 min".
func minutesText(m int) string {
	if m < 60 {
		return fmt.Sprintf("%d min", m)
	}
	if m%60 == 0 {
		return fmt.Sprintf("%d h", m/60)
	}
	return fmt.Sprintf("%d h %d min", m/60, m%60)
}
