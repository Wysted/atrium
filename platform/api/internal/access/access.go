// Package access holds the authorization domain: an AuthZEN-shaped request and
// a decision function. It has no knowledge of HTTP, JWT or storage.
package access

import (
	"context"
	"fmt"
	"strings"
)

// ActionAccess is the only action supported today: reaching an app.
const ActionAccess = "access"

// ResourceTypeApp identifies a resource addressed by its deployment host.
const ResourceTypeApp = "app"

// Deny reasons (and the single allow reason), stable strings for logs and denial records.
const (
	ReasonGranted            = "granted"
	ReasonUnknownHost        = "unknown_host"
	ReasonUnknownUser        = "unknown_user"
	ReasonUserDisabled       = "user_disabled"
	ReasonInitiativeInactive = "initiative_inactive"
	ReasonAppDisabled        = "app_disabled"
	ReasonNoGrant            = "no_grant"
	ReasonUnsupportedAction  = "unsupported_action"
)

// Status values mirrored from the database CHECK constraints.
const (
	UserStatusActive       = "active"
	InitiativeStatusActive = "active"
	AppStatusActive        = "active"
)

// PlatformInitiativeKey is the reserved initiative that owns the portal app. The portal exception only applies
// to an app of this initiative, so a mistyped PLATFORM_PORTAL_APP_ID cannot open another initiative's app.
const PlatformInitiativeKey = "platform"

// Subject is the user asking for access. ID is the internal user UUID.
type Subject struct {
	ID string
}

// Resource is the target of the request. For apps, ID is the deployment host.
type Resource struct {
	Type string
	ID   string
}

// Action is what the subject wants to do.
type Action struct {
	Name string
}

// Request follows the AuthZEN shape (subject, resource, action, context).
type Request struct {
	Subject  Subject
	Resource Resource
	Action   Action
	Context  map[string]any
}

// Decision is the outcome. AppID and InitiativeID are set whenever the host is known.
type Decision struct {
	Allow        bool
	Reason       string
	AppID        string
	InitiativeID string
	Roles        []string
	// PortalApp is the portal fact of the resolved app (Facts.PortalApp), set whenever the host is known, allow or
	// deny: ForwardAuth uses it to decide whether an Authorization header is read at all.
	PortalApp bool
}

// Facts are the stored data needed to decide, loaded in a single repository call.
// Grants must already be filtered to non-expired ones, direct or through groups.
type Facts struct {
	HostKnown        bool
	AppID            string
	InitiativeID     string
	InitiativeKey    string
	InitiativeStatus string
	AppStatus        string
	// PortalApp is set by the Evaluator when the resolved app is the portal app (docs/reference/api.md). It is a fact, not
	// configuration, so Decide stays a pure function of its arguments.
	PortalApp  bool
	UserFound  bool
	UserStatus string
	Roles      []string // distinct role keys of the initiative that owns the app, sorted
}

// FactsReader is the consumer-side port implemented by the storage adapter.
type FactsReader interface {
	AccessFacts(ctx context.Context, userID, host string) (Facts, error)
}

// NormalizeHost lowercases and trims the host, the form stored in deployments.host.
func NormalizeHost(host string) string {
	return strings.ToLower(strings.TrimSpace(host))
}

// Decide is the pure decision function. It fails closed: anything not explicitly granted is a deny.
// Precedence of deny reasons (first match wins): unknown_host, unknown_user, user_disabled,
// initiative_inactive, app_disabled, then no_grant. The portal app (Facts.PortalApp) skips the grant check:
// any active user gets an allow with empty roles, provided the app and its initiative are active.
// Platform administrators get no special treatment here; admin does not imply app access.
func Decide(req Request, f Facts) Decision {
	if req.Action.Name != ActionAccess {
		return Decision{Reason: ReasonUnsupportedAction}
	}
	if !f.HostKnown {
		return Decision{Reason: ReasonUnknownHost}
	}
	d := Decision{AppID: f.AppID, InitiativeID: f.InitiativeID, PortalApp: f.PortalApp}
	switch {
	case !f.UserFound:
		d.Reason = ReasonUnknownUser
	case f.UserStatus != UserStatusActive:
		d.Reason = ReasonUserDisabled
	case f.InitiativeStatus != InitiativeStatusActive:
		d.Reason = ReasonInitiativeInactive
	case f.AppStatus != AppStatusActive:
		d.Reason = ReasonAppDisabled
	case f.PortalApp:
		d.Allow = true
		d.Reason = ReasonGranted
		d.Roles = []string{}
	case len(f.Roles) == 0:
		d.Reason = ReasonNoGrant
	default:
		d.Allow = true
		d.Reason = ReasonGranted
		d.Roles = f.Roles
	}
	return d
}

// Evaluator loads facts and applies Decide.
type Evaluator struct {
	facts       FactsReader
	portalAppID string
}

// EvaluatorOption customizes an Evaluator.
type EvaluatorOption func(*Evaluator)

// WithPortalApp marks appID as the portal app (PLATFORM_PORTAL_APP_ID); empty means there is none.
func WithPortalApp(appID string) EvaluatorOption {
	return func(e *Evaluator) { e.portalAppID = appID }
}

// NewEvaluator builds an Evaluator over a FactsReader.
func NewEvaluator(facts FactsReader, opts ...EvaluatorOption) *Evaluator {
	e := &Evaluator{facts: facts}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Evaluate decides a request. A storage error is returned as an error, never as an allow;
// callers must fail closed.
func (e *Evaluator) Evaluate(ctx context.Context, req Request) (Decision, error) {
	if req.Action.Name != ActionAccess {
		return Decide(req, Facts{}), nil
	}
	req.Resource.ID = NormalizeHost(req.Resource.ID)
	facts, err := e.facts.AccessFacts(ctx, req.Subject.ID, req.Resource.ID)
	if err != nil {
		return Decision{}, fmt.Errorf("load access facts: %w", err)
	}
	facts.PortalApp = e.portalAppID != "" && facts.HostKnown && facts.AppID == e.portalAppID &&
		facts.InitiativeKey == PlatformInitiativeKey
	return Decide(req, facts), nil
}
