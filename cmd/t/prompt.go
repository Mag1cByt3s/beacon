package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/config"
)

// promptLimit is the hard time limit for t prompt, which runs whenever a
// terminal opens and must never make the user wait.
const promptLimit = 300 * time.Millisecond

// hooks are the shell snippets printed by t hook. They run t prompt once
// when an interactive shell starts, not on every prompt (that would be
// precmd in zsh or PROMPT_COMMAND in bash).
var hooks = map[string]string{
	"zsh": `# beacon: show the current task when a new terminal opens.
# Runs once per interactive shell, not on every prompt.
if [[ -o interactive ]] && (( $+commands[t] )); then
  command t prompt 2>/dev/null
fi
`,
	"bash": `# beacon: show the current task when a new terminal opens.
# Runs once per interactive shell, not on every prompt.
if [[ $- == *i* ]] && type -P t >/dev/null 2>&1; then
  command t prompt 2>/dev/null
fi
`,
}

// runPrompt prints the current task for the shell hook: one line, or
// nothing at all on any error or when there is no answer within the limit.
// It does not flush the offline queue, which could take too long.
func runPrompt(out io.Writer, cfg config.Config) {
	prompt(out, promptLimit, func(ctx context.Context) (string, error) {
		// nil: the password command must not print or ask anything here.
		b, err := newBackend(ctx, cfg, nil)
		if err != nil {
			return "", err
		}
		task, ok, err := b.Current(ctx)
		if err != nil || !ok {
			return "", err
		}
		return formatTask(task, time.Now(), cfg.DefaultList), nil
	})
}

// prompt runs line and prints its result, unless it fails or takes longer
// than limit. line gets a context that ends at the limit, but even if it
// ignores that, prompt returns in time.
func prompt(out io.Writer, limit time.Duration, line func(ctx context.Context) (string, error)) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()

	// The channel has room for one value, so the goroutine can always
	// finish, even after prompt has stopped waiting for it.
	result := make(chan string, 1)
	go func() {
		text := ""
		defer func() {
			recover() // a panic stays silent too
			result <- text
		}()
		if s, err := line(ctx); err == nil {
			text = s
		}
	}()

	// select waits for whichever happens first.
	select {
	case text := <-result:
		if text != "" {
			fmt.Fprintln(out, text)
		}
	case <-ctx.Done():
		// Too slow: print nothing. Work still running is abandoned and
		// ends with the process.
	}
}
