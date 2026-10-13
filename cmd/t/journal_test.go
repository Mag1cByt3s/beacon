package main

import (
	"bufio"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
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

func TestParseJournalArgs(t *testing.T) {
	tests := []struct {
		args    []string
		want    journalCmd
		wantErr bool
	}{
		{nil, journalCmd{mode: "auto"}, false},
		{[]string{"--morning"}, journalCmd{mode: "morning"}, false},
		{[]string{"--evening"}, journalCmd{mode: "evening"}, false},
		{[]string{"show"}, journalCmd{mode: "show", date: "today"}, false},
		{[]string{"show", "2026-10-12"}, journalCmd{mode: "show", date: "2026-10-12"}, false},
		{[]string{"status"}, journalCmd{mode: "status"}, false},
		{[]string{"status", "--quiet", "--write-cache"}, journalCmd{mode: "status", quiet: true, writeCache: true}, false},
		{[]string{"status", "--loud"}, journalCmd{}, true},
		{[]string{"show", "a", "b"}, journalCmd{}, true},
		{[]string{"--morning", "--evening"}, journalCmd{}, true},
		{[]string{"yesterday"}, journalCmd{}, true},
	}
	for _, tt := range tests {
		got, err := parseJournalArgs(tt.args)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("parseJournalArgs(%q) = %+v, %v; want %+v, error %v", tt.args, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestParseTimerArgs(t *testing.T) {
	tests := []struct {
		args    []string
		want    timerCmd
		wantErr bool
	}{
		{[]string{"start"}, timerCmd{action: "start", kind: "computer"}, false},
		{[]string{"start", "HTB", " box"}, timerCmd{action: "start", text: "HTB box", kind: "computer"}, false},
		{[]string{"start", "--offline", "Lesen"}, timerCmd{action: "start", text: "Lesen", kind: "offline"}, false},
		{[]string{"stop", "Writeup fertig"}, timerCmd{action: "stop", text: "Writeup fertig", kind: "computer"}, false},
		{[]string{"toggle"}, timerCmd{action: "toggle", kind: "computer"}, false},
		{[]string{"status"}, timerCmd{action: "status", kind: "computer"}, false},
		{[]string{"status", "x"}, timerCmd{}, true},
		{[]string{"stop", "--offline"}, timerCmd{}, true},
		{[]string{"pause"}, timerCmd{}, true},
		{nil, timerCmd{}, true},
	}
	for _, tt := range tests {
		got, err := parseTimerArgs(tt.args)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("parseTimerArgs(%q) = %+v, %v; want %+v, error %v", tt.args, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestParseDidArgs(t *testing.T) {
	n := func(i int) *int { return &i }
	tests := []struct {
		args    []string
		want    didCmd
		wantErr bool
	}{
		{[]string{"HTB box"}, didCmd{title: "HTB box", kind: "computer"}, false},
		{[]string{"HTB box", "90"}, didCmd{title: "HTB box", minutes: n(90), kind: "computer"}, false},
		{[]string{"Laufen", "45min", "--offline"}, didCmd{title: "Laufen", minutes: n(45), kind: "offline"}, false},
		{[]string{"--offline", "Lesen 30m"}, didCmd{title: "Lesen", minutes: n(30), kind: "offline"}, false},
		// A lone number is the title, not the minutes.
		{[]string{"42"}, didCmd{title: "42", kind: "computer"}, false},
		{[]string{"x", "2000"}, didCmd{}, true},
		{[]string{"--offline"}, didCmd{}, true},
		{nil, didCmd{}, true},
	}
	for _, tt := range tests {
		got, err := parseDidArgs(tt.args)
		if (err != nil) != tt.wantErr || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("parseDidArgs(%q) = %+v, %v; want %+v, error %v", tt.args, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestParseArgsJournalCommands(t *testing.T) {
	cmd, err := parseArgs([]string{"journal", "status", "--quiet"})
	if err != nil || cmd.name != "journal" || !cmd.journal.quiet {
		t.Errorf("journal status --quiet = %+v, %v", cmd, err)
	}
	cmd, err = parseArgs([]string{"did", "Writeup", "30"})
	if err != nil || cmd.name != "did" || cmd.did.title != "Writeup" {
		t.Errorf("did = %+v, %v", cmd, err)
	}
	if _, err := parseArgs([]string{"timer"}); err == nil {
		t.Error("t timer without an action was accepted")
	}
	// A typo is not captured as a task.
	if _, err := parseArgs([]string{"jornal"}); err == nil || !strings.Contains(err.Error(), "t journal") {
		t.Errorf("t jornal: err = %v, want a hint to t journal", err)
	}
}

func TestParseWindows(t *testing.T) {
	got, err := parseWindows("9-12, 14:00-15:30 offline; 20-21 c")
	want := []journal.Window{
		{Start: "09:00", End: "12:00"},
		{Start: "14:00", End: "15:30", Kind: "offline"},
		{Start: "20:00", End: "21:00", Kind: "computer"},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("parseWindows = %+v, %v; want %+v", got, err, want)
	}
	if got, err := parseWindows(""); err != nil || len(got) != 0 {
		t.Errorf("parseWindows(\"\") = %+v, %v; want none", got, err)
	}
	for _, bad := range []string{"9", "9-25", "9-12 abends", "9-12 offline extra", "x-y"} {
		if _, err := parseWindows(bad); err == nil {
			t.Errorf("parseWindows(%q) was accepted", bad)
		}
	}
}

// journalEnv is a beacon server on a fake CalDAV server, and an app that
// talks to it.
type journalEnv struct {
	t   *testing.T
	c   *api.Client
	app *app
	out *strings.Builder
}

func newJournalEnv(t *testing.T) *journalEnv {
	t.Helper()
	// The cache file goes to a temporary directory, not the user's.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

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
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(server.New(dav, st, map[string]string{"laptop": "test-token-0123456789"}, "Todo", logger).Handler())
	t.Cleanup(srv.Close)

	c, err := api.NewClient(srv.URL, "test-token-0123456789")
	if err != nil {
		t.Fatal(err)
	}
	out := &strings.Builder{}
	return &journalEnv{t: t, c: c, out: out, app: &app{b: c, out: out}}
}

// input sets the answers the next command reads, one per line.
func (e *journalEnv) input(lines ...string) {
	text := ""
	for _, l := range lines {
		text += l + "\n"
	}
	e.app.in = bufio.NewReader(strings.NewReader(text))
}

// today returns today's journal day from the server.
func (e *journalEnv) today() api.JournalResponse {
	e.t.Helper()
	ctx, cancel := request()
	defer cancel()
	day, err := e.c.Journal(ctx, "today")
	if err != nil {
		e.t.Fatal(err)
	}
	return day
}

func (e *journalEnv) items() []journal.Item {
	e.t.Helper()
	ctx, cancel := request()
	defer cancel()
	items, err := e.c.Items(ctx, "today")
	if err != nil {
		e.t.Fatal(err)
	}
	return items
}

func readCache(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(journalCachePath())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestJournalMorning(t *testing.T) {
	e := newJournalEnv(t)
	e.input("0", "7", "7,5")
	if err := e.app.runJournal(journalCmd{mode: "morning"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), "Bitte eine Zahl von 1 bis 10") {
		t.Errorf("0 was not refused:\n%s", e.out)
	}
	day := e.today()
	if q := day.Entry.SleepQuality; q == nil || *q != 7 {
		t.Errorf("sleep quality = %v, want 7", q)
	}
	if h := day.Entry.SleepHours; h == nil || *h != 7.5 {
		t.Errorf("sleep hours = %v, want 7.5", h)
	}
	if !reflect.DeepEqual(day.Missing, []string{"evening"}) {
		t.Errorf("missing = %v, want [evening]", day.Missing)
	}
	if got := readCache(t); got != "evening\n" {
		t.Errorf("cache = %q, want the evening as missing", got)
	}
}

func TestJournalEvening(t *testing.T) {
	e := newJournalEnv(t)
	for _, dc := range []didCmd{
		{title: "Writeup", minutes: ptr(30), kind: "offline"},
		{title: "Notes", minutes: ptr(60), kind: "computer"},
	} {
		if err := e.app.did(dc); err != nil {
			t.Fatal(err)
		}
	}
	before := e.items()

	e.input(
		"",                       // productive hours: take the suggestion
		"9-10:30 offline, 14-15", // windows (nothing suggested)
		"1", "",                  // remove "Writeup", then go on
		"Sport-Notizen", "15", "j", // something forgotten, offline
		"",       // nothing else
		"8",      // productivity
		"j",      // sport
		"Laufen", // what
		"30",     // minutes
	)
	if err := e.app.runJournal(journalCmd{mode: "evening"}); err != nil {
		t.Fatalf("%v\n%s", err, e.out)
	}
	if !strings.Contains(e.out.String(), "1,5 h") {
		t.Errorf("the suggestion of 1,5 h is not shown:\n%s", e.out)
	}

	entry := e.today().Entry
	if p := entry.Productivity; p == nil || *p != 8 {
		t.Errorf("productivity = %v, want 8", p)
	}
	if h := entry.ProductiveHours; h == nil || *h != 1.5 {
		t.Errorf("productive hours = %v, want 1.5", h)
	}
	wantWindows := []journal.Window{{Start: "09:00", End: "10:30", Kind: "offline"}, {Start: "14:00", End: "15:00"}}
	if !reflect.DeepEqual(entry.ProductiveWindows, wantWindows) {
		t.Errorf("windows = %+v, want %+v", entry.ProductiveWindows, wantWindows)
	}
	if entry.Sport == nil || !*entry.Sport || entry.SportType == nil || *entry.SportType != "Laufen" ||
		entry.SportMinutes == nil || *entry.SportMinutes != 30 {
		t.Errorf("sport = %v %v %v, want yes, Laufen, 30", entry.Sport, entry.SportType, entry.SportMinutes)
	}

	items := e.items()
	if len(items) != 2 || items[0].Title != "Notes" || items[1].Title != "Sport-Notizen" {
		t.Fatalf("items = %+v, want Notes and Sport-Notizen", items)
	}
	if items[1].Kind != "offline" || items[1].Minutes == nil || *items[1].Minutes != 15 {
		t.Errorf("new item = %+v, want 15 min offline", items[1])
	}
	if !items[0].CreatedAt.Equal(before[1].CreatedAt) {
		t.Errorf("Notes was recorded at %v, now %v; want it kept", before[1].CreatedAt, items[0].CreatedAt)
	}
}

func TestJournalEveningStoppedSavesNothing(t *testing.T) {
	e := newJournalEnv(t)
	if err := e.app.did(didCmd{title: "Notes", kind: "computer"}); err != nil {
		t.Fatal(err)
	}
	// The input ends (Ctrl-D) at the sport question, after an item was
	// removed.
	e.input("2", "", "1", "", "", "8")
	err := e.app.runJournal(journalCmd{mode: "evening"})
	if !errors.Is(err, errStopped) {
		t.Fatalf("err = %v, want errStopped\n%s", err, e.out)
	}
	if p := e.today().Entry.Productivity; p != nil {
		t.Errorf("productivity = %d, want nothing saved", *p)
	}
	if items := e.items(); len(items) != 1 {
		t.Errorf("items = %+v, want the item kept", items)
	}
}

func TestJournalAuto(t *testing.T) {
	e := newJournalEnv(t)
	e.app.clock = func() time.Time { return time.Date(2026, 10, 13, 10, 0, 0, 0, time.Local) }

	// In the morning only the morning part is asked.
	e.input("6", "")
	if err := e.app.runJournal(journalCmd{mode: "auto"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), "ab 16:00") {
		t.Errorf("no word about the evening:\n%s", e.out)
	}
	if day := e.today(); !reflect.DeepEqual(day.Missing, []string{"evening"}) {
		t.Errorf("missing = %v, want [evening]", day.Missing)
	}

	// In the evening the evening part follows.
	e.app.clock = func() time.Time { return time.Date(2026, 10, 13, 22, 0, 0, 0, time.Local) }
	e.input("", "", "", "5", "")
	if err := e.app.runJournal(journalCmd{mode: "auto"}); err != nil {
		t.Fatalf("%v\n%s", err, e.out)
	}
	if day := e.today(); len(day.Missing) != 0 {
		t.Errorf("missing = %v, want nothing", day.Missing)
	}

	e.out.Reset()
	if err := e.app.runJournal(journalCmd{mode: "auto"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), "alles eingetragen") {
		t.Errorf("complete day: %q", e.out)
	}
}

func TestJournalStatus(t *testing.T) {
	e := newJournalEnv(t)
	err := e.app.runJournal(journalCmd{mode: "status", writeCache: true})
	if code := exitCodeOf(err); code != 1 {
		t.Errorf("exit code = %d (%v), want 1", code, err)
	}
	if !strings.Contains(e.out.String(), "Offen: Morgen, Abend") {
		t.Errorf("output = %q", e.out)
	}
	if got := readCache(t); got != "morning evening\n" {
		t.Errorf("cache = %q", got)
	}

	ctx, cancel := request()
	defer cancel()
	if _, err := e.c.PatchJournal(ctx, "today", map[string]any{"sleep_quality": 5, "productivity": 5}); err != nil {
		t.Fatal(err)
	}
	e.out.Reset()
	err = e.app.runJournal(journalCmd{mode: "status", quiet: true, writeCache: true})
	if err != nil || e.out.Len() != 0 {
		t.Errorf("complete, quiet: err = %v, output %q; want nil and nothing", err, e.out)
	}
	if got := readCache(t); got != "" {
		t.Errorf("cache = %q, want empty", got)
	}
}

func TestJournalStatusUnreachable(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	writeJournalCache([]string{"evening"})
	a := &app{b: unreachableClient(t), out: io.Discard}

	err := a.runJournal(journalCmd{mode: "status", writeCache: true})
	var ec exitCode
	if !errors.As(err, &ec) || ec.code != 2 || ec.err == nil {
		t.Errorf("err = %#v, want exit code 2 with a message", err)
	}
	err = a.runJournal(journalCmd{mode: "status", quiet: true})
	if !errors.As(err, &ec) || ec.code != 2 || ec.err != nil {
		t.Errorf("quiet: err = %#v, want exit code 2 without a message", err)
	}
	// The prompt keeps showing what it knew.
	if got := readCache(t); got != "evening\n" {
		t.Errorf("cache = %q, want it unchanged", got)
	}
}

func TestJournalNeedsServer(t *testing.T) {
	t.Setenv("BEACON_SERVER_URL", "")
	for _, args := range [][]string{{"journal"}, {"timer", "start"}, {"did", "x"}} {
		err := run(args, strings.NewReader(""), io.Discard)
		if err == nil || !strings.Contains(err.Error(), "needs the beacon server") {
			t.Errorf("t %s: err = %v", strings.Join(args, " "), err)
		}
	}
	err := run([]string{"journal", "status", "--quiet"}, strings.NewReader(""), io.Discard)
	var ec exitCode
	if !errors.As(err, &ec) || ec.code != 2 || ec.err != nil {
		t.Errorf("status --quiet: err = %#v, want a silent exit code 2", err)
	}
}

func TestJournalShow(t *testing.T) {
	e := newJournalEnv(t)
	ctx, cancel := request()
	defer cancel()
	if _, err := e.c.PatchJournal(ctx, "today", map[string]any{"sleep_quality": 7, "sleep_hours": 6.5, "sport": false}); err != nil {
		t.Fatal(err)
	}
	if err := e.app.did(didCmd{title: "HTB box", minutes: ptr(90), kind: "computer"}); err != nil {
		t.Fatal(err)
	}
	if err := e.app.runJournal(journalCmd{mode: "show", date: "today"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Schlaf:", "7/10, 6,5 h", "Sport:", "nein", "HTB box (1 h 30 min, computer)", "Fehlt noch:", "Abend"} {
		if !strings.Contains(e.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, e.out)
		}
	}
	if err := e.app.runJournal(journalCmd{mode: "show", date: "gestern"}); err == nil {
		t.Error("an unreadable date was accepted")
	}
}

func TestTimer(t *testing.T) {
	e := newJournalEnv(t)
	e.app.interactive = true

	if err := e.app.runTimer(timerCmd{action: "start", text: "HTB box", kind: "computer"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), "Timer läuft: HTB box, seit") {
		t.Errorf("start: %q", e.out)
	}
	if err := e.app.runTimer(timerCmd{action: "start", kind: "computer"}); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("second start: err = %v", err)
	}
	if err := e.app.runTimer(timerCmd{action: "status"}); err != nil {
		t.Errorf("status while running: err = %v, want exit code 0", err)
	}

	// Enter takes the label as what was done.
	e.input("")
	if err := e.app.runTimer(timerCmd{action: "stop"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), "Was geschafft? [HTB box]") || !strings.Contains(e.out.String(), "notiert: HTB box") {
		t.Errorf("stop:\n%s", e.out)
	}
	if code := exitCodeOf(e.app.runTimer(timerCmd{action: "status"})); code != 1 {
		t.Errorf("status when stopped: exit code %d, want 1", code)
	}
	if err := e.app.runTimer(timerCmd{action: "stop"}); err == nil {
		t.Error("stop without a running timer succeeded")
	}

	// Without a label, toggle starts a session for the current task; "-"
	// records nothing.
	if err := e.app.runTimer(timerCmd{action: "toggle", kind: "computer"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), "Timer läuft: Open task") {
		t.Errorf("toggle start: %q", e.out)
	}
	e.input("-")
	if err := e.app.runTimer(timerCmd{action: "toggle"}); err != nil {
		t.Fatal(err)
	}

	// A text given on the command line is not asked for.
	if err := e.app.runTimer(timerCmd{action: "start", text: "Lesen", kind: "offline"}); err != nil {
		t.Fatal(err)
	}
	e.input()
	if err := e.app.runTimer(timerCmd{action: "stop", text: "Kapitel 3"}); err != nil {
		t.Fatal(err)
	}

	var titles []string
	for _, it := range e.items() {
		titles = append(titles, it.Title)
	}
	if want := []string{"HTB box", "Kapitel 3"}; !reflect.DeepEqual(titles, want) {
		t.Errorf("items = %q, want %q", titles, want)
	}
}

func TestDid(t *testing.T) {
	e := newJournalEnv(t)
	if err := e.app.did(didCmd{title: "Writeup", minutes: ptr(45), kind: "offline"}); err != nil {
		t.Fatal(err)
	}
	if e.out.Len() != 0 {
		t.Errorf("t did printed %q, want nothing", e.out)
	}
	items := e.items()
	if len(items) != 1 || items[0].Title != "Writeup" || items[0].Kind != "offline" ||
		items[0].Minutes == nil || *items[0].Minutes != 45 || items[0].Source != "manual" {
		t.Errorf("items = %+v", items)
	}
}

// TestJournalHookRuns runs the zsh-journal hook in a real zsh.
func TestJournalHookRuns(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is not installed")
	}
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	script := hooks["zsh-journal"] + "\n_beacon_journal_precmd; print -r -- \"[$BEACON_JOURNAL_OPEN]\"\n"
	check := func(want string) {
		t.Helper()
		cmd := exec.Command(zsh, "-f", "-c", script)
		cmd.Env = append(os.Environ(), "XDG_CACHE_HOME="+cache, "BEACON_JOURNAL_OPEN=stale")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if got := strings.TrimSpace(string(out)); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	check("[]") // no cache file yet
	writeJournalCache([]string{"morning"})
	check("[journal offen]")
	writeJournalCache(nil)
	check("[]")
}

func ptr(i int) *int { return &i }

// exitCodeOf returns the exit code t would end with for err.
func exitCodeOf(err error) int {
	var ec exitCode
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ec):
		return ec.code
	}
	return 1
}

// unreachableClient is an API client for a server that is gone.
func unreachableClient(t *testing.T) *api.Client {
	t.Helper()
	srv := httptest.NewServer(nil)
	url := srv.URL
	srv.Close()
	c, err := api.NewClient(url, "test-token-0123456789")
	if err != nil {
		t.Fatal(err)
	}
	return c
}
