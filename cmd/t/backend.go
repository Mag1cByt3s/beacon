package main

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/api"
	"github.com/Mag1cByt3s/beacon/internal/caldav"
	"github.com/Mag1cByt3s/beacon/internal/config"
	"github.com/Mag1cByt3s/beacon/internal/focus"
)

// backend is where t gets and changes tasks: the beacon server (*api.Client)
// or CalDAV directly (direct). In Go, a type satisfies an interface just by
// having its methods; no "implements" keyword is needed.
type backend interface {
	Tasks(ctx context.Context) ([]focus.Task, error)       // in focus order
	Current(ctx context.Context) (focus.Task, bool, error) // ok false: nothing open
	Add(ctx context.Context, uid, summary string) (list, etag string, err error)
	Done(ctx context.Context) (next focus.Task, ok bool, err error)
	Skip(ctx context.Context) (next focus.Task, ok bool, err error)
}

// newBackend picks the server when BEACON_SERVER_URL is set, else CalDAV.
// stderr is passed on to the password command (nil keeps it silent).
func newBackend(ctx context.Context, cfg config.Config, stderr io.Writer) (backend, error) {
	if cfg.UseServer() {
		if err := cfg.ReadTokenFile(false); err != nil {
			return nil, err
		}
		if err := cfg.CheckAPI(); err != nil {
			return nil, err
		}
		client, err := api.NewClient(cfg.ServerURL, cfg.Token)
		if err != nil {
			// Return a plain nil: a nil *api.Client inside the interface
			// would make the interface itself non-nil.
			return nil, err
		}
		return client, nil
	}

	if err := cfg.CheckCalDAV(); err != nil {
		return nil, err
	}
	password := ""
	if cfg.User != "" {
		var err error
		password, err = cfg.Password(ctx, stderr)
		if err != nil {
			return nil, err
		}
	}
	client, err := caldav.NewClient(cfg.CalDAVURL, cfg.User, password, cfg.Lists)
	if err != nil {
		return nil, err
	}
	return direct{client: client, defaultList: cfg.DefaultList}, nil
}

// errSkipNeedsServer is returned by t skip without a server: the skip
// order is focus state, and only the server keeps focus state.
var errSkipNeedsServer = errors.New("skip needs the beacon server; set BEACON_SERVER_URL")

// direct talks to CalDAV without a server. It has no focus state, so the
// current task is simply the first in the queue.
type direct struct {
	client      *caldav.Client
	defaultList string
}

func (d direct) Tasks(ctx context.Context) ([]focus.Task, error) {
	tasks, err := d.client.OpenTasks(ctx)
	if err != nil {
		return nil, err
	}
	return focus.Queue(tasks, time.Now()), nil
}

func (d direct) Current(ctx context.Context) (focus.Task, bool, error) {
	tasks, err := d.client.OpenTasks(ctx)
	if err != nil {
		return focus.Task{}, false, err
	}
	task, ok := focus.Next(tasks, time.Now())
	return task, ok, nil
}

func (d direct) Add(ctx context.Context, uid, summary string) (string, string, error) {
	return d.client.Create(ctx, d.defaultList, uid, summary)
}

// Done completes the first task in the queue and returns the one after it,
// from what was already read (no second request).
func (d direct) Done(ctx context.Context) (focus.Task, bool, error) {
	tasks, err := d.client.OpenTasks(ctx)
	if err != nil {
		return focus.Task{}, false, err
	}
	current, ok := focus.Next(tasks, time.Now())
	if !ok {
		return focus.Task{}, false, nil
	}
	if err := d.client.Complete(ctx, current); err != nil {
		return focus.Task{}, false, err
	}

	var rest []focus.Task
	for _, task := range tasks {
		if task.UID != current.UID || task.Path != current.Path {
			rest = append(rest, task)
		}
	}
	next, ok := focus.Next(rest, time.Now())
	return next, ok, nil
}

func (d direct) Skip(ctx context.Context) (focus.Task, bool, error) {
	return focus.Task{}, false, errSkipNeedsServer
}
