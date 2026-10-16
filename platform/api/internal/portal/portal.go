// Package portal holds the application layer of the portal process (contract block 6): the data it exposes,
// the small ports it needs from storage and the use-case service the HTTP handlers call. It knows nothing
// about HTTP or SQL.
//
// Mutations (later parts) follow one shape: a service method opens Store.WithTx, loads the caller's
// Permissions through the transaction's repositories, asks adminz.Decide, mutates and writes the audit row
// in that same transaction. Invariants that need serialization (the last manages_access grant of an
// initiative) take their locks with SELECT ... FOR UPDATE inside that transaction, always in the same order
// (initiative row first, then its grants ordered by id), so concurrent mutations cannot deadlock each other.
//
// Mutations must be idempotent by natural key (for example "grant of role R to user U on initiative I" is
// unique): a commit whose context was canceled can be ambiguous, the transaction may have committed although
// the caller saw an error, and the client's retry must find the row already there and answer as success
// instead of creating a duplicate or failing with a conflict.
package portal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"platform/api/internal/adminz"
	"platform/api/internal/audit"
)

// ErrUserInactive means the caller does not exist or is disabled. The assertion outlives a disable by up to
// its TTL, so the portal re-checks the user on every request.
var ErrUserInactive = errors.New("portal: user is unknown or disabled")

// ErrContention means a unit of work lost against concurrent transactions (deadlock or serialization failure)
// on every attempt. It is transient: the API answers 503 with Retry-After.
var ErrContention = errors.New("portal: too much contention, retry later")

// UserStatusActive mirrors the users.status CHECK.
const UserStatusActive = "active"

// InitiativeRef identifies an initiative in a response.
type InitiativeRef struct {
	ID   string
	Key  string
	Name string
}

// Permissions is what the portal knows about the caller, loaded from the database on every request.
type Permissions struct {
	UserID        string
	Email         string
	Name          string
	Status        string
	PlatformAdmin bool
	// Managed are the initiatives where a non-expired grant with manages_access exists (direct or through a
	// group), sorted by key.
	Managed []InitiativeRef
}

// ManagedIDs returns the ids of the managed initiatives (the input of Subject).
func (p Permissions) ManagedIDs() []string {
	ids := make([]string, 0, len(p.Managed))
	for _, m := range p.Managed {
		ids = append(ids, m.ID)
	}
	return ids
}

// Subject returns the adminz subject of these permissions. It fails with ErrUserInactive unless the user is
// active, so a disabled user (an administrator included) can never obtain a subject that adminz.Decide allows.
func (p Permissions) Subject() (adminz.Subject, error) {
	if p.Status != UserStatusActive {
		return adminz.Subject{}, ErrUserInactive
	}
	return adminz.ActiveSubject(p.UserID, p.PlatformAdmin, p.ManagedIDs()), nil
}

// HomeApp is one app the caller can open, with every host it is deployed on.
type HomeApp struct {
	ID         string
	Name       string
	Hosts      []string
	Initiative InitiativeRef
	Roles      []string
}

// PermissionsReader loads the permissions of a user in one query. It returns ErrNotFound when the user does
// not exist.
type PermissionsReader interface {
	LoadPermissions(ctx context.Context, userID string) (Permissions, error)
}

// ErrNotFound is returned by readers when the requested row does not exist.
var ErrNotFound = errors.New("portal: not found")

// HomeReader lists the apps a user can open. It applies exactly the rules of access.Decide.
type HomeReader interface {
	// HomeApps returns the apps userID can open, never including excludeAppID (the portal app itself).
	HomeApps(ctx context.Context, userID, excludeAppID string) ([]HomeApp, error)
}

// Tx exposes the repositories bound to one database transaction. Repositories never read a transaction from
// the context: whatever runs inside WithTx goes through the Tx it was handed.
type Tx interface {
	Permissions() PermissionsReader
	Grants() GrantRepo
	Catalog() CatalogRepo
	Groups() GroupRepo
	Users() UserRepo
	// Audit appends to the audit log inside the transaction: if the insert fails the mutation rolls back.
	Audit() audit.Recorder
	AuditLog() AuditReader
}

// Store runs units of work. fn may run more than once (the store retries deadlocks and serialization
// failures, see postgres.PortalStore), so it must not have side effects outside the transaction.
type Store interface {
	WithTx(ctx context.Context, fn func(tx Tx) error) error
}

// Config wires a Service.
type Config struct {
	Permissions PermissionsReader
	Home        HomeReader
	// Store runs the units of work of the administration use cases.
	Store Store
	// PortalAppID is the id of the portal app (hidden from the home: every user may open it, it is not one of
	// "their" apps; the administration refuses to disable it or to remove its last deployment). Empty means
	// there is none.
	PortalAppID string
	// AuthHost is the host of the auth server (from the issuer). No deployment may use it. Empty means none.
	AuthHost string
	// Now is the clock for "expires_at must be in the future"; nil means time.Now.
	Now func() time.Time
}

// Service implements the use cases of the portal API.
type Service struct {
	permissions PermissionsReader
	home        HomeReader
	store       Store
	portalAppID string
	authHost    string
	now         func() time.Time
}

// NewService builds a Service.
func NewService(cfg Config) (*Service, error) {
	if cfg.Permissions == nil || cfg.Home == nil || cfg.Store == nil {
		return nil, errors.New("portal: permissions and home readers and a store are required")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		permissions: cfg.Permissions, home: cfg.Home, store: cfg.Store, portalAppID: cfg.PortalAppID,
		authHost: strings.ToLower(cfg.AuthHost), now: now,
	}, nil
}

// Me returns the profile and permissions of the caller. It fails with ErrUserInactive for an unknown or
// disabled user.
func (s *Service) Me(ctx context.Context, userID string) (Permissions, error) {
	p, err := s.permissions.LoadPermissions(ctx, userID)
	if errors.Is(err, ErrNotFound) {
		return Permissions{}, ErrUserInactive
	}
	if err != nil {
		return Permissions{}, fmt.Errorf("load permissions: %w", err)
	}
	if p.Status != UserStatusActive {
		return Permissions{}, ErrUserInactive
	}
	return p, nil
}

// HomeApps returns the apps the caller can open. A disabled or unknown user gets an empty list (the query
// requires an active user, like access.Decide).
func (s *Service) HomeApps(ctx context.Context, userID string) ([]HomeApp, error) {
	apps, err := s.home.HomeApps(ctx, userID, s.portalAppID)
	if err != nil {
		return nil, fmt.Errorf("load home apps: %w", err)
	}
	return apps, nil
}
