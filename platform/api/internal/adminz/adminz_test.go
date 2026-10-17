package adminz_test

import (
	"slices"
	"testing"

	"platform/api/internal/adminz"
)

const (
	initA = "00000000-0000-0000-0000-00000000000a"
	initB = "00000000-0000-0000-0000-00000000000b"
)

var (
	admin      = adminz.ActiveSubject("u-admin", true, nil)
	adminAlsoA = adminz.ActiveSubject("u-admin-mgr", true, []string{initA})
	managerA   = adminz.ActiveSubject("u-mgr-a", false, []string{initA, initA})
	managerB   = adminz.ActiveSubject("u-mgr-b", false, []string{initB})
	managerAB  = adminz.ActiveSubject("u-mgr-ab", false, []string{initA, initB})
	plain      = adminz.ActiveSubject("u-plain", false, nil)
	zero       = adminz.Subject{}
	// Struct literals are never active, even with every flag set: a disabled platform admin.
	disabledAdmin = adminz.Subject{ID: "u-disabled", PlatformAdmin: true, Managed: map[string]struct{}{initA: {}}}
)

func inA(typ string) adminz.Resource {
	return adminz.Resource{Type: typ, ID: "r1", InitiativeID: initA}
}

// Every row of the "Who administers what" table of contract block 6, for each kind of caller.
func TestDecideWhoAdministersWhat(t *testing.T) {
	t.Parallel()
	const (
		a = adminz.Allow
		f = adminz.Forbidden
		n = adminz.NotFound
	)
	tests := []struct {
		name     string
		action   adminz.Action
		resource adminz.Resource
		// Expected outcome per caller: platform admin, manager of the resource's initiative, manager of another
		// initiative only, user without any managed initiative.
		admin, manager, otherManager, plain adminz.Outcome
	}{
		// Initiatives: create, rename, deactivate are platform-admin only.
		{"create initiative", adminz.InitiativeCreate, adminz.Resource{Type: "initiative"}, a, f, f, f},
		{"rename initiative", adminz.InitiativeUpdate, inA("initiative"), a, f, n, n},
		{"activate initiative", adminz.InitiativeActivate, inA("initiative"), a, f, n, n},
		{"deactivate initiative", adminz.InitiativeDeactivate, inA("initiative"), a, f, n, n},
		{"read initiative", adminz.InitiativeRead, inA("initiative"), a, a, n, n},

		// Roles: admin only (role keys are part of the apps' contract); managers may list them to grant.
		{"create role", adminz.RoleCreate, inA("role"), a, f, n, n},
		{"update role", adminz.RoleUpdate, inA("role"), a, f, n, n},
		{"delete role", adminz.RoleDelete, inA("role"), a, f, n, n},
		{"list roles", adminz.RoleList, inA("role"), a, a, n, n},

		// Apps and deployments: admin only; managers may read apps and health.
		{"create app", adminz.AppCreate, inA("app"), a, f, n, n},
		{"rename app", adminz.AppUpdate, inA("app"), a, f, n, n},
		{"enable app", adminz.AppEnable, inA("app"), a, f, n, n},
		{"disable app", adminz.AppDisable, inA("app"), a, f, n, n},
		{"list apps", adminz.AppList, inA("app"), a, a, n, n},
		{"app health", adminz.AppHealth, inA("app"), a, a, n, n},
		{"add host", adminz.DeploymentCreate, inA("deployment"), a, f, n, n},
		{"remove host", adminz.DeploymentDelete, inA("deployment"), a, f, n, n},

		// Grants: admins and the initiative's managers.
		{"create grant", adminz.GrantCreate, inA("grant"), a, a, n, n},
		{"revoke grant", adminz.GrantDelete, inA("grant"), a, a, n, n},
		{"list grants", adminz.GrantList, inA("grant"), a, a, n, n},

		// Groups and members are global: admin only.
		{"create group", adminz.GroupCreate, adminz.Resource{Type: "group"}, a, f, f, f},
		{"delete group", adminz.GroupDelete, adminz.Resource{Type: "group", ID: "g1"}, a, f, f, f},
		{"add group member", adminz.GroupMemberAdd, adminz.Resource{Type: "group", ID: "g1"}, a, f, f, f},
		{"remove group member", adminz.GroupMemberRemove, adminz.Resource{Type: "group", ID: "g1"}, a, f, f, f},
		{"list group members", adminz.GroupMemberList, adminz.Resource{Type: "group", ID: "g1"}, a, f, f, f},

		// Users and their sessions are global: admin only.
		{"disable user", adminz.UserDisable, adminz.Resource{Type: "user", ID: "u1"}, a, f, f, f},
		{"enable user", adminz.UserEnable, adminz.Resource{Type: "user", ID: "u1"}, a, f, f, f},
		{"revoke user sessions", adminz.UserSessionsRevoke, adminz.Resource{Type: "user", ID: "u1"}, a, f, f, f},
		{"list users", adminz.UserList, adminz.Resource{Type: "user"}, a, f, f, f},
		{"list orphan hosts", adminz.OrphanHostList, adminz.Resource{Type: "orphan_host"}, a, f, f, f},
		{"revoke any token", adminz.TokenRevokeAny, adminz.Resource{Type: "token", ID: "t1"}, a, f, f, f},

		// Own tokens: any active user, roles or not.
		{"create own token", adminz.TokenCreateOwn, adminz.Resource{Type: "token"}, a, a, a, a},
		{"list own tokens", adminz.TokenListOwn, adminz.Resource{Type: "token"}, a, a, a, a},
		{"revoke own token", adminz.TokenRevokeOwn, adminz.Resource{Type: "token", ID: "t1"}, a, a, a, a},

		// Listings are scoped per caller: the unscoped ones need any managed initiative.
		{"list initiatives", adminz.InitiativeList, adminz.Resource{Type: "initiative"}, a, a, a, f},
		// Group names (never members) are visible to whoever may grant.
		{"list groups", adminz.GroupList, adminz.Resource{Type: "group"}, a, a, a, f},
		{"list audit unfiltered", adminz.AuditList, adminz.Resource{Type: "audit"}, a, a, a, f},
		// Filtered by an initiative: admin, or a manager of it; anyone else must not learn it exists.
		{"list audit of the initiative", adminz.AuditList, inA("audit"), a, a, n, n},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, c := range []struct {
				caller string
				who    adminz.Subject
				want   adminz.Outcome
			}{
				{"platform admin", admin, tt.admin},
				{"manager of the initiative", managerA, tt.manager},
				{"manager of another initiative", managerB, tt.otherManager},
				{"user without managed initiatives", plain, tt.plain},
			} {
				if got := adminz.Decide(c.who, tt.action, tt.resource); got.Outcome != c.want {
					t.Errorf("%s: outcome = %q (%s), want %q", c.caller, got.Outcome, got.Reason, c.want)
				}
			}
		})
	}
}

func TestDecideEdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		subject  adminz.Subject
		action   adminz.Action
		resource adminz.Resource
		want     adminz.Outcome
		reason   string
	}{
		{"unknown action is forbidden for admins too", admin, "initiative.explode", inA("initiative"), adminz.Forbidden, adminz.ReasonUnknownAction},
		{"empty action is forbidden", admin, "", inA("initiative"), adminz.Forbidden, adminz.ReasonUnknownAction},
		{"scoped action without initiative is forbidden for admins", admin, adminz.GrantCreate, adminz.Resource{Type: "grant"}, adminz.Forbidden, adminz.ReasonMissingInitiative},
		{"scoped action without initiative is forbidden for managers", managerA, adminz.GrantCreate, adminz.Resource{Type: "grant"}, adminz.Forbidden, adminz.ReasonMissingInitiative},
		{"managing several initiatives covers each", managerAB, adminz.GrantCreate, inA("grant"), adminz.Allow, adminz.ReasonInitiativeManager},
		{"manager of two does not cover a third", managerAB, adminz.GrantCreate, adminz.Resource{Type: "grant", InitiativeID: "other"}, adminz.NotFound, adminz.ReasonNotManager},
		{"an admin that also manages stays an admin", adminAlsoA, adminz.AppDisable, inA("app"), adminz.Allow, adminz.ReasonPlatformAdmin},
		{"admin on any initiative", admin, adminz.AppDisable, adminz.Resource{Type: "app", InitiativeID: initB}, adminz.Allow, adminz.ReasonPlatformAdmin},
		{"manager denied an admin-only action on their initiative is forbidden, not hidden", managerA, adminz.RoleDelete, inA("role"), adminz.Forbidden, adminz.ReasonAdminOnly},
		{"non-manager on admin-only action is hidden, not forbidden", plain, adminz.RoleDelete, inA("role"), adminz.NotFound, adminz.ReasonNotManager},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := adminz.Decide(tt.subject, tt.action, tt.resource)
			if got.Outcome != tt.want || got.Reason != tt.reason {
				t.Fatalf("Decide = %+v, want {%s %s}", got, tt.want, tt.reason)
			}
		})
	}
}

func TestSubjectManages(t *testing.T) {
	t.Parallel()
	if !managerA.Manages(initA) || managerA.Manages(initB) || plain.Manages(initA) || zero.Manages(initA) {
		t.Fatal("Manages does not reflect the managed set")
	}
}

// A subject that was not built by ActiveSubject (a disabled user, the zero value, a literal) never gets Allow.
func TestInactiveSubjectIsNeverAllowed(t *testing.T) {
	t.Parallel()
	actions := []adminz.Action{
		adminz.InitiativeCreate, adminz.InitiativeUpdate, adminz.InitiativeRead, adminz.InitiativeList, adminz.RoleCreate,
		adminz.AppDisable, adminz.DeploymentDelete, adminz.GrantCreate, adminz.GrantDelete, adminz.GrantList,
		adminz.GroupCreate, adminz.GroupList, adminz.GroupMemberList, adminz.RoleList, adminz.RoleUpdate, adminz.AppList,
		adminz.DeploymentCreate, adminz.UserDisable, adminz.UserSessionsRevoke,
		adminz.UserList, adminz.OrphanHostList, adminz.AuditList, adminz.TokenRevokeAny,
		adminz.TokenCreateOwn, adminz.TokenListOwn, adminz.TokenRevokeOwn, "unknown.action",
	}
	for name, who := range map[string]adminz.Subject{"disabled platform admin": disabledAdmin, "zero value": zero} {
		for _, action := range actions {
			for _, res := range []adminz.Resource{inA("x"), {Type: "x"}} {
				got := adminz.Decide(who, action, res)
				if got.Outcome != adminz.Forbidden || got.Reason != adminz.ReasonInactiveSubject || !got.Scope.Empty() {
					t.Errorf("%s: %s on %+v = %+v, want forbidden/inactive with an empty scope", name, action, res, got)
				}
			}
		}
	}
}

// Listings answer with the scope the caller may see.
func TestDecideListingScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		subject  adminz.Subject
		action   adminz.Action
		resource adminz.Resource
		want     adminz.Outcome
		all      bool
		ids      []string
	}{
		{"admin lists initiatives", admin, adminz.InitiativeList, adminz.Resource{}, adminz.Allow, true, nil},
		{"manager lists their initiatives, sorted", adminz.ActiveSubject("m", false, []string{initB, initA, initB}), adminz.InitiativeList, adminz.Resource{}, adminz.Allow, false, []string{initA, initB}},
		{"manager audit without filter", managerA, adminz.AuditList, adminz.Resource{}, adminz.Allow, false, []string{initA}},
		{"admin audit without filter", admin, adminz.AuditList, adminz.Resource{}, adminz.Allow, true, nil},
		{"manager audit filtered to their initiative", managerAB, adminz.AuditList, inA("audit"), adminz.Allow, false, []string{initA}},
		{"admin audit filtered narrows to the initiative", admin, adminz.AuditList, inA("audit"), adminz.Allow, false, []string{initA}},
		{"audit filtered to a foreign initiative", managerB, adminz.AuditList, inA("audit"), adminz.NotFound, false, nil},
		{"manager sees every group name", managerA, adminz.GroupList, adminz.Resource{}, adminz.Allow, true, nil},
		{"admin sees every group name", admin, adminz.GroupList, adminz.Resource{}, adminz.Allow, true, nil},
		{"admin lists the grants of an initiative", admin, adminz.GrantList, inA("grant"), adminz.Allow, false, []string{initA}},
		{"manager lists the grants of their initiative", managerAB, adminz.GrantList, inA("grant"), adminz.Allow, false, []string{initA}},
		{"manager of another initiative lists no grants", managerB, adminz.GrantList, inA("grant"), adminz.NotFound, false, nil},
		{"admin lists the roles of an initiative", admin, adminz.RoleList, inA("role"), adminz.Allow, false, []string{initA}},
		{"manager lists the roles of their initiative", managerAB, adminz.RoleList, inA("role"), adminz.Allow, false, []string{initA}},
		{"manager of another initiative lists no roles", managerB, adminz.RoleList, inA("role"), adminz.NotFound, false, nil},
		{"roles need the initiative", admin, adminz.RoleList, adminz.Resource{Type: "role"}, adminz.Forbidden, false, nil},
		{"admin lists the apps of an initiative", admin, adminz.AppList, inA("app"), adminz.Allow, false, []string{initA}},
		{"manager lists the apps of their initiative", managerAB, adminz.AppList, inA("app"), adminz.Allow, false, []string{initA}},
		{"plain user lists no apps", plain, adminz.AppList, inA("app"), adminz.NotFound, false, nil},
		{"apps need the initiative", managerA, adminz.AppList, adminz.Resource{Type: "app"}, adminz.Forbidden, false, nil},
		{"grants need the initiative", admin, adminz.GrantList, adminz.Resource{Type: "grant"}, adminz.Forbidden, false, nil},
		{"admin lists group members", admin, adminz.GroupMemberList, adminz.Resource{Type: "group_member"}, adminz.Allow, true, nil},
		{"manager lists no group members", managerA, adminz.GroupMemberList, adminz.Resource{Type: "group_member"}, adminz.Forbidden, false, nil},
		{"admin lists users", admin, adminz.UserList, adminz.Resource{Type: "user"}, adminz.Allow, true, nil},
		{"manager lists no users", managerA, adminz.UserList, adminz.Resource{Type: "user"}, adminz.Forbidden, false, nil},
		{"admin lists orphan hosts", admin, adminz.OrphanHostList, adminz.Resource{Type: "orphan_host"}, adminz.Allow, true, nil},
		{"manager lists no orphan hosts", managerA, adminz.OrphanHostList, adminz.Resource{Type: "orphan_host"}, adminz.Forbidden, false, nil},
		{"plain user sees no groups", plain, adminz.GroupList, adminz.Resource{}, adminz.Forbidden, false, nil},
		{"plain user lists no initiatives", plain, adminz.InitiativeList, adminz.Resource{}, adminz.Forbidden, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := adminz.Decide(tt.subject, tt.action, tt.resource)
			if got.Outcome != tt.want || got.Scope.All != tt.all || !slices.Equal(got.Scope.Initiatives, tt.ids) {
				t.Fatalf("Decide = %+v, want %s all=%v ids=%v", got, tt.want, tt.all, tt.ids)
			}
			if got.Outcome != adminz.Allow && !got.Scope.Empty() {
				t.Fatalf("a refusal carries a scope: %+v", got.Scope)
			}
		})
	}
}

func TestScope(t *testing.T) {
	t.Parallel()
	if !(adminz.Scope{}).Empty() || (adminz.Scope{}).Includes(initA) {
		t.Fatal("the zero scope must match nothing")
	}
	all := adminz.Scope{All: true}
	if all.Empty() || !all.Includes(initA) {
		t.Fatal("All must match everything")
	}
	some := adminz.Scope{Initiatives: []string{initA}}
	if some.Empty() || !some.Includes(initA) || some.Includes(initB) {
		t.Fatal("an initiative scope must match only its initiatives")
	}
}
