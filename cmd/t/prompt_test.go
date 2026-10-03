package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrompt(t *testing.T) {
	tests := []struct {
		name string
		line func(ctx context.Context) (string, error)
		want string
	}{
		{"prints the line", func(ctx context.Context) (string, error) { return "Pay rent", nil }, "Pay rent\n"},
		{"nothing open", func(ctx context.Context) (string, error) { return "", nil }, ""},
		{"error is silent", func(ctx context.Context) (string, error) { return "x", errors.New("boom") }, ""},
		{"panic is silent", func(ctx context.Context) (string, error) { panic("boom") }, ""},
		{"too slow, even ignoring ctx", func(ctx context.Context) (string, error) {
			time.Sleep(2 * time.Second)
			return "late", nil
		}, ""},
		{"too slow, respecting ctx", func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "late", ctx.Err()
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out strings.Builder
			start := time.Now()
			prompt(&out, 50*time.Millisecond, tt.line)
			if d := time.Since(start); d > 250*time.Millisecond {
				t.Errorf("prompt took %v, want about 50ms at most", d)
			}
			if out.String() != tt.want {
				t.Errorf("output = %q, want %q", out.String(), tt.want)
			}
		})
	}
}

// clearBeaconEnv empties all beacon variables for the test and points
// the offline queue at a temporary directory, so tests never touch the
// user's real queue.
func clearBeaconEnv(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, name := range []string{
		"BEACON_CALDAV_URL", "BEACON_CALDAV_USER", "BEACON_CALDAV_PASSWORD_CMD",
		"BEACON_LISTS", "BEACON_DEFAULT_LIST", "BEACON_SERVER_URL", "BEACON_TOKEN", "BEACON_TOKEN_FILE",
	} {
		t.Setenv(name, "")
	}
}

// runPromptCommand runs "t prompt" as main would and checks that it is
// silent, succeeds and stays within the limit.
func runPromptCommand(t *testing.T) string {
	t.Helper()
	var out strings.Builder
	start := time.Now()
	if err := run([]string{"prompt"}, &out); err != nil {
		t.Errorf("err = %v, want nil (exit code 0)", err)
	}
	if d := time.Since(start); d > promptLimit+200*time.Millisecond {
		t.Errorf("t prompt took %v, limit is %v", d, promptLimit)
	}
	return out.String()
}

func TestPromptCommandSilentOnErrors(t *testing.T) {
	down := httptest.NewServer(nil)
	down.Close()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Answer after 2 seconds, or stop when the client gives up.
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()

	tests := []struct {
		name string
		env  map[string]string
	}{
		{"not configured", nil},
		{"server down", map[string]string{"BEACON_SERVER_URL": down.URL, "BEACON_TOKEN": "x"}},
		{"server slow", map[string]string{"BEACON_SERVER_URL": slow.URL, "BEACON_TOKEN": "x"}},
		{"radicale down", map[string]string{"BEACON_CALDAV_URL": down.URL + "/"}},
		{"password command fails", map[string]string{
			"BEACON_CALDAV_URL": down.URL + "/", "BEACON_CALDAV_USER": "u",
			"BEACON_CALDAV_PASSWORD_CMD": "sh -c echo-to-stderr-and-fail"}},
		{"password command hangs", map[string]string{
			"BEACON_CALDAV_URL": down.URL + "/", "BEACON_CALDAV_USER": "u",
			"BEACON_CALDAV_PASSWORD_CMD": "sleep 10"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearBeaconEnv(t)
			for name, value := range tt.env {
				t.Setenv(name, value)
			}
			if out := runPromptCommand(t); out != "" {
				t.Errorf("output = %q, want nothing", out)
			}
		})
	}
}

func TestPromptCommandShowsTask(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"task":{"uid":"a","summary":"Pay rent","list":"Todo"}}`))
	}))
	defer srv.Close()
	clearBeaconEnv(t)
	t.Setenv("BEACON_SERVER_URL", srv.URL)
	t.Setenv("BEACON_TOKEN", "x")

	if out := runPromptCommand(t); out != "Pay rent\n" {
		t.Errorf("output = %q, want the task", out)
	}
}

func TestClientUsesTokenFile(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"task":null}`))
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "token")
	os.WriteFile(path, []byte("token-from-the-file\n"), 0o600)
	clearBeaconEnv(t)
	t.Setenv("BEACON_SERVER_URL", srv.URL)
	t.Setenv("BEACON_TOKEN", "token-from-env")
	t.Setenv("BEACON_TOKEN_FILE", path)

	if err := run([]string{"focus"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer token-from-the-file" {
		t.Errorf("Authorization = %q, want the token from the file", gotAuth)
	}
}

func TestHookOutput(t *testing.T) {
	for _, shell := range []string{"zsh", "bash"} {
		var out strings.Builder
		if err := run([]string{"hook", shell}, &out); err != nil {
			t.Fatalf("%s: %v", shell, err)
		}
		hook := out.String()
		if !strings.Contains(hook, "t prompt") {
			t.Errorf("%s hook does not run t prompt:\n%s", shell, hook)
		}
		// Once per terminal, not on every prompt.
		for _, everyPrompt := range []string{"precmd", "PROMPT_COMMAND"} {
			if strings.Contains(hook, everyPrompt) {
				t.Errorf("%s hook uses %s:\n%s", shell, everyPrompt, hook)
			}
		}
	}
}

// TestHookRuns runs each hook in a real shell, with a fake t that records
// how it was called. It checks that an interactive shell calls t prompt
// once and a non-interactive one (a script) does not call it at all.
func TestHookRuns(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			shellPath, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("%s is not installed", shell)
			}
			dir := t.TempDir()
			calls := filepath.Join(dir, "calls")
			fakeT := "#!/bin/sh\necho \"$@\" >> " + calls + "\n"
			if err := os.WriteFile(filepath.Join(dir, "t"), []byte(fakeT), 0o755); err != nil {
				t.Fatal(err)
			}
			hook := hooks[shell]

			// Syntax check first, for a clearer failure.
			if out, err := exec.Command(shellPath, "-n", "-c", hook).CombinedOutput(); err != nil {
				t.Fatalf("syntax error: %v\n%s", err, out)
			}

			env := append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "HOME="+dir)
			noRC := []string{"--norc", "--noprofile"}
			if shell == "zsh" {
				noRC = []string{"-f"}
			}
			runShell := func(interactive bool) {
				args := append([]string{}, noRC...)
				if interactive {
					args = append(args, "-i")
				}
				args = append(args, "-c", hook)
				cmd := exec.Command(shellPath, args...)
				cmd.Env = env
				// The interactive shell may complain about job control
				// without a terminal; that is fine.
				cmd.CombinedOutput()
			}

			runShell(false)
			if data, _ := os.ReadFile(calls); len(data) != 0 {
				t.Errorf("non-interactive shell called t: %q", data)
			}
			runShell(true)
			if data, _ := os.ReadFile(calls); string(data) != "prompt\n" {
				t.Errorf("interactive shell called t with %q, want \"prompt\" once", data)
			}
		})
	}
}
