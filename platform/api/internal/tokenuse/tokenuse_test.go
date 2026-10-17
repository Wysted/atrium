package tokenuse

import (
	"context"
	"testing"
	"time"
)

type fakeStore struct {
	calls [][]Used
	skip  []string
}

func (f *fakeStore) MarkUsed(_ context.Context, used []Used) ([]string, error) {
	f.calls = append(f.calls, used)
	skipped := f.skip
	f.skip = nil
	return skipped, nil
}

func TestNewRequiresAStore(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{}); err == nil {
		t.Fatal("New without a store succeeded")
	}
}

// Touches are aggregated per token into one MarkUsed with the latest time, and a skipped token is written again
// by the next flush.
func TestTokensAreAggregatedAndSkippedOnesRetried(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{skip: []string{"t1"}}
	r, err := New(Config{Store: store, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	r.Touch("t1")
	first := now
	now = now.Add(time.Second)
	r.Touch("t1")
	r.Touch("t2")
	if err := r.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(store.calls) != 1 || len(store.calls[0]) != 2 {
		t.Fatalf("calls = %v, want one statement with two tokens", store.calls)
	}
	if r.Pending() != 1 {
		t.Fatalf("pending = %d, want the skipped token", r.Pending())
	}
	if err := r.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := store.calls[1]; len(got) != 1 || got[0].ID != "t1" || got[0].At.Equal(first) {
		t.Fatalf("retry wrote %v, want t1 at its latest time", got)
	}
}
