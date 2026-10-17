package lastseen

import (
	"context"
	"testing"
	"time"
)

type fakeStore struct {
	calls [][]Seen
	skip  []string
}

func (f *fakeStore) MarkSeen(_ context.Context, seen []Seen) ([]string, error) {
	f.calls = append(f.calls, seen)
	skipped := f.skip
	f.skip = nil
	return skipped, nil
}

func TestNewRequiresAStore(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{}); err == nil {
		t.Fatal("New accepted a missing store")
	}
}

// Touches are aggregated per host into one MarkSeen with the latest time, and a skipped host is written again by
// the next flush.
func TestHostsAreAggregatedAndSkippedOnesRetried(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{skip: []string{"a.example.test"}}
	r, err := New(Config{Store: store, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	r.Touch("a.example.test")
	first := now
	now = now.Add(time.Second)
	r.Touch("a.example.test")
	r.Touch("b.example.test")
	if err := r.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(store.calls) != 1 || len(store.calls[0]) != 2 {
		t.Fatalf("calls = %v, want one statement with two hosts", store.calls)
	}
	if r.Pending() != 1 {
		t.Fatalf("pending = %d, want the skipped host", r.Pending())
	}
	if err := r.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := store.calls[1]; len(got) != 1 || got[0].Host != "a.example.test" || got[0].At.Equal(first) {
		t.Fatalf("retry wrote %v, want a at its latest time", got)
	}
}
