package keys_test

import (
	"slices"
	"testing"
	"time"

	"platform/api/internal/keys"
)

var t0 = time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)

func rec(kid string, state keys.State, created time.Time, mutate func(*keys.Record)) keys.Record {
	r := keys.Record{KID: kid, State: state, CreatedAt: created}
	switch state {
	case keys.StateActive:
		r.ActivatedAt = &created
	}
	if mutate != nil {
		mutate(&r)
	}
	return r
}

func at(t time.Time) func(*keys.Record) {
	return func(r *keys.Record) { r.RetiredAt = &t }
}

func TestPlan(t *testing.T) {
	const day = 24 * time.Hour
	activeAge := func(d time.Duration) keys.Record { return rec("A", keys.StateActive, t0.Add(-d), nil) }

	// readyNext was published long ago; with it present the plan never needs to create another next.
	readyNext := rec("N", keys.StateNext, t0.Add(-day), nil)
	tests := []struct {
		name string
		now  time.Time
		recs []keys.Record
		want []keys.Action
	}{
		{"empty store bootstraps an active key and a next", t0, nil,
			[]keys.Action{{Kind: keys.ActionCreateActive}, {Kind: keys.ActionCreateNext}}},
		{"young active key without next: create next now", t0, []keys.Record{activeAge(day)},
			[]keys.Action{{Kind: keys.ActionCreateNext}}},
		{"young active key with next: nothing", t0, []keys.Record{activeAge(day), readyNext}, nil},
		{"existing unready next: no second next", t0, []keys.Record{
			activeAge(keys.ActiveDuration - time.Minute),
			rec("N", keys.StateNext, t0.Add(-time.Minute), nil)}, nil},
		{"next published < 1h is not promoted even if active expired", t0, []keys.Record{
			activeAge(keys.ActiveDuration + day),
			rec("N", keys.StateNext, t0.Add(-keys.PrepublishDuration+time.Second), nil)}, nil},
		{"next ready but active not expired: wait", t0, []keys.Record{
			activeAge(keys.ActiveDuration - time.Minute),
			rec("N", keys.StateNext, t0.Add(-keys.PrepublishDuration), nil)}, nil},
		{"swap when both conditions hold, then a new next", t0, []keys.Record{
			activeAge(keys.ActiveDuration),
			rec("N", keys.StateNext, t0.Add(-keys.PrepublishDuration), nil)},
			[]keys.Action{{Kind: keys.ActionRetire, KID: "A"}, {Kind: keys.ActionPromote, KID: "N"}, {Kind: keys.ActionCreateNext}}},
		{"oldest ready next wins; another next remains so none is created", t0, []keys.Record{
			activeAge(keys.ActiveDuration),
			rec("N2", keys.StateNext, t0.Add(-2*time.Hour), nil),
			rec("N1", keys.StateNext, t0.Add(-3*time.Hour), nil)},
			[]keys.Action{{Kind: keys.ActionRetire, KID: "A"}, {Kind: keys.ActionPromote, KID: "N1"}}},
		{"overdue active with no next: create next, keep signing", t0, []keys.Record{activeAge(keys.ActiveDuration + 10*day)},
			[]keys.Action{{Kind: keys.ActionCreateNext}}},
		{"no active, ready next: promote and replace it", t0, []keys.Record{
			rec("N", keys.StateNext, t0.Add(-keys.PrepublishDuration), nil)},
			[]keys.Action{{Kind: keys.ActionPromote, KID: "N"}, {Kind: keys.ActionCreateNext}}},
		{"no active, unready next: create active directly", t0, []keys.Record{
			rec("N", keys.StateNext, t0.Add(-time.Minute), nil)},
			[]keys.Action{{Kind: keys.ActionCreateActive}}},
		{"retired < 1h stays", t0, []keys.Record{
			activeAge(day), readyNext,
			rec("R", keys.StateRetired, t0.Add(-day), at(t0.Add(-keys.RetiredDuration+time.Second)))}, nil},
		{"retired >= 1h is removed", t0, []keys.Record{
			activeAge(day), readyNext,
			rec("R", keys.StateRetired, t0.Add(-day), at(t0.Add(-keys.RetiredDuration)))},
			[]keys.Action{{Kind: keys.ActionRemove, KID: "R"}}},
		{"retired without timestamp is removed", t0, []keys.Record{
			activeAge(day), readyNext, rec("R", keys.StateRetired, t0.Add(-day), nil)},
			[]keys.Action{{Kind: keys.ActionRemove, KID: "R"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := keys.Plan(tt.now, tt.recs)
			if !slices.Equal(got, tt.want) {
				t.Errorf("Plan() = %v, want %v", got, tt.want)
			}
		})
	}
}
