// Package audit records security-relevant events (contract block 5, rule 9) in the append-only
// audit_events table. Events never carry tokens, cookies or other secrets: only ids and coarse context.
package audit

import (
	"context"
	"log/slog"
	"time"
)

// Actions and target types written to audit_events.
const (
	ActionLogin   = "session.login"  // a central session was created
	ActionLogout  = "session.logout" // the user closed their session
	ActionRevoke  = "session.revoke" // a session was closed by the platform (e.g. replaced by a new login)
	TargetSession = "session"
)

// Write timeout for the best-effort insert: an audit failure is logged and never changes the response.
const WriteTimeout = 2 * time.Second

// Event is one audit row. ActorID is the user id (empty: unknown); TargetID is the session id for
// session events. InitiativeID scopes the event to an initiative (empty: NULL, a global target). Before and
// After are marshaled to jsonb (nil: NULL).
type Event struct {
	ActorID      string
	InitiativeID string
	Action       string
	TargetType   string
	TargetID     string
	Before       any
	After        any
}

// Recorder appends events to the audit log.
type Recorder interface {
	RecordAudit(ctx context.Context, e Event) error
}

// Write appends e on a context that outlives the request (the client may disconnect right after the
// action) bounded by WriteTimeout. A failure is logged with the ids needed to reconstruct the event.
func Write(ctx context.Context, rec Recorder, log *slog.Logger, e Event) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), WriteTimeout)
	defer cancel()
	if err := rec.RecordAudit(ctx, e); err != nil {
		log.Error("cannot record audit event", "error", err.Error(), "action", e.Action, "actor_id", e.ActorID, "target_id", e.TargetID)
	}
}
