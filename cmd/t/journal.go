package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/api"
	"github.com/Mag1cByt3s/beacon/internal/journal"
)

// t journal: the daily sleep and productivity journal. It needs the beacon
// server, which keeps the journal.

// eveningFrom is the hour from which "t journal" also asks for the
// evening part (until the journal day ends at 04:00).
const eveningFrom = 16

// journalCmd is what "t journal ..." asked for.
type journalCmd struct {
	mode       string // "auto", "morning", "evening", "show" or "status"
	date       string // for show: "today" or a date
	quiet      bool   // for status: no output, only the exit code
	writeCache bool   // for status: update the file the zsh prompt reads
}

const journalUsage = "usage: t journal [--morning | --evening | show [date] | status [--quiet] [--write-cache]]"

func parseJournalArgs(args []string) (journalCmd, error) {
	switch {
	case len(args) == 0:
		return journalCmd{mode: "auto"}, nil
	case len(args) == 1 && args[0] == "--morning":
		return journalCmd{mode: "morning"}, nil
	case len(args) == 1 && args[0] == "--evening":
		return journalCmd{mode: "evening"}, nil
	case args[0] == "show" && len(args) <= 2:
		jc := journalCmd{mode: "show", date: "today"}
		if len(args) == 2 {
			jc.date = args[1]
		}
		return jc, nil
	case args[0] == "status":
		jc := journalCmd{mode: "status"}
		for _, flag := range args[1:] {
			switch flag {
			case "--quiet":
				jc.quiet = true
			case "--write-cache":
				jc.writeCache = true
			default:
				return journalCmd{}, errors.New(journalUsage)
			}
		}
		return jc, nil
	}
	return journalCmd{}, errors.New(journalUsage)
}

// server returns the API client, or an error saying that what needs the
// beacon server.
func (a *app) server(what string) (*api.Client, error) {
	// a.b.(*api.Client) asks whether the backend is the API client.
	if c, ok := a.b.(*api.Client); ok {
		return c, nil
	}
	return nil, fmt.Errorf("%s needs the beacon server; set BEACON_SERVER_URL", what)
}

// request gives one request its own time limit. The questions in between
// take as long as they take.
func request() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}

func (a *app) runJournal(jc journalCmd) error {
	if jc.mode == "status" {
		return a.journalStatus(jc)
	}
	c, err := a.server("t journal")
	if err != nil {
		return err
	}
	date := "today"
	if jc.mode == "show" {
		date = jc.date
	}
	ctx, cancel := request()
	day, err := c.Journal(ctx, date)
	cancel()
	if err != nil {
		return err
	}
	date = day.Entry.Date

	switch jc.mode {
	case "show":
		a.showJournal(c, day)
		return nil
	case "morning":
		err = a.journalMorning(c, date)
	case "evening":
		err = a.journalEvening(c, date)
	default:
		err = a.journalAuto(c, day)
	}
	if err != nil {
		return err
	}
	a.refreshJournalCache(c)
	return nil
}

// journalAuto asks for the parts still missing: the morning part at any
// time, the evening part from eveningFrom on.
func (a *app) journalAuto(c *api.Client, day api.JournalResponse) error {
	missing := map[string]bool{}
	for _, part := range day.Missing {
		missing[part] = true
	}
	if len(missing) == 0 {
		fmt.Fprintln(a.out, "Heute ist alles eingetragen.")
		return nil
	}
	date := day.Entry.Date
	if missing[journal.PartMorning] {
		if err := a.journalMorning(c, date); err != nil {
			return err
		}
	}
	if !missing[journal.PartEvening] {
		return nil
	}
	if h := a.now().Hour(); h >= eveningFrom || h < 4 {
		if missing[journal.PartMorning] {
			fmt.Fprintln(a.out)
		}
		return a.journalEvening(c, date)
	}
	fmt.Fprintf(a.out, "Den Abend fragt t journal ab %d:00 ab (oder jetzt: t journal --evening).\n", eveningFrom)
	return nil
}

func (a *app) journalMorning(c *api.Client, date string) error {
	fmt.Fprintf(a.out, "Morgen-Check für %s\n", dateLabel(date))
	quality, err := a.askRequiredInt("Wie hast du geschlafen? (1-10): ", 1, 10)
	if err != nil {
		return err
	}
	sleepHours, err := a.askHours("Wie viele Stunden? (Enter = überspringen): ", nil)
	if err != nil {
		return err
	}

	fields := map[string]any{"sleep_quality": quality}
	if sleepHours != nil {
		fields["sleep_hours"] = *sleepHours
	}
	ctx, cancel := request()
	defer cancel()
	if _, err := c.PatchJournal(ctx, date, fields); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "Gespeichert.")
	return nil
}

func (a *app) journalEvening(c *api.Client, date string) error {
	ctx, cancel := request()
	sg, err := c.Suggestion(ctx, date)
	cancel()
	if err != nil {
		return err
	}

	fmt.Fprintf(a.out, "Abend-Check für %s\n\n", dateLabel(date))
	a.showSuggestion(sg)

	// Productive time: the suggestion is the default.
	var defHours *float64
	if sg.Source != journal.FromNothing {
		defHours = &sg.ProductiveHours
	}
	prompt := "Produktive Stunden (Enter = überspringen): "
	if defHours != nil {
		prompt = fmt.Sprintf("Produktive Stunden [%s]: ", strings.TrimSuffix(hours(*defHours), " h"))
	}
	productiveHours, err := a.askHours(prompt, defHours)
	if err != nil {
		return err
	}
	windows, err := a.askWindows(sg.ProductiveWindows)
	if err != nil {
		return err
	}

	items, itemsChanged, err := a.askItems(sg.Items)
	if err != nil {
		return err
	}

	productivity, err := a.askRequiredInt("Produktivität heute (1-10): ", 1, 10)
	if err != nil {
		return err
	}
	fields := map[string]any{"productivity": productivity, "productive_windows": windows}
	if productiveHours != nil {
		fields["productive_hours"] = *productiveHours
	}
	sport, err := a.askYesNo("Sport gemacht? [j/N]: ", false)
	if err != nil {
		return err
	}
	fields["sport"] = sport
	if sport {
		sportType, err := a.ask("  Was? (Enter = überspringen): ")
		if err != nil {
			return err
		}
		if sportType != "" {
			fields["sport_type"] = sportType
		}
		sportMinutes, err := a.askInt("  Minuten (Enter = überspringen): ", 0, 24*60, nil)
		if err != nil {
			return err
		}
		if sportMinutes != nil {
			fields["sport_minutes"] = *sportMinutes
		}
	}

	// Everything is asked; now save it.
	ctx, cancel = request()
	defer cancel()
	if itemsChanged {
		if _, err := c.ReplaceItems(ctx, date, items); err != nil {
			return err
		}
	}
	if _, err := c.PatchJournal(ctx, date, fields); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "Gespeichert.")
	return nil
}

// showSuggestion prints what the suggestion is based on and what looks
// wrong.
func (a *app) showSuggestion(sg journal.Suggestion) {
	switch sg.Source {
	case journal.FromSessions:
		fmt.Fprintf(a.out, "Produktive Zeit laut Focus-Sessions und Items: %s\n", hours(sg.ProductiveHours))
	case journal.FromAW:
		fmt.Fprintf(a.out, "Keine Sessions erfasst; laut ActivityWatch: %s am Computer\n", hours(sg.ProductiveHours))
	default:
		fmt.Fprintln(a.out, "Heute wurden keine Sessions erfasst.")
	}
	for _, w := range sg.ProductiveWindows {
		fmt.Fprintf(a.out, "  %s–%s  %s\n", w.Start, w.End, w.Kind)
	}
	for _, s := range sg.Suspicious {
		fmt.Fprintf(a.out, "  Achtung: die Session ab %s (%s) lief über 4 Stunden – vergessen zu stoppen?\n",
			s.Start.Local().Format("15:04"), strings.TrimSpace(s.Label))
	}
	if sg.Running != nil {
		fmt.Fprintf(a.out, "  Ein Timer läuft noch (seit %s) und zählt noch nicht mit.\n", sg.Running.Start.Local().Format("15:04"))
	}
	if sg.AWMinutes != nil && sg.Source == journal.FromSessions {
		fmt.Fprintf(a.out, "  Zum Vergleich: am Computer laut Sessions %s, laut ActivityWatch %s.\n",
			minutesText(sg.ComputerMinutes), minutesText(*sg.AWMinutes))
	}
	for _, w := range sg.Warnings {
		fmt.Fprintf(a.out, "  Hinweis: %s\n", w)
	}
	fmt.Fprintln(a.out)
}

// askWindows offers the suggested windows and lets the user type others.
func (a *app) askWindows(suggested []journal.Window) ([]journal.Window, error) {
	if len(suggested) > 0 {
		take, err := a.askYesNo("Zeitfenster übernehmen? [J/n]: ", true)
		if err != nil || take {
			return suggested, err
		}
	}
	for {
		answer, err := a.ask("Zeitfenster (z. B. 9-12, 14:00-15:30 offline; Enter = keine): ")
		if err != nil {
			return nil, err
		}
		windows, err := parseWindows(answer)
		if err == nil {
			return windows, nil
		}
		fmt.Fprintf(a.out, "  %v\n", err)
	}
}

// parseWindows reads "9-12, 14:00-15:30 offline" into windows.
func parseWindows(text string) ([]journal.Window, error) {
	windows := []journal.Window{}
	for _, part := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ';' }) {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		start, end, ok := strings.Cut(fields[0], "-")
		if !ok || len(fields) > 2 {
			return nil, fmt.Errorf("%q: bitte wie 09:00-12:00 oder 9-12", strings.TrimSpace(part))
		}
		w := journal.Window{Start: clockTime(start), End: clockTime(end)}
		if len(fields) == 2 {
			w.Kind = kindWord(fields[1])
			if w.Kind == "" {
				return nil, fmt.Errorf("%q: Art bitte computer oder offline", fields[1])
			}
		}
		for _, t := range []string{w.Start, w.End} {
			if _, err := time.Parse("15:04", t); err != nil {
				return nil, fmt.Errorf("%q: bitte wie 09:00-12:00 oder 9-12", strings.TrimSpace(part))
			}
		}
		windows = append(windows, w)
	}
	return windows, nil
}

// clockTime turns "9" or "9:30" into "09:00" or "09:30".
func clockTime(s string) string {
	h, m, _ := strings.Cut(s, ":")
	if m == "" {
		m = "00"
	}
	if len(h) == 1 {
		h = "0" + h
	}
	return h + ":" + m
}

// kindWord reads "computer", "offline", "c" or "o".
func kindWord(s string) string {
	switch strings.ToLower(s) {
	case "c", "computer":
		return journal.KindComputer
	case "o", "offline":
		return journal.KindOffline
	}
	return ""
}

// askItems shows the things done (recorded ones and suggested ones),
// lets the user remove some and add forgotten ones. changed says whether
// the list differs from what is stored.
func (a *app) askItems(items []journal.Item) (list []journal.Item, changed bool, err error) {
	for _, it := range items {
		if it.ID == 0 {
			changed = true // suggested tasks are not stored yet
		}
	}
	for {
		fmt.Fprintln(a.out, "Geschafft:")
		if len(items) == 0 {
			fmt.Fprintln(a.out, "  (noch nichts)")
		}
		for i, it := range items {
			fmt.Fprintf(a.out, "  %d. %s\n", i+1, itemLine(it))
		}
		if len(items) == 0 {
			break
		}
		answer, err := a.ask("Nummer zum Löschen, Enter = weiter: ")
		if err != nil {
			return nil, false, err
		}
		if answer == "" {
			break
		}
		n, err := strconv.Atoi(answer)
		if err != nil || n < 1 || n > len(items) {
			fmt.Fprintf(a.out, "  Bitte eine Nummer von 1 bis %d.\n", len(items))
			continue
		}
		items = append(items[:n-1:n-1], items[n:]...)
		changed = true
	}

	for {
		title, err := a.ask("Noch was vergessen? (Enter = nein): ")
		if err != nil {
			return nil, false, err
		}
		if title == "" {
			return items, changed, nil
		}
		minutes, err := a.askInt("  Minuten (Enter = unbekannt): ", 0, 24*60, nil)
		if err != nil {
			return nil, false, err
		}
		offline, err := a.askYesNo("  Offline (nicht am Computer)? [j/N]: ", false)
		if err != nil {
			return nil, false, err
		}
		it := journal.Item{Title: title, Minutes: minutes, Kind: journal.KindComputer, Source: journal.SourceManual}
		if offline {
			it.Kind = journal.KindOffline
		}
		items = append(items, it)
		changed = true
	}
}

// itemLine describes an item: "Writeup (90 min, offline)".
func itemLine(it journal.Item) string {
	var details []string
	if it.Minutes != nil {
		details = append(details, minutesText(*it.Minutes))
	}
	details = append(details, it.Kind)
	if it.ID == 0 {
		details = append(details, "neu, erledigte Aufgabe")
	}
	return fmt.Sprintf("%s (%s)", it.Title, strings.Join(details, ", "))
}

// showJournal prints a journal day.
func (a *app) showJournal(c *api.Client, day api.JournalResponse) {
	e := day.Entry
	fmt.Fprintln(a.out, dateLabel(e.Date))
	row := func(label, value string) {
		if value != "" {
			fmt.Fprintf(a.out, "  %-15s %s\n", label+":", value)
		}
	}
	row("Schlaf", join(rating(e.SleepQuality), hoursPtr(e.SleepHours)))
	row("Produktivität", join(rating(e.Productivity), hoursPtr(e.ProductiveHours)))
	var windows []string
	for _, w := range e.ProductiveWindows {
		windows = append(windows, strings.TrimSpace(w.Start+"–"+w.End+" "+w.Kind))
	}
	row("Zeitfenster", strings.Join(windows, ", "))
	if e.AWProductiveMinutes != nil {
		row("ActivityWatch", minutesText(*e.AWProductiveMinutes))
	}
	if e.Sport != nil {
		sport := "nein"
		if *e.Sport {
			sport = "ja"
			if e.SportType != nil {
				sport = join(sport, *e.SportType)
			}
			if e.SportMinutes != nil {
				sport = join(sport, minutesText(*e.SportMinutes))
			}
		}
		row("Sport", sport)
	}
	if e.Steps != nil {
		row("Schritte", strconv.Itoa(*e.Steps))
	}
	if e.Note != nil {
		row("Notiz", *e.Note)
	}

	ctx, cancel := request()
	items, err := c.Items(ctx, e.Date)
	cancel()
	if err == nil && len(items) > 0 {
		fmt.Fprintln(a.out, "  Geschafft:")
		for _, it := range items {
			fmt.Fprintf(a.out, "    - %s\n", itemLine(it))
		}
	}
	var missing []string
	for _, part := range day.Missing {
		missing = append(missing, partName(part))
	}
	row("Fehlt noch", strings.Join(missing, ", "))
}

// journalStatus reports whether today's journal is complete, for
// scripts: exit code 0 if so, 1 if a part is missing, 2 if the server
// could not be asked. With writeCache it also updates the file the zsh
// prompt reads.
func (a *app) journalStatus(jc journalCmd) error {
	c, err := a.server("t journal status")
	if err == nil {
		ctx, cancel := request()
		var day api.JournalResponse
		day, err = c.Journal(ctx, "today")
		cancel()
		if err == nil {
			if jc.writeCache {
				writeJournalCache(day.Missing)
			}
			if len(day.Missing) == 0 {
				if !jc.quiet {
					fmt.Fprintln(a.out, "Das Journal von heute ist vollständig.")
				}
				return nil
			}
			if !jc.quiet {
				var missing []string
				for _, part := range day.Missing {
					missing = append(missing, partName(part))
				}
				fmt.Fprintf(a.out, "Offen: %s\n", strings.Join(missing, ", "))
			}
			return exitCode{code: 1}
		}
	}
	return statusError(jc, err)
}

// statusError is t journal status's answer when it could not ask the
// server: exit code 2, silent with --quiet.
func statusError(jc journalCmd, err error) error {
	if jc.quiet {
		return exitCode{code: 2}
	}
	return exitCode{code: 2, err: err}
}

// refreshJournalCache updates the prompt's cache after the journal was
// changed. Errors only mean the prompt is updated a bit later.
func (a *app) refreshJournalCache(c *api.Client) {
	ctx, cancel := request()
	defer cancel()
	if day, err := c.Journal(ctx, "today"); err == nil {
		writeJournalCache(day.Missing)
	}
}

// journalCachePath is the file the zsh prompt reads: empty when today's
// journal is complete, otherwise the missing parts.
func journalCachePath() string {
	dir := os.Getenv("XDG_CACHE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".cache")
	}
	return filepath.Join(dir, "beacon", "journal-open")
}

func writeJournalCache(missing []string) {
	path := journalCachePath()
	if path == "" {
		return
	}
	content := ""
	if len(missing) > 0 {
		content = strings.Join(missing, " ") + "\n"
	}
	os.MkdirAll(filepath.Dir(path), 0o700)
	// Write a new file and rename it over the old one, so the prompt never
	// reads a half-written file.
	tmp := path + ".tmp"
	if os.WriteFile(tmp, []byte(content), 0o600) == nil {
		os.Rename(tmp, path)
	}
}

// Small formatting helpers.

var weekdays = [...]string{"So", "Mo", "Di", "Mi", "Do", "Fr", "Sa"}

// dateLabel turns 2026-10-13 into "Di 13.10.2026".
func dateLabel(date string) string {
	d, err := time.Parse(journal.DateLayout, date)
	if err != nil {
		return date
	}
	return weekdays[d.Weekday()] + " " + d.Format("02.01.2006")
}

func partName(part string) string {
	switch part {
	case journal.PartMorning:
		return "Morgen"
	case journal.PartEvening:
		return "Abend"
	}
	return part
}

func rating(v *int) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%d/10", *v)
}

func hoursPtr(v *float64) string {
	if v == nil {
		return ""
	}
	return hours(*v)
}

// join joins the non-empty parts with ", ".
func join(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, ", ")
}
