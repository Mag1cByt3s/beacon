package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/api"
	"github.com/Mag1cByt3s/beacon/internal/caldav"
	"github.com/Mag1cByt3s/beacon/internal/caldav/caldavtest"
	"github.com/Mag1cByt3s/beacon/internal/journal"
	"github.com/Mag1cByt3s/beacon/internal/server"
	"github.com/Mag1cByt3s/beacon/internal/store"
)

const (
	laptopToken = "laptop-token-0123456789"
	syncToken   = "aw-sync-token-0123456"
)

// fakeAW is an ActivityWatch server that answers every query with one
// hour in konsole on 2026-10-13 from 09:00 Berlin time.
func fakeAW(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[[{"timestamp": "2026-10-13T07:00:00Z", "duration": 3600,
			"data": {"app": "org.kde.konsole", "title": "zsh"}}]]`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// setup starts a beacon server, points the environment at it and the
// fake ActivityWatch, and returns a client with the laptop's token.
func setup(t *testing.T, tokenForSync string) *api.Client {
	t.Helper()
	davSrv, _ := caldavtest.NewServer(t)
	dav, err := caldav.NewClient(davSrv.URL+"/", caldavtest.User, caldavtest.Password, []string{"Todo"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "beacon.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tokens := map[string]string{"laptop": laptopToken, server.AWSyncClient: syncToken}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(server.New(dav, st, tokens, "Todo", logger).Handler())
	t.Cleanup(srv.Close)

	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(tokenForSync+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEACON_SERVER_URL", srv.URL)
	t.Setenv("BEACON_TOKEN_FILE", tokenFile)
	t.Setenv("BEACON_TOKEN", "")
	t.Setenv("BEACON_AW_URL", fakeAW(t).URL)
	t.Setenv("BEACON_AW_CONFIG", "")
	t.Setenv("BEACON_JOURNAL_TZ", "")
	t.Setenv("BEACON_JOURNAL_DAY_START", "")

	c, err := api.NewClient(srv.URL, laptopToken)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// evening is 2026-10-13 22:00 in Berlin.
var evening = time.Date(2026, 10, 13, 20, 0, 0, 0, time.UTC)

func entry(t *testing.T, c *api.Client, date string) journal.Entry {
	t.Helper()
	day, err := c.Journal(context.Background(), date)
	if err != nil {
		t.Fatal(err)
	}
	return day.Entry
}

func TestSyncYesterdayAndToday(t *testing.T) {
	c := setup(t, syncToken)
	var out strings.Builder
	if err := run(nil, &out, evening); err != nil {
		t.Fatal(err)
	}
	want := "2026-10-12: 0 min productive\n2026-10-13: 60 min productive (09:00-10:00)\n"
	if out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}

	today := entry(t, c, "2026-10-13")
	if m := today.AWProductiveMinutes; m == nil || *m != 60 {
		t.Errorf("AW minutes = %v, want 60", m)
	}
	wantWindows := []journal.Window{{Start: "09:00", End: "10:00", Kind: "aw"}}
	if !reflect.DeepEqual(today.AWWindows, wantWindows) {
		t.Errorf("AW windows = %+v, want %+v", today.AWWindows, wantWindows)
	}
	// The user's own fields are left alone.
	if today.Productivity != nil || today.ProductiveHours != nil {
		t.Errorf("productivity was set: %+v", today)
	}
	if m := entry(t, c, "2026-10-12").AWProductiveMinutes; m == nil || *m != 0 {
		t.Errorf("yesterday's AW minutes = %v, want 0", m)
	}
}

func TestSyncOneDay(t *testing.T) {
	c := setup(t, syncToken)
	var out strings.Builder
	if err := run([]string{"-date", "2026-10-13"}, &out, evening); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "\n") != 1 {
		t.Errorf("output = %q, want one day", out.String())
	}
	if m := entry(t, c, "2026-10-12").AWProductiveMinutes; m != nil {
		t.Errorf("yesterday was synced too: %d", *m)
	}
}

func TestSyncWithAnotherClientsToken(t *testing.T) {
	setup(t, laptopToken)
	err := run([]string{"-date", "today"}, io.Discard, evening)
	if err == nil || !strings.Contains(err.Error(), "may not use this endpoint") {
		t.Errorf("err = %v, want the server's refusal", err)
	}
}

func TestDryRun(t *testing.T) {
	setup(t, syncToken)
	t.Setenv("BEACON_SERVER_URL", "") // not needed for a dry run
	var out strings.Builder
	if err := run([]string{"-dry-run", "-date", "2026-10-13"}, &out, evening); err != nil {
		t.Fatal(err)
	}
	if want := "2026-10-13: 60 min productive (09:00-10:00), not sent (dry run)\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

func TestCategoriesFile(t *testing.T) {
	c := setup(t, syncToken)
	path := filepath.Join(t.TempDir(), "categories.json")
	// konsole is a distraction here.
	os.WriteFile(path, []byte(`{"rules": [{"category": ["Ablenkung"], "app": "konsole"}], "productive": "Arbeit"}`), 0o600)
	t.Setenv("BEACON_AW_CONFIG", path)
	if err := run([]string{"-date", "2026-10-13"}, io.Discard, evening); err != nil {
		t.Fatal(err)
	}
	if m := entry(t, c, "2026-10-13").AWProductiveMinutes; m == nil || *m != 0 {
		t.Errorf("AW minutes = %v, want 0", m)
	}

	os.WriteFile(path, []byte(`{"rules": [{"category": ["Arbeit"], "app": "("}]}`), 0o600)
	if err := run(nil, io.Discard, evening); err == nil || !strings.Contains(err.Error(), "BEACON_AW_CONFIG") {
		t.Errorf("broken file: err = %v", err)
	}
}

func TestArguments(t *testing.T) {
	setup(t, syncToken)
	for _, args := range [][]string{{"today"}, {"-date", "gestern"}, {"-nope"}} {
		if err := run(args, io.Discard, evening); err == nil {
			t.Errorf("%q was accepted", args)
		}
	}
	t.Setenv("BEACON_SERVER_URL", "")
	if err := run(nil, io.Discard, evening); err == nil || !strings.Contains(err.Error(), "BEACON_SERVER_URL") {
		t.Errorf("without a server: err = %v", err)
	}
}
