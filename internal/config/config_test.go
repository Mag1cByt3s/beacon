package config

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// clearEnv empties all beacon variables so the user's real environment
// cannot affect a test. t.Setenv restores the old values when the test ends.
func clearEnv(t *testing.T) {
	for _, name := range []string{
		"BEACON_CALDAV_URL", "BEACON_CALDAV_USER", "BEACON_CALDAV_PASSWORD_CMD",
		"BEACON_LISTS", "BEACON_DEFAULT_LIST",
		"BEACON_SERVER_URL", "BEACON_TOKEN", "BEACON_TOKEN_FILE", "BEACON_LISTEN", "BEACON_DB",
	} {
		t.Setenv(name, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)

	c := Load()
	want := Config{
		Lists:       []string{"Todo"},
		DefaultList: "Todo",
		Listen:      "127.0.0.1:8080",
		DB:          "beacon.db",
	}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("Load() = %+v, want %+v", c, want)
	}
	if c.UseServer() {
		t.Error("UseServer = true without BEACON_SERVER_URL")
	}
}

func TestLoadLists(t *testing.T) {
	clearEnv(t)
	t.Setenv("BEACON_LISTS", " Todo, Reminders ,,")

	want := []string{"Todo", "Reminders"}
	if got := Load().Lists; !reflect.DeepEqual(got, want) {
		t.Errorf("Lists = %q, want %q", got, want)
	}
}

func TestChecks(t *testing.T) {
	const url = "https://dav.example.org/"
	const token = "0123456789abcdef0123"

	tests := []struct {
		name    string
		env     map[string]string
		check   func(Config) error
		wantErr string // "" means no error
	}{
		{"caldav ok", map[string]string{"BEACON_CALDAV_URL": url}, Config.CheckCalDAV, ""},
		{"caldav missing url", nil, Config.CheckCalDAV, "BEACON_CALDAV_URL"},
		{"caldav user without password command",
			map[string]string{"BEACON_CALDAV_URL": url, "BEACON_CALDAV_USER": "someone"},
			Config.CheckCalDAV, "BEACON_CALDAV_PASSWORD_CMD"},
		{"server ok", map[string]string{"BEACON_CALDAV_URL": url, "BEACON_TOKEN": token}, Config.CheckServer, ""},
		{"server without token", map[string]string{"BEACON_CALDAV_URL": url}, Config.CheckServer, "BEACON_TOKEN_FILE or BEACON_TOKEN is not set"},
		{"server with short token",
			map[string]string{"BEACON_CALDAV_URL": url, "BEACON_TOKEN": "short"},
			Config.CheckServer, "too short"},
		{"server needs caldav", map[string]string{"BEACON_TOKEN": token}, Config.CheckServer, "BEACON_CALDAV_URL"},
		{"api ok", map[string]string{"BEACON_SERVER_URL": "https://beacon.example.org", "BEACON_TOKEN": "x"}, Config.CheckAPI, ""},
		{"api without token", map[string]string{"BEACON_SERVER_URL": "https://beacon.example.org"}, Config.CheckAPI, "BEACON_TOKEN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			for name, value := range tt.env {
				t.Setenv(name, value)
			}
			err := tt.check(Load())
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("err = %v, want one mentioning %q", err, tt.wantErr)
			}
		})
	}
}

// writeToken writes a token file with the given mode in a temp dir.
func writeToken(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	// Chmod sets the mode exactly; WriteFile's mode is reduced by the umask.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadTokenFile(t *testing.T) {
	const fileToken = "token-from-the-file"

	tests := []struct {
		name      string
		content   string
		mode      os.FileMode
		private   bool
		wantToken string
		wantErr   string
	}{
		{"newline trimmed", fileToken + "\n", 0o600, true, fileToken, ""},
		{"read-only for owner", fileToken, 0o400, true, fileToken, ""},
		{"group readable, server", fileToken, 0o640, true, "", "can be accessed by other users"},
		{"world readable, server", fileToken, 0o604, true, "", "chmod 600"},
		{"group writable, server", fileToken, 0o620, true, "", "other users"},
		{"group readable, client", fileToken, 0o644, false, fileToken, ""},
		{"empty file", "\n", 0o600, true, "", "is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeToken(t, tt.content, tt.mode)
			c := Config{Token: "token-from-env", TokenFile: path}
			err := c.ReadTokenFile(tt.private)

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				if strings.Contains(err.Error(), fileToken) {
					t.Errorf("error message contains the token: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// The file takes precedence over BEACON_TOKEN.
			if c.Token != tt.wantToken {
				t.Errorf("Token = %q, want %q", c.Token, tt.wantToken)
			}
		})
	}
}

func TestReadTokenFileMissing(t *testing.T) {
	c := Config{TokenFile: filepath.Join(t.TempDir(), "missing")}
	if err := c.ReadTokenFile(false); err == nil || !strings.Contains(err.Error(), "BEACON_TOKEN_FILE") {
		t.Errorf("err = %v, want one naming BEACON_TOKEN_FILE", err)
	}
}

func TestReadTokenFileUnset(t *testing.T) {
	c := Config{Token: "token-from-env"}
	if err := c.ReadTokenFile(true); err != nil || c.Token != "token-from-env" {
		t.Errorf("Token = %q, err = %v; want BEACON_TOKEN kept", c.Token, err)
	}
}

func TestLoadTokenFile(t *testing.T) {
	clearEnv(t)
	path := writeToken(t, "token-from-the-file\n", 0o600)
	t.Setenv("BEACON_TOKEN_FILE", path)
	c := Load()
	if c.TokenFile != path {
		t.Errorf("TokenFile = %q, want %q", c.TokenFile, path)
	}
}

func TestPassword(t *testing.T) {
	c := Config{PasswordCmd: "echo not-a-real-secret"}
	pw, err := c.Password(context.Background(), nil)
	if err != nil {
		t.Fatalf("Password: %v", err)
	}
	if pw != "not-a-real-secret" {
		t.Errorf("Password = %q, want trailing newline trimmed", pw)
	}
}

func TestPasswordCommandFails(t *testing.T) {
	c := Config{PasswordCmd: "false"}
	if _, err := c.Password(context.Background(), nil); err == nil {
		t.Error("Password succeeded, want an error")
	}
}

func TestPasswordTimeout(t *testing.T) {
	c := Config{PasswordCmd: "sleep 10"}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := c.Password(ctx, nil); err == nil {
		t.Error("Password succeeded, want an error")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Password took %v; the command was not stopped", d)
	}
}
