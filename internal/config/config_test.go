package config

import (
	"reflect"
	"testing"
)

// clearEnv empties all beacon variables so the user's real environment
// cannot affect a test. t.Setenv restores the old values when the test ends.
func clearEnv(t *testing.T) {
	for _, name := range []string{
		"BEACON_CALDAV_URL", "BEACON_CALDAV_USER", "BEACON_CALDAV_PASSWORD_CMD",
		"BEACON_LISTS", "BEACON_DEFAULT_LIST",
	} {
		t.Setenv(name, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("BEACON_CALDAV_URL", "https://dav.example.org/")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(c.Lists, []string{"Todo"}) {
		t.Errorf("Lists = %q, want [Todo]", c.Lists)
	}
	if c.DefaultList != "Todo" {
		t.Errorf("DefaultList = %q, want Todo", c.DefaultList)
	}
}

func TestLoadLists(t *testing.T) {
	clearEnv(t)
	t.Setenv("BEACON_CALDAV_URL", "https://dav.example.org/")
	t.Setenv("BEACON_LISTS", " Todo, Reminders ,,")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"Todo", "Reminders"}
	if !reflect.DeepEqual(c.Lists, want) {
		t.Errorf("Lists = %q, want %q", c.Lists, want)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name, url, user, cmd string
	}{
		{name: "missing url", url: ""},
		{name: "user without password command", url: "https://dav.example.org/", user: "someone"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("BEACON_CALDAV_URL", tt.url)
			t.Setenv("BEACON_CALDAV_USER", tt.user)
			t.Setenv("BEACON_CALDAV_PASSWORD_CMD", tt.cmd)
			if _, err := Load(); err == nil {
				t.Error("Load succeeded, want an error")
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
