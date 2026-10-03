package config

import (
	"reflect"
	"strings"
	"testing"
)

// clearEnv empties all beacon variables so the user's real environment
// cannot affect a test. t.Setenv restores the old values when the test ends.
func clearEnv(t *testing.T) {
	for _, name := range []string{
		"BEACON_CALDAV_URL", "BEACON_CALDAV_USER", "BEACON_CALDAV_PASSWORD_CMD",
		"BEACON_LISTS", "BEACON_DEFAULT_LIST",
		"BEACON_SERVER_URL", "BEACON_TOKEN", "BEACON_LISTEN", "BEACON_DB",
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
		{"server without token", map[string]string{"BEACON_CALDAV_URL": url}, Config.CheckServer, "BEACON_TOKEN is not set"},
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

func TestPassword(t *testing.T) {
	c := Config{PasswordCmd: "echo not-a-real-secret"}
	pw, err := c.Password()
	if err != nil {
		t.Fatalf("Password: %v", err)
	}
	if pw != "not-a-real-secret" {
		t.Errorf("Password = %q, want trailing newline trimmed", pw)
	}
}

func TestPasswordCommandFails(t *testing.T) {
	c := Config{PasswordCmd: "false"}
	if _, err := c.Password(); err == nil {
		t.Error("Password succeeded, want an error")
	}
}
