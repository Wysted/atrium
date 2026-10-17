package portal

import (
	"context"
	"time"

	"github.com/Wysted/atrium/platform/api/internal/access"
	"github.com/Wysted/atrium/platform/api/internal/adminz"
)

// PlatformInitiativeKey is the reserved initiative that owns the portal app (see access.PlatformInitiativeKey).
const PlatformInitiativeKey = access.PlatformInitiativeKey

// Statuses of the catalog (mirror the CHECK constraints of initiatives and apps).
const (
	InitiativeStatusActive   = "active"
	InitiativeStatusInactive = "inactive"
	AppStatusActive          = "active"
	AppStatusDisabled        = "disabled"
)

// Initiative is an initiative of the catalog.
type Initiative struct {
	ID        string
	Key       string
	Name      string
	Status    string
	CreatedAt time.Time
}

// NewInitiative is the request to create an initiative. The key is immutable afterwards.
type NewInitiative struct {
	Key  string
	Name string
}

// InitiativePatch is the request to change an initiative; nil fields stay as they are.
type InitiativePatch struct {
	Name   *string
	Status *string
}

// Role is a role of an initiative.
type Role struct {
	ID            string
	InitiativeID  string
	Key           string
	ManagesAccess bool
}

// NewRole is the request to create a role. The key is immutable afterwards: it is part of the apps' contract.
type NewRole struct {
	Key           string
	ManagesAccess bool
}

// RolePatch is the request to change a role; nil fields stay as they are.
type RolePatch struct {
	ManagesAccess *bool
}

// Deployment maps a host to an app. InitiativeID is the initiative that owns the app. The last three fields
// are integration telemetry (docs/reference/metrics.md, "last_seen_at"): when platform-api last authorized a request on
// the host, and when and with what result the portal last probed it. Nil and empty mean "never".
type Deployment struct {
	ID              string
	AppID           string
	InitiativeID    string
	Host            string
	CreatedAt       time.Time
	LastSeenAt      *time.Time
	LastProbeAt     *time.Time
	LastProbeStatus string // one of the Probe* constants, empty when never probed
}

// App is an app of an initiative with its deployments (ordered by host).
type App struct {
	ID            string
	InitiativeID  string
	InitiativeKey string
	Name          string
	Status        string
	CreatedAt     time.Time
	Deployments   []Deployment
	// PortalApp is filled by the Service (isPortalApp), never by the repo: the UI hides the actions its lockout
	// guards refuse.
	PortalApp bool
}

// NewApp is the request to create an app; its id is generated.
type NewApp struct {
	Name string
}

// AppPatch is the request to change an app; nil fields stay as they are. The initiative is immutable.
type AppPatch struct {
	Name   *string
	Status *string
}

// NewDeployment is the request to attach a host to an app.
type NewDeployment struct {
	Host string
}

// CatalogRepo is the storage of initiatives, roles, apps and deployments. Listing methods take the Scope the
// caller may see as a required parameter, like the other ports. The Get methods return ErrNotFound for an
// unknown id.
type CatalogRepo interface {
	ListInitiatives(ctx context.Context, scope adminz.Scope, q PageQuery) ([]Initiative, error)
	GetInitiative(ctx context.Context, id string) (Initiative, error)
	// InsertInitiative returns ErrDuplicate when the key exists.
	InsertInitiative(ctx context.Context, key, name string) (Initiative, error)
	UpdateInitiative(ctx context.Context, id, name, status string) error

	ListRoles(ctx context.Context, scope adminz.Scope, q PageQuery) ([]Role, error)
	GetRole(ctx context.Context, id string) (Role, error)
	// InsertRole returns ErrDuplicate when the initiative already has the key and a *ReferenceError when the
	// initiative does not exist.
	InsertRole(ctx context.Context, initiativeID, key string, managesAccess bool) (Role, error)
	SetRoleManagesAccess(ctx context.Context, id string, managesAccess bool) error
	// DeleteRole reports whether a row was deleted; a *ReferenceError means grants still use the role.
	DeleteRole(ctx context.Context, id string) (bool, error)

	// ListApps returns the apps with their deployments.
	ListApps(ctx context.Context, scope adminz.Scope, q PageQuery) ([]App, error)
	GetApp(ctx context.Context, id string) (App, error)
	// LockApp is GetApp with a row lock (FOR NO KEY UPDATE). The app row is a leaf of the lock order: nothing is
	// locked while holding it.
	LockApp(ctx context.Context, id string) (App, error)
	// InsertApp stores the app (its Deployments are ignored); a *ReferenceError means the initiative vanished.
	InsertApp(ctx context.Context, a App) error
	UpdateApp(ctx context.Context, id, name, status string) error

	GetDeployment(ctx context.Context, id string) (Deployment, error)
	CountDeployments(ctx context.Context, appID string) (int, error)
	// InsertDeployment returns ErrDuplicate when the host exists and a *ReferenceError when the app vanished.
	InsertDeployment(ctx context.Context, appID, host string) (Deployment, error)
	// DeleteDeployment reports whether a row was deleted.
	DeleteDeployment(ctx context.Context, id string) (bool, error)
}

// Cursor kinds of the catalog listings.
const (
	cursorKindInitiatives = "initiatives"
	cursorKindRoles       = "roles"
	cursorKindApps        = "apps"
)
