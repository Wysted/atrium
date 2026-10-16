package postgres_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"platform/api/internal/portal"
)

func TestGroups(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	alpha := e.initiative("alpha")
	admin := e.admin()
	mgr := e.manager(alpha.id, alpha.manager)
	plain := e.w.user(t, "active")
	off := e.w.user(t, "disabled")
	e.pw.platformAdmin(off)

	t.Run("create audits a global event and validates the key", func(t *testing.T) {
		events := e.auditCount()
		g, err := e.svc.CreateGroup(ctx, admin, portal.NewGroup{Key: "ops-team", Name: "Ops"})
		if err != nil || g.ID == "" || g.Key != "ops-team" || g.Name != "Ops" {
			t.Fatalf("group = %+v, %v", g, err)
		}
		a := e.lastAudit()
		if e.auditCount() != events+1 || a.Actor != admin || a.Initiative != "" || a.Action != "group.create" ||
			a.TargetType != "group" || a.TargetID != g.ID || a.After["key"] != "ops-team" {
			t.Fatalf("audit = %+v", a)
		}
		if _, err := e.svc.CreateGroup(ctx, admin, portal.NewGroup{Key: "ops-team"}); !errors.Is(err, portal.ErrGroupKeyTaken) {
			t.Fatalf("duplicate key: %v", err)
		}
		for _, key := range []string{"", "Upper", "-lead", "trail-", "has space", "a/b", "x'; --", string(make([]byte, 64))} {
			_, err := e.svc.CreateGroup(ctx, admin, portal.NewGroup{Key: key})
			if got := validationFields(t, err); got[0].Field != "key" {
				t.Errorf("key %q: %v", key, got)
			}
		}
		if _, err := e.svc.CreateGroup(ctx, admin, portal.NewGroup{Key: "ok", Name: string(make([]byte, 201))}); validationFields(t, err)[0].Field != "name" {
			t.Errorf("long name accepted: %v", err)
		}
		if e.auditCount() != events+1 {
			t.Fatal("a refused group was audited")
		}
	})

	t.Run("only platform admins change groups; managers and users are forbidden", func(t *testing.T) {
		events := e.auditCount()
		group := e.w.group(t, "frozen")
		for name, caller := range map[string]string{"manager": mgr, "plain": plain} {
			if _, err := e.svc.CreateGroup(ctx, caller, portal.NewGroup{Key: "x-" + name}); !errors.Is(err, portal.ErrForbidden) {
				t.Errorf("%s create: %v", name, err)
			}
			if err := e.svc.DeleteGroup(ctx, caller, group); !errors.Is(err, portal.ErrForbidden) {
				t.Errorf("%s delete: %v", name, err)
			}
			if _, err := e.svc.AddGroupMember(ctx, caller, group, e.email(plain)); !errors.Is(err, portal.ErrForbidden) {
				t.Errorf("%s add member: %v", name, err)
			}
			if err := e.svc.RemoveGroupMember(ctx, caller, group, plain); !errors.Is(err, portal.ErrForbidden) {
				t.Errorf("%s remove member: %v", name, err)
			}
			if _, err := e.svc.ListGroupMembers(ctx, caller, group, portal.PageRequest{}); !errors.Is(err, portal.ErrForbidden) {
				t.Errorf("%s list members: %v", name, err)
			}
		}
		if _, err := e.svc.CreateGroup(ctx, off, portal.NewGroup{Key: "x-off"}); !errors.Is(err, portal.ErrUserInactive) {
			t.Errorf("disabled admin: %v", err)
		}
		if e.auditCount() != events {
			t.Fatal("a refused change was audited")
		}
	})

	t.Run("managers list group names but plain users do not", func(t *testing.T) {
		p, err := e.svc.ListGroups(ctx, mgr, portal.PageRequest{})
		if err != nil || len(p.Items) == 0 {
			t.Fatalf("manager: %+v, %v", p, err)
		}
		if _, err := e.svc.ListGroups(ctx, plain, portal.PageRequest{}); !errors.Is(err, portal.ErrForbidden) {
			t.Fatalf("plain: %v", err)
		}
	})

	t.Run("members: add by email, list, repeat, remove", func(t *testing.T) {
		g, _ := e.svc.CreateGroup(ctx, admin, portal.NewGroup{Key: "crew"})
		u := e.w.user(t, "active")
		events := e.auditCount()

		m, err := e.svc.AddGroupMember(ctx, admin, g.ID, "  "+toUpper(e.email(u)))
		if err != nil || m.UserID != u || m.Email != e.email(u) || m.AddedAt.IsZero() {
			t.Fatalf("member = %+v, %v", m, err)
		}
		a := e.lastAudit()
		if e.auditCount() != events+1 || a.Action != "group_member.add" || a.TargetType != "group_member" || a.TargetID != g.ID ||
			a.Initiative != "" || a.After["user_id"] != u || a.After["email"] != e.email(u) {
			t.Fatalf("audit = %+v", a)
		}
		if _, err := e.svc.AddGroupMember(ctx, admin, g.ID, e.email(u)); !errors.Is(err, portal.ErrMemberExists) {
			t.Fatalf("repeat: %v", err)
		}
		if e.auditCount() != events+1 || e.count(`SELECT count(*) FROM group_members WHERE group_id = $1`, g.ID) != 1 {
			t.Fatal("a repeated add left rows behind")
		}
		// The same exactly-one-active-user rule as grants.
		if _, err := e.svc.AddGroupMember(ctx, admin, g.ID, "nobody@example.com"); validationFields(t, err)[0].Code != portal.CodeNoUniqueActiveUser {
			t.Fatalf("unknown email: %v", err)
		}
		if _, err := e.svc.AddGroupMember(ctx, admin, g.ID, e.email(e.w.user(t, "disabled"))); validationFields(t, err)[0].Code != portal.CodeNoUniqueActiveUser {
			t.Fatalf("disabled user: %v", err)
		}
		if _, err := e.svc.AddGroupMember(ctx, admin, "00000000-0000-0000-0000-000000000000", e.email(u)); !errors.Is(err, portal.ErrNotFound) {
			t.Fatalf("unknown group: %v", err)
		}

		list, err := e.svc.ListGroupMembers(ctx, admin, g.ID, portal.PageRequest{})
		if err != nil || len(list.Items) != 1 || list.Items[0].UserID != u || list.Items[0].Status != "active" {
			t.Fatalf("members = %+v, %v", list, err)
		}

		events = e.auditCount()
		if err := e.svc.RemoveGroupMember(ctx, admin, g.ID, u); err != nil {
			t.Fatal(err)
		}
		a = e.lastAudit()
		if e.auditCount() != events+1 || a.Action != "group_member.remove" || a.Before["user_id"] != u || a.After != nil {
			t.Fatalf("audit = %+v", a)
		}
		if err := e.svc.RemoveGroupMember(ctx, admin, g.ID, u); !errors.Is(err, portal.ErrNotFound) {
			t.Fatalf("repeat remove: %v", err)
		}
		if e.auditCount() != events+1 {
			t.Fatal("the repeated removal was audited")
		}
	})

	t.Run("delete only without members and grants", func(t *testing.T) {
		withMember, _ := e.svc.CreateGroup(ctx, admin, portal.NewGroup{Key: "has-member"})
		withGrant, _ := e.svc.CreateGroup(ctx, admin, portal.NewGroup{Key: "has-grant"})
		empty, _ := e.svc.CreateGroup(ctx, admin, portal.NewGroup{Key: "is-empty"})
		if _, err := e.svc.AddGroupMember(ctx, admin, withMember.ID, e.email(plain)); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.CreateGrant(ctx, admin, alpha.id, portal.NewGrant{GroupID: withGrant.ID, RoleID: alpha.editor}); err != nil {
			t.Fatal(err)
		}
		events := e.auditCount()
		for name, id := range map[string]string{"members": withMember.ID, "grants": withGrant.ID} {
			if err := e.svc.DeleteGroup(ctx, admin, id); !errors.Is(err, portal.ErrGroupInUse) {
				t.Errorf("group with %s: %v", name, err)
			}
			if e.count(`SELECT count(*) FROM groups WHERE id = $1`, id) != 1 {
				t.Errorf("the group with %s was deleted", name)
			}
		}
		if e.count(`SELECT count(*) FROM group_members WHERE group_id = $1`, withMember.ID) != 1 ||
			e.count(`SELECT count(*) FROM initiative_access WHERE group_id = $1`, withGrant.ID) != 1 {
			t.Fatal("the cascade removed members or grants of a group in use")
		}
		if e.auditCount() != events {
			t.Fatal("refused deletions were audited")
		}
		if err := e.svc.DeleteGroup(ctx, admin, empty.ID); err != nil {
			t.Fatal(err)
		}
		a := e.lastAudit()
		if a.Action != "group.delete" || a.TargetID != empty.ID || a.Before["key"] != "is-empty" || a.After != nil {
			t.Fatalf("audit = %+v", a)
		}
		if err := e.svc.DeleteGroup(ctx, admin, empty.ID); !errors.Is(err, portal.ErrNotFound) {
			t.Fatalf("repeat: %v", err)
		}
	})
}

func TestGroupsAndMembersPaginate(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	admin := e.admin()
	var want []string
	for i := range 7 {
		g, err := e.svc.CreateGroup(ctx, admin, portal.NewGroup{Key: fmt.Sprintf("g%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, g.ID)
	}
	var got []string
	cursor := ""
	for {
		p, err := e.svc.ListGroups(ctx, admin, portal.PageRequest{Cursor: cursor, Limit: 3})
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range p.Items {
			got = append(got, g.ID)
		}
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	if !slices.Equal(got, want) {
		t.Fatalf("groups paged = %v, want %v", got, want)
	}

	group := want[0]
	var members []string
	for range 5 {
		u := e.w.user(t, "active")
		if _, err := e.svc.AddGroupMember(ctx, admin, group, e.email(u)); err != nil {
			t.Fatal(err)
		}
		members = append(members, u)
	}
	slices.Sort(members)
	var gotMembers []string
	cursor = ""
	for {
		p, err := e.svc.ListGroupMembers(ctx, admin, group, portal.PageRequest{Cursor: cursor, Limit: 2})
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
	if _, err := e.svc.ListGroupMembers(ctx, admin, "00000000-0000-0000-0000-000000000000", portal.PageRequest{}); !errors.Is(err, portal.ErrNotFound) {
		t.Fatalf("unknown group: %v", err)
	}
}

func TestUsers(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	alpha := e.initiative("alpha")
	admin, otherAdmin := e.admin(), e.admin()
	mgr := e.manager(alpha.id, alpha.manager)
	off := e.w.user(t, "disabled")
	e.pw.platformAdmin(off)

	addSessions := func(userID string, n int) {
		for range n {
			sid := scalar[string](t, e.owner, `INSERT INTO sessions (token_hash, user_id, expires_at)
				VALUES (sha256(gen_random_uuid()::text::bytea), $1, now() + interval '1 hour') RETURNING id::text`, userID)
			appID := e.w.app(t, alpha.id, fmt.Sprintf("h%d.example.test", fixtureSeq.Add(1)))
			mustExec(t, e.owner, `INSERT INTO app_sessions (token_hash, session_id, app_id, host, expires_at)
				VALUES (sha256(gen_random_uuid()::text::bytea), $1, $2, 'x.example.test', now() + interval '1 hour')`, sid, appID)
		}
	}

	t.Run("find users by email, case-insensitively, admin only", func(t *testing.T) {
		u := e.w.user(t, "active")
		users, err := e.svc.FindUsers(ctx, admin, toUpper(e.email(u)))
		if err != nil || len(users) != 1 || users[0].ID != u || users[0].Status != "active" {
			t.Fatalf("users = %+v, %v", users, err)
		}
		twin := e.w.user(t, "disabled")
		mustExec(t, e.owner, `UPDATE users SET email = $2 WHERE id = $1`, twin, e.email(u))
		users, err = e.svc.FindUsers(ctx, admin, e.email(u))
		if err != nil || len(users) != 2 {
			t.Fatalf("a shared email returns every user: %+v, %v", users, err)
		}
		if users, err := e.svc.FindUsers(ctx, admin, "nobody@example.com"); err != nil || len(users) != 0 {
			t.Fatalf("unknown email: %+v, %v", users, err)
		}
		for name, caller := range map[string]string{"manager": mgr, "plain": e.w.user(t, "active")} {
			if _, err := e.svc.FindUsers(ctx, caller, e.email(u)); !errors.Is(err, portal.ErrForbidden) {
				t.Errorf("%s: %v", name, err)
			}
		}
		if _, err := e.svc.FindUsers(ctx, off, e.email(u)); !errors.Is(err, portal.ErrUserInactive) {
			t.Errorf("disabled admin: %v", err)
		}
		for _, bad := range []string{"", "  ", "no-at"} {
			if _, err := e.svc.FindUsers(ctx, admin, bad); validationFields(t, err)[0].Field != "email" {
				t.Errorf("%q accepted", bad)
			}
		}
	})

	t.Run("disabling deletes the sessions in the same transaction and audits once", func(t *testing.T) {
		u := e.w.user(t, "active")
		addSessions(u, 2)
		other := e.w.user(t, "active")
		addSessions(other, 1)
		events := e.auditCount()

		got, err := e.svc.SetUserStatus(ctx, admin, u, "disabled")
		if err != nil || got.ID != u || got.Status != "disabled" {
			t.Fatalf("user = %+v, %v", got, err)
		}
		if e.count(`SELECT count(*) FROM sessions WHERE user_id = $1`, u) != 0 ||
			e.count(`SELECT count(*) FROM app_sessions a JOIN sessions s ON s.id = a.session_id WHERE s.user_id = $1`, u) != 0 {
			t.Fatal("sessions survived the disable")
		}
		if e.count(`SELECT count(*) FROM sessions WHERE user_id = $1`, other) != 1 {
			t.Fatal("another user's session was deleted")
		}
		if scalar[string](t, e.owner, `SELECT status FROM users WHERE id = $1`, u) != "disabled" {
			t.Fatal("status not stored")
		}
		a := e.lastAudit()
		if e.auditCount() != events+1 || a.Actor != admin || a.Initiative != "" || a.Action != "user.disable" || a.TargetType != "user" ||
			a.TargetID != u || a.Before["status"] != "active" || a.After["status"] != "disabled" || a.After["sessions_revoked"] != float64(2) {
			t.Fatalf("audit = %+v", a)
		}

		// Setting the status the user already has changes nothing and is not audited.
		again, err := e.svc.SetUserStatus(ctx, admin, u, "disabled")
		if err != nil || again.Status != "disabled" || e.auditCount() != events+1 {
			t.Fatalf("repeat = %+v, %v (audit rows %d)", again, err, e.auditCount())
		}

		// Enabling.
		got, err = e.svc.SetUserStatus(ctx, admin, u, "active")
		if err != nil || got.Status != "active" {
			t.Fatalf("enable = %+v, %v", got, err)
		}
		if a := e.lastAudit(); a.Action != "user.enable" || a.Before["status"] != "disabled" || a.After["status"] != "active" {
			t.Fatalf("audit = %+v", a)
		}
	})

	t.Run("an administrator cannot disable themselves, but can disable another administrator", func(t *testing.T) {
		events := e.auditCount()
		if _, err := e.svc.SetUserStatus(ctx, admin, admin, "disabled"); !errors.Is(err, portal.ErrSelfDisable) {
			t.Fatalf("self disable: %v", err)
		}
		if scalar[string](t, e.owner, `SELECT status FROM users WHERE id = $1`, admin) != "active" || e.auditCount() != events {
			t.Fatal("the refused self-disable changed something")
		}
		if _, err := e.svc.SetUserStatus(ctx, admin, otherAdmin, "disabled"); err != nil {
			t.Fatalf("disabling another admin: %v", err)
		}
	})

	t.Run("who may change users and what is validated", func(t *testing.T) {
		u := e.w.user(t, "active")
		events := e.auditCount()
		if _, err := e.svc.SetUserStatus(ctx, mgr, u, "disabled"); !errors.Is(err, portal.ErrForbidden) {
			t.Errorf("manager: %v", err)
		}
		if _, err := e.svc.SetUserStatus(ctx, off, u, "disabled"); !errors.Is(err, portal.ErrUserInactive) {
			t.Errorf("disabled admin: %v", err)
		}
		if _, err := e.svc.SetUserStatus(ctx, admin, "00000000-0000-0000-0000-000000000000", "disabled"); !errors.Is(err, portal.ErrNotFound) {
			t.Errorf("unknown user: %v", err)
		}
		if _, err := e.svc.SetUserStatus(ctx, admin, "nope", "disabled"); !errors.Is(err, portal.ErrNotFound) {
			t.Errorf("malformed id: %v", err)
		}
		for _, status := range []string{"", "banned", "ACTIVE"} {
			if _, err := e.svc.SetUserStatus(ctx, admin, u, status); validationFields(t, err)[0].Field != "status" {
				t.Errorf("status %q accepted", status)
			}
		}
		if _, err := e.svc.SetUserStatus(ctx, mgr, u, "banned"); !errors.Is(err, portal.ErrForbidden) {
			t.Errorf("an invalid status must not get past authorization: %v", err)
		}
		if e.auditCount() != events {
			t.Fatal("refused changes were audited")
		}
	})

	t.Run("revoking sessions", func(t *testing.T) {
		u := e.w.user(t, "active")
		addSessions(u, 3)
		events := e.auditCount()
		n, err := e.svc.RevokeUserSessions(ctx, admin, u)
		if err != nil || n != 3 {
			t.Fatalf("revoked %d, %v", n, err)
		}
		if e.count(`SELECT count(*) FROM sessions WHERE user_id = $1`, u) != 0 {
			t.Fatal("sessions survived")
		}
		a := e.lastAudit()
		if e.auditCount() != events+1 || a.Action != "user_sessions.revoke" || a.TargetType != "user_sessions" || a.TargetID != u ||
			a.After["revoked"] != float64(3) || a.Initiative != "" {
			t.Fatalf("audit = %+v", a)
		}
		if scalar[string](t, e.owner, `SELECT status FROM users WHERE id = $1`, u) != "active" {
			t.Fatal("revoking sessions must not disable the user")
		}
		// An administrative action is audited even when there was nothing to revoke.
		if n, err := e.svc.RevokeUserSessions(ctx, admin, u); err != nil || n != 0 || e.auditCount() != events+2 {
			t.Fatalf("repeat: %d, %v, audit rows %d", n, err, e.auditCount())
		}
		if _, err := e.svc.RevokeUserSessions(ctx, mgr, u); !errors.Is(err, portal.ErrForbidden) {
			t.Errorf("manager: %v", err)
		}
		if _, err := e.svc.RevokeUserSessions(ctx, admin, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, portal.ErrNotFound) {
			t.Errorf("unknown user: %v", err)
		}
	})
}

func TestListAudit(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	alpha, beta := e.initiative("alpha"), e.initiative("beta")
	admin := e.admin()
	mgrA := e.manager(alpha.id, alpha.manager)
	mgrAB := e.manager(alpha.id, alpha.manager)
	e.w.grantUser(t, beta.id, beta.manager, mgrAB, "")
	plain := e.w.user(t, "active")
	off := e.w.user(t, "disabled")
	e.pw.platformAdmin(off)

	// 30 events with only 3 distinct instants, so the order within an instant depends on the id tie-break.
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	initiatives := []string{alpha.id, beta.id, ""}
	for i := range 30 {
		mustExec(t, e.owner, `INSERT INTO audit_events (occurred_at, actor_id, initiative_id, action, target_type, target_id, after)
			VALUES ($1, $2, NULLIF($3, '')::uuid, 'test.event', 'thing', $4, '{"secret":"hidden"}')`,
			base.Add(time.Duration(i/10)*time.Second), admin, initiatives[i%3], fmt.Sprint(i))
	}

	pageAll := func(caller, initiative string, limit int) ([]portal.AuditEvent, int) {
		t.Helper()
		var all []portal.AuditEvent
		cursor, pages := "", 0
		for {
			p, err := e.svc.ListAudit(ctx, caller, portal.AuditQuery{InitiativeID: initiative, Page: portal.PageRequest{Cursor: cursor, Limit: limit}})
			if err != nil {
				t.Fatal(err)
			}
			pages++
			all = append(all, p.Items...)
			if p.NextCursor == "" {
				return all, pages
			}
			cursor = p.NextCursor
		}
	}
	inOrder := func(evs []portal.AuditEvent) bool {
		return slices.IsSortedFunc(evs, func(a, b portal.AuditEvent) int {
			if c := b.OccurredAt.Compare(a.OccurredAt); c != 0 {
				return c
			}
			return -compareStrings(a.ID, b.ID)
		})
	}

	t.Run("an admin pages through everything, newest first, with no duplicates or skips", func(t *testing.T) {
		all, pages := pageAll(admin, "", 7)
		if len(all) != 30 || pages != 5 || !inOrder(all) {
			t.Fatalf("%d events in %d pages, ordered=%v", len(all), pages, inOrder(all))
		}
		ids := map[string]bool{}
		for _, ev := range all {
			ids[ev.ID] = true
		}
		if len(ids) != 30 {
			t.Fatalf("%d distinct events, want 30", len(ids))
		}
		// The same listing in one page gives the same order, so page boundaries do not matter.
		whole, pages := pageAll(admin, "", portal.MaxPageSize)
		if pages != 1 || len(whole) != 30 {
			t.Fatalf("one big page: %d events in %d pages", len(whole), pages)
		}
		for i := range whole {
			if whole[i].ID != all[i].ID {
				t.Fatalf("order differs at %d", i)
			}
		}
		if all[0].Before != nil || string(all[0].After) != `{"secret": "hidden"}` || all[0].ActorEmail != e.email(admin) {
			t.Fatalf("event = %+v", all[0])
		}
	})

	t.Run("a manager sees only the events of their initiatives", func(t *testing.T) {
		got, _ := pageAll(mgrA, "", 4)
		if len(got) != 10 {
			t.Fatalf("%d events, want the 10 of alpha", len(got))
		}
		for _, ev := range got {
			if ev.InitiativeID != alpha.id {
				t.Fatalf("event of %q leaked to a manager of alpha", ev.InitiativeID)
			}
		}
		both, _ := pageAll(mgrAB, "", 100)
		if len(both) != 20 {
			t.Fatalf("manager of two sees %d events, want 20", len(both))
		}
		filtered, _ := pageAll(mgrAB, beta.id, 3)
		if len(filtered) != 10 || filtered[0].InitiativeID != beta.id {
			t.Fatalf("filtered = %d events", len(filtered))
		}
		adminFiltered, _ := pageAll(admin, alpha.id, 100)
		if len(adminFiltered) != 10 {
			t.Fatalf("admin filtered = %d events", len(adminFiltered))
		}
	})

	t.Run("who may read the log", func(t *testing.T) {
		if _, err := e.svc.ListAudit(ctx, mgrA, portal.AuditQuery{InitiativeID: beta.id}); !errors.Is(err, portal.ErrNotFound) {
			t.Errorf("foreign initiative: %v", err)
		}
		if _, err := e.svc.ListAudit(ctx, plain, portal.AuditQuery{}); !errors.Is(err, portal.ErrForbidden) {
			t.Errorf("plain user: %v", err)
		}
		if _, err := e.svc.ListAudit(ctx, off, portal.AuditQuery{}); !errors.Is(err, portal.ErrUserInactive) {
			t.Errorf("disabled admin: %v", err)
		}
		if _, err := e.svc.ListAudit(ctx, admin, portal.AuditQuery{InitiativeID: "nope"}); validationFields(t, err)[0].Field != "initiative_id" {
			t.Errorf("malformed filter accepted")
		}
	})

	t.Run("cursors", func(t *testing.T) {
		first, err := e.svc.ListAudit(ctx, admin, portal.AuditQuery{Page: portal.PageRequest{Limit: 2}})
		if err != nil || first.NextCursor == "" {
			t.Fatalf("first page = %+v, %v", first, err)
		}
		for _, bad := range []string{"tampered", first.NextCursor + "x", first.NextCursor[:len(first.NextCursor)-3], "e30"} {
			if _, err := e.svc.ListAudit(ctx, admin, portal.AuditQuery{Page: portal.PageRequest{Cursor: bad}}); !errors.Is(err, portal.ErrInvalidCursor) {
				t.Errorf("cursor %q: %v", bad, err)
			}
		}
		// A cursor does not widen the scope: a manager using the admin's cursor still sees only their rows.
		got, err := e.svc.ListAudit(ctx, mgrA, portal.AuditQuery{Page: portal.PageRequest{Cursor: first.NextCursor, Limit: 100}})
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range got.Items {
			if ev.InitiativeID != alpha.id {
				t.Fatalf("event of %q leaked through a foreign cursor", ev.InitiativeID)
			}
		}
		// The limit is capped.
		big, err := e.svc.ListAudit(ctx, admin, portal.AuditQuery{Page: portal.PageRequest{Limit: 1_000_000}})
		if err != nil || len(big.Items) > portal.MaxPageSize {
			t.Fatalf("%d events for a huge limit, %v", len(big.Items), err)
		}
	})
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
