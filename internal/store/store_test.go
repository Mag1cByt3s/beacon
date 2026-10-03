package store

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	// t.TempDir is removed automatically when the test ends.
	path := filepath.Join(t.TempDir(), "beacon.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestCurrent(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)

	if c, err := s.Current(ctx); err != nil || c != (Current{}) {
		t.Fatalf("empty store: Current = %+v, %v", c, err)
	}

	for _, c := range []Current{{UID: "a", ETag: "1"}, {UID: "b", ETag: "2"}} {
		if err := s.SetCurrent(ctx, c); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Current(ctx); err != nil || got != c {
			t.Errorf("Current = %+v, %v; want %+v", got, err, c)
		}
	}

	if err := s.ClearCurrent(ctx); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Current(ctx); c != (Current{}) {
		t.Errorf("after ClearCurrent: %+v", c)
	}
}

func TestSkipOrder(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)

	steps := []struct {
		skip, unskip string
		want         []string
	}{
		{skip: "a", want: []string{"a"}},
		{skip: "b", want: []string{"a", "b"}},
		{skip: "a", want: []string{"b", "a"}}, // skipping again moves to the end
		{skip: "c", want: []string{"b", "a", "c"}},
		{unskip: "a", want: []string{"b", "c"}},
		{unskip: "missing", want: []string{"b", "c"}},
	}
	for _, step := range steps {
		if step.skip != "" {
			if err := s.Skip(ctx, step.skip); err != nil {
				t.Fatal(err)
			}
		}
		if step.unskip != "" {
			if err := s.Unskip(ctx, step.unskip); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.Skipped(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, step.want) {
			t.Errorf("after skip %q / unskip %q: Skipped = %q, want %q", step.skip, step.unskip, got, step.want)
		}
	}
}

func TestStateSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)
	s.SetCurrent(ctx, Current{UID: "a", ETag: "1"})
	s.Skip(ctx, "b")
	s.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if c, _ := s2.Current(ctx); c.UID != "a" {
		t.Errorf("Current after reopen = %+v", c)
	}
	if skipped, _ := s2.Skipped(ctx); !slices.Equal(skipped, []string{"b"}) {
		t.Errorf("Skipped after reopen = %q", skipped)
	}
}

func TestOpenBadPath(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "missing-dir", "beacon.db")); err == nil {
		t.Error("Open in a missing directory succeeded")
	}
}
