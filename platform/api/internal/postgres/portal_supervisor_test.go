package postgres_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"platform/api/internal/portal"
)

// supWorld is the cast of the supervisor tests (contract block 6, "Area supervisors"):
//
//	area A: supervisors sup and sup2 (both also members) and supOff (disabled, also a member); members alice, bob
//	        (disabled) and admMember (a platform admin)
//	area B: supervisor supB; member carol
//	both:   supervisor of A and B, a member of none; nobody is in no area; admin is a platform admin
//
// Robots: rAlice, rSup, rCarol and rBob belong to the person of that name, rNone has no owner. Every person and
// robot has one live token (tX for people, trX for robots).
type supWorld struct {
	e                                                *adminEnv
	areaA, areaB                                     string
	sup, sup2, supB, supOff, alice, bob, admMember   string
	carol, nobody, admin, both, supBInA, admOff      string
	rAlice, rSup, rCarol, rNone, rBob                string
	tAlice, tBob, tSup, tSup2, tCarol, tAdm, tNobody string
	trAlice, trSup, trCarol, trNone, trBob           string
	tSupBInA, tAdmOff, rSupBInA, trSupBInA           string
}

const leakedTID = "01a14296-1449-7be0-957b-4b1ad8d7a0ff"

func newSupWorld(t *testing.T) *supWorld {
	t.Helper()
	e := newAdminEnv(t)
	w := &supWorld{e: e, areaA: e.w.area(t, "area-a"), areaB: e.w.area(t, "area-b")}
	user := func(status string) string { return e.w.user(t, status) }
	w.sup, w.sup2, w.supB, w.supOff = user("active"), user("active"), user("active"), user("disabled")
	w.alice, w.bob, w.admMember, w.carol, w.nobody = user("active"), user("disabled"), user("active"), user("active"), user("active")
	w.both = user("active")
	w.admin = e.admin()
	e.pw.platformAdmin(w.admMember)
	// A member of A who supervises only B (not A), and a disabled platform admin who is a member of A: neither is supervisable.
	w.supBInA, w.admOff = user("active"), user("disabled")
	e.pw.platformAdmin(w.admOff)
	mustExec(t, e.owner, `INSERT INTO area_members (area_id, user_id) VALUES ($1, $2), ($1, $3)`, w.areaA, w.supBInA, w.admOff)
	mustExec(t, e.owner, `INSERT INTO area_supervisors (area_id, user_id) VALUES ($1, $2)`, w.areaB, w.supBInA)
	for _, m := range []struct{ area, user string }{
		{w.areaA, w.sup}, {w.areaA, w.sup2}, {w.areaA, w.alice}, {w.areaA, w.bob}, {w.areaA, w.admMember}, {w.areaA, w.supOff},
		{w.areaB, w.carol},
	} {
		mustExec(t, e.owner, `INSERT INTO area_members (area_id, user_id) VALUES ($1, $2)`, m.area, m.user)
	}
	for _, s := range []struct{ area, user string }{
		{w.areaA, w.sup}, {w.areaA, w.sup2}, {w.areaA, w.supOff}, {w.areaB, w.supB}, {w.areaA, w.both}, {w.areaB, w.both},
	} {
		mustExec(t, e.owner, `INSERT INTO area_supervisors (area_id, user_id) VALUES ($1, $2)`, s.area, s.user)
	}
	robot := func(name, owner string) string {
		id := e.w.robot(t, name)
		if owner != "" {
			mustExec(t, e.owner, `UPDATE users SET owner_user_id = $1 WHERE id = $2`, owner, id)
		}
		return id
	}
	w.rAlice, w.rSup, w.rCarol, w.rNone, w.rBob = robot("r-alice", w.alice), robot("r-sup", w.sup), robot("r-carol", w.carol), robot("r-none", ""), robot("r-bob", w.bob)
	tok := func(owner string) string { return e.w.apiToken(t, owner, "-1 hour", "1 hour", "") }
	w.tAlice, w.tBob, w.tSup, w.tSup2, w.tCarol, w.tAdm, w.tNobody = tok(w.alice), tok(w.bob), tok(w.sup), tok(w.sup2), tok(w.carol), tok(w.admMember), tok(w.nobody)
	w.rSupBInA = robot("r-supbina", w.supBInA)
	w.tSupBInA, w.tAdmOff, w.trSupBInA = tok(w.supBInA), tok(w.admOff), tok(w.rSupBInA)
	w.trAlice, w.trSup, w.trCarol, w.trNone, w.trBob = tok(w.rAlice), tok(w.rSup), tok(w.rCarol), tok(w.rNone), tok(w.rBob)
	return w
}

func (w *supWorld) revoked(id string) bool {
	w.e.t.Helper()
	return w.e.count(`SELECT count(*) FROM api_tokens WHERE id = $1 AND revoked_at IS NOT NULL`, id) == 1
}

func (w *supWorld) lastAuditArea() string {
	w.e.t.Helper()
	return scalar[string](w.e.t, w.e.owner, `SELECT COALESCE(area_id::text, '') FROM audit_events ORDER BY occurred_at DESC, id DESC LIMIT 1`)
}

func (w *supWorld) owner(robot string) string {
	w.e.t.Helper()
	return scalar[string](w.e.t, w.e.owner, `SELECT COALESCE(owner_user_id::text, '') FROM users WHERE id = $1`, robot)
}

func TestSupervisorRevokesTokens(t *testing.T) {
	t.Parallel()
	w := newSupWorld(t)
	e, ctx := w.e, w.e.ctx()

	t.Run("reaches the personal tokens of supervisable members, the disabled included, and their robots' tokens", func(t *testing.T) {
		for name, tok := range map[string]string{"personal": w.tAlice, "robot": w.trAlice, "disabled member": w.tBob, "disabled member's robot": w.trBob} {
			before := e.auditCount()
			if err := e.svc.RevokeToken(ctx, w.sup, tok); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if !w.revoked(tok) || e.auditCount() != before+1 {
				t.Fatalf("%s: revoked = %v, audit rows +%d", name, w.revoked(tok), e.auditCount()-before)
			}
			a := e.lastAudit()
			if a.Actor != w.sup || a.Action != "token.revoke" || a.TargetID != tok || w.lastAuditArea() != w.areaA {
				t.Fatalf("%s: audit = %+v, area %q", name, a, w.lastAuditArea())
			}
			if _, leaks := a.Before["token_hash"]; leaks || a.Before["name"] != "tok" {
				t.Fatalf("%s: audit document = %v", name, a.Before)
			}
		}
	})

	t.Run("an already revoked token answers success and writes nothing", func(t *testing.T) {
		before := e.auditCount()
		if err := e.svc.RevokeToken(ctx, w.sup, w.tAlice); err != nil || e.auditCount() != before {
			t.Fatalf("err = %v, audit rows +%d", err, e.auditCount()-before)
		}
	})

	t.Run("everything else is not found and stays valid", func(t *testing.T) {
		// A supervisor who is also a member reaches none of their own (the token of their own robot), nor a
		// co-supervisor, a platform admin member, a person without an area, another area, or a robot without owner.
		before := e.auditCount()
		for name, tok := range map[string]string{
			"robot of the supervisor": w.trSup, "co-supervisor": w.tSup2, "platform admin member": w.tAdm, "person in no area": w.tNobody,
			"other area": w.tCarol, "robot of another area": w.trCarol, "robot without owner": w.trNone,
		} {
			if err := e.svc.RevokeToken(ctx, w.sup, tok); !errors.Is(err, portal.ErrNotFound) || w.revoked(tok) {
				t.Errorf("%s: err = %v, revoked = %v", name, err, w.revoked(tok))
			}
		}
		// The same token as seen by the supervisor of the other area, a plain member and under a leaked token.
		fresh := e.w.apiToken(t, w.alice, "-1 hour", "1 hour", "")
		for name, call := range map[string]func() error{
			"supervisor of another area": func() error { return e.svc.RevokeToken(ctx, w.supB, fresh) },
			"a plain member":             func() error { return e.svc.RevokeToken(ctx, w.carol, fresh) },
			"under a token":              func() error { return e.svc.RevokeToken(portal.WithToken(ctx, leakedTID), w.sup, fresh) },
		} {
			if err := call(); !errors.Is(err, portal.ErrNotFound) || w.revoked(fresh) {
				t.Errorf("%s: err = %v, revoked = %v", name, err, w.revoked(fresh))
			}
		}
		if err := e.svc.RevokeToken(ctx, w.supOff, fresh); !errors.Is(err, portal.ErrUserInactive) || w.revoked(fresh) {
			t.Errorf("a disabled supervisor: err = %v, revoked = %v", err, w.revoked(fresh))
		}
		if e.auditCount() != before {
			t.Fatal("a refused revocation was audited")
		}
	})

	t.Run("a supervisor who is also a manager, with an admin token, is not found", func(t *testing.T) {
		alpha := e.initiative("sup-alpha")
		mgr := e.manager(alpha.id, alpha.manager)
		mustExec(t, e.owner, `INSERT INTO area_supervisors (area_id, user_id) VALUES ($1, $2)`, w.areaA, mgr)
		fresh := e.w.apiToken(t, w.alice, "-1 hour", "1 hour", "")
		if err := e.svc.RevokeToken(portal.WithToken(ctx, leakedTID), mgr, fresh); !errors.Is(err, portal.ErrNotFound) || w.revoked(fresh) {
			t.Fatalf("err = %v, revoked = %v", err, w.revoked(fresh))
		}
		if err := e.svc.RevokeToken(ctx, mgr, fresh); err != nil || !w.revoked(fresh) { // the same caller from a session
			t.Fatalf("session: %v", err)
		}
	})

	t.Run("revocation by secret stays for platform admins", func(t *testing.T) {
		if err := e.svc.RevokeBySecret(ctx, w.sup, "ptk_whatever"); !errors.Is(err, portal.ErrForbidden) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a platform admin who also supervises the area writes no area_id", func(t *testing.T) {
		mustExec(t, e.owner, `INSERT INTO area_supervisors (area_id, user_id) VALUES ($1, $2)`, w.areaB, w.admin)
		fresh := e.w.apiToken(t, w.carol, "-1 hour", "1 hour", "")
		if err := e.svc.RevokeToken(ctx, w.admin, fresh); err != nil || !w.revoked(fresh) {
			t.Fatal(err)
		}
		if a := e.lastAudit(); a.Actor != w.admin || a.Action != "token.revoke" || w.lastAuditArea() != "" {
			t.Fatalf("audit = %+v, area %q", a, w.lastAuditArea())
		}
		// Admins reach what no supervisor does: another admin, a supervisor, a person in no area.
		for _, tok := range []string{w.tAdm, w.tSup2, w.tNobody, w.trNone} {
			if err := e.svc.RevokeToken(ctx, w.admin, tok); err != nil || !w.revoked(tok) {
				t.Fatalf("admin on %s: %v", tok, err)
			}
		}
	})

	t.Run("the owner revokes their own token as before, with no area", func(t *testing.T) {
		mine := e.w.apiToken(t, w.sup, "-1 hour", "1 hour", "")
		if err := e.svc.RevokeToken(ctx, w.sup, mine); err != nil || !w.revoked(mine) || w.lastAuditArea() != "" {
			t.Fatalf("err = %v, area %q", err, w.lastAuditArea())
		}
	})
}

func TestSupervisorReassignsRobots(t *testing.T) {
	t.Parallel()
	w := newSupWorld(t)
	e, ctx := w.e, w.e.ctx()
	alice2 := e.w.user(t, "active")
	mustExec(t, e.owner, `INSERT INTO area_members (area_id, user_id) VALUES ($1, $2)`, w.areaA, alice2)
	reassign := func(caller, robot, owner string) (portal.Robot, error) {
		return e.svc.UpdateRobot(ctx, caller, robot, portal.RobotPatch{OwnerUserID: &owner})
	}

	t.Run("to another supervisable member of the area, audited with the previous owner's area", func(t *testing.T) {
		r, err := reassign(w.sup, w.rAlice, alice2)
		if err != nil || r.OwnerUserID != alice2 || !r.Detail || r.OwnerEmail != e.email(alice2) {
			t.Fatalf("robot = %+v, %v", r, err)
		}
		a := e.lastAudit()
		if a.Actor != w.sup || a.Action != "robot.update" || a.Before["owner_user_id"] != w.alice || a.After["owner_user_id"] != alice2 || w.lastAuditArea() != w.areaA {
			t.Fatalf("audit = %+v, area %q", a, w.lastAuditArea())
		}
	})

	t.Run("a patch that changes nothing writes no audit row", func(t *testing.T) {
		before := e.auditCount()
		if _, err := reassign(w.sup, w.rAlice, alice2); err != nil || e.auditCount() != before {
			t.Fatalf("err = %v, audit rows +%d", err, e.auditCount()-before)
		}
	})

	t.Run("a new owner outside the actor's areas, inactive, not supervisable or empty is refused", func(t *testing.T) {
		before, auditBefore := w.owner(w.rAlice), e.auditCount()
		for name, owner := range map[string]string{
			"another area": w.carol, "inactive": w.bob, "no area": w.nobody, "a co-supervisor": w.sup2, "themselves": w.sup,
			"a platform admin member": w.admMember, "an unknown user": unknownID, "a robot": w.rNone, "malformed": "nope",
		} {
			_, err := reassign(w.sup, w.rAlice, owner)
			if got := validationFields(t, err); got[0].Field != "owner_user_id" || got[0].Code != portal.CodeUnknown && got[0].Code != portal.CodeInvalid {
				t.Errorf("%s: %v", name, err)
			}
		}
		_, err := reassign(w.sup, w.rAlice, "")
		if got := validationFields(t, err); got[0].Field != "owner_user_id" || got[0].Code != portal.CodeNotAllowed {
			t.Errorf("clearing the owner: %v", err)
		}
		if w.owner(w.rAlice) != before || e.auditCount() != auditBefore {
			t.Fatal("a refused reassignment changed something")
		}
	})

	t.Run("only the owner may change: any other field is forbidden", func(t *testing.T) {
		name, status := "x", "disabled"
		for label, p := range map[string]portal.RobotPatch{
			"name": {Name: &name}, "status": {Status: &status}, "name and owner": {Name: &name, OwnerUserID: &alice2}, "nothing": {},
		} {
			if _, err := e.svc.UpdateRobot(ctx, w.sup, w.rAlice, p); !errors.Is(err, portal.ErrForbidden) {
				t.Errorf("%s: %v", label, err)
			}
		}
	})

	t.Run("out of scope is not found: no owner, own robot, other area, a human id, under a token", func(t *testing.T) {
		for label, c := range map[string]struct {
			ctx           context.Context
			caller, robot string
		}{
			"without owner": {ctx, w.sup, w.rNone}, "own robot": {ctx, w.sup, w.rSup}, "other area": {ctx, w.sup, w.rCarol},
			"another supervisor": {ctx, w.supB, w.rAlice}, "a human": {ctx, w.sup, w.alice}, "unknown": {ctx, w.sup, unknownID},
			"under a token": {portal.WithToken(ctx, leakedTID), w.sup, w.rAlice}, "a plain user": {ctx, w.carol, w.rAlice},
		} {
			_, err := e.svc.UpdateRobot(c.ctx, c.caller, c.robot, portal.RobotPatch{OwnerUserID: &alice2})
			if !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("%s: %v", label, err)
			}
		}
	})

	t.Run("a supervisor who is also a manager, with an admin token, is not found", func(t *testing.T) {
		alpha := e.initiative("sup-alpha")
		mgr := e.manager(alpha.id, alpha.manager)
		mustExec(t, e.owner, `INSERT INTO area_supervisors (area_id, user_id) VALUES ($1, $2)`, w.areaA, mgr)
		if _, err := reassign(mgr, w.rAlice, alice2); err != nil { // a session works (no change: same owner)
			t.Fatal(err)
		}
		_, err := e.svc.UpdateRobot(portal.WithToken(ctx, leakedTID), mgr, w.rAlice, portal.RobotPatch{OwnerUserID: ptr(w.alice)})
		if !errors.Is(err, portal.ErrNotFound) || w.owner(w.rAlice) != alice2 {
			t.Fatalf("err = %v, owner %s", err, w.owner(w.rAlice))
		}
	})

	t.Run("a supervisor of two areas moves a robot between them", func(t *testing.T) {
		if r, err := reassign(w.both, w.rAlice, w.carol); err != nil || r.OwnerUserID != w.carol {
			t.Fatalf("robot = %+v, %v", r, err)
		}
		if w.lastAuditArea() != w.areaA {
			t.Fatalf("area = %q, want the previous owner's area", w.lastAuditArea())
		}
		if _, err := reassign(w.sup, w.rAlice, alice2); !errors.Is(err, portal.ErrNotFound) { // now in B: sup lost it
			t.Fatalf("after the move: %v", err)
		}
	})

	t.Run("platform admins keep their path: any owner or none, no area recorded", func(t *testing.T) {
		if r, err := reassign(w.admin, w.rNone, w.nobody); err != nil || r.OwnerUserID != w.nobody || w.lastAuditArea() != "" {
			t.Fatalf("robot = %+v, %v, area %q", r, err, w.lastAuditArea())
		}
		if r, err := reassign(w.admin, w.rNone, ""); err != nil || r.OwnerUserID != "" {
			t.Fatalf("clear: %+v, %v", r, err)
		}
		name := "renamed"
		if _, err := e.svc.UpdateRobot(ctx, w.admin, w.rNone, portal.RobotPatch{Name: &name}); err != nil {
			t.Fatal(err)
		}
	})
}

// The scope is read again after LockRobot: a reassignment that waited for another writer of the robot is authorized
// against the owner that writer left, so the second of two reassignments (or an admin patch) is not found when the
// first moved the robot out of the actor's reach, and succeeds in the serial order otherwise.
func TestReassignRechecksScopeAfterTheLock(t *testing.T) {
	t.Parallel()
	w := newSupWorld(t)
	e, ctx := w.e, w.e.ctx()
	alice2 := e.w.user(t, "active")
	mustExec(t, e.owner, `INSERT INTO area_members (area_id, user_id) VALUES ($1, $2)`, w.areaA, alice2)

	hold := func(t *testing.T, sql string, args ...any) (commit func()) {
		t.Helper()
		tx, err := e.owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Rollback(ctx) })
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
		return func() {
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("an admin moved the robot out of the supervisor's areas while it waited", func(t *testing.T) {
		robot := e.w.robot(t, "race-out")
		mustExec(t, e.owner, `UPDATE users SET owner_user_id = $1 WHERE id = $2`, w.alice, robot)
		commit := hold(t, `UPDATE users SET owner_user_id = $1 WHERE id = $2`, w.carol, robot)
		res := make(chan error, 1)
		go func() {
			_, err := e.svc.UpdateRobot(ctx, w.sup, robot, portal.RobotPatch{OwnerUserID: ptr(alice2)})
			res <- err
		}()
		waitForLockWait(t, e)
		commit()
		if err := <-res; !errors.Is(err, portal.ErrNotFound) || w.owner(robot) != w.carol {
			t.Fatalf("err = %v, owner %s", err, w.owner(robot))
		}
	})

	t.Run("a competing reassignment stayed in scope: the second one runs after the first", func(t *testing.T) {
		robot := e.w.robot(t, "race-in")
		mustExec(t, e.owner, `UPDATE users SET owner_user_id = $1 WHERE id = $2`, w.alice, robot)
		commit := hold(t, `UPDATE users SET owner_user_id = $1 WHERE id = $2`, alice2, robot)
		res := make(chan error, 1)
		go func() {
			_, err := e.svc.UpdateRobot(ctx, w.sup2, robot, portal.RobotPatch{OwnerUserID: ptr(w.alice)})
			res <- err
		}()
		waitForLockWait(t, e)
		commit()
		if err := <-res; err != nil || w.owner(robot) != w.alice {
			t.Fatalf("err = %v, owner %s", err, w.owner(robot))
		}
		if a := e.lastAudit(); a.Before["owner_user_id"] != alice2 || a.After["owner_user_id"] != w.alice {
			t.Fatalf("the second change must record what the first one wrote: %+v", a)
		}
	})
}

// A member removed, or promoted to platform admin, while a supervisor acts on them ends in a serial order: the
// action sees the rows committed when it read them (no area row is locked), so an uncommitted change is "after".
func TestSupervisorScopeRaces(t *testing.T) {
	t.Parallel()
	w := newSupWorld(t)
	e, ctx := w.e, w.e.ctx()

	inFlight := func(t *testing.T, sql string, args ...any) (commit func()) {
		t.Helper()
		tx, err := e.owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Rollback(ctx) })
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
		return func() {
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}

	for _, c := range []struct{ name, sql string }{
		{"removed from the area", `DELETE FROM area_members WHERE user_id = $1`},
		{"promoted to platform admin", `INSERT INTO platform_admins (user_id) VALUES ($1)`},
		{"made a supervisor", `INSERT INTO area_supervisors (area_id, user_id) SELECT area_id, user_id FROM area_members WHERE user_id = $1`},
	} {
		t.Run("revoke: "+c.name+" in flight, then committed", func(t *testing.T) {
			person := e.w.user(t, "active")
			mustExec(t, e.owner, `INSERT INTO area_members (area_id, user_id) VALUES ($1, $2)`, w.areaA, person)
			tok := e.w.apiToken(t, person, "-1 hour", "1 hour", "")
			commit := inFlight(t, c.sql, person)
			if err := e.svc.RevokeToken(ctx, w.sup, tok); err != nil || !w.revoked(tok) { // serial order: revoke, then the change
				t.Fatalf("in flight: %v (revoked %v)", err, w.revoked(tok))
			}
			commit()
			fresh := e.w.apiToken(t, person, "-1 hour", "1 hour", "")
			if err := e.svc.RevokeToken(ctx, w.sup, fresh); !errors.Is(err, portal.ErrNotFound) || w.revoked(fresh) { // change, then revoke
				t.Fatalf("after the change: %v (revoked %v)", err, w.revoked(fresh))
			}
		})
	}

	t.Run("reassign: the new owner removed from the area in flight", func(t *testing.T) {
		target := e.w.user(t, "active")
		mustExec(t, e.owner, `INSERT INTO area_members (area_id, user_id) VALUES ($1, $2)`, w.areaA, target)
		robot := e.w.robot(t, "race-target")
		mustExec(t, e.owner, `UPDATE users SET owner_user_id = $1 WHERE id = $2`, w.alice, robot)
		commit := inFlight(t, `DELETE FROM area_members WHERE user_id = $1`, target)
		if _, err := e.svc.UpdateRobot(ctx, w.sup, robot, portal.RobotPatch{OwnerUserID: ptr(target)}); err != nil || w.owner(robot) != target {
			t.Fatalf("in flight: %v (owner %s)", err, w.owner(robot))
		}
		commit()
		if _, err := e.svc.UpdateRobot(ctx, w.sup, robot, portal.RobotPatch{OwnerUserID: ptr(w.alice)}); !errors.Is(err, portal.ErrNotFound) {
			t.Fatalf("after the removal the robot left the scope: %v", err)
		}
	})
}

func TestSupervisorLists(t *testing.T) {
	t.Parallel()
	w := newSupWorld(t)
	e, ctx := w.e, w.e.ctx()
	dead := e.w.apiToken(t, w.alice, "-2 hours", "-1 hour", "")
	revoked := e.w.apiToken(t, w.alice, "-2 hours", "1 hour", "-30 minutes")
	tokenIDs := func(p portal.Page[portal.APIToken]) []string {
		var ids []string
		for _, t := range p.Items {
			ids = append(ids, t.ID)
		}
		return ids
	}
	robotIDs := func(p portal.Page[portal.Robot]) []string {
		var ids []string
		for _, r := range p.Items {
			ids = append(ids, r.ID)
		}
		return ids
	}
	under := portal.WithToken(ctx, leakedTID)

	t.Run("user tokens: supervisable members, newest first with the dead ones, never secrets", func(t *testing.T) {
		p, err := e.svc.ListUserTokens(ctx, w.sup, w.alice, portal.PageRequest{})
		got := tokenIDs(p)
		if !slices.IsSortedFunc(got, func(a, b string) int { return strings.Compare(b, a) }) {
			t.Fatalf("not newest first: %v", got)
		}
		slices.Sort(got)
		if want := slices.Sorted(slices.Values([]string{revoked, dead, w.tAlice})); err != nil || !slices.Equal(got, want) {
			t.Fatalf("tokens = %v, %v, want %v", got, err, want)
		}
		if p, err := e.svc.ListUserTokens(ctx, w.sup, w.bob, portal.PageRequest{}); err != nil || len(p.Items) != 1 { // a disabled member
			t.Fatalf("disabled member: %v, %v", p, err)
		}
	})

	t.Run("user tokens and robots: everyone else is not found", func(t *testing.T) {
		for name, c := range map[string]struct {
			ctx            context.Context
			caller, person string
		}{
			"themselves": {ctx, w.sup, w.sup}, "a co-supervisor": {ctx, w.sup, w.sup2}, "a platform admin member": {ctx, w.sup, w.admMember},
			"a disabled supervisor": {ctx, w.sup, w.supOff}, "another area": {ctx, w.sup, w.carol}, "no area": {ctx, w.sup, w.nobody},
			"a robot id": {ctx, w.sup, w.rAlice}, "unknown": {ctx, w.sup, unknownID}, "malformed": {ctx, w.sup, "nope"},
			"another supervisor": {ctx, w.supB, w.alice}, "a plain member": {ctx, w.carol, w.alice},
			"a read token": {under, w.sup, w.alice},
		} {
			if _, err := e.svc.ListUserTokens(c.ctx, c.caller, c.person, portal.PageRequest{}); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("tokens, %s: %v", name, err)
			}
			if _, err := e.svc.ListUserRobots(c.ctx, c.caller, c.person, portal.PageRequest{}); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("robots, %s: %v", name, err)
			}
		}
		if _, err := e.svc.ListUserTokens(ctx, w.supOff, w.alice, portal.PageRequest{}); !errors.Is(err, portal.ErrUserInactive) {
			t.Errorf("a disabled supervisor: %v", err)
		}
	})

	t.Run("platform admins list any human, which they could not before", func(t *testing.T) {
		for _, person := range []string{w.sup, w.sup2, w.admMember, w.nobody, w.carol} {
			if _, err := e.svc.ListUserTokens(ctx, w.admin, person, portal.PageRequest{}); err != nil {
				t.Errorf("tokens of %s: %v", person, err)
			}
			if _, err := e.svc.ListUserRobots(ctx, w.admin, person, portal.PageRequest{}); err != nil {
				t.Errorf("robots of %s: %v", person, err)
			}
		}
		if _, err := e.svc.ListUserTokens(ctx, w.admin, w.rAlice, portal.PageRequest{}); !errors.Is(err, portal.ErrNotFound) {
			t.Errorf("a robot id: %v", err)
		}
		if p, err := e.svc.ListUserTokens(ctx, w.admin, w.sup, portal.PageRequest{}); err != nil || len(p.Items) != 1 || p.Items[0].ID != w.tSup {
			t.Errorf("tokens of a supervisor: %v, %v", p, err)
		}
	})

	t.Run("user robots: the admin document with the owner and the last use", func(t *testing.T) {
		mustExec(t, e.owner, `UPDATE api_tokens SET last_used_at = now() WHERE id = $1`, w.trAlice)
		p, err := e.svc.ListUserRobots(ctx, w.sup, w.alice, portal.PageRequest{})
		if err != nil || !slices.Equal(robotIDs(p), []string{w.rAlice}) {
			t.Fatalf("robots = %v, %v", robotIDs(p), err)
		}
		if r := p.Items[0]; !r.Detail || r.OwnerUserID != w.alice || r.OwnerEmail != e.email(w.alice) || r.LastUsedAt == nil {
			t.Fatalf("robot = %+v", r)
		}
		mustExec(t, e.owner, `UPDATE users SET status = 'disabled' WHERE id = $1`, w.rAlice) // a disabled robot is still listed
		if p, _ := e.svc.ListUserRobots(ctx, w.sup, w.alice, portal.PageRequest{}); len(p.Items) != 1 {
			t.Fatalf("disabled robot: %+v", p.Items)
		}
		if p, err := e.svc.ListUserRobots(ctx, w.admin, w.nobody, portal.PageRequest{}); err != nil || len(p.Items) != 0 {
			t.Fatalf("robots of nobody: %v, %v", p.Items, err)
		}
	})

	t.Run("user robots paginate", func(t *testing.T) {
		owner := e.w.user(t, "active")
		mustExec(t, e.owner, `INSERT INTO area_members (area_id, user_id) VALUES ($1, $2)`, w.areaB, owner)
		var want []string
		for _, n := range []string{"pg-1", "pg-2", "pg-3"} {
			id := e.w.robot(t, n)
			mustExec(t, e.owner, `UPDATE users SET owner_user_id = $1 WHERE id = $2`, owner, id)
			want = append(want, id)
		}
		slices.Sort(want)
		var seen []string
		req := portal.PageRequest{Limit: 2}
		for {
			p, err := e.svc.ListUserRobots(ctx, w.both, owner, req)
			if err != nil {
				t.Fatal(err)
			}
			seen = append(seen, robotIDs(p)...)
			if p.NextCursor == "" {
				break
			}
			req.Cursor = p.NextCursor
		}
		if !slices.Equal(seen, want) {
			t.Fatalf("seen %v, want %v", seen, want)
		}
	})

	t.Run("robot tokens: the robots of supervisable people only", func(t *testing.T) {
		if p, err := e.svc.ListRobotTokens(ctx, w.sup, w.rAlice, portal.PageRequest{}); err != nil || !slices.Equal(tokenIDs(p), []string{w.trAlice}) {
			t.Fatalf("tokens = %v, %v", tokenIDs(p), err)
		}
		if _, err := e.svc.ListRobotTokens(ctx, w.sup, w.rBob, portal.PageRequest{}); err != nil { // the owner is disabled: still supervisable
			t.Fatalf("robot of a disabled member: %v", err)
		}
		for name, c := range map[string]struct {
			ctx           context.Context
			caller, robot string
		}{
			"without owner": {ctx, w.sup, w.rNone}, "own robot": {ctx, w.sup, w.rSup}, "other area": {ctx, w.sup, w.rCarol},
			"a human id": {ctx, w.sup, w.alice}, "a read token": {under, w.sup, w.rAlice}, "another supervisor": {ctx, w.supB, w.rAlice},
		} {
			if _, err := e.svc.ListRobotTokens(c.ctx, c.caller, c.robot, portal.PageRequest{}); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("%s: %v", name, err)
			}
		}
	})

	t.Run("area members: a supervisor sees the supervisable ones, an admin all", func(t *testing.T) {
		ids := func(p portal.Page[portal.AreaPerson]) []string {
			var out []string
			for _, m := range p.Items {
				out = append(out, m.UserID)
			}
			slices.Sort(out)
			return out
		}
		p, err := e.svc.ListAreaMembers(ctx, w.sup, w.areaA, portal.PageRequest{})
		want := []string{w.alice, w.bob}
		slices.Sort(want)
		if err != nil || !slices.Equal(ids(p), want) {
			t.Fatalf("supervisor sees %v, %v, want %v", ids(p), err, want)
		}
		p, err = e.svc.ListAreaMembers(ctx, w.admin, w.areaA, portal.PageRequest{})
		if err != nil || len(p.Items) != 8 {
			t.Fatalf("admin sees %d members, %v", len(p.Items), err)
		}
		for name, c := range map[string]struct {
			ctx          context.Context
			caller, area string
		}{
			"another area's supervisor": {ctx, w.supB, w.areaA}, "a plain member": {ctx, w.alice, w.areaA}, "a read token": {under, w.sup, w.areaA},
			"the supervisor of a different area": {ctx, w.sup, w.areaB},
		} {
			if _, err := e.svc.ListAreaMembers(c.ctx, c.caller, c.area, portal.PageRequest{}); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("%s: %v", name, err)
			}
		}
		if _, err := e.svc.ListAreaSupervisors(ctx, w.sup, w.areaA, portal.PageRequest{}); !errors.Is(err, portal.ErrForbidden) {
			t.Errorf("the supervisors list stays admin-only: %v", err)
		}
	})
}

func TestSupervisorIsNotGivenAdminOrAuditAccess(t *testing.T) {
	t.Parallel()
	w := newSupWorld(t)
	e, ctx := w.e, w.e.ctx()

	if _, err := e.svc.CreateOwnToken(ctx, w.sup, portal.NewToken{Name: "n", Scope: "admin"}); validationFields(t, err)[0].Code != portal.CodeNotAllowed {
		t.Fatalf("a plain supervisor asking for an admin token: %v", err)
	}
	if _, err := e.svc.ListAudit(ctx, w.sup, portal.AuditQuery{}); !errors.Is(err, portal.ErrForbidden) {
		t.Fatalf("a supervisor has no audit view: %v", err)
	}
	if _, err := e.svc.SetUserStatus(ctx, w.sup, w.alice, "disabled"); !errors.Is(err, portal.ErrForbidden) {
		t.Fatalf("a supervisor disables nobody: %v", err)
	}
	if _, err := e.svc.RevokeUserSessions(ctx, w.sup, w.alice); !errors.Is(err, portal.ErrForbidden) {
		t.Fatalf("a supervisor revokes no sessions: %v", err)
	}
	if _, err := e.svc.CreateRobotToken(ctx, w.sup, w.rAlice, portal.NewToken{Name: "n", Scope: "read"}); !errors.Is(err, portal.ErrForbidden) {
		t.Fatalf("a supervisor creates no tokens for others: %v", err)
	}
	if _, err := e.svc.GetRobot(ctx, w.sup, w.rAlice); !errors.Is(err, portal.ErrForbidden) {
		t.Fatalf("GET /robots/{id} is unchanged: %v", err)
	}
}

func TestAuditExposesTheSupervisorArea(t *testing.T) {
	t.Parallel()
	w := newSupWorld(t)
	e, ctx := w.e, w.e.ctx()
	if err := e.svc.RevokeToken(ctx, w.sup, w.tAlice); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RevokeToken(ctx, w.admin, w.tCarol); err != nil {
		t.Fatal(err)
	}
	p, err := e.svc.ListAudit(ctx, w.admin, portal.AuditQuery{})
	if err != nil || len(p.Items) < 2 {
		t.Fatalf("events = %+v, %v", p.Items, err)
	}
	byTarget := map[string]portal.AuditEvent{}
	for _, ev := range p.Items {
		byTarget[ev.TargetID] = ev
	}
	if ev := byTarget[w.tAlice]; ev.AreaID != w.areaA || ev.ActorID != w.sup {
		t.Errorf("supervisor event = %+v", ev)
	}
	if ev := byTarget[w.tCarol]; ev.AreaID != "" || ev.ActorID != w.admin {
		t.Errorf("admin event = %+v", ev)
	}
}

// A member of A who supervises another area, and a disabled platform admin member, are not supervisable: the rows
// count whatever the area of the supervision or the status (fail closed).
func TestNonSupervisableMembersStayOutOfReach(t *testing.T) {
	t.Parallel()
	w := newSupWorld(t)
	e, ctx := w.e, w.e.ctx()
	someone := e.w.user(t, "active")
	mustExec(t, e.owner, `INSERT INTO area_members (area_id, user_id) VALUES ($1, $2)`, w.areaA, someone)
	for name, c := range map[string]struct{ person, token, robot, robotToken string }{
		"member of A, supervisor of B":   {w.supBInA, w.tSupBInA, w.rSupBInA, w.trSupBInA},
		"disabled platform admin member": {w.admOff, w.tAdmOff, "", ""},
	} {
		if err := e.svc.RevokeToken(ctx, w.sup, c.token); !errors.Is(err, portal.ErrNotFound) || w.revoked(c.token) {
			t.Errorf("%s: revoke: %v", name, err)
		}
		if _, err := e.svc.ListUserTokens(ctx, w.sup, c.person, portal.PageRequest{}); !errors.Is(err, portal.ErrNotFound) {
			t.Errorf("%s: user tokens: %v", name, err)
		}
		if _, err := e.svc.ListUserRobots(ctx, w.sup, c.person, portal.PageRequest{}); !errors.Is(err, portal.ErrNotFound) {
			t.Errorf("%s: user robots: %v", name, err)
		}
		if _, err := e.svc.UpdateRobot(ctx, w.sup, w.rAlice, portal.RobotPatch{OwnerUserID: &c.person}); validationFields(t, err)[0].Code != portal.CodeUnknown {
			t.Errorf("%s: as new owner: %v", name, err)
		}
		if c.robot != "" {
			if err := e.svc.RevokeToken(ctx, w.sup, c.robotToken); !errors.Is(err, portal.ErrNotFound) || w.revoked(c.robotToken) {
				t.Errorf("%s: robot token revoke: %v", name, err)
			}
			if _, err := e.svc.ListRobotTokens(ctx, w.sup, c.robot, portal.PageRequest{}); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("%s: robot tokens: %v", name, err)
			}
			if _, err := e.svc.UpdateRobot(ctx, w.sup, c.robot, portal.RobotPatch{OwnerUserID: &someone}); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("%s: reassign its robot: %v", name, err)
			}
		}
	}
	p, err := e.svc.ListAreaMembers(ctx, w.sup, w.areaA, portal.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range p.Items {
		if m.UserID == w.supBInA || m.UserID == w.admOff {
			t.Errorf("non-supervisable member %s listed to a supervisor", m.UserID)
		}
	}
}
