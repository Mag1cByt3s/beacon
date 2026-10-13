package api

import (
	"context"
	"net/http"
	"net/url"

	"github.com/Mag1cByt3s/beacon/internal/journal"
)

// Journal returns a journal day ("today" or a date like 2026-10-13) and
// the parts of it still missing.
func (c *Client) Journal(ctx context.Context, date string) (JournalResponse, error) {
	var resp JournalResponse
	err := c.do(ctx, http.MethodGet, "api/journal/"+url.PathEscape(date), nil, nil, &resp)
	return resp, err
}

// PatchJournal changes the given fields of a journal day; the keys are the
// JSON names ("sleep_quality", ...). A nil value clears a field.
func (c *Client) PatchJournal(ctx context.Context, date string, fields map[string]any) (JournalResponse, error) {
	var resp JournalResponse
	err := c.do(ctx, http.MethodPatch, "api/journal/"+url.PathEscape(date), nil, fields, &resp)
	return resp, err
}

// Suggestion returns the proposal for the evening part of a day.
func (c *Client) Suggestion(ctx context.Context, date string) (journal.Suggestion, error) {
	var sg journal.Suggestion
	err := c.do(ctx, http.MethodGet, "api/journal/"+url.PathEscape(date)+"/suggestion", nil, nil, &sg)
	return sg, err
}

// Items returns the things done on a day.
func (c *Client) Items(ctx context.Context, date string) ([]journal.Item, error) {
	var resp ItemsResponse
	err := c.do(ctx, http.MethodGet, "api/journal/"+url.PathEscape(date)+"/items", nil, nil, &resp)
	return resp.Items, err
}

// AddItem records one thing done on a day.
func (c *Client) AddItem(ctx context.Context, date string, it journal.Item) (journal.Item, error) {
	var stored journal.Item
	err := c.do(ctx, http.MethodPost, "api/journal/"+url.PathEscape(date)+"/items", nil, it, &stored)
	return stored, err
}

// ReplaceItems makes items the complete list of a day.
func (c *Client) ReplaceItems(ctx context.Context, date string, items []journal.Item) ([]journal.Item, error) {
	var resp ItemsResponse
	err := c.do(ctx, http.MethodPut, "api/journal/"+url.PathEscape(date)+"/items", nil, ItemsResponse{Items: items}, &resp)
	return resp.Items, err
}

// FocusStart starts a focus session. Without a label the server uses the
// current task.
func (c *Client) FocusStart(ctx context.Context, req FocusRequest) (FocusResponse, error) {
	return c.focus(ctx, "start", req)
}

// FocusStop stops the running focus session; a result becomes a journal
// item.
func (c *Client) FocusStop(ctx context.Context, req FocusRequest) (FocusResponse, error) {
	return c.focus(ctx, "stop", req)
}

// FocusToggle stops the running session or starts one.
func (c *Client) FocusToggle(ctx context.Context, req FocusRequest) (FocusResponse, error) {
	return c.focus(ctx, "toggle", req)
}

func (c *Client) focus(ctx context.Context, action string, req FocusRequest) (FocusResponse, error) {
	var resp FocusResponse
	err := c.do(ctx, http.MethodPost, "api/focus/"+action, nil, req, &resp)
	return resp, err
}

// FocusStatus returns the running session and the sessions of a day
// ("today" or a date).
func (c *Client) FocusStatus(ctx context.Context, date string) (FocusStatus, error) {
	var status FocusStatus
	err := c.do(ctx, http.MethodGet, "api/focus?date="+url.QueryEscape(date), nil, nil, &status)
	return status, err
}

// PutAW stores the productive computer time ActivityWatch measured on a
// day. Only the aw-sync client may do this.
func (c *Client) PutAW(ctx context.Context, date string, req AWRequest) (JournalResponse, error) {
	var resp JournalResponse
	err := c.do(ctx, http.MethodPut, "api/journal/"+url.PathEscape(date)+"/aw", nil, req, &resp)
	return resp, err
}
