// Package adminz answers "may this caller do this administrative action on this resource?" for the portal
// API (contract block 6, "Who administers what"). Like access.Decide it is a pure, AuthZEN-shaped function
// (subject, action, resource): it knows nothing about HTTP, JWT or storage, and it fails closed.
//
// Decide says who may attempt an action. It does not enforce invariants that depend on stored data; those run
// inside the mutation's transaction:
//   - "do not revoke the last manages_access grant of an initiative" belongs to the grant.delete use case,
//     which takes a row lock (SELECT ... FOR UPDATE, in a consistent order) on the initiative's manages_access
//     grants in the same transaction as the delete, so two managers revoking each other cannot both succeed;
//   - "grants by email resolve to exactly one active user", "a role is deleted only without grants" and the
//     like are validations of their use cases.
package adminz

import (
	"maps"
	"slices"
)

// Action names follow <resource>.<verb> and are the same names the audit log stores.
type Action string

// Mutations that only platform admins may perform.
const (
	InitiativeCreate     Action = "initiative.create"
	InitiativeUpdate     Action = "initiative.update"
	InitiativeActivate   Action = "initiative.activate"
	InitiativeDeactivate Action = "initiative.deactivate"

	RoleCreate Action = "role.create"
	RoleUpdate Action = "role.update"
	RoleDelete Action = "role.delete"

	AppCreate  Action = "app.create"
	AppUpdate  Action = "app.update"
	AppEnable  Action = "app.enable"
	AppDisable Action = "app.disable"

	DeploymentCreate Action = "deployment.create"
	DeploymentDelete Action = "deployment.delete"

	GroupCreate       Action = "group.create"
	GroupDelete       Action = "group.delete"
	GroupMemberAdd    Action = "group_member.add"
	GroupMemberRemove Action = "group_member.remove"

	UserDisable        Action = "user.disable"
	UserEnable         Action = "user.enable"
	UserSessionsRevoke Action = "user_sessions.revoke"
)

// Mutations that platform admins and the managers of the initiative may perform.
const (
	GrantCreate Action = "grant.create"
	GrantDelete Action = "grant.delete"
)

// Reads. The ones on an initiative resource are open to its managers; the global ones are admin-only.
const (
	InitiativeList  Action = "initiative.list" // each caller sees only what InitiativeRead allows
	InitiativeRead  Action = "initiative.read"
	RoleList        Action = "role.list"
	AppList         Action = "app.list"
	AppHealth       Action = "app.health"
	GrantList       Action = "grant.list"
	AuditList       Action = "audit.list"
	GroupList       Action = "group.list"        // names only: id, key, name
	GroupMemberList Action = "group_member.list" // admin only: who is in a group
	UserList        Action = "user.list"
)

// Outcome is the verdict of Decide.
type Outcome string

const (
	// Allow means the caller may perform the action.
	Allow Outcome = "allow"
	// Forbidden maps to 403 without any detail about other resources.
	Forbidden Outcome = "forbidden"
	// NotFound maps to 404: the resource belongs to an initiative the caller does not manage, so its
	// existence is not confirmed.
	NotFound Outcome = "not_found"
)

// Reasons, stable strings for logs.
const (
	ReasonPlatformAdmin     = "platform_admin"
	ReasonInitiativeManager = "initiative_manager"
	ReasonAdminOnly         = "admin_only"
	ReasonNotManager        = "not_manager"
	ReasonNoManagedScope    = "no_managed_initiative"
	ReasonMissingInitiative = "missing_initiative"
	ReasonUnknownAction     = "unknown_action"
	ReasonInactiveSubject   = "inactive_subject"
)

// Subject is the caller, as loaded from the database on every request (never from the assertion).
//
// A Subject is only active when built by ActiveSubject, which portal.Permissions.Subject calls for active users
// alone. The zero value, and any struct literal, is inactive and Decide forbids everything for it, so a disabled
// platform admin can never be allowed, whatever the code that builds the subject forgets.
type Subject struct {
	ID            string
	PlatformAdmin bool
	// Managed is the set of initiative ids where the caller holds a non-expired grant of a role with
	// manages_access, directly or through a group.
	Managed map[string]struct{}

	active bool
}

// ActiveSubject builds the Subject of an active user; managed may contain duplicates. Callers must not call it
// for a disabled or unknown user (portal.Permissions.Subject enforces that).
func ActiveSubject(id string, platformAdmin bool, managed []string) Subject {
	s := Subject{ID: id, PlatformAdmin: platformAdmin, Managed: make(map[string]struct{}, len(managed)), active: true}
	for _, m := range managed {
		s.Managed[m] = struct{}{}
	}
	return s
}

// Manages reports whether the subject manages the initiative.
func (s Subject) Manages(initiativeID string) bool {
	_, ok := s.Managed[initiativeID]
	return ok
}

// managedIDs returns the managed initiative ids, sorted.
func (s Subject) managedIDs() []string {
	return slices.Sorted(maps.Keys(s.Managed))
}

// Scope is the part of a listing a caller may see. Listing repository methods must take a Scope as a required
// parameter and filter by it, so a handler cannot query unscoped by mistake. The zero value (not All, no
// initiatives) matches nothing.
type Scope struct {
	// All means every row (platform admins, and global listings such as group names).
	All bool
	// Initiatives are the initiative ids visible when All is false, sorted.
	Initiatives []string
}

// Includes reports whether a row of the initiative is in scope.
func (s Scope) Includes(initiativeID string) bool {
	return s.All || slices.Contains(s.Initiatives, initiativeID)
}

// Empty reports whether nothing is in scope.
func (s Scope) Empty() bool { return !s.All && len(s.Initiatives) == 0 }

// Resource is the target of the action. InitiativeID is the initiative that owns it, resolved by the caller
// from storage (for example an app or a grant looked up by id); it is empty for global resources (groups,
// users, the list of initiatives) and for an unfiltered audit listing.
type Resource struct {
	Type         string
	ID           string
	InitiativeID string
}

// Decision is the outcome of Decide plus a reason for logs. Scope is set on an allowed listing (see Decide) and
// is the zero value, which matches nothing, everywhere else.
type Decision struct {
	Outcome Outcome
	Reason  string
	Scope   Scope
}

// scope says where an action applies.
type scope int

const (
	scopeGlobal     scope = iota // groups, users, creating initiatives: no initiative owns the target
	scopeInitiative              // the target belongs to one initiative
)

// rule is one row of the "Who administers what" table.
type rule struct {
	scope scope
	// managerAllowed: the managers of the initiative may perform it (only meaningful for scopeInitiative).
	managerAllowed bool
}

var rules = map[Action]rule{
	InitiativeCreate:     {scope: scopeGlobal},
	InitiativeUpdate:     {scope: scopeInitiative},
	InitiativeActivate:   {scope: scopeInitiative},
	InitiativeDeactivate: {scope: scopeInitiative},
	InitiativeRead:       {scope: scopeInitiative, managerAllowed: true},

	RoleCreate: {scope: scopeInitiative},
	RoleUpdate: {scope: scopeInitiative},
	RoleDelete: {scope: scopeInitiative},
	RoleList:   {scope: scopeInitiative, managerAllowed: true},

	AppCreate:  {scope: scopeInitiative},
	AppUpdate:  {scope: scopeInitiative},
	AppEnable:  {scope: scopeInitiative},
	AppDisable: {scope: scopeInitiative},
	AppList:    {scope: scopeInitiative, managerAllowed: true},
	AppHealth:  {scope: scopeInitiative, managerAllowed: true},

	DeploymentCreate: {scope: scopeInitiative},
	DeploymentDelete: {scope: scopeInitiative},

	GrantCreate: {scope: scopeInitiative, managerAllowed: true},
	GrantDelete: {scope: scopeInitiative, managerAllowed: true},
	GrantList:   {scope: scopeInitiative, managerAllowed: true},

	GroupCreate:       {scope: scopeGlobal},
	GroupDelete:       {scope: scopeGlobal},
	GroupMemberAdd:    {scope: scopeGlobal},
	GroupMemberRemove: {scope: scopeGlobal},
	GroupMemberList:   {scope: scopeGlobal},

	UserDisable:        {scope: scopeGlobal},
	UserEnable:         {scope: scopeGlobal},
	UserSessionsRevoke: {scope: scopeGlobal},
	UserList:           {scope: scopeGlobal},
}

// Decide is the pure decision function. Rules, in order:
//
//  1. An inactive subject (see Subject) and an action not in the table are forbidden.
//  2. Listings answer with the Scope the caller may see, never a bare allow:
//     InitiativeList and AuditList without a filter: a platform admin sees All, a manager only their initiatives,
//     anyone else is forbidden. AuditList with Resource.InitiativeID set is a filter: allowed to admins and to
//     managers of that initiative with that single initiative as scope, and not_found for anyone else.
//     GroupList (id, key, name only; groups are global): admins and managers see All, anyone else is forbidden.
//     GrantList lists one initiative: allowed to admins and to its managers with that initiative as scope.
//     The admin-only listings of global data (GroupMemberList, UserList) answer Scope All to admins.
//  3. An action on an initiative-scoped resource needs Resource.InitiativeID (otherwise forbidden, even for
//     admins: the caller forgot to resolve the owner). A caller who is neither platform admin nor manager of
//     that initiative gets not_found, whatever the action, so the resource's existence is not confirmed.
//  4. A manager of the initiative is allowed only where the table says so; otherwise forbidden (they already
//     know the initiative exists).
//  5. Global actions are for platform admins only; anyone else is forbidden.
//
// Being a platform admin gives no access to the apps themselves (see access.Decide); it only governs the portal.
func Decide(subject Subject, action Action, resource Resource) Decision {
	if !subject.active {
		return Decision{Outcome: Forbidden, Reason: ReasonInactiveSubject}
	}
	switch action {
	case InitiativeList:
		return decideListing(subject, false)
	case GroupList:
		return decideListing(subject, true)
	case AuditList:
		if resource.InitiativeID == "" {
			return decideListing(subject, false)
		}
		d := decideInitiative(subject, rule{scope: scopeInitiative, managerAllowed: true}, resource)
		if d.Outcome == Allow {
			d.Scope = Scope{Initiatives: []string{resource.InitiativeID}}
		}
		return d
	}
	r, ok := rules[action]
	if !ok {
		return Decision{Outcome: Forbidden, Reason: ReasonUnknownAction}
	}
	if r.scope == scopeGlobal {
		if !subject.PlatformAdmin {
			return Decision{Outcome: Forbidden, Reason: ReasonAdminOnly}
		}
		d := Decision{Outcome: Allow, Reason: ReasonPlatformAdmin}
		if globalListings[action] {
			d.Scope = Scope{All: true}
		}
		return d
	}
	d := decideInitiative(subject, r, resource)
	if d.Outcome == Allow && action == GrantList {
		d.Scope = Scope{Initiatives: []string{resource.InitiativeID}}
	}
	return d
}

// globalListings are the admin-only listings of global data: allowed, they answer with Scope All.
var globalListings = map[Action]bool{GroupMemberList: true, UserList: true}

// decideListing answers a listing. globalForManagers: managers see everything (the listed data is global and
// harmless, like group names); otherwise they see only their initiatives.
func decideListing(subject Subject, globalForManagers bool) Decision {
	switch {
	case subject.PlatformAdmin:
		return Decision{Outcome: Allow, Reason: ReasonPlatformAdmin, Scope: Scope{All: true}}
	case len(subject.Managed) > 0 && globalForManagers:
		return Decision{Outcome: Allow, Reason: ReasonInitiativeManager, Scope: Scope{All: true}}
	case len(subject.Managed) > 0:
		return Decision{Outcome: Allow, Reason: ReasonInitiativeManager, Scope: Scope{Initiatives: subject.managedIDs()}}
	default:
		return Decision{Outcome: Forbidden, Reason: ReasonNoManagedScope}
	}
}

func decideInitiative(subject Subject, r rule, resource Resource) Decision {
	if resource.InitiativeID == "" {
		return Decision{Outcome: Forbidden, Reason: ReasonMissingInitiative}
	}
	switch {
	case subject.PlatformAdmin:
		return Decision{Outcome: Allow, Reason: ReasonPlatformAdmin}
	case !subject.Manages(resource.InitiativeID):
		return Decision{Outcome: NotFound, Reason: ReasonNotManager}
	case r.managerAllowed:
		return Decision{Outcome: Allow, Reason: ReasonInitiativeManager}
	default:
		return Decision{Outcome: Forbidden, Reason: ReasonAdminOnly}
	}
}
