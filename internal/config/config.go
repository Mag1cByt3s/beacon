// Package config reads beacon's settings from environment variables.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// MinTokenLength is the shortest BEACON_TOKEN the server accepts.
const MinTokenLength = 16

// Config holds all of beacon's settings. Which ones are required depends
// on the program; see CheckCalDAV, CheckServer and CheckAPI.
type Config struct {
	// Talking to Radicale.
	CalDAVURL   string
	User        string
	PasswordCmd string
	Lists       []string // collection names used for focus
	DefaultList string   // collection for new captures

	// The beacon server.
	ServerURL string // where t finds the server; empty means talk to CalDAV directly
	Token     string // API bearer token
	Listen    string // address the server listens on
	DB        string // SQLite file for focus state
}

// Load reads the configuration from the environment and fills in defaults.
// It does not check that required settings are present.
func Load() Config {
	c := Config{
		CalDAVURL:   env("BEACON_CALDAV_URL"),
		User:        env("BEACON_CALDAV_USER"),
		PasswordCmd: env("BEACON_CALDAV_PASSWORD_CMD"),
		Lists:       splitList(os.Getenv("BEACON_LISTS")),
		DefaultList: env("BEACON_DEFAULT_LIST"),
		ServerURL:   env("BEACON_SERVER_URL"),
		Token:       env("BEACON_TOKEN"),
		Listen:      env("BEACON_LISTEN"),
		DB:          env("BEACON_DB"),
	}
	if len(c.Lists) == 0 {
		c.Lists = []string{"Todo"}
	}
	if c.DefaultList == "" {
		c.DefaultList = "Todo"
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	if c.DB == "" {
		c.DB = "beacon.db"
	}
	return c
}

// UseServer reports whether t should talk to the beacon server instead of
// CalDAV.
func (c Config) UseServer() bool {
	return c.ServerURL != ""
}

// CheckCalDAV checks the settings needed to talk to Radicale.
func (c Config) CheckCalDAV() error {
	if c.CalDAVURL == "" {
		return errors.New("BEACON_CALDAV_URL is not set")
	}
	if c.User != "" && c.PasswordCmd == "" {
		return errors.New("BEACON_CALDAV_PASSWORD_CMD is not set (needed because BEACON_CALDAV_USER is set)")
	}
	return nil
}

// CheckServer checks the settings the beacon server needs.
func (c Config) CheckServer() error {
	if err := c.CheckCalDAV(); err != nil {
		return err
	}
	if c.Token == "" {
		return errors.New("BEACON_TOKEN is not set; the server will not start without it (create one with: openssl rand -hex 32)")
	}
	if len(c.Token) < MinTokenLength {
		return fmt.Errorf("BEACON_TOKEN is too short; use at least %d characters (create one with: openssl rand -hex 32)", MinTokenLength)
	}
	return nil
}

// CheckAPI checks the settings t needs to talk to the beacon server.
func (c Config) CheckAPI() error {
	if c.Token == "" {
		return errors.New("BEACON_TOKEN is not set (needed because BEACON_SERVER_URL is set)")
	}
	return nil
}

// Password runs PasswordCmd and returns its output without the trailing
// newline. The command is split on whitespace and run directly, not through
// a shell, so quotes and pipes are not supported.
func (c Config) Password() (string, error) {
	argv := strings.Fields(c.PasswordCmd)
	if len(argv) == 0 {
		return "", errors.New("BEACON_CALDAV_PASSWORD_CMD is not set")
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	// Let tools like `pass` ask for a GPG passphrase on the terminal.
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		// Never include the output here: it might contain the password.
		return "", fmt.Errorf("password command %q failed: %v", argv[0], err)
	}

	pw := string(bytes.TrimRight(out, "\r\n"))
	if pw == "" {
		return "", fmt.Errorf("password command %q printed nothing", argv[0])
	}
	return pw, nil
}

func env(name string) string {
	return strings.TrimSpace(os.Getenv(name))
}

// splitList turns "Todo, Reminders" into ["Todo", "Reminders"].
func splitList(s string) []string {
	var lists []string
	for _, part := range strings.Split(s, ",") {
		if name := strings.TrimSpace(part); name != "" {
			lists = append(lists, name)
		}
	}
	return lists
}
