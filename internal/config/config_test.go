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
		"BEACON_TRUSTED_PROXIES", "BEACON_TOKENS_DIR", "BEACON_JOURNAL_TZ", "BEACON_JOURNAL_DAY_START",
	} {
		t.Setenv(name, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)

	c := Load()
	want := Config{
		Lists:           []string{"Todo"},
		DefaultList:     "Todo",
		Listen:          "127.0.0.1:8080",
		DB:              "beacon.db",
		JournalZone:     "Europe/Berlin",
		JournalDayStart: "04:00",
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
	const dir = "/usr/local/etc/beacon/tokens"

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
		{"server ok", map[string]string{"BEACON_CALDAV_URL": url, "BEACON_TOKENS_DIR": dir}, Config.CheckServer, ""},
		{"server without tokens dir", map[string]string{"BEACON_CALDAV_URL": url}, Config.CheckServer, "BEACON_TOKENS_DIR is not set"},
		{"server still with a token file",
			map[string]string{"BEACON_CALDAV_URL": url, "BEACON_TOKENS_DIR": dir, "BEACON_TOKEN_FILE": "/usr/local/etc/beacon/token"},
			Config.CheckServer, "move the token to a file in BEACON_TOKENS_DIR"},
		{"server still with a token",
			map[string]string{"BEACON_CALDAV_URL": url, "BEACON_TOKENS_DIR": dir, "BEACON_TOKEN": "token-from-before"},
			Config.CheckServer, "no longer uses"},
		{"server needs caldav", map[string]string{"BEACON_TOKENS_DIR": dir}, Config.CheckServer, "BEACON_CALDAV_URL"},
		{"server with trusted proxies",
			map[string]string{"BEACON_CALDAV_URL": url, "BEACON_TOKENS_DIR": dir, "BEACON_TRUSTED_PROXIES": "10.0.0.4, 192.168.1.0/24,::1"},
			Config.CheckServer, ""},
		{"server with a bad trusted proxy",
			map[string]string{"BEACON_CALDAV_URL": url, "BEACON_TOKENS_DIR": dir, "BEACON_TRUSTED_PROXIES": "10.0.0.4,caddy"},
			Config.CheckServer, `"caddy" is not an IP address`},
		{"server with a bad journal zone",
			map[string]string{"BEACON_CALDAV_URL": url, "BEACON_TOKENS_DIR": dir, "BEACON_JOURNAL_TZ": "Europe/Atlantis"},
			Config.CheckServer, "BEACON_JOURNAL_TZ"},
		{"server with a bad day start",
			map[string]string{"BEACON_CALDAV_URL": url, "BEACON_TOKENS_DIR": dir, "BEACON_JOURNAL_DAY_START": "4"},
			Config.CheckServer, "BEACON_JOURNAL_DAY_START"},
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

func TestProxyPrefixes(t *testing.T) {
	tests := []struct {
		in      []string
		want    []string
		wantErr bool
	}{
		{nil, nil, false},
		{[]string{"10.0.0.4"}, []string{"10.0.0.4/32"}, false},
		{[]string{"10.0.0.7/24"}, []string{"10.0.0.0/24"}, false},
		{[]string{"::1", "fd00::/8"}, []string{"::1/128", "fd00::/8"}, false},
		{[]string{"::ffff:10.0.0.4"}, []string{"10.0.0.4/32"}, false},
		{[]string{"10.0.0.300"}, nil, true},
		{[]string{"10.0.0.0/33"}, nil, true},
		{[]string{"proxy.example.org"}, nil, true},
	}
	for _, tt := range tests {
		got, err := Config{TrustedProxies: tt.in}.ProxyPrefixes()
		if (err != nil) != tt.wantErr {
			t.Errorf("%q: err = %v, want error %v", tt.in, err, tt.wantErr)
			continue
		}
		var gotStrings []string
		for _, p := range got {
			gotStrings = append(gotStrings, p.String())
		}
		if !reflect.DeepEqual(gotStrings, tt.want) {
			t.Errorf("%q: got %q, want %q", tt.in, gotStrings, tt.want)
		}
	}
}

func TestJournalClock(t *testing.T) {
	clearEnv(t)
	c, err := Load().JournalClock()
	if err != nil || c.Loc.String() != "Europe/Berlin" || c.Hour != 4 || c.Minute != 0 {
		t.Errorf("default JournalClock = %+v, %v", c, err)
	}
	t.Setenv("BEACON_JOURNAL_TZ", "UTC")
	t.Setenv("BEACON_JOURNAL_DAY_START", "05:30")
	c, err = Load().JournalClock()
	if err != nil || c.Loc.String() != "UTC" || c.Hour != 5 || c.Minute != 30 {
		t.Errorf("JournalClock = %+v, %v", c, err)
	}
}

// tokensDir creates a token directory with the given files (name to
// content) and modes 0700/0600.
func tokensDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "tokens")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestReadClientTokens(t *testing.T) {
	const (
		laptop = "laptop-token-0123456789"
		iphone = "iphone-token-0123456789"
		aw     = "aw-sync-token-012345678"
	)
	dir := tokensDir(t, map[string]string{
		"laptop":      laptop + "\n",
		"iphone":      iphone,
		"aw-sync":     aw,
		".laptop.swp": "editor junk", // ignored
	})
	got, err := ReadClientTokens(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"laptop": laptop, "iphone": iphone, "aw-sync": aw}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadClientTokens = %v, want %v", got, want)
	}
}

func TestReadClientTokensRefusals(t *testing.T) {
	const good = "a-good-token-0123456789"
	tests := []struct {
		name    string
		files   map[string]string
		setup   func(dir string)
		wantErr string
	}{
		{"missing dir", nil, func(dir string) { os.RemoveAll(dir) }, "cannot read BEACON_TOKENS_DIR"},
		{"dir readable by others", map[string]string{"laptop": good}, func(dir string) { os.Chmod(dir, 0o755) }, "chmod 700"},
		{"file readable by others", map[string]string{"laptop": good}, func(dir string) { os.Chmod(filepath.Join(dir, "laptop"), 0o644) }, "chmod 600"},
		{"short token", map[string]string{"laptop": "short"}, nil, `client "laptop" is too short`},
		{"same token twice", map[string]string{"laptop": good, "iphone": good}, nil, "the same token"},
		{"bad client name", map[string]string{"Laptop Pro": good}, nil, "client name"},
		{"empty dir", map[string]string{}, nil, "holds no tokens"},
		{"only a hidden file", map[string]string{".backup": good}, nil, "holds no tokens"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := tokensDir(t, tt.files)
			if tt.setup != nil {
				tt.setup(dir)
			}
			_, err := ReadClientTokens(dir)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want one mentioning %q", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), good) {
				t.Errorf("error message contains a token: %v", err)
			}
		})
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
