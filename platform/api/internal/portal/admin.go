package portal

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Wysted/atrium/platform/api/internal/adminz"
)

// Errors of the administration use cases. The HTTP layer maps them to problem documents; none of them carries
// data of the request or of other resources.
var (
	// ErrForbidden means the caller may not perform the action (403).
	ErrForbidden = errors.New("portal: forbidden")
	// ErrDuplicate is returned by repositories when a unique key already exists.
	ErrDuplicate = errors.New("portal: duplicate")
	// ErrInUse is returned by repositories when ON DELETE RESTRICT refuses to delete a row that is still referenced.
	ErrInUse = errors.New("portal: still referenced")
	// ErrInvalidCursor means the pagination cursor is malformed or was not issued by this listing (400).
	ErrInvalidCursor = errors.New("portal: invalid cursor")
)

// Conflict is a state conflict (409). Detail is a fixed text safe to show to the caller.
type Conflict struct{ Detail string }

func (c *Conflict) Error() string { return "portal: conflict: " + c.Detail }

// The conflicts of the use cases. They are compared by identity (errors.Is).
var (
	ErrGrantExists   = &Conflict{Detail: "a grant for this subject and role already exists"}
	ErrLastManager   = &Conflict{Detail: "the initiative must keep at least one effective manages_access grant"}
	ErrGroupKeyTaken = &Conflict{Detail: "a group with this key already exists"}
	ErrGroupInUse    = &Conflict{Detail: "the group still has members or grants"}
	ErrMemberExists  = &Conflict{Detail: "the user is already a member of the group"}
	ErrSelfDisable   = &Conflict{Detail: "an administrator cannot disable their own account"}
	ErrLastAdmin     = &Conflict{Detail: "the portal must keep at least one active platform administrator"}

	ErrInitiativeKeyTaken   = &Conflict{Detail: "an initiative with this key already exists"}
	ErrRoleKeyTaken         = &Conflict{Detail: "the initiative already has a role with this key"}
	ErrRoleInUse            = &Conflict{Detail: "the role still has grants"}
	ErrHostTaken            = &Conflict{Detail: "a deployment for this host already exists"}
	ErrPlatformInitiative   = &Conflict{Detail: "the platform initiative cannot be deactivated"}
	ErrPortalApp            = &Conflict{Detail: "the portal app cannot be disabled"}
	ErrLastPortalDeployment = &Conflict{Detail: "the portal app must keep at least one deployment"}
)

// ReferenceError is returned by repositories when a foreign key rejects a write; Field names the offending
// request field (role_id, group_id, user_id).
type ReferenceError struct{ Field string }

func (e *ReferenceError) Error() string { return "portal: unknown reference: " + e.Field }

// Validation problem codes. They are fixed texts: validation never echoes the input.
const (
	CodeRequired           = "required"
	CodeInvalid            = "invalid"
	CodeNotInTheFuture     = "must_be_in_the_future"
	CodeNoUniqueActiveUser = "no_unique_active_user"
	CodeUnknown            = "unknown"
	CodeExclusive          = "exactly_one_required"
	CodeReserved           = "reserved"
	CodeAtLeastOne         = "at_least_one_required"
)

// FieldProblem is one invalid field of a request.
type FieldProblem struct {
	Field string
	Code  string
}

// ValidationError lists the invalid fields of a request (422).
type ValidationError struct{ Problems []FieldProblem }

func (e *ValidationError) Error() string { return "portal: invalid request" }

func invalid(field, code string) *ValidationError {
	return &ValidationError{Problems: []FieldProblem{{Field: field, Code: code}}}
}

// Pagination. Lists use keyset pagination with an opaque cursor (contract block 6).
const (
	DefaultPageSize = 50
	MaxPageSize     = 200
)

// PageRequest is what the caller asks of a listing: the opaque cursor of the previous page (empty for the
// first) and the page size (zero means DefaultPageSize; above MaxPageSize it is capped).
type PageRequest struct {
	Cursor string
	Limit  int
}

// Page is one page of a listing; NextCursor is empty on the last page.
type Page[T any] struct {
	Items      []T
	NextCursor string
}

// PageQuery is the page a repository returns: the rows after the keyset position, at most Limit of them.
// The service asks for one row more than the page size to know whether there is a next page.
type PageQuery struct {
	After *Cursor
	Limit int
	// Search is the validated text a group listing filters by (case-insensitive substring of key or name);
	// empty means no filter. Other listings ignore it.
	Search string
	// Status is the validated status a robot listing filters by (UserStatusActive or UserStatusDisabled); empty
	// means no filter. Other listings ignore it.
	Status string
}

// uuidPattern is the canonical text form of an id; anything else is "not found" before it reaches SQL.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsUUID reports whether s is a canonical UUID.
func IsUUID(s string) bool { return uuidPattern.MatchString(s) }

// canonicalUUID returns the lowercase form of a UUID, which is how ids are stored and compared in adminz
// (a manager's own initiative must not turn into "not found" because the client sent uppercase hex).
func canonicalUUID(s string) (string, bool) {
	if !IsUUID(s) {
		return "", false
	}
	return strings.ToLower(s), true
}

// cleanText reports whether s is valid UTF-8 without NUL: PostgreSQL text cannot hold a NUL, and invalid
// UTF-8 would fail the insert with a 500 instead of a validation problem.
func cleanText(s string) bool { return utf8.ValidString(s) && !strings.ContainsRune(s, 0) }

// RoleRef identifies a role in a response.
type RoleRef struct {
	ID            string
	Key           string
	ManagesAccess bool
}

// UserRef identifies a user in a response.
type UserRef struct {
	ID    string
	Email string
	Name  string
}

// GroupRef identifies a group in a response.
type GroupRef struct {
	ID   string
	Key  string
	Name string
}

// Grant gives a user or a group a role in an initiative. Exactly one of User and Group is set.
type Grant struct {
	ID           string
	InitiativeID string
	Role         RoleRef
	User         *UserRef
	Group        *GroupRef
	ExpiresAt    *time.Time
	GrantedBy    string // empty when the granting user no longer exists
	GrantedAt    time.Time
}

// NewGrant is the request to grant a role. Exactly one of Email, GroupID and RobotID names the subject.
type NewGrant struct {
	Email     string
	GroupID   string
	RobotID   string
	RoleID    string
	ExpiresAt *time.Time
}

// GrantRow is a grant ready to be inserted; exactly one of UserID and GroupID is set.
type GrantRow struct {
	InitiativeID string
	RoleID       string
	UserID       string
	GroupID      string
	ExpiresAt    *time.Time
	GrantedBy    string
}

// ManagerGrant is a manages_access grant of an initiative, locked by LockManagerGrants. Effective means it
// currently makes at least one active user a manager (see Service.DeleteGrant). RoleID is the role it grants:
// turning manages_access off for a role removes every grant of that role from the managers.
type ManagerGrant struct {
	ID        string
	RoleID    string
	Effective bool
}

// Group is a group of users. Groups are global.
type Group struct {
	ID        string
	Key       string
	Name      string
	CreatedAt time.Time
}

// NewGroup is the request to create a group.
type NewGroup struct {
	Key  string
	Name string
}

// GroupMember is a user in a group.
type GroupMember struct {
	UserID  string
	Email   string
	Name    string
	Status  string
	AddedAt time.Time
}

// User is an account, as the administration sees it.
type User struct {
	ID        string
	Email     string
	Name      string
	Status    string
	CreatedAt time.Time
}

// User statuses (mirror the users.status CHECK).
const UserStatusDisabled = "disabled"

// AuditEvent is one row of the audit log. Before and After are the stored jsonb documents (nil: NULL).
type AuditEvent struct {
	ID           string
	OccurredAt   time.Time
	ActorID      string
	ActorEmail   string // empty for a robot
	ActorName    string // the robot's name, or the human's display name
	InitiativeID string
	Action       string
	TargetType   string
	TargetID     string
	Before       json.RawMessage
	After        json.RawMessage
	TokenID      string // the API token that made the request; empty for a browser session
	AreaID       string // the area a supervisor acted through; empty for every other event
}

// AuditQuery filters the audit listing. InitiativeID is optional.
type AuditQuery struct {
	InitiativeID string
	Page         PageRequest
}

// Target types written to audit_events by the portal.
const (
	TargetGrant        = "grant"
	TargetGroup        = "group"
	TargetGroupMember  = "group_member"
	TargetUser         = "user"
	TargetUserSessions = "user_sessions"
	TargetInitiative   = "initiative"
	TargetRole         = "role"
	TargetApp          = "app"
	TargetDeployment   = "deployment"
	// TargetOrphanHost is the resource type of the orphan host listing (nothing is written for it).
	TargetOrphanHost = "orphan_host"
)

// GrantRepo is the storage of grants. Listing methods take the Scope the caller may see as a required
// parameter: a handler cannot list without one, and the zero Scope lists nothing.
type GrantRepo interface {
	InitiativeExists(ctx context.Context, id string) (bool, error)
	// GetGrant returns ErrNotFound for an unknown id.
	GetGrant(ctx context.Context, id string) (Grant, error)
	ListGrants(ctx context.Context, scope adminz.Scope, q PageQuery) ([]Grant, error)
	// InsertGrant returns ErrDuplicate for an existing (initiative, role, subject) and a *ReferenceError when
	// the role does not belong to the initiative or the subject does not exist.
	InsertGrant(ctx context.Context, row GrantRow) (string, error)
	// DeleteGrant reports whether a row was deleted.
	DeleteGrant(ctx context.Context, id string) (bool, error)
	// LockInitiative takes the initiative row lock that serializes grant revocations; ErrNotFound when the
	// initiative does not exist. It is always the first lock of a revocation.
	LockInitiative(ctx context.Context, id string) error
	// LockInitiativeShared takes the same row in share mode: grant creations run concurrently with each other
	// but wait for a revocation in progress (and the other way round). ErrNotFound when it does not exist.
	LockInitiativeShared(ctx context.Context, id string) error
	// LockManagerGrants locks (SELECT ... FOR UPDATE, ordered by id) the manages_access grants of the
	// initiative and reports which of them are effective.
	LockManagerGrants(ctx context.Context, initiativeID string) ([]ManagerGrant, error)
}

// GroupRepo is the storage of groups and their members.
type GroupRepo interface {
	// ListGroups filters by q.Search when it is not empty.
	ListGroups(ctx context.Context, scope adminz.Scope, q PageQuery) ([]Group, error)
	// GetGroup returns ErrNotFound for an unknown id.
	GetGroup(ctx context.Context, id string) (Group, error)
	// LockGroup is GetGroup with a row lock (FOR UPDATE).
	LockGroup(ctx context.Context, id string) (Group, error)
	// InsertGroup returns ErrDuplicate when the key exists.
	InsertGroup(ctx context.Context, key, name string) (Group, error)
	// GroupUsage counts the members and the grants of a group.
	GroupUsage(ctx context.Context, id string) (members, grants int, err error)
	DeleteGroup(ctx context.Context, id string) error
	ListMembers(ctx context.Context, scope adminz.Scope, groupID string, q PageQuery) ([]GroupMember, error)
	// InsertMember returns when the user was added; ErrDuplicate for an existing member and a *ReferenceError
	// for an unknown group or user.
	InsertMember(ctx context.Context, groupID, userID string) (time.Time, error)
	// DeleteMember reports whether a row was deleted.
	DeleteMember(ctx context.Context, groupID, userID string) (bool, error)
}

// UserRepo is the storage of human users (the portal reads them and changes only their status). The lookups and
// locks (ListUsersByEmail, FindActiveByEmail, GetUser, LockUser, LockActiveAdmins) see humans only: a robot id is
// ErrNotFound. SetStatus, DeleteSessions and RevokeTokens operate on an id already locked by the caller and do
// not filter by kind, so the robot path can reuse them.
type UserRepo interface {
	// ListUsersByEmail returns up to limit users whose email matches case-insensitively.
	ListUsersByEmail(ctx context.Context, scope adminz.Scope, email string, limit int) ([]User, error)
	// FindActiveByEmail returns up to two active users with that email, enough to tell "exactly one".
	FindActiveByEmail(ctx context.Context, email string) ([]User, error)
	// GetUser returns ErrNotFound for an unknown id.
	GetUser(ctx context.Context, id string) (User, error)
	// LockUser is GetUser with a row lock (FOR NO KEY UPDATE).
	LockUser(ctx context.Context, id string) (User, error)
	// LockActiveAdmins locks (FOR NO KEY UPDATE, ordered by id) the rows of the active platform admins and
	// returns their ids.
	LockActiveAdmins(ctx context.Context) ([]string, error)
	SetStatus(ctx context.Context, id, status string) error
	// DeleteSessions deletes every session of the user (app sessions cascade) and returns how many central
	// sessions were removed. It runs in the caller's transaction, unlike session.Service.RevokeUser.
	DeleteSessions(ctx context.Context, userID string) (int64, error)
	// RevokeTokens revokes every live API token of the user (revoked_at) and returns their ids, sorted. It runs
	// in the caller's transaction, after the user was locked. The one implementation behind "disabling revokes
	// the tokens", for humans and robots alike.
	RevokeTokens(ctx context.Context, userID string) ([]string, error)
}

// AuditReader lists audit events newest first; PageQuery.After is a position in (occurred_at, id) order.
type AuditReader interface {
	ListAudit(ctx context.Context, scope adminz.Scope, q PageQuery) ([]AuditEvent, error)
}

// Cursor kinds: a cursor is only valid for the listing that issued it.
const (
	cursorKindGrants  = "grants"
	cursorKindGroups  = "groups"
	cursorKindMembers = "members"
	cursorKindAudit   = "audit"
	cursorKindTokens  = "tokens"
	cursorKindRobots  = "robots"
)
