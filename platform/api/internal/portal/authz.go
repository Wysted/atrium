package portal

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"platform/api/internal/adminz"
	"platform/api/internal/audit"
)

// subject loads the caller's permissions through the transaction and builds the adminz subject. An unknown or
// disabled caller fails with ErrUserInactive. This is the only way the use cases obtain a subject.
func subject(ctx context.Context, tx Tx, callerID string) (adminz.Subject, error) {
	p, err := tx.Permissions().LoadPermissions(ctx, callerID)
	if errors.Is(err, ErrNotFound) {
		return adminz.Subject{}, ErrUserInactive
	}
	if err != nil {
		return adminz.Subject{}, fmt.Errorf("load permissions: %w", err)
	}
	return p.Subject()
}

// authorize runs adminz.Decide and turns a refusal into ErrForbidden or ErrNotFound; an allowed decision is
// returned with the Scope it carries (listings).
func authorize(sub adminz.Subject, action adminz.Action, res adminz.Resource) (adminz.Decision, error) {
	d := adminz.Decide(sub, action, res)
	switch d.Outcome {
	case adminz.Allow:
		return d, nil
	case adminz.NotFound:
		return d, ErrNotFound
	default:
		return d, ErrForbidden
	}
}

// begin loads the caller's subject and authorizes the action in one step.
func begin(ctx context.Context, tx Tx, callerID string, action adminz.Action, res adminz.Resource) (adminz.Subject, adminz.Decision, error) {
	sub, err := subject(ctx, tx, callerID)
	if err != nil {
		return adminz.Subject{}, adminz.Decision{}, err
	}
	d, err := authorize(sub, action, res)
	return sub, d, err
}

// tokenKey carries the id of the API token that authenticated the request through the context.
type tokenKey struct{}

// WithToken marks ctx as belonging to a request authenticated by the API token tokenID (the assertion's tid
// claim). Every audit row written under it records the token (audit_events.token_id), and the token endpoints
// that must not be reached with a token (creation) or that act on the current one read it. The HTTP layer sets
// it once, after the assertion and its scope were checked; a browser session never sets it.
func WithToken(ctx context.Context, tokenID string) context.Context {
	return context.WithValue(ctx, tokenKey{}, tokenID)
}

// TokenFrom returns the id set by WithToken, or "" for a browser session.
func TokenFrom(ctx context.Context) string {
	id, _ := ctx.Value(tokenKey{}).(string)
	return id
}

// event is an audit row under construction: every mutation writes exactly one, in its own transaction.
type event struct {
	actor        string
	action       adminz.Action
	targetType   string
	targetID     string
	initiativeID string
	before       any
	after        any
}

// record appends the event, with the API token of the request when there is one; its failure aborts the unit of work, so the mutation rolls back with it.
func record(ctx context.Context, tx Tx, e event) error {
	err := tx.Audit().RecordAudit(ctx, audit.Event{
		ActorID: e.actor, InitiativeID: e.initiativeID, TokenID: TokenFrom(ctx), Action: string(e.action),
		TargetType: e.targetType, TargetID: e.targetID, Before: e.before, After: e.after,
	})
	if err != nil {
		return fmt.Errorf("audit %s: %w", e.action, err)
	}
	return nil
}

// normalizeEmail trims the address; comparisons against users.email are case-insensitive in SQL.
func normalizeEmail(email string) string { return strings.TrimSpace(email) }

// Bounds of the free-text inputs.
const (
	maxEmailLength = 254 // users_email_length
	maxGroupKey    = 63
	maxGroupName   = 200
	// maxGroupSearch bounds the text a group listing filters by (characters, not bytes).
	maxGroupSearch = 64
)

// validEmail is a deliberately small syntactic check: the address is only used to look up an existing user.
func validEmail(email string) bool {
	if email == "" || len(email) > maxEmailLength || !cleanText(email) {
		return false
	}
	at := strings.IndexByte(email, '@')
	if at <= 0 || at == len(email)-1 || strings.Count(email, "@") != 1 {
		return false
	}
	for _, r := range email {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

// resolveActiveUser finds the one active user with that email. Zero and several matches fail the same way
// (a generic validation problem), so the answer never reveals which. The email is not unique in users:
// google_sub is the identity.
func resolveActiveUser(ctx context.Context, tx Tx, email string) (User, error) {
	if !validEmail(email) {
		return User{}, invalid("email", CodeInvalid)
	}
	users, err := tx.Users().FindActiveByEmail(ctx, email)
	if err != nil {
		return User{}, fmt.Errorf("resolve user by email: %w", err)
	}
	if len(users) != 1 {
		return User{}, invalid("email", CodeNoUniqueActiveUser)
	}
	return users[0], nil
}

// pageOf trims rows fetched with one extra row and builds the next cursor from the last row kept.
func pageOf[T any](rows []T, size int, kind string, cursorOf func(T) Cursor) Page[T] {
	if len(rows) <= size {
		return Page[T]{Items: rows}
	}
	rows = rows[:size]
	return Page[T]{Items: rows, NextCursor: encodeCursor(kind, cursorOf(rows[size-1]))}
}
