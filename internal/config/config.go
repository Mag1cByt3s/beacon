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

// Config holds everything beacon needs to talk to Radicale.
type Config struct {
	CalDAVURL   string
	User        string
	PasswordCmd string
	Lists       []string // collection names used for focus
	DefaultList string   // collection for new captures
}

// Load reads the configuration from the environment.
func Load() (Config, error) {
	c := Config{
		CalDAVURL:   strings.TrimSpace(os.Getenv("BEACON_CALDAV_URL")),
		User:        strings.TrimSpace(os.Getenv("BEACON_CALDAV_USER")),
		PasswordCmd: strings.TrimSpace(os.Getenv("BEACON_CALDAV_PASSWORD_CMD")),
		Lists:       splitList(os.Getenv("BEACON_LISTS")),
		DefaultList: strings.TrimSpace(os.Getenv("BEACON_DEFAULT_LIST")),
	}

	if c.CalDAVURL == "" {
		return Config{}, errors.New("BEACON_CALDAV_URL is not set")
	}
	if c.User != "" && c.PasswordCmd == "" {
		return Config{}, errors.New("BEACON_CALDAV_PASSWORD_CMD is not set (needed because BEACON_CALDAV_USER is set)")
	}
	if len(c.Lists) == 0 {
		c.Lists = []string{"Todo"}
	}
	if c.DefaultList == "" {
		c.DefaultList = "Todo"
	}
	return c, nil
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
