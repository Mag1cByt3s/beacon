// Package queue keeps captures that could not be sent yet, one JSON object
// per line in a local file, until they can be flushed to the server.
//
// Each entry carries the UID the task will get. Sending the same entry
// twice therefore cannot create a duplicate: the second attempt is refused
// as "already exists", which the sender treats as success.
package queue

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Entry is one queued capture.
type Entry struct {
	UID     string    `json:"uid"`
	Summary string    `json:"summary"`
	Queued  time.Time `json:"queued"`
}

// Queue is a queue file. Several t processes may use it at the same time;
// a lock file next to it keeps their reads and writes apart.
type Queue struct {
	path string
}

// DefaultPath returns $XDG_STATE_HOME/beacon/queue.jsonl, or
// ~/.local/state/beacon/queue.jsonl if XDG_STATE_HOME is not set.
func DefaultPath() (string, error) {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot find a place for the offline queue: %w", err)
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "beacon", "queue.jsonl"), nil
}

// New returns the queue stored at path. Nothing is created until the first
// Append.
func New(path string) *Queue {
	return &Queue{path: path}
}

// Path returns the queue file's path.
func (q *Queue) Path() string {
	return q.path
}

// Append adds e to the end of the queue.
func (q *Queue) Append(e Entry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return q.withLock(func() error {
		// O_APPEND adds to the end; 0o600 keeps the file private.
		f, err := os.OpenFile(q.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	})
}

// Entries returns all queued entries, oldest first. Lines that cannot be
// read are skipped here but stay in the file.
func (q *Queue) Entries() ([]Entry, error) {
	var entries []Entry
	err := q.withLock(func() error {
		lines, err := q.readLines()
		for _, line := range lines {
			if e, ok := parse(line); ok {
				entries = append(entries, e)
			}
		}
		return err
	})
	return entries, err
}

// Flush tries to send every queued entry, oldest first, and removes the
// ones that were sent. An entry stays queued if send fails, and flushing
// stops early when ctx is done.
//
// It returns how many entries were sent and the first error, which is
// either a send error or a problem with the queue file.
func (q *Queue) Flush(ctx context.Context, send func(context.Context, Entry) error) (sent int, err error) {
	entries, err := q.Entries()
	if err != nil || len(entries) == 0 {
		return 0, err
	}

	// Send without holding the lock, so a capture made meanwhile does not
	// have to wait for the network.
	done := map[string]bool{}
	var firstErr error
	for _, e := range entries {
		if ctx.Err() != nil {
			if firstErr == nil {
				firstErr = ctx.Err()
			}
			break
		}
		if err := send(ctx, e); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue // keep it, try the next one
		}
		done[e.UID] = true
	}

	if len(done) > 0 {
		if err := q.remove(done); err != nil {
			return len(done), err
		}
	}
	return len(done), firstErr
}

// Remove takes the entry with this UID out of the queue, so it will never
// be sent. found is false if it is not queued (any more).
func (q *Queue) Remove(uid string) (found bool, err error) {
	err = q.withLock(func() error {
		n, err := q.removeLocked(map[string]bool{uid: true})
		found = n > 0
		return err
	})
	return found, err
}

// remove deletes the entries whose UIDs are in done. It reads the file
// again first, so entries appended during a flush are kept.
func (q *Queue) remove(done map[string]bool) error {
	return q.withLock(func() error {
		_, err := q.removeLocked(done)
		return err
	})
}

// removeLocked does the work of remove and returns how many entries it
// removed. Call it with the lock held.
func (q *Queue) removeLocked(done map[string]bool) (removed int, err error) {
	lines, err := q.readLines()
	if err != nil {
		return 0, err
	}
	var keep bytes.Buffer
	for _, line := range lines {
		if e, ok := parse(line); ok && done[e.UID] {
			removed++
			continue
		}
		keep.WriteString(line)
		keep.WriteByte('\n')
	}
	if removed == 0 {
		return 0, nil // nothing to change
	}

	if keep.Len() == 0 {
		err := os.Remove(q.path)
		if errors.Is(err, fs.ErrNotExist) {
			return removed, nil
		}
		return removed, err
	}
	// Write a new file and rename it over the old one. A rename is
	// atomic, so the queue is never left half written.
	tmp := q.path + ".tmp"
	if err := os.WriteFile(tmp, keep.Bytes(), 0o600); err != nil {
		return removed, err
	}
	return removed, os.Rename(tmp, q.path)
}

// readLines returns the file's non-empty lines. A missing file is an empty
// queue. Call it with the lock held.
func (q *Queue) readLines() ([]string, error) {
	f, err := os.Open(q.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(nil, 1<<20) // allow lines up to 1 MiB
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			lines = append(lines, line)
		}
	}
	return lines, scanner.Err()
}

func parse(line string) (Entry, bool) {
	var e Entry
	if err := json.Unmarshal([]byte(line), &e); err != nil || e.UID == "" {
		return Entry{}, false
	}
	return e, true
}

// withLock runs fn while holding an exclusive lock on the queue's lock
// file, creating the directory if needed.
func (q *Queue) withLock(fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(q.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(q.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := lock(f); err != nil {
		return err
	}
	defer unlock(f)
	return fn()
}
