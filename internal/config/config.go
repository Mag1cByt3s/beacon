// Package config reads beacon's settings from environment variables.
package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Mag1cByt3s/beacon/internal/journal"
)

// MinTokenLength is the shortest token the server accepts.
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
	TokenFile string // file holding the token; see ReadTokenFile
	Listen    string // address the server listens on
	DB        string // SQLite file for focus state

	// Addresses or CIDR ranges of reverse proxies whose X-Forwarded-For
	// header the server believes; see ProxyPrefixes.
	TrustedProxies []string

	// TokensDir holds one token file per client; see ReadClientTokens.
	TokensDir string

	// When a journal day starts: JournalDayStart ("04:00") in the time
	// zone JournalZone ("Europe/Berlin"); see JournalClock.
	JournalZone     string
	JournalDayStart string
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
		TokenFile:   env("BEACON_TOKEN_FILE"),
		Listen:      env("BEACON_LISTEN"),
		DB:          env("BEACON_DB"),

		TrustedProxies: splitList(os.Getenv("BEACON_TRUSTED_PROXIES")),
		TokensDir:      env("BEACON_TOKENS_DIR"),

		JournalZone:     env("BEACON_JOURNAL_TZ"),
		JournalDayStart: env("BEACON_JOURNAL_DAY_START"),
	}
	if len(c.Lists) == 0 {
		c.Lists = []string{"Todo"}
	}
	if c.DefaultList == "" {
		c.DefaultList = "Todo"
	}
	if c.JournalZone == "" {
		c.JournalZone = "Europe/Berlin"
	}
	if c.JournalDayStart == "" {
		c.JournalDayStart = "04:00"
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

// CheckServer checks the settings the beacon server needs. The tokens
// themselves are read by ReadClientTokens.
func (c Config) CheckServer() error {
	if err := c.CheckCalDAV(); err != nil {
		return err
	}
	// The server has one token per client now; a single token is a
	// leftover from before and would silently be ignored.
	if c.TokenFile != "" || c.Token != "" {
		return errors.New("the server no longer uses BEACON_TOKEN_FILE or BEACON_TOKEN: move the token to a file in BEACON_TOKENS_DIR named after the client (for example tokens/laptop) and unset them; see docs/deploy.md")
	}
	if c.TokensDir == "" {
		return errors.New("BEACON_TOKENS_DIR is not set; it is a directory with one token file per client (laptop, iphone, aw-sync)")
	}
	if _, err := c.ProxyPrefixes(); err != nil {
		return err
	}
	if _, err := c.JournalClock(); err != nil {
		return err
	}
	return nil
}

// JournalClock returns when journal days start, from JournalZone and
// JournalDayStart.
func (c Config) JournalClock() (journal.Clock, error) {
	clock, err := journal.NewClock(c.JournalZone, c.JournalDayStart)
	if err != nil {
		return journal.Clock{}, fmt.Errorf("BEACON_JOURNAL_TZ / BEACON_JOURNAL_DAY_START: %w", err)
	}
	return clock, nil
}

// ReadClientTokens reads the clients' tokens from dir: every file in it is
// one client, named like the file ("laptop", "iphone", "aw-sync"), and
// holds that client's token. The result maps client names to tokens.
//
// Other users must not be able to open the directory or the files, every
// token needs MinTokenLength characters, and no two clients may share a
// token (the server could not tell them apart).
func ReadClientTokens(dir string) (map[string]string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("cannot read BEACON_TOKENS_DIR: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("BEACON_TOKENS_DIR %s is not a directory", dir)
	}
	// Perm()&0o077 keeps only the group and other bits.
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("BEACON_TOKENS_DIR %s can be accessed by other users (mode %04o); run: chmod 700 %s",
			dir, info.Mode().Perm(), dir)
	}

	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("cannot read BEACON_TOKENS_DIR: %w", err)
	}
	tokens := map[string]string{}
	owner := map[string]string{} // token -> client, to find duplicates
	for _, f := range files {
		name := f.Name()
		if strings.HasPrefix(name, ".") {
			continue // editor backups and the like
		}
		if !validClientName(name) {
			return nil, fmt.Errorf("token file %q: use a client name of lower-case letters, digits and - (like aw-sync)", name)
		}
		path := filepath.Join(dir, name)
		fi, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("cannot read token file %s: %w", path, err)
		}
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("token file %s is not a regular file", path)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("token file %s can be accessed by other users (mode %04o); run: chmod 600 %s",
				path, fi.Mode().Perm(), path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("cannot read token file %s: %w", path, err)
		}
		token := strings.TrimSpace(string(data))
		if len(token) < MinTokenLength {
			return nil, fmt.Errorf("token for client %q is too short; use at least %d characters (create one with: openssl rand -hex 32)", name, MinTokenLength)
		}
		if other, dup := owner[token]; dup {
			return nil, fmt.Errorf("clients %q and %q have the same token; give each its own", other, name)
		}
		owner[token] = name
		tokens[name] = token
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("BEACON_TOKENS_DIR %s holds no tokens; add a file per client, e.g. laptop", dir)
	}
	return tokens, nil
}

func validClientName(name string) bool {
	for i, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || (r == '-' && i > 0)
		if !ok {
			return false
		}
	}
	return name != ""
}

// ProxyPrefixes parses TrustedProxies into address ranges. A single
// address ("10.0.0.4") becomes a range of just that address.
func (c Config) ProxyPrefixes() ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, s := range c.TrustedProxies {
		if strings.Contains(s, "/") {
			p, err := netip.ParsePrefix(s)
			if err != nil {
				return nil, badProxy(s)
			}
			// Masked turns 10.0.0.7/24 into 10.0.0.0/24.
			prefixes = append(prefixes, p.Masked())
			continue
		}
		addr, err := netip.ParseAddr(s)
		if err != nil {
			return nil, badProxy(s)
		}
		addr = addr.Unmap() // ::ffff:10.0.0.4 is the IPv4 address 10.0.0.4
		prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return prefixes, nil
}

func badProxy(s string) error {
	return fmt.Errorf("BEACON_TRUSTED_PROXIES: %q is not an IP address or CIDR range (example: 10.0.0.4 or 10.0.0.0/24)", s)
}

// CheckAPI checks the settings t needs to talk to the beacon server.
func (c Config) CheckAPI() error {
	if c.Token == "" {
		return errors.New("BEACON_TOKEN_FILE or BEACON_TOKEN is not set (needed because BEACON_SERVER_URL is set)")
	}
	return nil
}

// ReadTokenFile reads the token from TokenFile, if it is set, and puts it
// in Token: a token file takes precedence over BEACON_TOKEN. Surrounding
// whitespace, such as the final newline, is removed.
//
// With private set (the server does this), a file that other users may
// read or write is refused, so a token cannot leak or be swapped by
// accident.
func (c *Config) ReadTokenFile(private bool) error {
	if c.TokenFile == "" {
		return nil
	}
	info, err := os.Stat(c.TokenFile)
	if err != nil {
		return fmt.Errorf("cannot read BEACON_TOKEN_FILE: %w", err)
	}
	// Perm()&0o077 keeps only the group and other bits (rwx for each).
	if private && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("BEACON_TOKEN_FILE %s can be accessed by other users (mode %04o); run: chmod 600 %s",
			c.TokenFile, info.Mode().Perm(), c.TokenFile)
	}
	data, err := os.ReadFile(c.TokenFile)
	if err != nil {
		return fmt.Errorf("cannot read BEACON_TOKEN_FILE: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return fmt.Errorf("BEACON_TOKEN_FILE %s is empty", c.TokenFile)
	}
	c.Token = token
	return nil
}

// Password runs PasswordCmd and returns its output without the trailing
// newline. The command is split on whitespace and run directly, not through
// a shell, so quotes and pipes are not supported. It is killed when ctx ends.
//
// stderr receives the command's messages, and then the command may also
// read from the terminal (for example to ask for a GPG passphrase). With
// stderr nil it runs silently and without the terminal.
func (c Config) Password(ctx context.Context, stderr io.Writer) (string, error) {
	argv := strings.Fields(c.PasswordCmd)
	if len(argv) == 0 {
		return "", errors.New("BEACON_CALDAV_PASSWORD_CMD is not set")
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if stderr != nil {
		cmd.Stdin = os.Stdin
		cmd.Stderr = stderr
	}
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
