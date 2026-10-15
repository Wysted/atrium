package keys

import (
	"cmp"
	"slices"
	"time"
)

// Lifecycle durations from block 3 of the contract.
const (
	// PrepublishDuration is how long a next key is published before it may sign.
	PrepublishDuration = time.Hour
	// ActiveDuration is how long a key signs before the next one takes over.
	ActiveDuration = 30 * 24 * time.Hour
	// RetiredDuration covers token TTL + leeway + JWKS cache after a key stops signing.
	RetiredDuration = time.Hour
)

// ActionKind is one step of a rotation plan.
type ActionKind string

const (
	ActionCreateActive ActionKind = "create_active" // bootstrap: no signer exists
	ActionCreateNext   ActionKind = "create_next"
	ActionRetire       ActionKind = "retire"
	ActionPromote      ActionKind = "promote"
	ActionRemove       ActionKind = "remove"
)

// Action is a step of the plan; KID is empty for create actions (the key does not exist yet).
type Action struct {
	Kind ActionKind
	KID  string
}

// Plan decides what the rotator must do at now given the non-removed keys. It is a pure function.
//
// Rules: retired keys older than RetiredDuration are removed; with no active key a ready next is
// promoted (or a new active key is created); once the active key is past ActiveDuration and a next
// key has been published for PrepublishDuration, they swap. Whenever the plan leaves no next key
// (bootstrap, right after a promotion) it creates one, so a long-published next key is always
// available for an emergency revocation. If the rotator was down and the active key is overdue, it
// keeps signing until the next key is ready: signing never stops.
func Plan(now time.Time, recs []Record) []Action {
	var (
		actions []Action
		active  *Record
		nexts   []Record
	)
	for i := range recs {
		r := recs[i]
		switch r.State {
		case StateActive:
			active = &recs[i]
		case StateNext:
			nexts = append(nexts, r)
		case StateRetired:
			if !now.Before(retiredSince(r).Add(RetiredDuration)) {
				actions = append(actions, Action{Kind: ActionRemove, KID: r.KID})
			}
		}
	}
	slices.SortFunc(nexts, func(a, b Record) int { return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.KID, b.KID)) })

	var ready *Record
	for i := range nexts {
		if !now.Before(nexts[i].CreatedAt.Add(PrepublishDuration)) {
			ready = &nexts[i]
			break
		}
	}

	remainingNexts := len(nexts)
	switch {
	case active == nil && ready != nil:
		actions = append(actions, Action{Kind: ActionPromote, KID: ready.KID})
		remainingNexts--
	case active == nil:
		actions = append(actions, Action{Kind: ActionCreateActive})
	case ready != nil && !now.Before(activeSince(*active).Add(ActiveDuration)):
		actions = append(actions,
			Action{Kind: ActionRetire, KID: active.KID},
			Action{Kind: ActionPromote, KID: ready.KID})
		remainingNexts--
	}
	if remainingNexts == 0 {
		actions = append(actions, Action{Kind: ActionCreateNext})
	}
	return actions
}

// activeSince falls back to creation time if the timestamp is missing (should not happen).
func activeSince(r Record) time.Time {
	if r.ActivatedAt != nil {
		return *r.ActivatedAt
	}
	return r.CreatedAt
}

// retiredSince treats a missing timestamp as long ago, so such a key is removed on the next tick.
func retiredSince(r Record) time.Time {
	if r.RetiredAt != nil {
		return *r.RetiredAt
	}
	return time.Time{}
}
