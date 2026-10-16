package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"platform/api/internal/adminz"
	"platform/api/internal/audit"
	"platform/api/internal/portal"
	"platform/api/internal/postgres"
	"platform/api/internal/testdb"
)

// adminEnv is a portal service over a real database, running as the platform_portal role, plus the owner
// connection to build scenarios and inspect results.
type adminEnv struct {
	t     *testing.T
	owner *pgxpool.Pool
	w     world
	pw    portalWorld
	svc   *portal.Service
	store portal.Store
}

func newAdminEnv(t *testing.T) *adminEnv {
	t.Helper()
	return newAdminEnvWith(t, func(s portal.Store) portal.Store { return s })
}

// newAdminEnvWith lets a test decorate the store (to fail the audit insert, or to force an interleaving).
func newAdminEnvWith(t *testing.T, wrap func(portal.Store) portal.Store) *adminEnv {
	t.Helper()
	owner := newTestDB(t)
	pool := testdb.Portal(t, owner)
	store := wrap(postgres.NewPortalStore(pool))
	svc, err := portal.NewService(portal.Config{
		Permissions: postgres.NewPermissionsRepo(pool), Home: postgres.NewHomeRepo(pool), Store: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	w := world{pool: owner}
	return &adminEnv{t: t, owner: owner, w: w, pw: newPortalWorld(t, w), svc: svc, store: store}
}

func (e *adminEnv) ctx() context.Context {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(e.t.Context(), testTimeout)
	e.t.Cleanup(cancel)
	return ctx
}

func (e *adminEnv) admin() string {
	e.t.Helper()
	id := e.w.user(e.t, "active")
	e.pw.platformAdmin(id)
	return id
}

// manager creates an active user with a direct grant of the (manages_access) role.
func (e *adminEnv) manager(initiativeID, roleID string) string {
	e.t.Helper()
	id := e.w.user(e.t, "active")
	e.w.grantUser(e.t, initiativeID, roleID, id, "")
	return id
}

func (e *adminEnv) email(userID string) string {
	e.t.Helper()
	return scalar[string](e.t, e.owner, `SELECT email FROM users WHERE id = $1`, userID)
}

func (e *adminEnv) count(sql string, args ...any) int {
	e.t.Helper()
	return scalar[int](e.t, e.owner, sql, args...)
}

func (e *adminEnv) auditCount() int { return e.count(`SELECT count(*) FROM audit_events`) }

// auditRow is one audit_events row as the tests assert it.
type auditRow struct {
	Actor      string
	Initiative string
	Action     string
	TargetType string
	TargetID   string
	Before     map[string]any
	After      map[string]any
}

// lastAudit returns the newest audit row; it fails if there is none.
func (e *adminEnv) lastAudit() auditRow {
	e.t.Helper()
	var (
		r             auditRow
		before, after []byte
	)
	err := e.owner.QueryRow(e.ctx(), `
SELECT COALESCE(actor_id::text, ''), COALESCE(initiative_id::text, ''), action, target_type, target_id, before, after
FROM audit_events ORDER BY occurred_at DESC, id DESC LIMIT 1`).Scan(&r.Actor, &r.Initiative, &r.Action, &r.TargetType, &r.TargetID, &before, &after)
	if err != nil {
		e.t.Fatalf("no audit row: %v", err)
	}
	for _, d := range []struct {
		raw []byte
		to  *map[string]any
	}{{before, &r.Before}, {after, &r.After}} {
		if d.raw != nil {
			if err := json.Unmarshal(d.raw, d.to); err != nil {
				e.t.Fatalf("audit document %s: %v", d.raw, err)
			}
		}
	}
	return r
}

// initiativeWorld is an initiative with a manager role and a plain role.
type initiativeWorld struct {
	id      string
	manager string // role id with manages_access
	editor  string // role id without it
}

func (e *adminEnv) initiative(key string) initiativeWorld {
	e.t.Helper()
	id := e.w.initiative(e.t, key, "active")
	return initiativeWorld{id: id, manager: e.pw.managerRole(id, "owner"), editor: e.w.role(e.t, id, "editor")}
}

func validationFields(t *testing.T, err error) []portal.FieldProblem {
	t.Helper()
	v, ok := errors.AsType[*portal.ValidationError](err)
	if !ok {
		t.Fatalf("err = %v, want a validation error", err)
	}
	return v.Problems
}

func TestCreateGrant(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	alpha, beta := e.initiative("alpha"), e.initiative("beta")
	admin := e.admin()
	mgrA := e.manager(alpha.id, alpha.manager)
	mgrB := e.manager(beta.id, beta.manager)
	plain := e.w.user(t, "active")
	disabledAdmin := e.w.user(t, "disabled")
	e.pw.platformAdmin(disabledAdmin)
	target := e.w.user(t, "active")
	group := e.w.group(t, "ops")
	future := time.Now().Add(time.Hour)

	t.Run("admin grants a role to a user by email and one audit row records it", func(t *testing.T) {
		before := e.auditCount()
		g, err := e.svc.CreateGrant(ctx, admin, alpha.id, portal.NewGrant{Email: e.email(target), RoleID: alpha.editor, ExpiresAt: &future})
		if err != nil {
			t.Fatal(err)
		}
		if g.ID == "" || g.InitiativeID != alpha.id || g.Role.Key != "editor" || g.User == nil || g.User.ID != target || g.Group != nil ||
			g.ExpiresAt == nil || g.GrantedBy != admin {
			t.Fatalf("grant = %+v", g)
		}
		if n := e.count(`SELECT count(*) FROM initiative_access WHERE id = $1`, g.ID); n != 1 {
			t.Fatalf("%d rows", n)
		}
		if got := e.auditCount(); got != before+1 {
			t.Fatalf("audit rows %d -> %d, want exactly one more", before, got)
		}
		a := e.lastAudit()
		if a.Actor != admin || a.Initiative != alpha.id || a.Action != "grant.create" || a.TargetType != "grant" || a.TargetID != g.ID ||
			a.Before != nil || a.After["role_key"] != "editor" || a.After["user_id"] != target || a.After["user_email"] != e.email(target) {
			t.Fatalf("audit = %+v", a)
		}
	})

	t.Run("a manager grants to a group in their initiative, matched case-insensitively by email too", func(t *testing.T) {
		g, err := e.svc.CreateGrant(ctx, mgrA, alpha.id, portal.NewGrant{GroupID: group, RoleID: alpha.editor})
		if err != nil || g.Group == nil || g.Group.Key != "ops" || g.User != nil || g.ExpiresAt != nil {
			t.Fatalf("grant = %+v, %v", g, err)
		}
		if a := e.lastAudit(); a.Actor != mgrA || a.After["group_key"] != "ops" {
			t.Fatalf("audit = %+v", a)
		}
		upper := e.w.user(t, "active")
		g, err = e.svc.CreateGrant(ctx, mgrA, alpha.id, portal.NewGrant{Email: "  " + toUpper(e.email(upper)) + " ", RoleID: alpha.editor})
		if err != nil || g.User == nil || g.User.ID != upper {
			t.Fatalf("grant = %+v, %v", g, err)
		}
	})

	t.Run("a repeated grant is a conflict and adds no row and no audit event", func(t *testing.T) {
		in := portal.NewGrant{Email: e.email(target), RoleID: alpha.editor}
		rows, events := e.count(`SELECT count(*) FROM initiative_access`), e.auditCount()
		if _, err := e.svc.CreateGrant(ctx, admin, alpha.id, in); !errors.Is(err, portal.ErrGrantExists) {
			t.Fatalf("err = %v, want ErrGrantExists", err)
		}
		if _, err := e.svc.CreateGrant(ctx, admin, alpha.id, portal.NewGrant{GroupID: group, RoleID: alpha.editor}); !errors.Is(err, portal.ErrGrantExists) {
			t.Fatalf("group: err = %v, want ErrGrantExists", err)
		}
		if e.count(`SELECT count(*) FROM initiative_access`) != rows || e.auditCount() != events {
			t.Fatal("a conflicting grant left rows behind")
		}
	})

	t.Run("an email that is not exactly one active user is a generic validation problem", func(t *testing.T) {
		twin1, twin2 := e.w.user(t, "active"), e.w.user(t, "active")
		mustExec(t, e.owner, `UPDATE users SET email = 'twin@example.com' WHERE id IN ($1, $2)`, twin1, twin2)
		off := e.w.user(t, "disabled")
		onlyOff := e.email(off)
		cases := map[string]string{
			"unknown":           "nobody@example.com",
			"two active users":  "twin@example.com",
			"only a disabled":   onlyOff,
			"malformed address": "not-an-email",
		}
		var first []portal.FieldProblem
		for name, email := range cases {
			_, err := e.svc.CreateGrant(ctx, admin, alpha.id, portal.NewGrant{Email: email, RoleID: alpha.editor})
			got := validationFields(t, err)
			if name == "malformed address" {
				if got[0] != (portal.FieldProblem{Field: "email", Code: portal.CodeInvalid}) {
					t.Errorf("%s: %v", name, got)
				}
				continue
			}
			if first == nil {
				first = got
			}
			if !slices.Equal(got, first) || got[0].Field != "email" {
				t.Errorf("%s: %v differs from %v: the answer must not reveal which case it was", name, got, first)
			}
		}
		// A disabled twin does not make an active user ambiguous.
		mustExec(t, e.owner, `UPDATE users SET status = 'disabled' WHERE id = $1`, twin2)
		if _, err := e.svc.CreateGrant(ctx, admin, alpha.id, portal.NewGrant{Email: "twin@example.com", RoleID: alpha.editor}); err != nil {
			t.Fatalf("one active twin: %v", err)
		}
	})

	t.Run("references and times are validated", func(t *testing.T) {
		other := e.initiative("other")
		past := time.Now().Add(-time.Minute)
		u := e.w.user(t, "active")
		tests := []struct {
			name  string
			in    portal.NewGrant
			field string
			code  string
		}{
			{"role of another initiative", portal.NewGrant{Email: e.email(u), RoleID: other.editor}, "role_id", portal.CodeUnknown},
			{"role that does not exist", portal.NewGrant{Email: e.email(u), RoleID: "00000000-0000-0000-0000-000000000000"}, "role_id", portal.CodeUnknown},
			{"group that does not exist", portal.NewGrant{GroupID: "00000000-0000-0000-0000-000000000000", RoleID: alpha.editor}, "group_id", portal.CodeUnknown},
			{"expired", portal.NewGrant{Email: e.email(u), RoleID: alpha.editor, ExpiresAt: &past}, "expires_at", portal.CodeNotInTheFuture},
			{"both subjects", portal.NewGrant{Email: e.email(u), GroupID: group, RoleID: alpha.editor}, "email", portal.CodeExclusive},
		}
		rows, events := e.count(`SELECT count(*) FROM initiative_access`), e.auditCount()
		for _, tt := range tests {
			for who, caller := range map[string]string{"admin": admin, "manager": mgrA} {
				_, err := e.svc.CreateGrant(ctx, caller, alpha.id, tt.in)
				if got := validationFields(t, err); got[0].Field != tt.field || got[0].Code != tt.code {
					t.Errorf("%s as %s: %v, want %s/%s", tt.name, who, got, tt.field, tt.code)
				}
			}
		}
		if e.count(`SELECT count(*) FROM initiative_access`) != rows || e.auditCount() != events {
			t.Fatal("an invalid grant left rows behind")
		}
	})

	t.Run("who may grant", func(t *testing.T) {
		in := func() portal.NewGrant {
			return portal.NewGrant{Email: e.email(e.w.user(t, "active")), RoleID: alpha.editor}
		}
		tests := []struct {
			name   string
			caller string
			want   error
		}{
			{"manager of another initiative", mgrB, portal.ErrNotFound},
			{"user without any grant", plain, portal.ErrNotFound},
			{"disabled admin", disabledAdmin, portal.ErrUserInactive},
			{"unknown caller", "00000000-0000-0000-0000-000000000000", portal.ErrUserInactive},
		}
		events := e.auditCount()
		for _, tt := range tests {
			if _, err := e.svc.CreateGrant(ctx, tt.caller, alpha.id, in()); !errors.Is(err, tt.want) {
				t.Errorf("%s: err = %v, want %v", tt.name, err, tt.want)
			}
		}
		if _, err := e.svc.CreateGrant(ctx, admin, "00000000-0000-0000-0000-000000000000", in()); !errors.Is(err, portal.ErrNotFound) {
			t.Errorf("unknown initiative: err = %v, want ErrNotFound", err)
		}
		if _, err := e.svc.CreateGrant(ctx, admin, "not-a-uuid", in()); !errors.Is(err, portal.ErrNotFound) {
			t.Errorf("malformed initiative id: err = %v, want ErrNotFound", err)
		}
		if e.auditCount() != events {
			t.Fatal("a refused grant was audited")
		}
	})
}

func toUpper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 'a' + 'A'
		}
	}
	return string(b)
}

func TestListGrantsPaginatesInAScope(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	alpha, beta := e.initiative("alpha"), e.initiative("beta")
	admin := e.admin()
	mgrA := e.manager(alpha.id, alpha.manager) // one grant in alpha
	e.manager(beta.id, beta.manager)
	var created []string
	for range 6 {
		g, err := e.svc.CreateGrant(ctx, admin, alpha.id, portal.NewGrant{Email: e.email(e.w.user(t, "active")), RoleID: alpha.editor})
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, g.ID)
	}

	var seen []string
	cursor := ""
	pages := 0
	for {
		p, err := e.svc.ListGrants(ctx, mgrA, alpha.id, portal.PageRequest{Cursor: cursor, Limit: 3})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, g := range p.Items {
			if g.InitiativeID != alpha.id {
				t.Fatalf("grant of another initiative in the page: %+v", g)
			}
			seen = append(seen, g.ID)
		}
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	if len(seen) != 7 || pages != 3 || !slices.IsSorted(seen) || len(slices.Compact(slices.Clone(seen))) != 7 {
		t.Fatalf("seen %d grants in %d pages (sorted=%v)", len(seen), pages, slices.IsSorted(seen))
	}
	for _, id := range created {
		if !slices.Contains(seen, id) {
			t.Fatalf("grant %s skipped", id)
		}
	}

	if _, err := e.svc.ListGrants(ctx, mgrA, beta.id, portal.PageRequest{}); !errors.Is(err, portal.ErrNotFound) {
		t.Fatalf("manager of another initiative: %v", err)
	}
	if _, err := e.svc.ListGrants(ctx, admin, "00000000-0000-0000-0000-000000000000", portal.PageRequest{}); !errors.Is(err, portal.ErrNotFound) {
		t.Fatalf("unknown initiative: %v", err)
	}
	if _, err := e.svc.ListGrants(ctx, admin, alpha.id, portal.PageRequest{Cursor: "tampered"}); !errors.Is(err, portal.ErrInvalidCursor) {
		t.Fatalf("tampered cursor: %v", err)
	}
	// A cursor issued by another listing is refused too.
	groups, _ := e.svc.CreateGroup(ctx, admin, portal.NewGroup{Key: "g-one"})
	_, _ = e.svc.CreateGroup(ctx, admin, portal.NewGroup{Key: "g-two"})
	gp, err := e.svc.ListGroups(ctx, admin, portal.PageRequest{Limit: 1})
	if err != nil || gp.NextCursor == "" || len(gp.Items) != 1 || gp.Items[0].ID != groups.ID {
		t.Fatalf("groups page = %+v, %v", gp, err)
	}
	if _, err := e.svc.ListGrants(ctx, admin, alpha.id, portal.PageRequest{Cursor: gp.NextCursor}); !errors.Is(err, portal.ErrInvalidCursor) {
		t.Fatalf("a groups cursor on the grants listing: %v", err)
	}
}

// An empty scope lists nothing from any repository, whatever is stored: a handler that forgot to ask
// adminz.Decide, or got a refusal, cannot read rows by accident.
func TestListingsWithAnEmptyScopeReturnNothing(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	alpha := e.initiative("alpha")
	admin := e.admin()
	mgr := e.manager(alpha.id, alpha.manager)
	group := e.w.group(t, "ops")
	e.w.member(t, group, mgr)
	if _, err := e.svc.CreateGroup(ctx, admin, portal.NewGroup{Key: "audited"}); err != nil {
		t.Fatal(err)
	}

	page := portal.PageQuery{Limit: 100}
	err := e.store.WithTx(ctx, func(tx portal.Tx) error {
		for _, scope := range []adminz.Scope{{}, {Initiatives: []string{}}} {
			if rows, err := tx.Grants().ListGrants(ctx, scope, page); err != nil || len(rows) != 0 {
				t.Errorf("grants for %+v = %d rows, %v", scope, len(rows), err)
			}
			if rows, err := tx.Groups().ListGroups(ctx, scope, page); err != nil || len(rows) != 0 {
				t.Errorf("groups for %+v = %d rows, %v", scope, len(rows), err)
			}
			if rows, err := tx.Groups().ListMembers(ctx, scope, group, page); err != nil || len(rows) != 0 {
				t.Errorf("members for %+v = %d rows, %v", scope, len(rows), err)
			}
			if rows, err := tx.Users().ListUsersByEmail(ctx, scope, e.email(mgr), 10); err != nil || len(rows) != 0 {
				t.Errorf("users for %+v = %d rows, %v", scope, len(rows), err)
			}
			if rows, err := tx.AuditLog().ListAudit(ctx, scope, page); err != nil || len(rows) != 0 {
				t.Errorf("audit for %+v = %d rows, %v", scope, len(rows), err)
			}
		}
		// Groups, members and users are global: an initiative scope does not list them either.
		scope := adminz.Scope{Initiatives: []string{alpha.id}}
		if rows, _ := tx.Groups().ListGroups(ctx, scope, page); len(rows) != 0 {
			t.Errorf("an initiative scope listed %d groups", len(rows))
		}
		if rows, _ := tx.Groups().ListMembers(ctx, scope, group, page); len(rows) != 0 {
			t.Errorf("an initiative scope listed %d members", len(rows))
		}
		if rows, _ := tx.Users().ListUsersByEmail(ctx, scope, e.email(mgr), 10); len(rows) != 0 {
			t.Errorf("an initiative scope listed %d users", len(rows))
		}
		// And the scope narrows the rows that are initiative-bound.
		if rows, err := tx.Grants().ListGrants(ctx, scope, page); err != nil || len(rows) != 1 {
			t.Errorf("grants of alpha = %d rows, %v", len(rows), err)
		}
		other := adminz.Scope{Initiatives: []string{"00000000-0000-0000-0000-000000000000"}}
		if rows, err := tx.Grants().ListGrants(ctx, other, page); err != nil || len(rows) != 0 {
			t.Errorf("grants of another initiative = %d rows, %v", len(rows), err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDeleteGrant(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	alpha, beta := e.initiative("alpha"), e.initiative("beta")
	admin := e.admin()
	mgrA := e.manager(alpha.id, alpha.manager)
	mgrB := e.manager(beta.id, beta.manager)
	plain := e.w.user(t, "active")
	disabledAdmin := e.w.user(t, "disabled")
	e.pw.platformAdmin(disabledAdmin)

	newGrant := func(init initiativeWorld) string {
		g, err := e.svc.CreateGrant(ctx, admin, init.id, portal.NewGrant{Email: e.email(e.w.user(t, "active")), RoleID: init.editor})
		if err != nil {
			t.Fatal(err)
		}
		return g.ID
	}

	t.Run("a manager revokes a grant of their initiative, auditing the full before", func(t *testing.T) {
		id := newGrant(alpha)
		events := e.auditCount()
		if err := e.svc.DeleteGrant(ctx, mgrA, id); err != nil {
			t.Fatal(err)
		}
		if e.count(`SELECT count(*) FROM initiative_access WHERE id = $1`, id) != 0 || e.auditCount() != events+1 {
			t.Fatal("the grant is still there or the audit row is missing")
		}
		a := e.lastAudit()
		if a.Actor != mgrA || a.Initiative != alpha.id || a.Action != "grant.delete" || a.TargetID != id || a.After != nil ||
			a.Before["role_key"] != "editor" || a.Before["user_email"] == nil {
			t.Fatalf("audit = %+v", a)
		}
		// Idempotent by natural key: the repeat finds nothing.
		if err := e.svc.DeleteGrant(ctx, mgrA, id); !errors.Is(err, portal.ErrNotFound) {
			t.Fatalf("repeat: %v, want ErrNotFound", err)
		}
		if e.auditCount() != events+1 {
			t.Fatal("the repeat was audited")
		}
	})

	t.Run("who may revoke", func(t *testing.T) {
		id := newGrant(alpha)
		events := e.auditCount()
		tests := []struct {
			name   string
			caller string
			want   error
		}{
			{"manager of another initiative", mgrB, portal.ErrNotFound},
			{"user without any grant", plain, portal.ErrNotFound},
			{"disabled admin", disabledAdmin, portal.ErrUserInactive},
		}
		for _, tt := range tests {
			if err := e.svc.DeleteGrant(ctx, tt.caller, id); !errors.Is(err, tt.want) {
				t.Errorf("%s: err = %v, want %v", tt.name, err, tt.want)
			}
		}
		if err := e.svc.DeleteGrant(ctx, admin, "not-a-uuid"); !errors.Is(err, portal.ErrNotFound) {
			t.Errorf("malformed id: %v", err)
		}
		if e.count(`SELECT count(*) FROM initiative_access WHERE id = $1`, id) != 1 || e.auditCount() != events {
			t.Fatal("a refused revocation changed something")
		}
		if err := e.svc.DeleteGrant(ctx, admin, id); err != nil {
			t.Fatalf("admin: %v", err)
		}
	})
}

// managerGrantID returns the id of the manager grant of a user.
func (e *adminEnv) managerGrantID(init initiativeWorld, userID string) string {
	e.t.Helper()
	return scalar[string](e.t, e.owner, `SELECT id::text FROM initiative_access WHERE initiative_id = $1 AND role_id = $2 AND user_id = $3`,
		init.id, init.manager, userID)
}

func TestLastManagerRule(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	admin := e.admin()
	n := 0

	// scenario builds a fresh initiative and lets the test add manager grants.
	type scenario struct {
		init initiativeWorld
		// grants by label, in the order they were added
		ids map[string]string
	}
	newScenario := func() *scenario {
		n++
		return &scenario{init: e.initiative(fmt.Sprintf("init-%d", n)), ids: map[string]string{}}
	}
	direct := func(s *scenario, label, status, expires string) string {
		u := e.w.user(t, status)
		e.w.grantUser(t, s.init.id, s.init.manager, u, expires)
		s.ids[label] = e.managerGrantID(s.init, u)
		return u
	}
	viaGroup := func(s *scenario, label string, memberStatuses ...string) {
		g := e.w.group(t, fmt.Sprintf("grp-%d-%s", n, label))
		for _, st := range memberStatuses {
			e.w.member(t, g, e.w.user(t, st))
		}
		e.w.grantGroup(t, s.init.id, s.init.manager, g)
		s.ids[label] = scalar[string](t, e.owner, `SELECT id::text FROM initiative_access WHERE initiative_id = $1 AND group_id = $2`, s.init.id, g)
	}

	tests := []struct {
		name  string
		setup func(s *scenario)
		// revoke lists the labels revoked in order, each with the expected error (nil: success).
		revoke []struct {
			label string
			want  error
		}
	}{
		{"the only manager cannot be revoked", func(s *scenario) { direct(s, "a", "active", "") },
			[]struct {
				label string
				want  error
			}{{"a", portal.ErrLastManager}}},
		{"one of two can go, then the other cannot", func(s *scenario) { direct(s, "a", "active", ""); direct(s, "b", "active", "") },
			[]struct {
				label string
				want  error
			}{{"a", nil}, {"b", portal.ErrLastManager}}},
		{"an expired manager grant does not count as the remaining one", func(s *scenario) {
			direct(s, "live", "active", "")
			direct(s, "expired", "active", "2000-01-01T00:00:00Z")
		}, []struct {
			label string
			want  error
		}{{"live", portal.ErrLastManager}, {"expired", nil}, {"live", portal.ErrLastManager}}},
		{"a disabled user is no manager", func(s *scenario) { direct(s, "live", "active", ""); direct(s, "off", "disabled", "") },
			[]struct {
				label string
				want  error
			}{{"live", portal.ErrLastManager}, {"off", nil}}},
		{"a group with an active member counts", func(s *scenario) { direct(s, "u", "active", ""); viaGroup(s, "g", "active") },
			[]struct {
				label string
				want  error
			}{{"u", nil}, {"g", portal.ErrLastManager}}},
		{"a group without members or with only disabled ones does not count", func(s *scenario) {
			direct(s, "u", "active", "")
			viaGroup(s, "empty")
			viaGroup(s, "off", "disabled", "disabled")
		}, []struct {
			label string
			want  error
		}{{"u", portal.ErrLastManager}, {"empty", nil}, {"off", nil}}},
		{"an initiative with no effective manager lets the dead grants go", func(s *scenario) { direct(s, "off", "disabled", ""); viaGroup(s, "empty") },
			[]struct {
				label string
				want  error
			}{{"off", nil}, {"empty", nil}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newScenario()
			tt.setup(s)
			for _, step := range tt.revoke {
				id := s.ids[step.label]
				events := e.auditCount()
				err := e.svc.DeleteGrant(ctx, admin, id)
				if !errors.Is(err, step.want) || (step.want == nil && err != nil) {
					t.Fatalf("revoke %s: err = %v, want %v", step.label, err, step.want)
				}
				gone := e.count(`SELECT count(*) FROM initiative_access WHERE id = $1`, id) == 0
				if gone != (step.want == nil) {
					t.Fatalf("revoke %s: grant gone = %v", step.label, gone)
				}
				wantEvents := events
				if step.want == nil {
					wantEvents++
				}
				if e.auditCount() != wantEvents {
					t.Fatalf("revoke %s: audit rows %d -> %d", step.label, events, e.auditCount())
				}
			}
		})
	}

	t.Run("grants of roles without manages_access are not affected", func(t *testing.T) {
		s := newScenario()
		direct(s, "m", "active", "")
		u := e.w.user(t, "active")
		e.w.grantUser(t, s.init.id, s.init.editor, u, "")
		editorGrant := scalar[string](t, e.owner, `SELECT id::text FROM initiative_access WHERE initiative_id = $1 AND role_id = $2`, s.init.id, s.init.editor)
		if err := e.svc.DeleteGrant(ctx, admin, editorGrant); err != nil {
			t.Fatal(err)
		}
	})
}

// barrier makes two units of work meet at a point of their code.
type barrier struct {
	arrived atomic.Int32
	open    chan struct{}
	once    sync.Once
}

func newBarrier() *barrier { return &barrier{open: make(chan struct{})} }

func (b *barrier) arrive(t *testing.T) {
	if b.arrived.Add(1) >= 2 {
		b.once.Do(func() { close(b.open) })
	}
	select {
	case <-b.open:
	case <-time.After(testTimeout):
		t.Error("the two units of work never met")
	}
}

// meetingStore holds every unit of work at its first read of a grant until two have done it, so both have
// seen the grants as they were before either of them takes a lock. Without the row locks of DeleteGrant, both
// would then pass the last-manager check and delete.
type meetingStore struct {
	portal.Store
	t *testing.T
	b *barrier
}

func (s meetingStore) WithTx(ctx context.Context, fn func(portal.Tx) error) error {
	return s.Store.WithTx(ctx, func(tx portal.Tx) error { return fn(&meetingTx{Tx: tx, t: s.t, b: s.b}) })
}

type meetingTx struct {
	portal.Tx
	t      *testing.T
	b      *barrier
	waited bool
}

func (m *meetingTx) Grants() portal.GrantRepo { return meetingGrants{GrantRepo: m.Tx.Grants(), m: m} }

type meetingGrants struct {
	portal.GrantRepo
	m *meetingTx
}

func (g meetingGrants) GetGrant(ctx context.Context, id string) (portal.Grant, error) {
	grant, err := g.GrantRepo.GetGrant(ctx, id)
	if !g.m.waited {
		g.m.waited = true
		g.m.b.arrive(g.m.t)
	}
	return grant, err
}

// Two revocations of the two manager grants of one initiative, run at the same time against a real database:
// exactly one succeeds and the initiative keeps a manager, however they interleave.
func TestConcurrentRevocationsKeepAManager(t *testing.T) {
	t.Parallel()
	const rounds = 5
	// A store whose units of work meet before locking; a fresh barrier per scenario.
	meeting := func(s portal.Store) portal.Store { return meetingStore{Store: s, t: t, b: newBarrier()} }

	for round := range rounds {
		t.Run(fmt.Sprintf("two platform admins, round %d", round), func(t *testing.T) {
			t.Parallel()
			e := newAdminEnvWith(t, meeting)
			init := e.initiative("race")
			adminX, adminY := e.admin(), e.admin()
			m1, m2 := e.w.user(t, "active"), e.w.user(t, "active")
			e.w.grantUser(t, init.id, init.manager, m1, "")
			e.w.grantUser(t, init.id, init.manager, m2, "")
			g1, g2 := e.managerGrantID(init, m1), e.managerGrantID(init, m2)
			events := e.auditCount()

			errs := revokeTogether(e.ctx(), e, [2]string{adminX, g1}, [2]string{adminY, g2})
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
			if n := e.count(`SELECT count(*) FROM initiative_access WHERE initiative_id = $1 AND role_id = $2`, init.id, init.manager); n != 1 {
				t.Fatalf("%d manager grants left, want 1", n)
			}
			if e.auditCount() != events+1 {
				t.Fatalf("audit rows %d -> %d, want exactly one more", events, e.auditCount())
			}
		})

		t.Run(fmt.Sprintf("two managers revoking each other, round %d", round), func(t *testing.T) {
			t.Parallel()
			e := newAdminEnvWith(t, meeting)
			init := e.initiative("race")
			m1, m2 := e.manager(init.id, init.manager), e.manager(init.id, init.manager)
			g1, g2 := e.managerGrantID(init, m1), e.managerGrantID(init, m2)

			// m1 revokes m2's grant while m2 revokes m1's.
			errs := revokeTogether(e.ctx(), e, [2]string{m1, g2}, [2]string{m2, g1})
			var ok int
			for _, err := range errs {
				switch {
				case err == nil:
					ok++
				case errors.Is(err, portal.ErrLastManager), errors.Is(err, portal.ErrNotFound):
					// The loser is refused: either by the rule, or because it no longer manages the initiative.
				default:
					t.Errorf("unexpected error: %v", err)
				}
			}
			if ok != 1 {
				t.Fatalf("results = %v, want exactly one success", errs)
			}
			if n := e.count(`SELECT count(*) FROM initiative_access WHERE initiative_id = $1 AND role_id = $2`, init.id, init.manager); n != 1 {
				t.Fatalf("%d manager grants left, want 1", n)
			}
		})
	}
}

// revokeTogether runs the two revocations ({caller, grant id}) concurrently and returns their errors.
func revokeTogether(ctx context.Context, e *adminEnv, a, b [2]string) [2]error {
	var (
		wg   sync.WaitGroup
		errs [2]error
	)
	for i, r := range [2][2]string{a, b} {
		wg.Go(func() { errs[i] = e.svc.DeleteGrant(ctx, r[0], r[1]) })
	}
	wg.Wait()
	return errs
}

// failingAudit makes every audit insert fail, as a full table or a revoked privilege would.
type failingAuditStore struct{ portal.Store }

func (s failingAuditStore) WithTx(ctx context.Context, fn func(portal.Tx) error) error {
	return s.Store.WithTx(ctx, func(tx portal.Tx) error { return fn(failingAuditTx{Tx: tx}) })
}

type failingAuditTx struct{ portal.Tx }

var errAuditDown = errors.New("audit insert failed")

func (failingAuditTx) Audit() audit.Recorder { return failingRecorder{} }

type failingRecorder struct{}

func (failingRecorder) RecordAudit(context.Context, audit.Event) error { return errAuditDown }

// If the audit insert fails the mutation rolls back with it: nothing it did survives.
func TestMutationsRollBackWhenTheAuditInsertFails(t *testing.T) {
	t.Parallel()
	e := newAdminEnvWith(t, func(s portal.Store) portal.Store { return failingAuditStore{Store: s} })
	ctx := e.ctx()
	alpha := e.initiative("alpha")
	admin := e.admin()
	target := e.w.user(t, "active")
	member := e.w.user(t, "active")
	group := e.w.group(t, "ops")
	emptyGroup := e.w.group(t, "empty")
	e.w.member(t, group, member)
	e.w.grantUser(t, alpha.id, alpha.editor, member, "")
	grant := scalar[string](t, e.owner, `SELECT id::text FROM initiative_access WHERE user_id = $1`, member)
	grantee := e.email(e.w.user(t, "active"))
	e.w.grantUser(t, alpha.id, alpha.editor, target, "")
	mustExec(t, e.owner, `INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (sha256('s1'::bytea), $1, now() + interval '1 hour')`, target)

	rows := func() string {
		return fmt.Sprintf("grants=%d groups=%d members=%d active=%d sessions=%d audit=%d",
			e.count(`SELECT count(*) FROM initiative_access`), e.count(`SELECT count(*) FROM groups`),
			e.count(`SELECT count(*) FROM group_members`), e.count(`SELECT count(*) FROM users WHERE status = 'active'`),
			e.count(`SELECT count(*) FROM sessions`), e.auditCount())
	}
	mutations := map[string]func() error{
		"grant.create": func() error {
			_, err := e.svc.CreateGrant(ctx, admin, alpha.id, portal.NewGrant{Email: grantee, RoleID: alpha.editor})
			return err
		},
		"grant.delete":        func() error { return e.svc.DeleteGrant(ctx, admin, grant) },
		"group.create":        func() error { _, err := e.svc.CreateGroup(ctx, admin, portal.NewGroup{Key: "new"}); return err },
		"group.delete":        func() error { return e.svc.DeleteGroup(ctx, admin, emptyGroup) },
		"group_member.add":    func() error { _, err := e.svc.AddGroupMember(ctx, admin, group, e.email(target)); return err },
		"group_member.remove": func() error { return e.svc.RemoveGroupMember(ctx, admin, group, member) },
		"user.disable":        func() error { _, err := e.svc.SetUserStatus(ctx, admin, target, "disabled"); return err },
		"user_sessions.revoke": func() error {
			_, err := e.svc.RevokeUserSessions(ctx, admin, target)
			return err
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			before := rows()
			err := mutate()
			if !errors.Is(err, errAuditDown) {
				t.Fatalf("err = %v, want the audit failure", err)
			}
			if after := rows(); after != before {
				t.Fatalf("state changed although the audit insert failed:\n before %s\n after  %s", before, after)
			}
		})
	}
	if e.auditCount() != 0 {
		t.Fatalf("%d audit rows", e.auditCount())
	}
}

// permMeetingStore holds every unit of work right after its first read of the caller's permissions until two
// have done it, so both decided on the world as it was before either took a lock.
type permMeetingStore struct {
	portal.Store
	t *testing.T
	b *barrier
}

func (s permMeetingStore) WithTx(ctx context.Context, fn func(portal.Tx) error) error {
	return s.Store.WithTx(ctx, func(tx portal.Tx) error { return fn(&permMeetingTx{Tx: tx, t: s.t, b: s.b}) })
}

type permMeetingTx struct {
	portal.Tx
	t      *testing.T
	b      *barrier
	waited bool
}

func (m *permMeetingTx) Permissions() portal.PermissionsReader {
	return permMeetingReader{PermissionsReader: m.Tx.Permissions(), m: m}
}

type permMeetingReader struct {
	portal.PermissionsReader
	m *permMeetingTx
}

func (p permMeetingReader) LoadPermissions(ctx context.Context, id string) (portal.Permissions, error) {
	perms, err := p.PermissionsReader.LoadPermissions(ctx, id)
	if !p.m.waited {
		p.m.waited = true
		p.m.b.arrive(p.m.t)
	}
	return perms, err
}

// Two platform admins disabling each other at the same time: the second one finds itself disabled once the
// first has committed, so exactly one succeeds and an active administrator remains.
func TestConcurrentMutualAdminDisable(t *testing.T) {
	t.Parallel()
	for round := range 5 {
		t.Run(fmt.Sprintf("round %d", round), func(t *testing.T) {
			t.Parallel()
			e := newAdminEnvWith(t, func(s portal.Store) portal.Store { return permMeetingStore{Store: s, t: t, b: newBarrier()} })
			a, b := e.admin(), e.admin()
			var (
				wg   sync.WaitGroup
				errs [2]error
			)
			for i, pair := range [2][2]string{{a, b}, {b, a}} {
				wg.Go(func() { _, errs[i] = e.svc.SetUserStatus(e.ctx(), pair[0], pair[1], "disabled") })
			}
			wg.Wait()
			var ok, inactive int
			for _, err := range errs {
				switch {
				case err == nil:
					ok++
				case errors.Is(err, portal.ErrUserInactive):
					inactive++
				default:
					t.Errorf("unexpected error: %v", err)
				}
			}
			active := e.count(`SELECT count(*) FROM users u JOIN platform_admins pa ON pa.user_id = u.id WHERE u.status = 'active'`)
			if ok != 1 || inactive != 1 || active != 1 {
				t.Fatalf("results = %v, %d active admins; want one success, one inactive caller, one active admin", errs, active)
			}
			if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'user.disable'`); n != 1 {
				t.Fatalf("%d audit rows", n)
			}
		})
	}
}

// gateStore pauses the first unit of work that reads permissions after arm() right after that read, until
// release() is called: the caller's permissions are then stale by the time it continues.
type gateStore struct {
	portal.Store
	reached chan struct{}
	release chan struct{}
	armed   atomic.Bool
}

func newGateStore(s portal.Store) *gateStore {
	return &gateStore{Store: s, reached: make(chan struct{}), release: make(chan struct{})}
}

func (g *gateStore) WithTx(ctx context.Context, fn func(portal.Tx) error) error {
	return g.Store.WithTx(ctx, func(tx portal.Tx) error { return fn(&gateTx{Tx: tx, g: g}) })
}

type gateTx struct {
	portal.Tx
	g      *gateStore
	passed bool
}

func (x *gateTx) Permissions() portal.PermissionsReader {
	return gateReader{PermissionsReader: x.Tx.Permissions(), x: x}
}

type gateReader struct {
	portal.PermissionsReader
	x *gateTx
}

func (r gateReader) LoadPermissions(ctx context.Context, id string) (portal.Permissions, error) {
	perms, err := r.PermissionsReader.LoadPermissions(ctx, id)
	if !r.x.passed && r.x.g.armed.CompareAndSwap(true, false) {
		r.x.passed = true
		close(r.x.g.reached)
		<-r.x.g.release
	}
	return perms, err
}

// A manager whose grant is revoked while their POST is in flight must not still create the grant: the share
// lock on the initiative waits for the revocation, and the permissions are read again after it.
func TestRevokedManagerCannotFinishAnInFlightGrant(t *testing.T) {
	t.Parallel()
	var gate *gateStore
	e := newAdminEnvWith(t, func(s portal.Store) portal.Store { gate = newGateStore(s); return gate })
	ctx := e.ctx()
	init := e.initiative("alpha")
	admin := e.admin()
	mgr, other := e.manager(init.id, init.manager), e.manager(init.id, init.manager)
	_ = other
	target := e.email(e.w.user(t, "active"))

	gate.armed.Store(true)
	done := make(chan error, 1)
	go func() {
		_, err := e.svc.CreateGrant(ctx, mgr, init.id, portal.NewGrant{Email: target, RoleID: init.editor})
		done <- err
	}()
	select {
	case <-gate.reached:
	case <-time.After(testTimeout):
		t.Fatal("the grant never reached the gate")
	}
	if err := e.svc.DeleteGrant(ctx, admin, e.managerGrantID(init, mgr)); err != nil {
		t.Fatal(err)
	}
	close(gate.release)
	if err := <-done; !errors.Is(err, portal.ErrNotFound) {
		t.Fatalf("in-flight grant of a revoked manager: %v, want ErrNotFound", err)
	}
	if n := e.count(`SELECT count(*) FROM initiative_access WHERE role_id = $1`, init.editor); n != 0 {
		t.Fatalf("%d grants were created by a revoked manager", n)
	}
}

// Ids are case-insensitive hex: a manager's own initiative must not become "not found" because the client sent
// uppercase.
func TestUppercaseIDsAreNormalized(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	init := e.initiative("alpha")
	mgr := e.manager(init.id, init.manager)
	group := e.w.group(t, "ops")
	up := strings.ToUpper

	g, err := e.svc.CreateGrant(ctx, mgr, up(init.id), portal.NewGrant{GroupID: up(group), RoleID: up(init.editor)})
	if err != nil || g.InitiativeID != init.id || g.Group == nil || g.Group.ID != group {
		t.Fatalf("create = %+v, %v", g, err)
	}
	if p, err := e.svc.ListGrants(ctx, mgr, up(init.id), portal.PageRequest{}); err != nil || len(p.Items) != 2 {
		t.Fatalf("list = %+v, %v", p, err)
	}
	if _, err := e.svc.ListAudit(ctx, mgr, portal.AuditQuery{InitiativeID: up(init.id)}); err != nil {
		t.Fatalf("audit filter: %v", err)
	}
	if err := e.svc.DeleteGrant(ctx, mgr, up(g.ID)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	admin := e.admin()
	u := e.w.user(t, "active")
	if _, err := e.svc.SetUserStatus(ctx, admin, up(u), "disabled"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	// The caller's own id may arrive in uppercase too: the self-disable guard still holds.
	if _, err := e.svc.SetUserStatus(ctx, admin, up(admin), "disabled"); !errors.Is(err, portal.ErrSelfDisable) {
		t.Fatalf("self disable with an uppercase id: %v", err)
	}
}

// Text that PostgreSQL cannot store is a validation problem, not a 500.
func TestUnstorableTextIsRejected(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	init := e.initiative("alpha")
	admin := e.admin()
	group := e.w.group(t, "ops")
	bad := map[string]string{"NUL": "a\x00b@example.com", "invalid UTF-8": "a\xffb@example.com"}
	events := e.auditCount()
	for name, email := range bad {
		if _, err := e.svc.CreateGrant(ctx, admin, init.id, portal.NewGrant{Email: email, RoleID: init.editor}); validationFields(t, err)[0].Field != "email" {
			t.Errorf("grant email with %s: %v", name, err)
		}
		if _, err := e.svc.AddGroupMember(ctx, admin, group, email); validationFields(t, err)[0].Field != "email" {
			t.Errorf("member email with %s: %v", name, err)
		}
		if _, err := e.svc.FindUsers(ctx, admin, email); validationFields(t, err)[0].Field != "email" {
			t.Errorf("search email with %s: %v", name, err)
		}
		if _, err := e.svc.CreateGroup(ctx, admin, portal.NewGroup{Key: "ok", Name: "n" + email}); validationFields(t, err)[0].Field != "name" {
			t.Errorf("group name with %s: %v", name, err)
		}
		if _, err := e.svc.CreateGroup(ctx, admin, portal.NewGroup{Key: "k" + email}); validationFields(t, err)[0].Field != "key" {
			t.Errorf("group key with %s: %v", name, err)
		}
	}
	if e.auditCount() != events {
		t.Fatal("a refused request was audited")
	}
}

// The foreign keys the writes can violate are named exactly: a wrong guess would blame the wrong field.
func TestReferenceViolationsNameTheRightField(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	init := e.initiative("alpha")
	group, user := e.w.group(t, "ops"), e.w.user(t, "active")
	missing := "00000000-0000-0000-0000-000000000000"
	field := func(err error) string {
		t.Helper()
		ref, ok := errors.AsType[*portal.ReferenceError](err)
		if !ok {
			t.Fatalf("err = %v, want a reference error", err)
		}
		return ref.Field
	}
	// Each statement fails, so each one runs in its own transaction.
	cases := []struct {
		name string
		run  func(tx portal.Tx) error
		want string
	}{
		{"member of an unknown group", func(tx portal.Tx) error { _, err := tx.Groups().InsertMember(ctx, missing, user); return err }, "group_id"},
		{"unknown user as member", func(tx portal.Tx) error { _, err := tx.Groups().InsertMember(ctx, group, missing); return err }, "user_id"},
		{"grant with a role of nowhere", func(tx portal.Tx) error {
			_, err := tx.Grants().InsertGrant(ctx, portal.GrantRow{InitiativeID: init.id, RoleID: missing, UserID: user})
			return err
		}, "role_id"},
		{"grant to an unknown user", func(tx portal.Tx) error {
			_, err := tx.Grants().InsertGrant(ctx, portal.GrantRow{InitiativeID: init.id, RoleID: init.editor, UserID: missing})
			return err
		}, "user_id"},
		{"grant to an unknown group", func(tx portal.Tx) error {
			_, err := tx.Grants().InsertGrant(ctx, portal.GrantRow{InitiativeID: init.id, RoleID: init.editor, GroupID: missing})
			return err
		}, "group_id"},
	}
	for _, tt := range cases {
		err := e.store.WithTx(ctx, tt.run)
		if got := field(err); got != tt.want {
			t.Errorf("%s: field %q, want %q", tt.name, got, tt.want)
		}
	}
}
