package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRefusesTokenFileReadableByOthers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("token-from-the-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEACON_CALDAV_URL", "http://127.0.0.1:1/")
	t.Setenv("BEACON_CALDAV_USER", "")
	t.Setenv("BEACON_TOKEN", "")
	t.Setenv("BEACON_TOKEN_FILE", path)
	t.Setenv("BEACON_DB", filepath.Join(t.TempDir(), "beacon.db"))

	err := run(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("run: err = %v, want a refusal to start", err)
	}
}
