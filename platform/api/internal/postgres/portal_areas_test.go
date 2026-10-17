package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"platform/api/internal/portal"
)

const (
	unknownID = "00000000-0000-0000-0000-000000000000"
	// codeRestrictViolation is what ON DELETE RESTRICT raises; NO ACTION would raise 23503.
	codeRestrictViolation = "23001"
)

func (w world) area(t *testing.T, key string) string {
	t.Helper()
	return scalar[string](t, w.pool, `INSERT INTO areas (key) VALUES ($1) RETURNING id::text`, key)
}

func TestAreaConstraints(t *testing.T) {
	t.Parallel()
	pool := newTestDB(t)
	w := world{pool: pool}
	a1, a2 := w.area(t, "a1"), w.area(t, "a2")
	human, other, third := w.user(t, "active"), w.user(t, "active"), w.user(t, "active")
	robot := w.robot(t, "bot")
	mustExec(t, pool, `INSERT INTO area_members (area_id, user_id) VALUES ($1, $2)`, a1, human)
	mustExec(t, pool, `INSERT INTO area_supervisors (area_id, user_id) VALUES ($1, $2)`, a1, human)
	mustExec(t, pool, `INSERT INTO area_supervisors (area_id, user_id) VALUES ($1, $2)`, a1, third) // a2 and a person in no area stay empty

	tests := []struct {
		name string
		sql  string
		args []any
		code string
	}{
		{"robot as a member", `INSERT INTO area_members (area_id, user_id) VALUES ($1, $2)`, []any{a1, robot}, codeForeignKeyViolation},
		{"robot as a member with its kind", `INSERT INTO area_members (area_id, user_id, kind) VALUES ($1, $2, 'robot')`, []any{a1, robot}, codeCheckViolation},
		{"robot as a supervisor", `INSERT INTO area_supervisors (area_id, user_id) VALUES ($1, $2)`, []any{a1, robot}, codeForeignKeyViolation},
		{"robot as a supervisor with its kind", `INSERT INTO area_supervisors (area_id, user_id, kind) VALUES ($1, $2, 'robot')`, []any{a1, robot}, codeCheckViolation},
		{"a second area for the same person", `INSERT INTO area_members (area_id, user_id) VALUES ($1, $2)`, []any{a2, human}, codeUniqueViolation},
		{"the same member twice", `INSERT INTO area_members (area_id, user_id) VALUES ($1, $2)`, []any{a1, human}, codeUniqueViolation},
		{"the same supervisor twice", `INSERT INTO area_supervisors (area_id, user_id) VALUES ($1, $2)`, []any{a1, human}, codeUniqueViolation},
		{"member of an unknown area", `INSERT INTO area_members (area_id, user_id) VALUES ($1, $2)`, []any{unknownID, other}, codeForeignKeyViolation},
		{"supervisor of an unknown area", `INSERT INTO area_supervisors (area_id, user_id) VALUES ($1, $2)`, []any{unknownID, other}, codeForeignKeyViolation},
		{"unknown user as a member", `INSERT INTO area_members (area_id, user_id) VALUES ($1, $2)`, []any{a1, unknownID}, codeForeignKeyViolation},
		{"delete an area with members", `DELETE FROM areas WHERE id = $1`, []any{a1}, codeRestrictViolation},
		{"upper case key", `INSERT INTO areas (key) VALUES ('Ops')`, nil, codeCheckViolation},
		{"key of 64 characters", `INSERT INTO areas (key) VALUES (repeat('a', 64))`, nil, codeCheckViolation},
		{"empty key", `INSERT INTO areas (key) VALUES ('')`, nil, codeCheckViolation},
		{"duplicate key", `INSERT INTO areas (key) VALUES ('a1')`, nil, codeUniqueViolation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := exec(pool, tt.sql, tt.args...)
			if err == nil {
				t.Fatalf("expected error with code %s, got nil", tt.code)
			}
			if got := pgErrCode(err); got != tt.code {
				t.Fatalf("code = %s (%v), want %s", got, err, tt.code)
			}
		})
	}

	t.Run("an area with only supervisors is not deletable either", func(t *testing.T) {
		only := w.area(t, "only-supervisors")
		mustExec(t, pool, `INSERT INTO area_supervisors (area_id, user_id) VALUES ($1, $2)`, only, other)
		if err := exec(pool, `DELETE FROM areas WHERE id = $1`, only); pgErrCode(err) != codeRestrictViolation {
			t.Fatalf("err = %v, want a restrict violation", err)
		}
	})
	t.Run("the rows of a deleted user go with it, and the empty area can then be deleted", func(t *testing.T) {
		gone, area := w.user(t, "active"), w.area(t, "cascade")
		mustExec(t, pool, `INSERT INTO area_members (area_id, user_id) VALUES ($1, $2)`, area, gone)
		mustExec(t, pool, `INSERT INTO area_supervisors (area_id, user_id) VALUES ($1, $2)`, area, gone)
		mustExec(t, pool, `DELETE FROM users WHERE id = $1`, gone)
		if n := scalar[int](t, pool, `SELECT count(*) FROM area_members WHERE area_id = $1`, area) +
			scalar[int](t, pool, `SELECT count(*) FROM area_supervisors WHERE area_id = $1`, area); n != 0 {
			t.Fatalf("%d rows survived their user", n)
		}
		mustExec(t, pool, `DELETE FROM areas WHERE id = $1`, area)
	})
}

func TestAreasAdministration(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	alpha := e.initiative("alpha")
	admin := e.admin()
	mgr := e.manager(alpha.id, alpha.manager)
	plain := e.w.user(t, "active")
	off := e.w.user(t, "disabled")
	e.pw.platformAdmin(off)
	noArea := func() int { return e.count(`SELECT count(*) FROM audit_events WHERE area_id IS NOT NULL`) }

	t.Run("create audits a global event and validates the key", func(t *testing.T) {
		events := e.auditCount()
		a, err := e.svc.CreateArea(ctx, admin, portal.NewArea{Key: "ops-team", Name: "Ops"})
		if err != nil || a.ID == "" || a.Key != "ops-team" || a.Name != "Ops" || a.CreatedAt.IsZero() {
			t.Fatalf("area = %+v, %v", a, err)
		}
		au := e.lastAudit()
		if e.auditCount() != events+1 || au.Actor != admin || au.Initiative != "" || au.Action != "area.create" ||
			au.TargetType != "area" || au.TargetID != a.ID || au.After["key"] != "ops-team" || au.After["name"] != "Ops" || au.Before != nil {
			t.Fatalf("audit = %+v", au)
		}
		if _, err := e.svc.CreateArea(ctx, admin, portal.NewArea{Key: "ops-team"}); !errors.Is(err, portal.ErrAreaKeyTaken) {
			t.Fatalf("duplicate key: %v", err)
		}
		for _, key := range []string{"", "Upper", "-lead", "trail-", "has space", "a/b", "x'; --", strings.Repeat("a", 64)} {
			_, err := e.svc.CreateArea(ctx, admin, portal.NewArea{Key: key})
			if got := validationFields(t, err); got[0].Field != "key" {
				t.Errorf("key %q: %v", key, got)
			}
		}
		if _, err := e.svc.CreateArea(ctx, admin, portal.NewArea{Key: "ok", Name: strings.Repeat("n", 201)}); validationFields(t, err)[0].Field != "name" {
			t.Errorf("long name accepted: %v", err)
		}
		if e.auditCount() != events+1 {
			t.Fatal("a refused area was audited")
		}
	})

	t.Run("only platform admins reach the areas; everyone else is forbidden", func(t *testing.T) {
		events := e.auditCount()
		area := e.w.area(t, "frozen")
		for name, caller := range map[string]string{"manager": mgr, "plain": plain} {
			checks := map[string]error{
				"create":            func() error { _, err := e.svc.CreateArea(ctx, caller, portal.NewArea{Key: "x-" + name}); return err }(),
				"get":               func() error { _, err := e.svc.GetArea(ctx, caller, area); return err }(),
				"list":              func() error { _, err := e.svc.ListAreas(ctx, caller, portal.PageRequest{}); return err }(),
				"delete":            e.svc.DeleteArea(ctx, caller, area),
				"add member":        func() error { _, err := e.svc.AddAreaMember(ctx, caller, area, e.email(plain)); return err }(),
				"remove member":     e.svc.RemoveAreaMember(ctx, caller, area, plain),
				"list members":      func() error { _, err := e.svc.ListAreaMembers(ctx, caller, area, portal.PageRequest{}); return err }(),
				"add supervisor":    func() error { _, err := e.svc.AddAreaSupervisor(ctx, caller, area, e.email(plain)); return err }(),
				"remove supervisor": e.svc.RemoveAreaSupervisor(ctx, caller, area, plain),
				"list supervisors":  func() error { _, err := e.svc.ListAreaSupervisors(ctx, caller, area, portal.PageRequest{}); return err }(),
			}
			for op, err := range checks {
				want := portal.ErrForbidden
				if op == "list members" { // area scope: out of scope is hidden, not forbidden
					want = portal.ErrNotFound
				}
				if !errors.Is(err, want) {
					t.Errorf("%s %s: %v", name, op, err)
				}
			}
		}
		if _, err := e.svc.CreateArea(ctx, off, portal.NewArea{Key: "x-off"}); !errors.Is(err, portal.ErrUserInactive) {
			t.Errorf("disabled admin: %v", err)
		}
		if e.auditCount() != events {
			t.Fatal("a refused change was audited")
		}
	})

	t.Run("get and list", func(t *testing.T) {
		a, _ := e.svc.CreateArea(ctx, admin, portal.NewArea{Key: "get-me", Name: "Get"})
		got, err := e.svc.GetArea(ctx, admin, a.ID)
		if err != nil || got != a {
			t.Fatalf("get = %+v, %v (want %+v)", got, err, a)
		}
		if _, err := e.svc.GetArea(ctx, admin, unknownID); !errors.Is(err, portal.ErrNotFound) {
			t.Fatalf("unknown: %v", err)
		}
		if _, err := e.svc.GetArea(ctx, admin, "not-a-uuid"); !errors.Is(err, portal.ErrNotFound) {
			t.Fatalf("malformed: %v", err)
		}
		p, err := e.svc.ListAreas(ctx, admin, portal.PageRequest{})
		if err != nil || !slices.ContainsFunc(p.Items, func(x portal.Area) bool { return x.ID == a.ID }) {
			t.Fatalf("list = %+v, %v", p, err)
		}
	})

	for _, tc := range []struct {
		name       string
		add        func(context.Context, string, string, string) (portal.AreaPerson, error)
		remove     func(context.Context, string, string, string) error
		list       func(context.Context, string, string, portal.PageRequest) (portal.Page[portal.AreaPerson], error)
		table      string
		prefix     string
		duplicate  *portal.Conflict
		alsoMember bool
	}{
		{"members", e.svc.AddAreaMember, e.svc.RemoveAreaMember, e.svc.ListAreaMembers, "area_members", "area_member", portal.ErrAreaMemberExists, true},
		{"supervisors", e.svc.AddAreaSupervisor, e.svc.RemoveAreaSupervisor, e.svc.ListAreaSupervisors, "area_supervisors", "area_supervisor", portal.ErrAreaSupervisorExists, false},
	} {
		t.Run(tc.name+": add by email, list, repeat, remove", func(t *testing.T) {
			a, _ := e.svc.CreateArea(ctx, admin, portal.NewArea{Key: "crew-" + tc.name})
			u := e.w.user(t, "active")
			events := e.auditCount()

			m, err := tc.add(ctx, admin, a.ID, "  "+toUpper(e.email(u)))
			if err != nil || m.UserID != u || m.Email != e.email(u) || m.Status != "active" || m.AddedAt.IsZero() {
				t.Fatalf("person = %+v, %v", m, err)
			}
			au := e.lastAudit()
			if e.auditCount() != events+1 || au.Action != tc.prefix+".add" || au.TargetType != "area" || au.TargetID != a.ID ||
				au.Initiative != "" || au.After["area_id"] != a.ID || au.After["user_id"] != u || au.After["email"] != e.email(u) {
				t.Fatalf("audit = %+v", au)
			}
			if _, err := tc.add(ctx, admin, a.ID, e.email(u)); !errors.Is(err, tc.duplicate) {
				t.Fatalf("repeat: %v", err)
			}
			if e.auditCount() != events+1 {
				t.Fatal("a repeated add was audited")
			}
			// The same exactly-one-active-human rule as grants and groups.
			if _, err := tc.add(ctx, admin, a.ID, "nobody@example.com"); validationFields(t, err)[0].Code != portal.CodeNoUniqueActiveUser {
				t.Fatalf("unknown email: %v", err)
			}
			if _, err := tc.add(ctx, admin, a.ID, e.email(e.w.user(t, "disabled"))); validationFields(t, err)[0].Code != portal.CodeNoUniqueActiveUser {
				t.Fatalf("disabled user: %v", err)
			}
			if _, err := tc.add(ctx, admin, a.ID, "not-an-email"); validationFields(t, err)[0] != (portal.FieldProblem{Field: "email", Code: portal.CodeInvalid}) {
				t.Fatalf("malformed email: %v", err)
			}
			if _, err := tc.add(ctx, admin, unknownID, e.email(u)); !errors.Is(err, portal.ErrNotFound) {
				t.Fatalf("unknown area: %v", err)
			}

			list, err := tc.list(ctx, admin, a.ID, portal.PageRequest{})
			if err != nil || len(list.Items) != 1 || list.Items[0].UserID != u || list.Items[0].Email != e.email(u) {
				t.Fatalf("list = %+v, %v", list, err)
			}
			if _, err := tc.list(ctx, admin, unknownID, portal.PageRequest{}); !errors.Is(err, portal.ErrNotFound) {
				t.Fatalf("list of an unknown area: %v", err)
			}

			events = e.auditCount()
			if err := tc.remove(ctx, admin, a.ID, u); err != nil {
				t.Fatal(err)
			}
			au = e.lastAudit()
			if e.auditCount() != events+1 || au.Action != tc.prefix+".remove" || au.TargetID != a.ID || au.Before["user_id"] != u || au.After != nil {
				t.Fatalf("audit = %+v", au)
			}
			if err := tc.remove(ctx, admin, a.ID, u); !errors.Is(err, portal.ErrNotFound) {
				t.Fatalf("repeat remove: %v", err)
			}
			if err := tc.remove(ctx, admin, a.ID, e.w.robot(t, "bot-"+tc.name)); !errors.Is(err, portal.ErrNotFound) {
				t.Fatalf("remove of a robot id: %v", err)
			}
			if e.auditCount() != events+1 {
				t.Fatal("a refused removal was audited")
			}
		})
	}

	t.Run("a disabled member can be removed and the removal is audited (offboarding)", func(t *testing.T) {
		a, _ := e.svc.CreateArea(ctx, admin, portal.NewArea{Key: "offboard"})
		u := e.w.user(t, "active")
		if _, err := e.svc.AddAreaMember(ctx, admin, a.ID, e.email(u)); err != nil {
			t.Fatal(err)
		}
		mustExec(t, e.owner, `UPDATE users SET status = 'disabled' WHERE id = $1`, u)
		events := e.auditCount()
		if err := e.svc.RemoveAreaMember(ctx, admin, a.ID, u); err != nil {
			t.Fatal(err)
		}
		au := e.lastAudit()
		if e.auditCount() != events+1 || au.Action != "area_member.remove" || au.Before["user_id"] != u {
			t.Fatalf("audit = %+v", au)
		}
	})

	t.Run("a person is in one area at most: another area is 409, and moving is remove then add", func(t *testing.T) {
		a, _ := e.svc.CreateArea(ctx, admin, portal.NewArea{Key: "from"})
		b, _ := e.svc.CreateArea(ctx, admin, portal.NewArea{Key: "to"})
		u := e.w.user(t, "active")
		if _, err := e.svc.AddAreaMember(ctx, admin, a.ID, e.email(u)); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.AddAreaMember(ctx, admin, b.ID, e.email(u)); !errors.Is(err, portal.ErrAreaMemberExists) {
			t.Fatalf("second area: %v", err)
		}
		if err := e.svc.RemoveAreaMember(ctx, admin, a.ID, u); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.AddAreaMember(ctx, admin, b.ID, e.email(u)); err != nil {
			t.Fatalf("after removing: %v", err)
		}
	})

	t.Run("a supervisor may be a member, of the area they supervise included, and supervise several areas", func(t *testing.T) {
		a, _ := e.svc.CreateArea(ctx, admin, portal.NewArea{Key: "both"})
		b, _ := e.svc.CreateArea(ctx, admin, portal.NewArea{Key: "both-2"})
		u := e.w.user(t, "active")
		for _, add := range []func() error{
			func() error { _, err := e.svc.AddAreaMember(ctx, admin, a.ID, e.email(u)); return err },
			func() error { _, err := e.svc.AddAreaSupervisor(ctx, admin, a.ID, e.email(u)); return err },
			func() error { _, err := e.svc.AddAreaSupervisor(ctx, admin, b.ID, e.email(u)); return err },
		} {
			if err := add(); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("delete only without members and supervisors", func(t *testing.T) {
		withMember, _ := e.svc.CreateArea(ctx, admin, portal.NewArea{Key: "has-member"})
		withSupervisor, _ := e.svc.CreateArea(ctx, admin, portal.NewArea{Key: "has-supervisor"})
		empty, _ := e.svc.CreateArea(ctx, admin, portal.NewArea{Key: "is-empty", Name: "Empty"})
		if _, err := e.svc.AddAreaMember(ctx, admin, withMember.ID, e.email(plain)); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.AddAreaSupervisor(ctx, admin, withSupervisor.ID, e.email(plain)); err != nil {
			t.Fatal(err)
		}
		events := e.auditCount()
		for name, id := range map[string]string{"members": withMember.ID, "supervisors": withSupervisor.ID} {
			if err := e.svc.DeleteArea(ctx, admin, id); !errors.Is(err, portal.ErrAreaInUse) {
				t.Errorf("area with %s: %v", name, err)
			}
			if e.count(`SELECT count(*) FROM areas WHERE id = $1`, id) != 1 {
				t.Errorf("the area with %s was deleted", name)
			}
		}
		if e.count(`SELECT count(*) FROM area_members WHERE area_id = $1`, withMember.ID) != 1 ||
			e.count(`SELECT count(*) FROM area_supervisors WHERE area_id = $1`, withSupervisor.ID) != 1 {
			t.Fatal("rows of an area in use were removed")
		}
		if e.auditCount() != events {
			t.Fatal("refused deletions were audited")
		}
		if err := e.svc.DeleteArea(ctx, admin, empty.ID); err != nil {
			t.Fatal(err)
		}
		au := e.lastAudit()
		if au.Action != "area.delete" || au.TargetID != empty.ID || au.Before["key"] != "is-empty" || au.Before["name"] != "Empty" || au.After != nil {
			t.Fatalf("audit = %+v", au)
		}
		if err := e.svc.DeleteArea(ctx, admin, empty.ID); !errors.Is(err, portal.ErrNotFound) {
			t.Fatalf("repeat: %v", err)
		}
		if err := e.svc.DeleteArea(ctx, admin, "nope"); !errors.Is(err, portal.ErrNotFound) {
			t.Fatalf("malformed: %v", err)
		}
	})

	if n := noArea(); n != 0 {
		t.Fatalf("%d audit rows carry an area_id: nothing writes it before part 1b", n)
	}
}

// waitForLockWait polls until some backend of the test database waits for a lock, so the test can release the
// transaction that holds it knowing the other one is queued behind it.
func waitForLockWait(t *testing.T, e *adminEnv) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for e.count(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'`) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no backend is waiting for a lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Deleting an area takes no explicit lock: the foreign keys are ON DELETE RESTRICT. Whichever of an add and a delete
// commits first, the other one ends in a defined state and neither leaves a member of a deleted area.
func TestAreaAddAndDeleteRace(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	admin := e.admin()

	t.Run("a delete in flight wins: the add answers not found", func(t *testing.T) {
		area, user := e.w.area(t, "race-delete"), e.w.user(t, "active")
		tx, err := e.owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `DELETE FROM areas WHERE id = $1`, area); err != nil {
			t.Fatal(err)
		}
		res := make(chan error, 1)
		go func() { _, err := e.svc.AddAreaMember(ctx, admin, area, e.email(user)); res <- err }()
		waitForLockWait(t, e)
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-res; !errors.Is(err, portal.ErrNotFound) {
			t.Fatalf("add to an area deleted meanwhile: %v", err)
		}
		if e.count(`SELECT count(*) FROM area_members WHERE user_id = $1`, user) != 0 {
			t.Fatal("a member of a deleted area")
		}
	})

	t.Run("an add in flight wins: the delete is refused", func(t *testing.T) {
		area, user := e.w.area(t, "race-add"), e.w.user(t, "active")
		tx, err := e.owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `INSERT INTO area_members (area_id, user_id) VALUES ($1, $2)`, area, user); err != nil {
			t.Fatal(err)
		}
		res := make(chan error, 1)
		go func() { res <- e.svc.DeleteArea(ctx, admin, area) }()
		waitForLockWait(t, e)
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-res; !errors.Is(err, portal.ErrAreaInUse) {
			t.Fatalf("delete of an area that got a member meanwhile: %v", err)
		}
		if e.count(`SELECT count(*) FROM areas WHERE id = $1`, area) != 1 {
			t.Fatal("the area was deleted under its new member")
		}
	})

	t.Run("a burst of adds against a delete never leaves orphans", func(t *testing.T) {
		area := e.w.area(t, "race-burst")
		users := make([]string, 8)
		for i := range users {
			users[i] = e.w.user(t, "active")
		}
		var wg sync.WaitGroup
		for _, u := range users {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := e.svc.AddAreaMember(ctx, admin, area, e.email(u))
				if err != nil && !errors.Is(err, portal.ErrNotFound) {
					t.Errorf("add: %v", err)
				}
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.svc.DeleteArea(ctx, admin, area); err != nil && !errors.Is(err, portal.ErrAreaInUse) {
				t.Errorf("delete: %v", err)
			}
		}()
		wg.Wait()
		if e.count(`SELECT count(*) FROM area_members m WHERE NOT EXISTS (SELECT 1 FROM areas a WHERE a.id = m.area_id)`) != 0 {
			t.Fatal("a member of a deleted area")
		}
	})
}

func TestAreasPaginate(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	admin := e.admin()
	var want []string
	for i := range 5 {
		a, err := e.svc.CreateArea(ctx, admin, portal.NewArea{Key: fmt.Sprintf("p%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, a.ID)
	}
	var got []string
	cursor := ""
	for {
		p, err := e.svc.ListAreas(ctx, admin, portal.PageRequest{Cursor: cursor, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range p.Items {
			got = append(got, a.ID)
		}
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	if !slices.Equal(got, want) {
		t.Fatalf("areas paged = %v, want %v", got, want)
	}

	var members []string
	for range 5 {
		u := e.w.user(t, "active")
		if _, err := e.svc.AddAreaMember(ctx, admin, want[0], e.email(u)); err != nil {
			t.Fatal(err)
		}
		members = append(members, u)
	}
	slices.Sort(members)
	var gotMembers []string
	cursor = ""
	for {
		p, err := e.svc.ListAreaMembers(ctx, admin, want[0], portal.PageRequest{Cursor: cursor, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range p.Items {
			gotMembers = append(gotMembers, m.UserID)
		}
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	if !slices.Equal(gotMembers, members) {
		t.Fatalf("members paged = %v, want %v", gotMembers, members)
	}
	// A cursor belongs to the listing that issued it.
	p, _ := e.svc.ListAreas(ctx, admin, portal.PageRequest{Limit: 1})
	if _, err := e.svc.ListAreaMembers(ctx, admin, want[0], portal.PageRequest{Cursor: p.NextCursor}); !errors.Is(err, portal.ErrInvalidCursor) {
		t.Fatalf("cursor of another listing: %v", err)
	}
	if _, err := e.svc.ListAreaSupervisors(ctx, admin, want[0], portal.PageRequest{Cursor: "garbage"}); !errors.Is(err, portal.ErrInvalidCursor) {
		t.Fatalf("garbage cursor: %v", err)
	}
}

// /me lists the areas a user supervises, sorted by key, from the rows whatever the status; under an API token the
// list is empty (rule 8 of "Area supervisors").
func TestMeSupervisedAreas(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	sup, plain := e.w.user(t, "active"), e.w.user(t, "active")
	b, a := e.w.area(t, "b-area"), e.w.area(t, "a-area")
	mustExec(t, e.owner, `UPDATE areas SET name = 'Alpha' WHERE id = $1`, a)
	mustExec(t, e.owner, `INSERT INTO area_supervisors (area_id, user_id) VALUES ($1, $3), ($2, $3)`, b, a, sup)
	mustExec(t, e.owner, `INSERT INTO area_members (area_id, user_id) VALUES ($1, $2)`, b, plain) // a member is not a supervisor

	p, err := e.svc.Me(ctx, sup)
	want := []portal.AreaRef{{ID: a, Key: "a-area", Name: "Alpha"}, {ID: b, Key: "b-area", Name: ""}}
	if err != nil || !slices.Equal(p.Supervised, want) {
		t.Fatalf("supervised = %+v, %v, want %+v", p.Supervised, err, want)
	}
	p, err = e.svc.Me(portal.WithToken(ctx, "01a14296-1449-7be0-957b-4b1ad8d7a0ff"), sup)
	if err != nil || p.Supervised == nil || len(p.Supervised) != 0 {
		t.Fatalf("under a token: %+v, %v", p.Supervised, err)
	}
	if p, err = e.svc.Me(ctx, plain); err != nil || p.Supervised == nil || len(p.Supervised) != 0 {
		t.Fatalf("a member: %+v, %v", p.Supervised, err)
	}
	mustExec(t, e.owner, `UPDATE users SET status = 'disabled' WHERE id = $1`, sup)
	if _, err := e.svc.Me(ctx, sup); !errors.Is(err, portal.ErrUserInactive) {
		t.Fatalf("disabled supervisor: %v", err)
	}
}
