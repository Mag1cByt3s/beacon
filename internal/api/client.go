package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Mag1cByt3s/beacon/internal/focus"
)

// ErrUnauthorized means the server did not accept the token.
var ErrUnauthorized = errors.New("the beacon server rejected the token; check BEACON_TOKEN")

// ErrUnreachable is matched (with errors.Is) by errors that mean the task
// could not get through and trying again later may work: the beacon server
// is down or slow, or it cannot reach the CalDAV server (503).
var ErrUnreachable = errors.New("unreachable")

// ErrExists means a task with this UID already exists, so an earlier
// attempt of the same capture already got through.
var ErrExists = errors.New("a task with this uid already exists")

// ErrNotFound means there is no open task with the given UID (it was
// completed or removed already).
var ErrNotFound = errors.New("no open task with this uid")

// unreachableError keeps a human message while matching ErrUnreachable.
type unreachableError struct{ msg string }

func (e unreachableError) Error() string        { return e.msg }
func (e unreachableError) Is(target error) bool { return target == ErrUnreachable }

// ConflictError means the server refused to complete the current task
// because it changed elsewhere since it became current. Current is the task
// as it is now, if the server sent it.
type ConflictError struct {
	Message string
	Current *focus.Task
}

func (e *ConflictError) Error() string {
	return e.Message
}

// Client talks to a beacon server.
type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

// NewClient creates a client for the server at baseURL.
func NewClient(baseURL, token string) (*Client, error) {
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, errors.New("BEACON_SERVER_URL is not a valid URL (example: https://beacon.example.org)")
	}
	// Requests are bounded by the caller's context, so no client timeout.
	return &Client{base: base, token: token, http: &http.Client{}}, nil
}

// Tasks returns the open tasks in focus order, current task first.
func (c *Client) Tasks(ctx context.Context) ([]focus.Task, error) {
	var resp TasksResponse
	if err := c.do(ctx, http.MethodGet, "tasks", nil, nil, &resp); err != nil {
		return nil, err
	}
	tasks := make([]focus.Task, len(resp.Tasks))
	for i, t := range resp.Tasks {
		tasks[i] = t.Focus()
	}
	return tasks, nil
}

// Current returns the current task. ok is false when nothing is open.
func (c *Client) Current(ctx context.Context) (task focus.Task, ok bool, err error) {
	var resp CurrentResponse
	if err := c.do(ctx, http.MethodGet, "current", nil, nil, &resp); err != nil {
		return focus.Task{}, false, err
	}
	return optional(resp.Task)
}

// Add creates a task with the given UID and returns the name of the list
// it went to and the new task's ETag (may be empty). If a task with that
// UID already exists, it returns ErrExists.
func (c *Client) Add(ctx context.Context, uid, summary string) (list, etag string, err error) {
	var resp AddResponse
	err = c.do(ctx, http.MethodPost, "tasks", nil, AddRequest{UID: uid, Summary: summary}, &resp)
	var conflict *ConflictError
	if errors.As(err, &conflict) {
		return "", "", ErrExists
	}
	if err != nil {
		return "", "", err
	}
	return resp.List, resp.ETag, nil
}

// Rename gives the open task with this UID a new title and returns the
// task as it is now (with its new ETag). With etag set, only that version
// is renamed: if the task changed since, it returns a *ConflictError. If
// there is no such open task, ErrNotFound.
func (c *Client) Rename(ctx context.Context, uid, etag, summary string) (focus.Task, error) {
	var resp TaskResponse
	err := c.do(ctx, http.MethodPatch, "tasks/"+url.PathEscape(uid), ifMatch(etag), RenameRequest{Summary: summary}, &resp)
	if err != nil {
		return focus.Task{}, err
	}
	if resp.Task == nil {
		return focus.Task{}, errors.New("the beacon server sent an unexpected answer")
	}
	return resp.Task.Focus(), nil
}

// ifMatch returns an If-Match header for etag, or nil for no condition.
// ETags travel without quotes; the header needs them, unless the ETag is
// already quoted or weak (W/"...").
func ifMatch(etag string) http.Header {
	if etag == "" {
		return nil
	}
	if !strings.HasPrefix(etag, `"`) && !strings.HasPrefix(etag, `W/"`) {
		etag = `"` + etag + `"`
	}
	return http.Header{"If-Match": {etag}}
}

// Remove deletes the open task with this UID for good and returns it. With
// etag set, only that version is deleted: if the task changed since, it
// returns a *ConflictError. If there is no such open task, ErrNotFound.
func (c *Client) Remove(ctx context.Context, uid, etag string) (focus.Task, error) {
	var resp DeleteResponse
	err := c.do(ctx, http.MethodDelete, "tasks/"+url.PathEscape(uid), ifMatch(etag), nil, &resp)
	if err != nil {
		return focus.Task{}, err
	}
	if resp.Deleted == nil {
		return focus.Task{}, errors.New("the beacon server sent an unexpected answer")
	}
	return resp.Deleted.Focus(), nil
}

// Done completes the current task and returns the next one.
// It returns a *ConflictError if the task changed since it became current.
func (c *Client) Done(ctx context.Context) (next focus.Task, ok bool, err error) {
	var resp DoneResponse
	if err := c.do(ctx, http.MethodPost, "current/done", nil, nil, &resp); err != nil {
		return focus.Task{}, false, err
	}
	return optional(resp.Current)
}

// Skip moves the current task to the end of the skip order and returns
// the next one.
func (c *Client) Skip(ctx context.Context) (next focus.Task, ok bool, err error) {
	var resp SkipResponse
	if err := c.do(ctx, http.MethodPost, "current/skip", nil, nil, &resp); err != nil {
		return focus.Task{}, false, err
	}
	return optional(resp.Current)
}

func optional(t *Task) (focus.Task, bool, error) {
	if t == nil {
		return focus.Task{}, false, nil
	}
	return t.Focus(), true, nil
}

// do sends one request with the extra header (may be nil). in, if not nil,
// is sent as JSON; the answer is decoded into out. Errors are turned into
// short messages for people.
func (c *Client) do(ctx context.Context, method, path string, header http.Header, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base.JoinPath(path).String(), body)
	if err != nil {
		return err
	}
	for name, values := range header {
		req.Header[name] = values
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return unreachableError{fmt.Sprintf("the beacon server at %s did not answer in time", c.base.Host)}
		}
		return unreachableError{fmt.Sprintf("cannot reach the beacon server at %s; is it running? (check BEACON_SERVER_URL)", c.base.Host)}
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 == 2 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("the beacon server sent an unexpected answer: %w", err)
		}
		return nil
	}

	// Errors carry an ErrorResponse; fall back to the status if they don't.
	var errResp ErrorResponse
	json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&errResp)
	msg := strings.TrimSpace(errResp.Error)
	if msg == "" {
		msg = resp.Status
	}

	// Only DELETE and PATCH name one task; any other 404 is a server problem.
	if resp.StatusCode == http.StatusNotFound && (method == http.MethodDelete || method == http.MethodPatch) {
		return ErrNotFound
	}

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusServiceUnavailable:
		return unreachableError{"beacon server: " + msg}
	case http.StatusConflict:
		conflict := &ConflictError{Message: msg}
		if errResp.Current != nil {
			current := errResp.Current.Focus()
			conflict.Current = &current
		}
		return conflict
	default:
		return fmt.Errorf("beacon server: %s", msg)
	}
}
