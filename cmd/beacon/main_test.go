package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// serverEnv sets a minimal server environment for run, without starting
// anything that needs the network.
func serverEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"BEACON_CALDAV_USER", "BEACON_TOKEN", "BEACON_TOKEN_FILE", "BEACON_TOKENS_DIR",
		"BEACON_TRUSTED_PROXIES", "BEACON_JOURNAL_TZ", "BEACON_JOURNAL_DAY_START",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("BEACON_CALDAV_URL", "http://127.0.0.1:1/")
	t.Setenv("BEACON_DB", filepath.Join(t.TempDir(), "beacon.db"))
}

func runServer() error {
	return run(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestRefusesOldTokenFile: a server that still has the single token from
// before is not started; the message says how to move it.
func TestRefusesOldTokenFile(t *testing.T) {
	serverEnv(t)
	t.Setenv("BEACON_TOKENS_DIR", t.TempDir())
	t.Setenv("BEACON_TOKEN_FILE", "/usr/local/etc/beacon/token")

	err := runServer()
	if err == nil || !strings.Contains(err.Error(), "BEACON_TOKENS_DIR named after the client") {
		t.Errorf("run: err = %v, want a refusal with migration hint", err)
	}
}

func TestRefusesTokensDirReadableByOthers(t *testing.T) {
	serverEnv(t)
	dir := filepath.Join(t.TempDir(), "tokens")
	os.Mkdir(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "laptop"), []byte("laptop-token-0123456789\n"), 0o600)
	os.Chmod(dir, 0o755)
	t.Setenv("BEACON_TOKENS_DIR", dir)

	err := runServer()
	if err == nil || !strings.Contains(err.Error(), "chmod 700") {
		t.Errorf("run: err = %v, want a refusal to start", err)
	}
}
