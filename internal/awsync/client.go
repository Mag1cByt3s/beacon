package awsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// query asks ActivityWatch for the window events during which the user
// was not away from the keyboard. find_bucket picks the buckets by
// prefix, so it works with aw-watcher-window as well as awatcher.
var query = []string{
	`window = flood(query_bucket(find_bucket("aw-watcher-window_")));`,
	`afk = flood(query_bucket(find_bucket("aw-watcher-afk_")));`,
	`not_afk = filter_keyvals(afk, "status", ["not-afk"]);`,
	`RETURN = filter_period_intersect(window, not_afk);`,
}

// Events returns the active window events between from and to from the
// ActivityWatch server at base (usually http://localhost:5600).
func Events(ctx context.Context, base string, from, to time.Time) ([]Event, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("BEACON_AW_URL %q is not a URL like http://localhost:5600", base)
	}
	body, err := json.Marshal(map[string]any{
		"timeperiods": []string{from.Format(time.RFC3339) + "/" + to.Format(time.RFC3339)},
		"query":       query,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.JoinPath("api/0/query/").String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach ActivityWatch at %s; is it running? (%w)", base, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ActivityWatch answered %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	// One list of events per time period; there is one period.
	var periods [][]Event
	if err := json.Unmarshal(data, &periods); err != nil {
		return nil, fmt.Errorf("unexpected answer from ActivityWatch: %w", err)
	}
	if len(periods) != 1 {
		return nil, fmt.Errorf("unexpected answer from ActivityWatch: %d periods", len(periods))
	}
	return periods[0], nil
}
