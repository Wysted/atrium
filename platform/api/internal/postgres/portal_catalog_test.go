package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"platform/api/internal/portal"
)

const (
	portalAppTestID = "app_0000000000000000000000000P"
	catalogAuthHost = "auth.apps.example.test"
)

var appIDCheck = regexp.MustCompile(`^app_[0-9A-HJKMNP-TV-Z]{26}$`)

// newCatalogEnv is an admin environment that knows the portal app and the auth host.
func newCatalogEnv(t *testing.T) *adminEnv {
	t.Helper()
	return newAdminEnvCfg(t, func(s portal.Store) portal.Store { return s }, func(c *portal.Config) {
		c.PortalAppID, c.AuthHost, c.ProbeInterval = portalAppTestID, catalogAuthHost, 5*time.Minute
	})
}

func uniqueHost() string { return fmt.Sprintf("h%d.example.test", fixtureSeq.Add(1)) }

// platform creates the reserved initiative with the portal app deployed on hosts and returns the initiative id.
func (e *adminEnv) platform(hosts ...string) string {
	e.t.Helper()
	id := e.w.initiative(e.t, "platform", "active")
	mustExec(e.t, e.owner, `INSERT INTO apps (id, initiative_id, name) VALUES ($1, $2, 'Portal')`, portalAppTestID, id)
	for _, h := range hosts {
		mustExec(e.t, e.owner, `INSERT INTO deployments (host, app_id) VALUES ($1, $2)`, h, portalAppTestID)
	}
	return id
}

// deploymentID returns the id of the deployment on host.
func (e *adminEnv) deploymentID(host string) string {
	e.t.Helper()
	return scalar[string](e.t, e.owner, `SELECT id::text FROM deployments WHERE host = $1`, host)
}

// callers is the cast of the catalog tests.
type callers struct {
	admin, mgrA, mgrB, plain, disabledAdmin string
}

func (e *adminEnv) callers(alpha, beta initiativeWorld) callers {
	e.t.Helper()
	c := callers{
		admin: e.admin(), mgrA: e.manager(alpha.id, alpha.manager), mgrB: e.manager(beta.id, beta.manager),
		plain: e.w.user(e.t, "active"), disabledAdmin: e.w.user(e.t, "disabled"),
	}
	e.pw.platformAdmin(c.disabledAdmin)
	return c
}

func wantValidation(t *testing.T, err error, want ...portal.FieldProblem) {
	t.Helper()
	got := validationFields(t, err)
	if !slices.Equal(got, want) {
		t.Fatalf("problems = %v, want %v", got, want)
	}
}

func TestInitiativeCatalog(t *testing.T) {
	t.Parallel()
	e := newCatalogEnv(t)
	ctx := e.ctx()
	alpha, beta := e.initiative("alpha"), e.initiative("beta")
	c := e.callers(alpha, beta)

	t.Run("an admin creates an initiative and one audit row records it", func(t *testing.T) {
		before := e.auditCount()
		i, err := e.svc.CreateInitiative(ctx, c.admin, portal.NewInitiative{Key: "gamma", Name: "Gamma"})
		if err != nil {
			t.Fatal(err)
		}
		if i.ID == "" || i.Key != "gamma" || i.Name != "Gamma" || i.Status != "active" || i.CreatedAt.IsZero() {
			t.Fatalf("initiative = %+v", i)
		}
		if e.auditCount() != before+1 {
			t.Fatalf("audit rows %d -> %d, want exactly one more", before, e.auditCount())
		}
		a := e.lastAudit()
		if a.Actor != c.admin || a.Initiative != i.ID || a.Action != "initiative.create" || a.TargetType != "initiative" ||
			a.TargetID != i.ID || a.Before != nil || a.After["key"] != "gamma" || a.After["name"] != "Gamma" || a.After["status"] != "active" {
			t.Fatalf("audit = %+v", a)
		}
	})

	t.Run("a repeated key is a conflict and creates nothing", func(t *testing.T) {
		before := e.auditCount()
		if _, err := e.svc.CreateInitiative(ctx, c.admin, portal.NewInitiative{Key: "gamma"}); !errors.Is(err, portal.ErrInitiativeKeyTaken) {
			t.Fatalf("err = %v, want ErrInitiativeKeyTaken", err)
		}
		if n := e.count(`SELECT count(*) FROM initiatives WHERE key = 'gamma'`); n != 1 || e.auditCount() != before {
			t.Fatalf("%d rows, audit %d -> %d", n, before, e.auditCount())
		}
	})

	t.Run("invalid input is a validation problem and creates nothing", func(t *testing.T) {
		before := e.count(`SELECT count(*) FROM initiatives`)
		tests := []struct {
			name string
			in   portal.NewInitiative
			want []portal.FieldProblem
		}{
			{"no key", portal.NewInitiative{}, []portal.FieldProblem{{Field: "key", Code: portal.CodeRequired}}},
			{"uppercase", portal.NewInitiative{Key: "Gamma"}, []portal.FieldProblem{{Field: "key", Code: portal.CodeInvalid}}},
			{"leading hyphen", portal.NewInitiative{Key: "-a"}, []portal.FieldProblem{{Field: "key", Code: portal.CodeInvalid}}},
			{"space", portal.NewInitiative{Key: "a b"}, []portal.FieldProblem{{Field: "key", Code: portal.CodeInvalid}}},
			{"too long", portal.NewInitiative{Key: strings.Repeat("a", 64)}, []portal.FieldProblem{{Field: "key", Code: portal.CodeInvalid}}},
			{"NUL in the key", portal.NewInitiative{Key: "a\x00"}, []portal.FieldProblem{{Field: "key", Code: portal.CodeInvalid}}},
			{"name too long", portal.NewInitiative{Key: "ok", Name: strings.Repeat("n", 201)}, []portal.FieldProblem{{Field: "name", Code: portal.CodeInvalid}}},
			{"NUL in the name", portal.NewInitiative{Key: "ok", Name: "a\x00"}, []portal.FieldProblem{{Field: "name", Code: portal.CodeInvalid}}},
			{"invalid UTF-8 in the name", portal.NewInitiative{Key: "ok", Name: "a\xff"}, []portal.FieldProblem{{Field: "name", Code: portal.CodeInvalid}}},
		}
		for _, tt := range tests {
			_, err := e.svc.CreateInitiative(ctx, c.admin, tt.in)
			wantValidation(t, err, tt.want...)
		}
		if got := e.count(`SELECT count(*) FROM initiatives`); got != before {
			t.Fatalf("%d initiatives, want %d", got, before)
		}
	})

	t.Run("only platform admins create", func(t *testing.T) {
		for name, who := range map[string]string{"manager": c.mgrA, "plain user": c.plain} {
			if _, err := e.svc.CreateInitiative(ctx, who, portal.NewInitiative{Key: "nope"}); !errors.Is(err, portal.ErrForbidden) {
				t.Errorf("%s: err = %v, want ErrForbidden", name, err)
			}
		}
		if _, err := e.svc.CreateInitiative(ctx, c.disabledAdmin, portal.NewInitiative{Key: "nope"}); !errors.Is(err, portal.ErrUserInactive) {
			t.Errorf("disabled admin: err = %v, want ErrUserInactive", err)
		}
		if e.count(`SELECT count(*) FROM initiatives WHERE key = 'nope'`) != 0 {
			t.Fatal("a refused request created an initiative")
		}
	})

	t.Run("reads: admin any, manager their own, everyone else a not found", func(t *testing.T) {
		if i, err := e.svc.GetInitiative(ctx, c.admin, beta.id); err != nil || i.Key != "beta" {
			t.Fatalf("admin: %+v, %v", i, err)
		}
		if i, err := e.svc.GetInitiative(ctx, c.mgrA, strings.ToUpper(alpha.id)); err != nil || i.Key != "alpha" {
			t.Fatalf("manager with an uppercase id: %+v, %v", i, err)
		}
		for name, tt := range map[string]struct{ who, id string }{
			"manager of another initiative": {c.mgrB, alpha.id}, "plain user": {c.plain, alpha.id},
			"unknown id": {c.admin, "00000000-0000-7000-8000-000000000000"}, "malformed id": {c.admin, "not-a-uuid"},
		} {
			if _, err := e.svc.GetInitiative(ctx, tt.who, tt.id); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("%s: err = %v, want ErrNotFound", name, err)
			}
		}
		if _, err := e.svc.GetInitiative(ctx, c.disabledAdmin, alpha.id); !errors.Is(err, portal.ErrUserInactive) {
			t.Errorf("disabled admin: %v", err)
		}
	})

	t.Run("the listing is scoped to the caller and paginated", func(t *testing.T) {
		keys := func(p portal.Page[portal.Initiative]) []string {
			var out []string
			for _, i := range p.Items {
				out = append(out, i.Key)
			}
			return out
		}
		p, err := e.svc.ListInitiatives(ctx, c.mgrA, portal.PageRequest{})
		if err != nil || !slices.Equal(keys(p), []string{"alpha"}) {
			t.Fatalf("manager: %v, %v", keys(p), err)
		}
		if _, err := e.svc.ListInitiatives(ctx, c.plain, portal.PageRequest{}); !errors.Is(err, portal.ErrForbidden) {
			t.Fatalf("plain user: %v", err)
		}
		var all []string
		req := portal.PageRequest{Limit: 1}
		for range 10 {
			page, err := e.svc.ListInitiatives(ctx, c.admin, req)
			if err != nil {
				t.Fatal(err)
			}
			all = append(all, keys(page)...)
			if page.NextCursor == "" {
				break
			}
			req.Cursor = page.NextCursor
		}
		if !slices.Equal(all, []string{"alpha", "beta", "gamma"}) {
			t.Fatalf("paged keys = %v", all)
		}
		if _, err := e.svc.ListInitiatives(ctx, c.admin, portal.PageRequest{Cursor: req.Cursor + "x"}); !errors.Is(err, portal.ErrInvalidCursor) {
			t.Fatalf("tampered cursor: %v", err)
		}
	})

	str := func(s string) *string { return &s }

	t.Run("rename, deactivate and activate are audited with their own action", func(t *testing.T) {
		steps := []struct {
			patch  portal.InitiativePatch
			action string
			status string
			name   string
		}{
			{portal.InitiativePatch{Name: str("Beta 2")}, "initiative.update", "active", "Beta 2"},
			{portal.InitiativePatch{Status: str("inactive")}, "initiative.deactivate", "inactive", "Beta 2"},
			{portal.InitiativePatch{Status: str("active")}, "initiative.activate", "active", "Beta 2"},
			{portal.InitiativePatch{Name: str("Beta 3"), Status: str("inactive")}, "initiative.deactivate", "inactive", "Beta 3"},
		}
		for _, st := range steps {
			before := e.auditCount()
			prev := scalar[string](t, e.owner, `SELECT status FROM initiatives WHERE id = $1`, beta.id)
			i, err := e.svc.UpdateInitiative(ctx, c.admin, beta.id, st.patch)
			if err != nil || i.Status != st.status || i.Name != st.name || i.Key != "beta" {
				t.Fatalf("%+v: %+v, %v", st.patch, i, err)
			}
			if e.auditCount() != before+1 {
				t.Fatalf("audit rows %d -> %d", before, e.auditCount())
			}
			a := e.lastAudit()
			if a.Actor != c.admin || a.Initiative != beta.id || a.Action != st.action || a.TargetType != "initiative" || a.TargetID != beta.id ||
				a.Before["status"] != prev || a.After["status"] != st.status || a.After["name"] != st.name || a.After["key"] != "beta" {
				t.Fatalf("audit = %+v", a)
			}
		}
	})

	t.Run("a patch that changes nothing writes no audit row", func(t *testing.T) {
		before := e.auditCount()
		i, err := e.svc.UpdateInitiative(ctx, c.admin, alpha.id, portal.InitiativePatch{Status: str("active")})
		if err != nil || i.Status != "active" || e.auditCount() != before {
			t.Fatalf("%+v, %v, audit %d -> %d", i, err, before, e.auditCount())
		}
	})

	t.Run("an invalid patch is a validation problem", func(t *testing.T) {
		_, err := e.svc.UpdateInitiative(ctx, c.admin, alpha.id, portal.InitiativePatch{})
		wantValidation(t, err, portal.FieldProblem{Field: "name", Code: portal.CodeAtLeastOne}, portal.FieldProblem{Field: "status", Code: portal.CodeAtLeastOne})
		_, err = e.svc.UpdateInitiative(ctx, c.admin, alpha.id, portal.InitiativePatch{Status: str("disabled")})
		wantValidation(t, err, portal.FieldProblem{Field: "status", Code: portal.CodeInvalid})
		_, err = e.svc.UpdateInitiative(ctx, c.admin, alpha.id, portal.InitiativePatch{Name: str("a\x00")})
		wantValidation(t, err, portal.FieldProblem{Field: "name", Code: portal.CodeInvalid})
	})

	t.Run("only platform admins change an initiative", func(t *testing.T) {
		before := e.auditCount()
		patch := portal.InitiativePatch{Name: str("hijacked")}
		if _, err := e.svc.UpdateInitiative(ctx, c.mgrA, alpha.id, patch); !errors.Is(err, portal.ErrForbidden) {
			t.Errorf("manager: %v", err)
		}
		for name, who := range map[string]string{"manager of another initiative": c.mgrB, "plain user": c.plain} {
			if _, err := e.svc.UpdateInitiative(ctx, who, alpha.id, patch); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("%s: %v", name, err)
			}
		}
		if _, err := e.svc.UpdateInitiative(ctx, c.disabledAdmin, alpha.id, patch); !errors.Is(err, portal.ErrUserInactive) {
			t.Errorf("disabled admin: %v", err)
		}
		if _, err := e.svc.UpdateInitiative(ctx, c.admin, "00000000-0000-7000-8000-000000000000", patch); !errors.Is(err, portal.ErrNotFound) {
			t.Errorf("unknown id: %v", err)
		}
		if _, err := e.svc.UpdateInitiative(ctx, c.admin, "x", patch); !errors.Is(err, portal.ErrNotFound) {
			t.Errorf("malformed id: %v", err)
		}
		if e.auditCount() != before || scalar[string](t, e.owner, `SELECT name FROM initiatives WHERE id = $1`, alpha.id) == "hijacked" {
			t.Fatal("a refused request changed state")
		}
	})

	t.Run("the platform initiative cannot be deactivated but can be renamed", func(t *testing.T) {
		platform := e.platform()
		before := e.auditCount()
		if _, err := e.svc.UpdateInitiative(ctx, c.admin, platform, portal.InitiativePatch{Status: str("inactive")}); !errors.Is(err, portal.ErrPlatformInitiative) {
			t.Fatalf("deactivate: %v, want ErrPlatformInitiative", err)
		}
		if _, err := e.svc.UpdateInitiative(ctx, c.admin, platform, portal.InitiativePatch{Name: str("Renamed"), Status: str("inactive")}); !errors.Is(err, portal.ErrPlatformInitiative) {
			t.Fatalf("rename and deactivate: %v", err)
		}
		if st := scalar[string](t, e.owner, `SELECT status FROM initiatives WHERE id = $1`, platform); st != "active" || e.auditCount() != before {
			t.Fatalf("status %q, audit %d -> %d", st, before, e.auditCount())
		}
		if i, err := e.svc.UpdateInitiative(ctx, c.admin, platform, portal.InitiativePatch{Name: str("Platform")}); err != nil || i.Name != "Platform" {
			t.Fatalf("rename: %+v, %v", i, err)
		}
	})
}

func TestRoleCatalog(t *testing.T) {
	t.Parallel()
	e := newCatalogEnv(t)
	ctx := e.ctx()
	alpha, beta := e.initiative("alpha"), e.initiative("beta")
	c := e.callers(alpha, beta)
	yes, no := true, false

	t.Run("an admin creates a role and one audit row records it", func(t *testing.T) {
		before := e.auditCount()
		r, err := e.svc.CreateRole(ctx, c.admin, alpha.id, portal.NewRole{Key: "viewer", ManagesAccess: true})
		if err != nil {
			t.Fatal(err)
		}
		if r.ID == "" || r.InitiativeID != alpha.id || r.Key != "viewer" || !r.ManagesAccess {
			t.Fatalf("role = %+v", r)
		}
		if e.auditCount() != before+1 {
			t.Fatalf("audit rows %d -> %d", before, e.auditCount())
		}
		a := e.lastAudit()
		if a.Actor != c.admin || a.Initiative != alpha.id || a.Action != "role.create" || a.TargetType != "role" || a.TargetID != r.ID ||
			a.Before != nil || a.After["key"] != "viewer" || a.After["manages_access"] != true {
			t.Fatalf("audit = %+v", a)
		}
	})

	t.Run("a repeated key is a conflict, in another initiative it is fine", func(t *testing.T) {
		before := e.auditCount()
		if _, err := e.svc.CreateRole(ctx, c.admin, alpha.id, portal.NewRole{Key: "viewer"}); !errors.Is(err, portal.ErrRoleKeyTaken) {
			t.Fatalf("err = %v, want ErrRoleKeyTaken", err)
		}
		if e.auditCount() != before || e.count(`SELECT count(*) FROM initiative_roles WHERE initiative_id = $1 AND key = 'viewer'`, alpha.id) != 1 {
			t.Fatal("a conflict changed state")
		}
		if _, err := e.svc.CreateRole(ctx, c.admin, beta.id, portal.NewRole{Key: "viewer"}); err != nil {
			t.Fatalf("same key in another initiative: %v", err)
		}
	})

	t.Run("invalid keys are validation problems", func(t *testing.T) {
		before := e.count(`SELECT count(*) FROM initiative_roles`)
		for key, code := range map[string]string{
			"": portal.CodeRequired, "Viewer": portal.CodeInvalid, "-x": portal.CodeInvalid, "a b": portal.CodeInvalid,
			strings.Repeat("k", 33): portal.CodeInvalid, "a\x00": portal.CodeInvalid, "a\xff": portal.CodeInvalid,
		} {
			_, err := e.svc.CreateRole(ctx, c.admin, alpha.id, portal.NewRole{Key: key})
			wantValidation(t, err, portal.FieldProblem{Field: "key", Code: code})
		}
		if _, err := e.svc.CreateRole(ctx, c.admin, alpha.id, portal.NewRole{Key: strings.Repeat("k", 32)}); err != nil {
			t.Fatalf("a key of the maximum length: %v", err)
		}
		if got := e.count(`SELECT count(*) FROM initiative_roles`); got != before+1 {
			t.Fatalf("%d roles, want %d", got, before+1)
		}
	})

	t.Run("creating needs a platform admin and an existing initiative", func(t *testing.T) {
		in := portal.NewRole{Key: "intruder"}
		if _, err := e.svc.CreateRole(ctx, c.mgrA, alpha.id, in); !errors.Is(err, portal.ErrForbidden) {
			t.Errorf("manager: %v", err)
		}
		for name, who := range map[string]string{"manager of another initiative": c.mgrB, "plain user": c.plain} {
			if _, err := e.svc.CreateRole(ctx, who, alpha.id, in); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("%s: %v", name, err)
			}
		}
		if _, err := e.svc.CreateRole(ctx, c.disabledAdmin, alpha.id, in); !errors.Is(err, portal.ErrUserInactive) {
			t.Errorf("disabled admin: %v", err)
		}
		for _, id := range []string{"00000000-0000-7000-8000-000000000000", "x"} {
			if _, err := e.svc.CreateRole(ctx, c.admin, id, in); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("initiative %q: %v", id, err)
			}
		}
		if e.count(`SELECT count(*) FROM initiative_roles WHERE key = 'intruder'`) != 0 {
			t.Fatal("a refused request created a role")
		}
	})

	t.Run("managers list the roles of their initiative to grant them", func(t *testing.T) {
		p, err := e.svc.ListRoles(ctx, c.mgrA, strings.ToUpper(alpha.id), portal.PageRequest{})
		if err != nil {
			t.Fatal(err)
		}
		var keys []string
		for _, r := range p.Items {
			keys = append(keys, r.Key)
			if r.InitiativeID != alpha.id {
				t.Fatalf("a role of another initiative in the listing: %+v", r)
			}
		}
		slices.Sort(keys)
		if want := []string{"editor", strings.Repeat("k", 32), "owner", "viewer"}; !slices.Equal(keys, want) {
			t.Fatalf("keys = %v, want %v", keys, want)
		}
		if _, err := e.svc.ListRoles(ctx, c.admin, beta.id, portal.PageRequest{}); err != nil {
			t.Fatalf("admin: %v", err)
		}
		for name, who := range map[string]string{"manager of another initiative": c.mgrB, "plain user": c.plain} {
			if _, err := e.svc.ListRoles(ctx, who, alpha.id, portal.PageRequest{}); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("%s: %v", name, err)
			}
		}
		if _, err := e.svc.ListRoles(ctx, c.disabledAdmin, alpha.id, portal.PageRequest{}); !errors.Is(err, portal.ErrUserInactive) {
			t.Errorf("disabled admin: %v", err)
		}
		if _, err := e.svc.ListRoles(ctx, c.admin, "00000000-0000-7000-8000-000000000000", portal.PageRequest{}); !errors.Is(err, portal.ErrNotFound) {
			t.Errorf("unknown initiative: %v", err)
		}
		page, err := e.svc.ListRoles(ctx, c.admin, alpha.id, portal.PageRequest{Limit: 2})
		if err != nil || len(page.Items) != 2 || page.NextCursor == "" {
			t.Fatalf("first page: %+v, %v", page, err)
		}
		next, err := e.svc.ListRoles(ctx, c.admin, alpha.id, portal.PageRequest{Limit: 2, Cursor: page.NextCursor})
		if err != nil || len(next.Items) != 2 || next.NextCursor != "" || next.Items[0].ID == page.Items[0].ID {
			t.Fatalf("second page: %+v, %v", next, err)
		}
		if _, err := e.svc.ListApps(ctx, c.admin, alpha.id, portal.PageRequest{Cursor: page.NextCursor}); !errors.Is(err, portal.ErrInvalidCursor) {
			t.Fatalf("a roles cursor on the apps listing: %v", err)
		}
	})

	t.Run("toggling manages_access is audited, and a no-op writes nothing", func(t *testing.T) {
		before := e.auditCount()
		r, err := e.svc.UpdateRole(ctx, c.admin, alpha.editor, portal.RolePatch{ManagesAccess: &yes})
		if err != nil || !r.ManagesAccess || r.Key != "editor" || r.ID != alpha.editor {
			t.Fatalf("role = %+v, %v", r, err)
		}
		if e.auditCount() != before+1 {
			t.Fatalf("audit rows %d -> %d", before, e.auditCount())
		}
		a := e.lastAudit()
		if a.Actor != c.admin || a.Initiative != alpha.id || a.Action != "role.update" || a.TargetType != "role" || a.TargetID != alpha.editor ||
			a.Before["manages_access"] != false || a.After["manages_access"] != true || a.After["key"] != "editor" {
			t.Fatalf("audit = %+v", a)
		}
		if _, err := e.svc.UpdateRole(ctx, c.admin, strings.ToUpper(alpha.editor), portal.RolePatch{ManagesAccess: &yes}); err != nil || e.auditCount() != before+1 {
			t.Fatalf("no-op: %v, audit %d", err, e.auditCount())
		}
		if _, err := e.svc.UpdateRole(ctx, c.admin, alpha.editor, portal.RolePatch{ManagesAccess: &no}); err != nil {
			t.Fatalf("demote a role without grants: %v", err)
		}
		_, err = e.svc.UpdateRole(ctx, c.admin, alpha.editor, portal.RolePatch{})
		wantValidation(t, err, portal.FieldProblem{Field: "manages_access", Code: portal.CodeRequired})
	})

	t.Run("updating needs a platform admin", func(t *testing.T) {
		before := e.auditCount()
		patch := portal.RolePatch{ManagesAccess: &yes}
		if _, err := e.svc.UpdateRole(ctx, c.mgrA, alpha.editor, patch); !errors.Is(err, portal.ErrForbidden) {
			t.Errorf("manager: %v", err)
		}
		for name, who := range map[string]string{"manager of another initiative": c.mgrB, "plain user": c.plain} {
			if _, err := e.svc.UpdateRole(ctx, who, alpha.editor, patch); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("%s: %v", name, err)
			}
		}
		if _, err := e.svc.UpdateRole(ctx, c.disabledAdmin, alpha.editor, patch); !errors.Is(err, portal.ErrUserInactive) {
			t.Errorf("disabled admin: %v", err)
		}
		for _, id := range []string{"00000000-0000-7000-8000-000000000000", "x"} {
			if _, err := e.svc.UpdateRole(ctx, c.admin, id, patch); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("role %q: %v", id, err)
			}
		}
		if e.auditCount() != before || scalar[bool](t, e.owner, `SELECT manages_access FROM initiative_roles WHERE id = $1`, alpha.editor) {
			t.Fatal("a refused request changed state")
		}
	})

	t.Run("deleting needs a platform admin", func(t *testing.T) {
		before := e.auditCount()
		if err := e.svc.DeleteRole(ctx, c.mgrA, alpha.editor); !errors.Is(err, portal.ErrForbidden) {
			t.Errorf("manager: %v", err)
		}
		for name, who := range map[string]string{"manager of another initiative": c.mgrB, "plain user": c.plain} {
			if err := e.svc.DeleteRole(ctx, who, alpha.editor); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("%s: %v", name, err)
			}
		}
		if err := e.svc.DeleteRole(ctx, c.disabledAdmin, alpha.editor); !errors.Is(err, portal.ErrUserInactive) {
			t.Errorf("disabled admin: %v", err)
		}
		if err := e.svc.DeleteRole(ctx, c.admin, "x"); !errors.Is(err, portal.ErrNotFound) {
			t.Errorf("malformed id: %v", err)
		}
		if e.auditCount() != before || e.count(`SELECT count(*) FROM initiative_roles WHERE id = $1`, alpha.editor) != 1 {
			t.Fatal("a refused request changed state")
		}
	})

	t.Run("a role is deleted only without grants", func(t *testing.T) {
		// editor has no grants yet; give a second role one.
		used := e.w.role(t, alpha.id, "used")
		e.w.grantUser(t, alpha.id, used, e.w.user(t, "active"), "")
		before := e.auditCount()
		if err := e.svc.DeleteRole(ctx, c.admin, used); !errors.Is(err, portal.ErrRoleInUse) {
			t.Fatalf("err = %v, want ErrRoleInUse", err)
		}
		if e.count(`SELECT count(*) FROM initiative_roles WHERE id = $1`, used) != 1 || e.auditCount() != before {
			t.Fatal("a refused deletion changed state")
		}
		if err := e.svc.DeleteRole(ctx, c.admin, alpha.editor); err != nil {
			t.Fatal(err)
		}
		a := e.lastAudit()
		if e.auditCount() != before+1 || a.Actor != c.admin || a.Initiative != alpha.id || a.Action != "role.delete" ||
			a.TargetType != "role" || a.TargetID != alpha.editor || a.Before["key"] != "editor" || a.After != nil {
			t.Fatalf("audit = %+v (%d rows)", a, e.auditCount())
		}
		if err := e.svc.DeleteRole(ctx, c.admin, alpha.editor); !errors.Is(err, portal.ErrNotFound) {
			t.Fatalf("repeated deletion: %v, want ErrNotFound", err)
		}
	})
}

// Turning off manages_access follows the last-manager rule of grant revocation, with the same definition of an
// effective manager.
func TestRoleDemotionKeepsAnEffectiveManager(t *testing.T) {
	t.Parallel()
	no := false
	tests := []struct {
		name  string
		build func(e *adminEnv, init initiativeWorld, lead string) // lead: a second manager role
		role  func(init initiativeWorld, lead string) string       // the role to demote
		want  error
	}{
		{"another role keeps an active manager", func(e *adminEnv, init initiativeWorld, lead string) {
			e.w.grantUser(e.t, init.id, init.manager, e.w.user(e.t, "active"), "")
			e.w.grantUser(e.t, init.id, lead, e.w.user(e.t, "active"), "")
		}, func(i initiativeWorld, _ string) string { return i.manager }, nil},
		{"the only role with an active manager", func(e *adminEnv, init initiativeWorld, lead string) {
			e.w.grantUser(e.t, init.id, init.manager, e.w.user(e.t, "active"), "")
		}, func(i initiativeWorld, _ string) string { return i.manager }, portal.ErrLastManager},
		{"the other role's manager is disabled", func(e *adminEnv, init initiativeWorld, lead string) {
			e.w.grantUser(e.t, init.id, init.manager, e.w.user(e.t, "active"), "")
			e.w.grantUser(e.t, init.id, lead, e.w.user(e.t, "disabled"), "")
		}, func(i initiativeWorld, _ string) string { return i.manager }, portal.ErrLastManager},
		{"the other role's grant is expired", func(e *adminEnv, init initiativeWorld, lead string) {
			e.w.grantUser(e.t, init.id, init.manager, e.w.user(e.t, "active"), "")
			e.w.grantUser(e.t, init.id, lead, e.w.user(e.t, "active"), "2000-01-01T00:00:00Z")
		}, func(i initiativeWorld, _ string) string { return i.manager }, portal.ErrLastManager},
		{"the other role's group has an active member", func(e *adminEnv, init initiativeWorld, lead string) {
			e.w.grantUser(e.t, init.id, init.manager, e.w.user(e.t, "active"), "")
			g := e.w.group(e.t, "ops")
			e.w.member(e.t, g, e.w.user(e.t, "active"))
			e.w.grantGroup(e.t, init.id, lead, g)
		}, func(i initiativeWorld, _ string) string { return i.manager }, nil},
		{"the other role's group has no active member", func(e *adminEnv, init initiativeWorld, lead string) {
			e.w.grantUser(e.t, init.id, init.manager, e.w.user(e.t, "active"), "")
			g := e.w.group(e.t, "ops")
			e.w.member(e.t, g, e.w.user(e.t, "disabled"))
			e.w.grantGroup(e.t, init.id, lead, g)
		}, func(i initiativeWorld, _ string) string { return i.manager }, portal.ErrLastManager},
		{"the role holds only a disabled user", func(e *adminEnv, init initiativeWorld, lead string) {
			e.w.grantUser(e.t, init.id, init.manager, e.w.user(e.t, "disabled"), "")
		}, func(i initiativeWorld, _ string) string { return i.manager }, nil},
		{"the role holds only an expired grant", func(e *adminEnv, init initiativeWorld, lead string) {
			e.w.grantUser(e.t, init.id, init.manager, e.w.user(e.t, "active"), "2000-01-01T00:00:00Z")
		}, func(i initiativeWorld, _ string) string { return i.manager }, nil},
		{"the role holds no grants", func(e *adminEnv, init initiativeWorld, lead string) {}, func(i initiativeWorld, _ string) string { return i.manager }, nil},
		{"two grants of the same role are one role", func(e *adminEnv, init initiativeWorld, lead string) {
			e.w.grantUser(e.t, init.id, init.manager, e.w.user(e.t, "active"), "")
			e.w.grantUser(e.t, init.id, init.manager, e.w.user(e.t, "active"), "")
		}, func(i initiativeWorld, _ string) string { return i.manager }, portal.ErrLastManager},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newCatalogEnv(t)
			ctx := e.ctx()
			admin := e.admin()
			init := e.initiative("alpha")
			lead := e.pw.managerRole(init.id, "lead")
			tt.build(e, init, lead)
			role := tt.role(init, lead)
			before, events := e.count(`SELECT count(*) FROM initiative_roles WHERE manages_access`), e.auditCount()

			_, err := e.svc.UpdateRole(ctx, admin, role, portal.RolePatch{ManagesAccess: &no})
			if tt.want != nil {
				if !errors.Is(err, tt.want) {
					t.Fatalf("err = %v, want %v", err, tt.want)
				}
				if e.count(`SELECT count(*) FROM initiative_roles WHERE manages_access`) != before || e.auditCount() != events {
					t.Fatal("a refused demotion changed state")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if e.count(`SELECT count(*) FROM initiative_roles WHERE manages_access`) != before-1 || e.auditCount() != events+1 {
				t.Fatalf("roles/audit after: %d / %d", e.count(`SELECT count(*) FROM initiative_roles WHERE manages_access`), e.auditCount())
			}
		})
	}
}

// After a demotion the grants of the role are no longer manager grants: revoking one is ordinary.
func TestDemotedRoleGrantsAreOrdinaryGrants(t *testing.T) {
	t.Parallel()
	e := newCatalogEnv(t)
	ctx := e.ctx()
	admin := e.admin()
	init := e.initiative("alpha")
	lead := e.pw.managerRole(init.id, "lead")
	e.w.grantUser(t, init.id, init.manager, e.w.user(t, "active"), "")
	e.w.grantUser(t, init.id, lead, e.w.user(t, "active"), "")
	ownerGrant := scalar[string](t, e.owner, `SELECT id::text FROM initiative_access WHERE role_id = $1`, init.manager)
	leadGrant := scalar[string](t, e.owner, `SELECT id::text FROM initiative_access WHERE role_id = $1`, lead)
	no := false

	if _, err := e.svc.UpdateRole(ctx, admin, init.manager, portal.RolePatch{ManagesAccess: &no}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteGrant(ctx, admin, leadGrant); !errors.Is(err, portal.ErrLastManager) {
		t.Fatalf("the last manager grant: %v, want ErrLastManager", err)
	}
	if err := e.svc.DeleteGrant(ctx, admin, ownerGrant); err != nil {
		t.Fatalf("a grant of the demoted role: %v", err)
	}
}

func TestAppCatalog(t *testing.T) {
	t.Parallel()
	e := newCatalogEnv(t)
	ctx := e.ctx()
	alpha, beta := e.initiative("alpha"), e.initiative("beta")
	c := e.callers(alpha, beta)
	e.platform(uniqueHost())
	str := func(s string) *string { return &s }

	t.Run("an admin creates an app with a generated id and one audit row records it", func(t *testing.T) {
		before := e.auditCount()
		a, err := e.svc.CreateApp(ctx, c.admin, alpha.id, portal.NewApp{Name: "Hello"})
		if err != nil {
			t.Fatal(err)
		}
		if !appIDCheck.MatchString(a.ID) || a.InitiativeID != alpha.id || a.InitiativeKey != "alpha" || a.Name != "Hello" ||
			a.Status != "active" || a.Deployments == nil || len(a.Deployments) != 0 || a.CreatedAt.IsZero() {
			t.Fatalf("app = %+v", a)
		}
		if e.auditCount() != before+1 {
			t.Fatalf("audit rows %d -> %d", before, e.auditCount())
		}
		au := e.lastAudit()
		if au.Actor != c.admin || au.Initiative != alpha.id || au.Action != "app.create" || au.TargetType != "app" || au.TargetID != a.ID ||
			au.Before != nil || au.After["name"] != "Hello" || au.After["status"] != "active" || au.After["initiative_id"] != alpha.id {
			t.Fatalf("audit = %+v", au)
		}
		b, err := e.svc.CreateApp(ctx, c.admin, alpha.id, portal.NewApp{})
		if err != nil || b.ID == a.ID || !appIDCheck.MatchString(b.ID) {
			t.Fatalf("second app = %+v, %v", b, err)
		}
	})

	t.Run("creating validates the name and needs a platform admin and an existing initiative", func(t *testing.T) {
		before := e.count(`SELECT count(*) FROM apps`)
		for _, name := range []string{strings.Repeat("n", 201), "a\x00", "a\xff"} {
			_, err := e.svc.CreateApp(ctx, c.admin, alpha.id, portal.NewApp{Name: name})
			wantValidation(t, err, portal.FieldProblem{Field: "name", Code: portal.CodeInvalid})
		}
		if _, err := e.svc.CreateApp(ctx, c.mgrA, alpha.id, portal.NewApp{}); !errors.Is(err, portal.ErrForbidden) {
			t.Errorf("manager: %v", err)
		}
		for name, who := range map[string]string{"manager of another initiative": c.mgrB, "plain user": c.plain} {
			if _, err := e.svc.CreateApp(ctx, who, alpha.id, portal.NewApp{}); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("%s: %v", name, err)
			}
		}
		if _, err := e.svc.CreateApp(ctx, c.disabledAdmin, alpha.id, portal.NewApp{}); !errors.Is(err, portal.ErrUserInactive) {
			t.Errorf("disabled admin: %v", err)
		}
		for _, id := range []string{"00000000-0000-7000-8000-000000000000", "x"} {
			if _, err := e.svc.CreateApp(ctx, c.admin, id, portal.NewApp{}); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("initiative %q: %v", id, err)
			}
		}
		if e.count(`SELECT count(*) FROM apps`) != before {
			t.Fatal("a refused request created an app")
		}
	})

	t.Run("the listing carries the deployments, is scoped to the initiative and paginates by app id", func(t *testing.T) {
		withHosts := e.w.app(t, alpha.id, "z.example.test")
		mustExec(t, e.owner, `INSERT INTO deployments (host, app_id) VALUES ('a.example.test', $1)`, withHosts)
		e.w.app(t, beta.id, "beta.example.test") // never in the listing of alpha

		var all []portal.App
		req := portal.PageRequest{Limit: 2}
		for range 10 {
			p, err := e.svc.ListApps(ctx, c.mgrA, strings.ToUpper(alpha.id), req)
			if err != nil {
				t.Fatal(err)
			}
			all = append(all, p.Items...)
			if p.NextCursor == "" {
				break
			}
			req.Cursor = p.NextCursor
		}
		if len(all) != 3 {
			t.Fatalf("%d apps over the pages, want 3", len(all))
		}
		ids := make([]string, len(all))
		for i, a := range all {
			ids[i] = a.ID
			if a.InitiativeID != alpha.id {
				t.Fatalf("an app of another initiative: %+v", a)
			}
		}
		if !slices.IsSorted(ids) || len(slices.Compact(slices.Clone(ids))) != 3 {
			t.Fatalf("ids out of order or repeated: %v", ids)
		}
		for _, a := range all {
			if a.ID != withHosts {
				continue
			}
			if len(a.Deployments) != 2 || a.Deployments[0].Host != "a.example.test" || a.Deployments[1].Host != "z.example.test" ||
				a.Deployments[0].AppID != a.ID || a.Deployments[0].InitiativeID != alpha.id || a.Deployments[0].ID == "" {
				t.Fatalf("deployments = %+v", a.Deployments)
			}
		}
		if _, err := e.svc.ListApps(ctx, c.admin, alpha.id, portal.PageRequest{Cursor: strings.ToLower(req.Cursor) + "x"}); !errors.Is(err, portal.ErrInvalidCursor) {
			t.Errorf("tampered cursor: %v", err)
		}
		for name, who := range map[string]string{"manager of another initiative": c.mgrB, "plain user": c.plain} {
			if _, err := e.svc.ListApps(ctx, who, alpha.id, portal.PageRequest{}); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("%s: %v", name, err)
			}
		}
		if _, err := e.svc.ListApps(ctx, c.disabledAdmin, alpha.id, portal.PageRequest{}); !errors.Is(err, portal.ErrUserInactive) {
			t.Errorf("disabled admin: %v", err)
		}
		if _, err := e.svc.ListApps(ctx, c.admin, "00000000-0000-7000-8000-000000000000", portal.PageRequest{}); !errors.Is(err, portal.ErrNotFound) {
			t.Errorf("unknown initiative: %v", err)
		}
	})

	t.Run("rename, disable and enable are audited with their own action", func(t *testing.T) {
		app := e.w.app(t, alpha.id, uniqueHost())
		steps := []struct {
			patch  portal.AppPatch
			action string
			name   string
			status string
		}{
			{portal.AppPatch{Name: str("Renamed")}, "app.update", "Renamed", "active"},
			{portal.AppPatch{Status: str("disabled")}, "app.disable", "Renamed", "disabled"},
			{portal.AppPatch{Status: str("active")}, "app.enable", "Renamed", "active"},
			{portal.AppPatch{Name: str("Both"), Status: str("disabled")}, "app.disable", "Both", "disabled"},
		}
		for _, st := range steps {
			before := e.auditCount()
			prev := scalar[string](t, e.owner, `SELECT status FROM apps WHERE id = $1`, app)
			a, err := e.svc.UpdateApp(ctx, c.admin, app, st.patch)
			if err != nil || a.Status != st.status || a.Name != st.name || a.InitiativeID != alpha.id || len(a.Deployments) != 1 {
				t.Fatalf("%+v: %+v, %v", st.patch, a, err)
			}
			if e.auditCount() != before+1 {
				t.Fatalf("audit rows %d -> %d", before, e.auditCount())
			}
			au := e.lastAudit()
			if au.Actor != c.admin || au.Initiative != alpha.id || au.Action != st.action || au.TargetType != "app" || au.TargetID != app ||
				au.Before["status"] != prev || au.After["status"] != st.status || au.After["name"] != st.name || au.After["initiative_id"] != alpha.id {
				t.Fatalf("audit = %+v", au)
			}
		}
		before := e.auditCount()
		if _, err := e.svc.UpdateApp(ctx, c.admin, app, portal.AppPatch{Status: str("disabled")}); err != nil || e.auditCount() != before {
			t.Fatalf("no-op: %v, audit %d -> %d", err, before, e.auditCount())
		}
		_, err := e.svc.UpdateApp(ctx, c.admin, app, portal.AppPatch{})
		wantValidation(t, err, portal.FieldProblem{Field: "name", Code: portal.CodeAtLeastOne}, portal.FieldProblem{Field: "status", Code: portal.CodeAtLeastOne})
		_, err = e.svc.UpdateApp(ctx, c.admin, app, portal.AppPatch{Status: str("inactive")})
		wantValidation(t, err, portal.FieldProblem{Field: "status", Code: portal.CodeInvalid})
	})

	t.Run("only platform admins update an app", func(t *testing.T) {
		app := e.w.app(t, alpha.id, uniqueHost())
		before := e.auditCount()
		patch := portal.AppPatch{Name: str("hijacked")}
		if _, err := e.svc.UpdateApp(ctx, c.mgrA, app, patch); !errors.Is(err, portal.ErrForbidden) {
			t.Errorf("manager: %v", err)
		}
		for name, who := range map[string]string{"manager of another initiative": c.mgrB, "plain user": c.plain} {
			if _, err := e.svc.UpdateApp(ctx, who, app, patch); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("%s: %v", name, err)
			}
		}
		if _, err := e.svc.UpdateApp(ctx, c.disabledAdmin, app, patch); !errors.Is(err, portal.ErrUserInactive) {
			t.Errorf("disabled admin: %v", err)
		}
		for _, id := range []string{"app_00000000000000000000000000", "x", strings.ToLower(portalAppTestID)} {
			if _, err := e.svc.UpdateApp(ctx, c.admin, id, patch); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("app %q: %v", id, err)
			}
		}
		if e.auditCount() != before || scalar[string](t, e.owner, `SELECT name FROM apps WHERE id = $1`, app) == "hijacked" {
			t.Fatal("a refused request changed state")
		}
	})

	t.Run("the portal app cannot be disabled but can be renamed", func(t *testing.T) {
		before := e.auditCount()
		if _, err := e.svc.UpdateApp(ctx, c.admin, portalAppTestID, portal.AppPatch{Status: str("disabled")}); !errors.Is(err, portal.ErrPortalApp) {
			t.Fatalf("err = %v, want ErrPortalApp", err)
		}
		if st := scalar[string](t, e.owner, `SELECT status FROM apps WHERE id = $1`, portalAppTestID); st != "active" || e.auditCount() != before {
			t.Fatalf("status %q, audit %d -> %d", st, before, e.auditCount())
		}
		if a, err := e.svc.UpdateApp(ctx, c.admin, portalAppTestID, portal.AppPatch{Name: str("Portal 2"), Status: str("active")}); err != nil || a.Name != "Portal 2" {
			t.Fatalf("rename: %+v, %v", a, err)
		}
	})
}

// PORTAL_APP_ID pointing to an app of another initiative protects nothing, like in the ForwardAuth evaluator.
func TestMistypedPortalAppIsNotGuarded(t *testing.T) {
	t.Parallel()
	e := newCatalogEnv(t)
	ctx := e.ctx()
	admin := e.admin()
	other := e.initiative("alpha")
	host := uniqueHost()
	mustExec(t, e.owner, `INSERT INTO apps (id, initiative_id) VALUES ($1, $2)`, portalAppTestID, other.id)
	mustExec(t, e.owner, `INSERT INTO deployments (host, app_id) VALUES ($1, $2)`, host, portalAppTestID)
	disabled := "disabled"

	if _, err := e.svc.UpdateApp(ctx, admin, portalAppTestID, portal.AppPatch{Status: &disabled}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := e.svc.DeleteDeployment(ctx, admin, e.deploymentID(host)); err != nil {
		t.Fatalf("delete the last deployment: %v", err)
	}
}

func TestDeploymentCatalog(t *testing.T) {
	t.Parallel()
	e := newCatalogEnv(t)
	ctx := e.ctx()
	alpha, beta := e.initiative("alpha"), e.initiative("beta")
	c := e.callers(alpha, beta)
	app := e.w.app(t, alpha.id, uniqueHost())
	portalHost := uniqueHost()
	e.platform(portalHost)

	t.Run("an admin attaches a host and one audit row records it", func(t *testing.T) {
		host := uniqueHost()
		before := e.auditCount()
		d, err := e.svc.CreateDeployment(ctx, c.admin, app, portal.NewDeployment{Host: host})
		if err != nil {
			t.Fatal(err)
		}
		if d.ID == "" || d.AppID != app || d.Host != host || d.InitiativeID != alpha.id || d.CreatedAt.IsZero() {
			t.Fatalf("deployment = %+v", d)
		}
		if got := scalar[string](t, e.owner, `SELECT app_id FROM deployments WHERE host = $1`, host); got != app {
			t.Fatalf("stored app = %q", got)
		}
		if e.auditCount() != before+1 {
			t.Fatalf("audit rows %d -> %d", before, e.auditCount())
		}
		a := e.lastAudit()
		if a.Actor != c.admin || a.Initiative != alpha.id || a.Action != "deployment.create" || a.TargetType != "deployment" || a.TargetID != d.ID ||
			a.Before != nil || a.After["host"] != host || a.After["app_id"] != app {
			t.Fatalf("audit = %+v", a)
		}
	})

	t.Run("hosts that are not valid DNS names, or are reserved, are refused", func(t *testing.T) {
		label63 := strings.Repeat("a", 63)
		tests := []struct {
			name, host, code string
		}{
			{"empty", "", portal.CodeRequired},
			{"uppercase", "App.Example.Test", portal.CodeInvalid},
			{"trailing dot", "app.example.test.", portal.CodeInvalid},
			{"wildcard", "*.example.test", portal.CodeInvalid},
			{"empty label", "a..example.test", portal.CodeInvalid},
			{"label of 64", label63 + "a.example.test", portal.CodeInvalid},
			{"total of 254", label63 + "." + label63 + "." + label63 + "." + strings.Repeat("b", 254-3*64), portal.CodeInvalid},
			{"hyphen at the start of a label", "-a.example.test", portal.CodeInvalid},
			{"underscore", "a_b.example.test", portal.CodeInvalid},
			{"port", "app.example.test:8443", portal.CodeInvalid},
			{"path", "app.example.test/x", portal.CodeInvalid},
			{"IP address", "10.0.0.1", portal.CodeInvalid},
			{"NUL", "app.example.test\x00", portal.CodeInvalid},
			{"invalid UTF-8", "a\xff.example.test", portal.CodeInvalid},
			{"the auth host", catalogAuthHost, portal.CodeReserved},
			{"the auth host in uppercase", strings.ToUpper(catalogAuthHost), portal.CodeInvalid},
		}
		before, events := e.count(`SELECT count(*) FROM deployments`), e.auditCount()
		for _, tt := range tests {
			_, err := e.svc.CreateDeployment(ctx, c.admin, app, portal.NewDeployment{Host: tt.host})
			if got := validationFields(t, err); len(got) != 1 || got[0].Field != "host" || got[0].Code != tt.code {
				t.Errorf("%s: problems = %v, want host/%s", tt.name, got, tt.code)
			}
		}
		if e.count(`SELECT count(*) FROM deployments`) != before || e.auditCount() != events {
			t.Fatal("a refused host changed state")
		}
		if _, err := e.svc.CreateDeployment(ctx, c.admin, app, portal.NewDeployment{Host: label63 + "." + label63 + "." + label63 + "." + strings.Repeat("b", 253-3*64)}); err != nil {
			t.Fatalf("a host of 253 bytes: %v", err)
		}
	})

	t.Run("a host that exists, on this app or on another one, is a conflict", func(t *testing.T) {
		before, events := e.count(`SELECT count(*) FROM deployments`), e.auditCount()
		mine := scalar[string](t, e.owner, `SELECT host FROM deployments WHERE app_id = $1 LIMIT 1`, app)
		for name, host := range map[string]string{"same app": mine, "the portal's host on another app": portalHost} {
			if _, err := e.svc.CreateDeployment(ctx, c.admin, app, portal.NewDeployment{Host: host}); !errors.Is(err, portal.ErrHostTaken) {
				t.Errorf("%s: %v, want ErrHostTaken", name, err)
			}
		}
		if e.count(`SELECT count(*) FROM deployments`) != before || e.auditCount() != events {
			t.Fatal("a conflict changed state")
		}
		if got := scalar[string](t, e.owner, `SELECT app_id FROM deployments WHERE host = $1`, portalHost); got != portalAppTestID {
			t.Fatalf("the portal's host moved to %q", got)
		}
	})

	t.Run("creating needs a platform admin and an existing app", func(t *testing.T) {
		in := portal.NewDeployment{Host: uniqueHost()}
		before := e.auditCount()
		if _, err := e.svc.CreateDeployment(ctx, c.mgrA, app, in); !errors.Is(err, portal.ErrForbidden) {
			t.Errorf("manager: %v", err)
		}
		for name, who := range map[string]string{"manager of another initiative": c.mgrB, "plain user": c.plain} {
			if _, err := e.svc.CreateDeployment(ctx, who, app, in); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("%s: %v", name, err)
			}
		}
		if _, err := e.svc.CreateDeployment(ctx, c.disabledAdmin, app, in); !errors.Is(err, portal.ErrUserInactive) {
			t.Errorf("disabled admin: %v", err)
		}
		for _, id := range []string{"app_00000000000000000000000000", "x", strings.ToLower(portalAppTestID)} {
			if _, err := e.svc.CreateDeployment(ctx, c.admin, id, in); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("app %q: %v", id, err)
			}
		}
		if e.auditCount() != before || e.count(`SELECT count(*) FROM deployments WHERE host = $1`, in.Host) != 0 {
			t.Fatal("a refused request changed state")
		}
	})

	t.Run("an admin removes a host and one audit row keeps it", func(t *testing.T) {
		host := uniqueHost()
		mustExec(t, e.owner, `INSERT INTO deployments (host, app_id) VALUES ($1, $2)`, host, app)
		id := e.deploymentID(host)
		before := e.auditCount()
		if err := e.svc.DeleteDeployment(ctx, c.admin, strings.ToUpper(id)); err != nil {
			t.Fatal(err)
		}
		a := e.lastAudit()
		if e.auditCount() != before+1 || a.Actor != c.admin || a.Initiative != alpha.id || a.Action != "deployment.delete" ||
			a.TargetType != "deployment" || a.TargetID != id || a.Before["host"] != host || a.Before["app_id"] != app || a.After != nil {
			t.Fatalf("audit = %+v (%d rows)", a, e.auditCount())
		}
		if e.count(`SELECT count(*) FROM deployments WHERE host = $1`, host) != 0 {
			t.Fatal("the deployment is still there")
		}
		if err := e.svc.DeleteDeployment(ctx, c.admin, id); !errors.Is(err, portal.ErrNotFound) {
			t.Fatalf("repeated removal: %v, want ErrNotFound", err)
		}
	})

	t.Run("removing needs a platform admin", func(t *testing.T) {
		host := uniqueHost()
		mustExec(t, e.owner, `INSERT INTO deployments (host, app_id) VALUES ($1, $2)`, host, app)
		id := e.deploymentID(host)
		before := e.auditCount()
		if err := e.svc.DeleteDeployment(ctx, c.mgrA, id); !errors.Is(err, portal.ErrForbidden) {
			t.Errorf("manager: %v", err)
		}
		for name, who := range map[string]string{"manager of another initiative": c.mgrB, "plain user": c.plain} {
			if err := e.svc.DeleteDeployment(ctx, who, id); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("%s: %v", name, err)
			}
		}
		if err := e.svc.DeleteDeployment(ctx, c.disabledAdmin, id); !errors.Is(err, portal.ErrUserInactive) {
			t.Errorf("disabled admin: %v", err)
		}
		if err := e.svc.DeleteDeployment(ctx, c.admin, "x"); !errors.Is(err, portal.ErrNotFound) {
			t.Errorf("malformed id: %v", err)
		}
		if e.auditCount() != before || e.count(`SELECT count(*) FROM deployments WHERE id = $1`, id) != 1 {
			t.Fatal("a refused request changed state")
		}
	})

	t.Run("the last deployment of the portal app stays, any other can go", func(t *testing.T) {
		before := e.auditCount()
		if err := e.svc.DeleteDeployment(ctx, c.admin, e.deploymentID(portalHost)); !errors.Is(err, portal.ErrLastPortalDeployment) {
			t.Fatalf("err = %v, want ErrLastPortalDeployment", err)
		}
		if e.count(`SELECT count(*) FROM deployments WHERE app_id = $1`, portalAppTestID) != 1 || e.auditCount() != before {
			t.Fatal("a refused removal changed state")
		}
		second := uniqueHost()
		if _, err := e.svc.CreateDeployment(ctx, c.admin, portalAppTestID, portal.NewDeployment{Host: second}); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.DeleteDeployment(ctx, c.admin, e.deploymentID(portalHost)); err != nil {
			t.Fatalf("with a second host: %v", err)
		}
		if err := e.svc.DeleteDeployment(ctx, c.admin, e.deploymentID(second)); !errors.Is(err, portal.ErrLastPortalDeployment) {
			t.Fatalf("the new last one: %v", err)
		}

		// An app that is not the portal may lose its last deployment.
		lone := uniqueHost()
		e.w.app(t, beta.id, lone)
		if err := e.svc.DeleteDeployment(ctx, c.admin, e.deploymentID(lone)); err != nil {
			t.Fatalf("last deployment of an ordinary app: %v", err)
		}
	})
}

// Two administrators turning off manages_access on the two manager roles of an initiative at the same time:
// exactly one succeeds and a manager remains, however they interleave. Both read the roles before either takes
// a lock; without the initiative lock and the lock on the manager grants both would count the other role as
// the remaining manager.
func TestConcurrentRoleDemotionsKeepAManager(t *testing.T) {
	t.Parallel()
	no := false
	for round := range 5 {
		t.Run(fmt.Sprintf("round %d", round), func(t *testing.T) {
			t.Parallel()
			e := newAdminEnvWith(t, func(s portal.Store) portal.Store { return meetingStore{Store: s, t: t, b: newBarrier()} })
			init := e.initiative("race")
			lead := e.pw.managerRole(init.id, "lead")
			e.w.grantUser(t, init.id, init.manager, e.w.user(t, "active"), "")
			e.w.grantUser(t, init.id, lead, e.w.user(t, "active"), "")
			adminX, adminY := e.admin(), e.admin()
			events := e.auditCount()

			var (
				wg   sync.WaitGroup
				errs [2]error
			)
			for i, pair := range [2][2]string{{adminX, init.manager}, {adminY, lead}} {
				wg.Go(func() { _, errs[i] = e.svc.UpdateRole(e.ctx(), pair[0], pair[1], portal.RolePatch{ManagesAccess: &no}) })
			}
			wg.Wait()
			var ok, last int
			for _, err := range errs {
				switch {
				case err == nil:
					ok++
				case errors.Is(err, portal.ErrLastManager):
					last++
				default:
					t.Errorf("unexpected error: %v", err)
				}
			}
			if ok != 1 || last != 1 {
				t.Fatalf("results = %v, want exactly one success and one ErrLastManager", errs)
			}
			if n := e.count(`SELECT count(*) FROM initiative_roles WHERE initiative_id = $1 AND manages_access`, init.id); n != 1 {
				t.Fatalf("%d manager roles left, want 1", n)
			}
			if e.auditCount() != events+1 {
				t.Fatalf("audit rows %d -> %d, want exactly one more", events, e.auditCount())
			}
		})
	}
}

// A demotion and a revocation of the other manager's grant, at the same time: they share the lock order, so
// exactly one of them goes through.
func TestConcurrentDemotionAndRevocationKeepAManager(t *testing.T) {
	t.Parallel()
	no := false
	for round := range 5 {
		t.Run(fmt.Sprintf("round %d", round), func(t *testing.T) {
			t.Parallel()
			e := newAdminEnvWith(t, func(s portal.Store) portal.Store { return meetingStore{Store: s, t: t, b: newBarrier()} })
			init := e.initiative("race")
			lead := e.pw.managerRole(init.id, "lead")
			ownerHolder, leadHolder := e.w.user(t, "active"), e.w.user(t, "active")
			e.w.grantUser(t, init.id, init.manager, ownerHolder, "")
			e.w.grantUser(t, init.id, lead, leadHolder, "")
			leadGrant := scalar[string](t, e.owner, `SELECT id::text FROM initiative_access WHERE user_id = $1`, leadHolder)
			adminX, adminY := e.admin(), e.admin()

			var (
				wg   sync.WaitGroup
				errs [2]error
			)
			wg.Go(func() {
				_, errs[0] = e.svc.UpdateRole(e.ctx(), adminX, init.manager, portal.RolePatch{ManagesAccess: &no})
			})
			wg.Go(func() { errs[1] = e.svc.DeleteGrant(e.ctx(), adminY, leadGrant) })
			wg.Wait()
			var ok int
			for _, err := range errs {
				switch {
				case err == nil:
					ok++
				case errors.Is(err, portal.ErrLastManager):
				default:
					t.Errorf("unexpected error: %v", err)
				}
			}
			if ok != 1 {
				t.Fatalf("results = %v, want exactly one success", errs)
			}
			managers := e.count(`
SELECT count(*) FROM initiative_access ia JOIN initiative_roles ir ON ir.id = ia.role_id
WHERE ia.initiative_id = $1 AND ir.manages_access`, init.id)
			if managers != 1 {
				t.Fatalf("%d manager grants left, want 1", managers)
			}
		})
	}
}

// Two administrators removing the last two deployments of the portal app at the same time: exactly one succeeds
// and the portal keeps a host. Both read their deployment before either locks the app row; without that lock
// both would count two deployments and delete.
func TestConcurrentRemovalOfThePortalsLastDeployments(t *testing.T) {
	t.Parallel()
	for round := range 5 {
		t.Run(fmt.Sprintf("round %d", round), func(t *testing.T) {
			t.Parallel()
			e := newAdminEnvCfg(t, func(s portal.Store) portal.Store { return meetingStore{Store: s, t: t, b: newBarrier()} },
				func(c *portal.Config) { c.PortalAppID = portalAppTestID })
			hostX, hostY := uniqueHost(), uniqueHost()
			e.platform(hostX, hostY)
			adminX, adminY := e.admin(), e.admin()
			events := e.auditCount()

			var (
				wg   sync.WaitGroup
				errs [2]error
			)
			for i, pair := range [2][2]string{{adminX, hostX}, {adminY, hostY}} {
				id := e.deploymentID(pair[1])
				wg.Go(func() { errs[i] = e.svc.DeleteDeployment(e.ctx(), pair[0], id) })
			}
			wg.Wait()
			var ok, last int
			for _, err := range errs {
				switch {
				case err == nil:
					ok++
				case errors.Is(err, portal.ErrLastPortalDeployment):
					last++
				default:
					t.Errorf("unexpected error: %v", err)
				}
			}
			if ok != 1 || last != 1 {
				t.Fatalf("results = %v, want exactly one success and one ErrLastPortalDeployment", errs)
			}
			if n := e.count(`SELECT count(*) FROM deployments WHERE app_id = $1`, portalAppTestID); n != 1 {
				t.Fatalf("%d deployments left, want 1", n)
			}
			if e.auditCount() != events+1 {
				t.Fatalf("audit rows %d -> %d, want exactly one more", events, e.auditCount())
			}
		})
	}
}

// crossStore makes the interleaving that deadlocks without the initiative lock deterministic: a deletion of a
// role waits (bounded) until some unit of work has locked the manager grants, then runs its DELETE.
type crossStore struct {
	portal.Store
	locked   chan struct{}
	once     *sync.Once
	attempts *atomic.Int32 // units of work started, retries included
}

func newCrossStore(s portal.Store) crossStore {
	return crossStore{Store: s, locked: make(chan struct{}), once: new(sync.Once), attempts: new(atomic.Int32)}
}

func (s crossStore) WithTx(ctx context.Context, fn func(portal.Tx) error) error {
	return s.Store.WithTx(ctx, func(tx portal.Tx) error {
		s.attempts.Add(1)
		return fn(crossTx{Tx: tx, s: s})
	})
}

type crossTx struct {
	portal.Tx
	s crossStore
}

func (x crossTx) Grants() portal.GrantRepo { return crossGrants{GrantRepo: x.Tx.Grants(), s: x.s} }
func (x crossTx) Catalog() portal.CatalogRepo {
	return crossCatalog{CatalogRepo: x.Tx.Catalog(), s: x.s}
}

type crossGrants struct {
	portal.GrantRepo
	s crossStore
}

func (g crossGrants) LockManagerGrants(ctx context.Context, id string) ([]portal.ManagerGrant, error) {
	out, err := g.GrantRepo.LockManagerGrants(ctx, id)
	g.s.once.Do(func() { close(g.s.locked) })
	return out, err
}

type crossCatalog struct {
	portal.CatalogRepo
	s crossStore
}

func (c crossCatalog) DeleteRole(ctx context.Context, id string) (bool, error) {
	select {
	case <-c.s.locked:
	case <-time.After(crossWait):
	}
	return c.CatalogRepo.DeleteRole(ctx, id)
}

// SetRoleManagesAccess gives a concurrent deletion time to take the role row before the demotion's UPDATE.
func (c crossCatalog) SetRoleManagesAccess(ctx context.Context, id string, v bool) error {
	time.Sleep(crossWait / 3)
	return c.CatalogRepo.SetRoleManagesAccess(ctx, id, v)
}

// crossWait bounds the wait of a deletion for the demotion's lock: with the initiative lock the deletion only
// gets here after the demotion finished, or the demotion never gets to lock, so the wait must not be unbounded.
const crossWait = 300 * time.Millisecond

// Deleting a role and turning off its manages_access at the same time. Both read the role before either takes a
// lock. The delete's foreign-key check takes key-share locks on the grants that the demotion locks FOR UPDATE:
// without the initiative lock in DeleteRole the two cross and deadlock (the store's retry then waits at the
// barrier for a partner that never comes).
func TestConcurrentRoleDeleteAndDemotion(t *testing.T) {
	t.Parallel()
	no := false
	for round := range 5 {
		t.Run(fmt.Sprintf("a role with grants, round %d", round), func(t *testing.T) {
			t.Parallel()
			var cross crossStore
			e := newAdminEnvWith(t, func(s portal.Store) portal.Store {
				cross = newCrossStore(meetingStore{Store: s, t: t, b: newBarrier()})
				return cross
			})
			init := e.initiative("race")
			lead := e.pw.managerRole(init.id, "lead")
			e.w.grantUser(t, init.id, init.manager, e.w.user(t, "active"), "")
			e.w.grantUser(t, init.id, lead, e.w.user(t, "active"), "")
			adminX, adminY := e.admin(), e.admin()

			var (
				wg   sync.WaitGroup
				errs [2]error
			)
			wg.Go(func() {
				_, errs[0] = e.svc.UpdateRole(e.ctx(), adminX, init.manager, portal.RolePatch{ManagesAccess: &no})
			})
			wg.Go(func() { errs[1] = e.svc.DeleteRole(e.ctx(), adminY, init.manager) })
			wg.Wait()
			if errs[0] != nil || !errors.Is(errs[1], portal.ErrRoleInUse) {
				t.Fatalf("results = %v, want the demotion to succeed and the deletion to be refused", errs)
			}
			// A deadlock is resolved by the store's retry, so it would not show in the results: count the attempts.
			if n := cross.attempts.Load(); n != 2 {
				t.Fatalf("%d attempts for two units of work: one was retried (a deadlock)", n)
			}
			if e.count(`SELECT count(*) FROM initiative_roles WHERE id = $1 AND NOT manages_access`, init.manager) != 1 {
				t.Fatal("the role is not demoted")
			}
		})

		t.Run(fmt.Sprintf("a role without grants, round %d", round), func(t *testing.T) {
			t.Parallel()
			e := newAdminEnvWith(t, func(s portal.Store) portal.Store { return meetingStore{Store: s, t: t, b: newBarrier()} })
			init := e.initiative("race")
			adminX, adminY := e.admin(), e.admin()
			role := init.manager

			var (
				wg   sync.WaitGroup
				errs [2]error
			)
			wg.Go(func() { _, errs[0] = e.svc.UpdateRole(e.ctx(), adminX, role, portal.RolePatch{ManagesAccess: &no}) })
			wg.Go(func() { errs[1] = e.svc.DeleteRole(e.ctx(), adminY, role) })
			wg.Wait()
			if errs[1] != nil {
				t.Fatalf("deletion: %v", errs[1])
			}
			if e.count(`SELECT count(*) FROM initiative_roles WHERE id = $1`, role) != 0 {
				t.Fatal("the role survived its deletion")
			}
			// The demotion either ran first (and is audited) or found the role gone: never a success on a deleted
			// role without its audit row, and never a false success with no row updated.
			updates := e.count(`SELECT count(*) FROM audit_events WHERE action = 'role.update' AND target_id = $1`, role)
			switch {
			case errs[0] == nil && updates == 1:
			case errors.Is(errs[0], portal.ErrNotFound) && updates == 0:
			default:
				t.Fatalf("demotion = %v with %d audit rows", errs[0], updates)
			}
		})
	}
}

// A vanished row is never a silent success: updating it reports not found.
func TestCatalogUpdatesOfAVanishedRowAreNotFound(t *testing.T) {
	t.Parallel()
	e := newCatalogEnv(t)
	ctx := e.ctx()
	missing := "00000000-0000-7000-8000-000000000000"
	err := e.store.WithTx(ctx, func(tx portal.Tx) error {
		c := tx.Catalog()
		for name, err := range map[string]error{
			"initiative": c.UpdateInitiative(ctx, missing, "n", "active"),
			"role":       c.SetRoleManagesAccess(ctx, missing, true),
			"app":        c.UpdateApp(ctx, "app_00000000000000000000000000", "n", "active"),
		} {
			if !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("%s: err = %v, want ErrNotFound", name, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
