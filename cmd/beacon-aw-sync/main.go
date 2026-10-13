// Command beacon-aw-sync sends the productive computer time measured by
// ActivityWatch to the beacon journal. It reads the window events of a
// journal day from the local ActivityWatch server, sorts them into
// categories (see internal/awsync) and stores the minutes and windows of
// the productive category with PUT /api/journal/{date}/aw.
//
// Without -date it syncs yesterday and today, so the end of a day (until
// 04:00) is sent even when the last run of the day was earlier.
//
//	beacon-aw-sync [-date today|2026-10-13] [-dry-run]
//
// Settings come from environment variables: BEACON_SERVER_URL and
// BEACON_TOKEN_FILE (the token of the client "aw-sync"), BEACON_AW_URL
// (default http://localhost:5600), BEACON_AW_CONFIG (a categories file;
// built-in categories without it), BEACON_JOURNAL_TZ and
// BEACON_JOURNAL_DAY_START.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/api"
	"github.com/Mag1cByt3s/beacon/internal/awsync"
	"github.com/Mag1cByt3s/beacon/internal/config"
	"github.com/Mag1cByt3s/beacon/internal/journal"
)

const (
	defaultAWURL = "http://localhost:5600"
	timeout      = 30 * time.Second // per day
)

func main() {
	if err := run(os.Args[1:], os.Stdout, time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, "beacon-aw-sync:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer, now time.Time) error {
	flags := flag.NewFlagSet("beacon-aw-sync", flag.ContinueOnError)
	date := flags.String("date", "", `journal day to sync, "today" or like 2026-10-13 (default: yesterday and today)`)
	dryRun := flags.Bool("dry-run", false, "print what would be sent instead of sending it")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q (see -help)", flags.Arg(0))
	}

	cfg := config.Load()
	clock, err := cfg.JournalClock()
	if err != nil {
		return err
	}
	categorizer, err := loadCategories(os.Getenv("BEACON_AW_CONFIG"))
	if err != nil {
		return err
	}
	awURL := os.Getenv("BEACON_AW_URL")
	if awURL == "" {
		awURL = defaultAWURL
	}

	var client *api.Client
	if !*dryRun {
		if cfg.ServerURL == "" {
			return errors.New("BEACON_SERVER_URL is not set")
		}
		if err := cfg.ReadTokenFile(false); err != nil {
			return err
		}
		if err := cfg.CheckAPI(); err != nil {
			return err
		}
		if client, err = api.NewClient(cfg.ServerURL, cfg.Token); err != nil {
			return err
		}
	}

	var days []string
	if *date != "" {
		day, err := clock.Resolve(*date, now)
		if err != nil {
			return err
		}
		days = []string{day}
	} else {
		today := clock.Day(now)
		days = []string{previousDay(today), today}
	}

	for _, day := range days {
		if err := syncDay(client, awURL, categorizer, clock, day, now, out); err != nil {
			return fmt.Errorf("%s: %w", day, err)
		}
	}
	return nil
}

// syncDay reads one journal day from ActivityWatch and sends the result,
// or prints it if client is nil (dry run).
func syncDay(client *api.Client, awURL string, c *awsync.Categorizer, clock journal.Clock, day string, now time.Time, out io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	from, to, err := clock.Bounds(day)
	if err != nil {
		return err
	}
	// The part of today that has not happened yet holds no events.
	if to.After(now) {
		to = now
	}
	events, err := awsync.Events(ctx, awURL, from, to)
	if err != nil {
		return err
	}
	minutes, windows := c.Summarize(events, from, to, clock.Loc)

	var spans []string
	for _, w := range windows {
		spans = append(spans, w.Start+"-"+w.End)
	}
	line := fmt.Sprintf("%s: %d min productive", day, minutes)
	if len(spans) > 0 {
		line += " (" + strings.Join(spans, ", ") + ")"
	}

	if client == nil {
		fmt.Fprintln(out, line+", not sent (dry run)")
		return nil
	}
	// A token of another client gets "client ... may not use this
	// endpoint": BEACON_TOKEN_FILE must hold the token of aw-sync.
	if _, err := client.PutAW(ctx, day, api.AWRequest{Minutes: minutes, Windows: windows}); err != nil {
		return err
	}
	fmt.Fprintln(out, line)
	return nil
}

// loadCategories reads the categories file at path, or returns the
// built-in categories if path is empty.
func loadCategories(path string) (*awsync.Categorizer, error) {
	cfg := awsync.DefaultConfig()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("cannot read BEACON_AW_CONFIG: %w", err)
		}
		if cfg, err = awsync.ParseConfig(data); err != nil {
			return nil, fmt.Errorf("BEACON_AW_CONFIG %s: %w", path, err)
		}
	}
	c, err := awsync.New(cfg)
	if err != nil && path != "" {
		return nil, fmt.Errorf("BEACON_AW_CONFIG %s: %w", path, err)
	}
	return c, err
}

// previousDay returns the date before a journal date.
func previousDay(date string) string {
	d, err := time.Parse(journal.DateLayout, date)
	if err != nil {
		return date
	}
	return d.AddDate(0, 0, -1).Format(journal.DateLayout)
}
