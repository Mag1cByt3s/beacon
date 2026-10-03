package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/api"
	"github.com/Mag1cByt3s/beacon/internal/caldav"
	"github.com/Mag1cByt3s/beacon/internal/queue"
)

// add captures a task. If the server (or Radicale) cannot be reached, the
// capture is saved in the offline queue and sent by a later t command.
// Only captures are ever queued; done and skip are not.
func (a *app) add(ctx context.Context, summary string) error {
	// The UID is chosen here, once. If the capture is retried later, the
	// same UID makes sure it cannot be created twice.
	uid := caldav.NewUID()

	// Skip the network if the flush just now found it unreachable.
	if !a.offline || a.queue == nil {
		addCtx, cancel := context.WithTimeout(ctx, addTimeout)
		defer cancel()
		list, _, err := a.b.Add(addCtx, uid, summary)
		if err == nil {
			fmt.Fprintf(a.out, "Added to %s.\n", list)
			return nil
		}
		if !unreachable(err) || a.queue == nil {
			return err
		}
	}

	err := a.queue.Append(queue.Entry{UID: uid, Summary: summary, Queued: time.Now()})
	if err != nil {
		return fmt.Errorf("cannot reach the server, and saving the task offline failed too: %w", err)
	}
	fmt.Fprintln(a.out, "Saved offline, will sync later.")
	return nil
}

// flush sends queued captures, quietly and within flushTimeout. Failures
// are not reported: the captures stay queued for the next try.
func (a *app) flush() {
	if a.queue == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()

	_, err := a.queue.Flush(ctx, func(ctx context.Context, e queue.Entry) error {
		_, _, err := a.b.Add(ctx, e.UID, e.Summary)
		if alreadyAdded(err) {
			return nil // an earlier try got through after all
		}
		return err
	})
	a.offline = unreachable(err)
}

// openQueue returns the offline queue, or nil if there is no place for it.
func openQueue() *queue.Queue {
	path, err := queue.DefaultPath()
	if err != nil {
		return nil
	}
	return queue.New(path)
}

// unreachable reports whether err means "try again later": the server or
// Radicale could not be reached, or did not answer in time.
func unreachable(err error) bool {
	return errors.Is(err, api.ErrUnreachable) ||
		errors.Is(err, caldav.ErrUnreachable) ||
		errors.Is(err, context.DeadlineExceeded)
}

// alreadyAdded reports whether err says a task with this UID exists.
func alreadyAdded(err error) bool {
	return errors.Is(err, api.ErrExists) || errors.Is(err, caldav.ErrExists)
}
