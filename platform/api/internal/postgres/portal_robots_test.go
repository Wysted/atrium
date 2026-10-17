package postgres_test

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"platform/api/internal/apitoken"
	"platform/api/internal/portal"
)

func ptr[T any](v T) *T { return &v }

// robotEnv is an admin environment with the lifetime caps of the robot tests (personal 48h, robot 72h).
func newRobotEnv(t *testing.T) *adminEnv {
	t.Helper()
	return newAdminEnvCfg(t, func(s portal.Store) portal.Store { return s }, func(c *portal.Config) {
		c.TokenMaxTTLPersonal, c.TokenMaxTTLRobot = 48*time.Hour, 72*time.Hour
	})
}

func (e *adminEnv) robotStatus(id string) string {
	e.t.Helper()
	return scalar[string](e.t, e.owner, `SELECT status FROM users WHERE id = $1`, id)
}

func TestCreateRobot(t *testing.T) {
	t.Parallel()
	e := newRobotEnv(t)
	ctx := e.ctx()
	admin := e.admin()
	alpha := e.initiative("alpha")
	mgr := e.manager(alpha.id, alpha.manager)
	human := e.w.user(t, "active")
	managerBot := e.w.robot(t, "manager-bot") // holds manages_access, still not an administrator
	e.w.grantUser(t, alpha.id, alpha.manager, managerBot, "")

	t.Run("a platform admin creates a robot without email or google_sub; one audit row records it", func(t *testing.T) {
		before := e.auditCount()
		r, err := e.svc.CreateRobot(ctx, admin, portal.NewRobot{Name: "  ci-bot ", OwnerUserID: strings.ToUpper(human)})
		if err != nil {
			t.Fatal(err)
		}
		if r.ID == "" || r.Name != "ci-bot" || r.Status != "active" || r.OwnerUserID != human || !r.Detail || r.CreatedAt.IsZero() {
			t.Fatalf("robot = %+v", r)
		}
		var kind, email string
		var sub *string
		if err := e.owner.QueryRow(ctx, `SELECT kind, email, google_sub FROM users WHERE id = $1`, r.ID).Scan(&kind, &email, &sub); err != nil {
			t.Fatal(err)
		}
		if kind != "robot" || email != "" || sub != nil {
			t.Fatalf("stored kind %q, email %q, google_sub %v", kind, email, sub)
		}
		if got := e.auditCount(); got != before+1 {
			t.Fatalf("audit rows %d -> %d", before, got)
		}
		a := e.lastAudit()
		if a.Actor != admin || a.Action != "robot.create" || a.TargetType != "robot" || a.TargetID != r.ID || a.Initiative != "" ||
			a.Before != nil || a.After["name"] != "ci-bot" || a.After["status"] != "active" || a.After["owner_user_id"] != human {
			t.Fatalf("audit = %+v", a)
		}
	})

	t.Run("the name is unique among robots, case-insensitively; humans do not matter", func(t *testing.T) {
		if _, err := e.svc.CreateRobot(ctx, admin, portal.NewRobot{Name: "Dup"}); err != nil {
			t.Fatal(err)
		}
		before := e.auditCount()
		for _, name := range []string{"Dup", "dup", " DUP "} {
			if _, err := e.svc.CreateRobot(ctx, admin, portal.NewRobot{Name: name}); !errors.Is(err, portal.ErrRobotNameTaken) {
				t.Errorf("%q: err = %v, want ErrRobotNameTaken", name, err)
			}
		}
		if e.auditCount() != before {
			t.Fatal("a refused creation wrote an audit row")
		}
		// A human with the same display name does not collide.
		mustExec(t, e.owner, `UPDATE users SET name = 'Shared' WHERE id = $1`, human)
		if _, err := e.svc.CreateRobot(ctx, admin, portal.NewRobot{Name: "shared"}); err != nil {
			t.Fatalf("a robot named like a human: %v", err)
		}
	})

	t.Run("name and owner are validated; the owner must be an active human", func(t *testing.T) {
		robot := e.w.robot(t, "an-owner-that-is-a-robot")
		disabled := e.w.user(t, "disabled")
		tests := []struct {
			name string
			in   portal.NewRobot
			want []portal.FieldProblem
		}{
			{"no name", portal.NewRobot{}, []portal.FieldProblem{{Field: "name", Code: portal.CodeRequired}}},
			{"blank name", portal.NewRobot{Name: "   "}, []portal.FieldProblem{{Field: "name", Code: portal.CodeRequired}}},
			{"name of 65 characters", portal.NewRobot{Name: strings.Repeat("a", 65)}, []portal.FieldProblem{{Field: "name", Code: portal.CodeInvalid}}},
			{"name with NUL", portal.NewRobot{Name: "a\x00b"}, []portal.FieldProblem{{Field: "name", Code: portal.CodeInvalid}}},
			{"owner is not a uuid", portal.NewRobot{Name: "x1", OwnerUserID: "nope"}, []portal.FieldProblem{{Field: "owner_user_id", Code: portal.CodeInvalid}}},
			{"owner does not exist", portal.NewRobot{Name: "x2", OwnerUserID: "01a14296-1449-7be0-957b-4b1ad8d7a998"}, []portal.FieldProblem{{Field: "owner_user_id", Code: portal.CodeUnknown}}},
			{"owner is a robot", portal.NewRobot{Name: "x3", OwnerUserID: robot}, []portal.FieldProblem{{Field: "owner_user_id", Code: portal.CodeUnknown}}},
			{"owner is disabled", portal.NewRobot{Name: "x4", OwnerUserID: disabled}, []portal.FieldProblem{{Field: "owner_user_id", Code: portal.CodeUnknown}}},
		}
		before := e.count(`SELECT count(*) FROM users WHERE kind = 'robot'`)
		for _, tt := range tests {
			_, err := e.svc.CreateRobot(ctx, admin, tt.in)
			if got := validationFields(t, err); !slices.Equal(got, tt.want) {
				t.Errorf("%s: problems = %v, want %v", tt.name, got, tt.want)
			}
		}
		if e.count(`SELECT count(*) FROM users WHERE kind = 'robot'`) != before {
			t.Fatal("a rejected request stored a robot")
		}
		if _, err := e.svc.CreateRobot(ctx, admin, portal.NewRobot{Name: strings.Repeat("é", 64)}); err != nil {
			t.Fatalf("64 multibyte characters must fit: %v", err)
		}
	})

	t.Run("only platform admins create robots", func(t *testing.T) {
		for name, caller := range map[string]string{"manager": mgr, "robot manager": managerBot, "plain user": human, "disabled user": e.w.user(t, "disabled")} {
			_, err := e.svc.CreateRobot(ctx, caller, portal.NewRobot{Name: "nope-" + name})
			if !errors.Is(err, portal.ErrForbidden) && !errors.Is(err, portal.ErrUserInactive) {
				t.Errorf("%s: err = %v", name, err)
			}
		}
		if n := e.count(`SELECT count(*) FROM users WHERE name LIKE 'nope-%'`); n != 0 {
			t.Fatalf("%d robots stored by non-admins", n)
		}
	})
}

func TestListRobots(t *testing.T) {
	t.Parallel()
	e := newRobotEnv(t)
	ctx := e.ctx()
	admin := e.admin()
	alpha := e.initiative("alpha")
	mgr := e.manager(alpha.id, alpha.manager)
	plain := e.w.user(t, "active")
	owner := e.w.user(t, "active")
	a := e.w.robot(t, "a-bot")
	b := e.w.robot(t, "b-bot")
	c := e.w.robot(t, "c-bot")
	mustExec(t, e.owner, `UPDATE users SET owner_user_id = $2, status = 'disabled' WHERE id = $1`, b, owner)
	tok := e.w.apiToken(t, a, "-1 hour", "1 hour", "")
	mustExec(t, e.owner, `UPDATE api_tokens SET last_used_at = '2030-01-01T00:00:00Z' WHERE id = $1`, tok)

	t.Run("admins see every field; humans are not listed", func(t *testing.T) {
		p, err := e.svc.ListRobots(ctx, admin, portal.PageRequest{})
		if err != nil || len(p.Items) != 3 || p.NextCursor != "" {
			t.Fatalf("page = %+v, %v", p, err)
		}
		byID := map[string]portal.Robot{}
		for _, r := range p.Items {
			byID[r.ID] = r
		}
		if r := byID[a]; r.OwnerEmail != "" || r.OwnerName != "" {
			t.Errorf("a has no owner but shows %q %q", r.OwnerEmail, r.OwnerName)
		}
		if r := byID[a]; !r.Detail || r.Name != "a-bot" || r.Status != "active" || r.LastUsedAt == nil || r.LastUsedAt.Year() != 2030 {
			t.Errorf("a = %+v", r)
		}
		if r := byID[b]; r.OwnerUserID != owner || r.OwnerEmail != e.email(owner) || r.OwnerName != "n" || r.Status != "disabled" || r.LastUsedAt != nil {
			t.Errorf("b = %+v", r)
		}
		if _, ok := byID[c]; !ok {
			t.Errorf("c missing")
		}
	})

	t.Run("a manager gets id, name and status only", func(t *testing.T) {
		p, err := e.svc.ListRobots(ctx, mgr, portal.PageRequest{})
		if err != nil || len(p.Items) != 3 {
			t.Fatalf("page = %+v, %v", p, err)
		}
		for _, r := range p.Items {
			if r.Detail || r.ID == "" || r.Name == "" || r.Status == "" || r.OwnerUserID != "" || !r.CreatedAt.IsZero() || r.LastUsedAt != nil {
				t.Errorf("manager sees %+v", r)
			}
		}
	})

	t.Run("a user without managed initiatives is refused", func(t *testing.T) {
		if _, err := e.svc.ListRobots(ctx, plain, portal.PageRequest{}); !errors.Is(err, portal.ErrForbidden) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("keyset pagination", func(t *testing.T) {
		var seen []string
		req := portal.PageRequest{Limit: 2}
		for pages := 0; ; pages++ {
			p, err := e.svc.ListRobots(ctx, admin, req)
			if err != nil || pages > 3 {
				t.Fatalf("page %d: %v", pages, err)
			}
			for _, r := range p.Items {
				seen = append(seen, r.ID)
			}
			if p.NextCursor == "" {
				break
			}
			req.Cursor = p.NextCursor
		}
		want := []string{a, b, c}
		slices.Sort(want)
		if !slices.Equal(seen, want) {
			t.Fatalf("seen %v, want %v", seen, want)
		}
		if _, err := e.svc.ListRobots(ctx, admin, portal.PageRequest{Cursor: "garbage"}); !errors.Is(err, portal.ErrInvalidCursor) {
			t.Fatalf("garbage cursor: %v", err)
		}
	})
}

func TestGetRobot(t *testing.T) {
	t.Parallel()
	e := newRobotEnv(t)
	ctx := e.ctx()
	admin := e.admin()
	alpha := e.initiative("alpha")
	mgr := e.manager(alpha.id, alpha.manager)
	plain := e.w.user(t, "active")
	owner := e.w.user(t, "active")
	bot := e.w.robot(t, "bot")
	mustExec(t, e.owner, `UPDATE users SET owner_user_id = $2 WHERE id = $1`, bot, owner)
	tok := e.w.apiToken(t, bot, "-1 hour", "1 hour", "")
	mustExec(t, e.owner, `UPDATE api_tokens SET last_used_at = '2030-01-01T00:00:00Z' WHERE id = $1`, tok)

	t.Run("admins see every field with the owner", func(t *testing.T) {
		r, err := e.svc.GetRobot(ctx, admin, bot)
		if err != nil || !r.Detail || r.Name != "bot" || r.OwnerUserID != owner || r.OwnerEmail != e.email(owner) || r.OwnerName != "n" ||
			r.LastUsedAt == nil || r.LastUsedAt.Year() != 2030 || r.CreatedAt.IsZero() {
			t.Fatalf("robot = %+v, %v", r, err)
		}
	})
	t.Run("a manager gets id, name and status only", func(t *testing.T) {
		r, err := e.svc.GetRobot(ctx, mgr, bot)
		if err != nil || r != (portal.Robot{ID: bot, Name: "bot", Status: "active"}) {
			t.Fatalf("robot = %+v, %v", r, err)
		}
	})
	t.Run("others are refused", func(t *testing.T) {
		if _, err := e.svc.GetRobot(ctx, plain, bot); !errors.Is(err, portal.ErrForbidden) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a human, an unknown and a malformed id are not found", func(t *testing.T) {
		for _, id := range []string{owner, "01a14296-1449-7be0-957b-4b1ad8d7a0bb", "nope"} {
			if _, err := e.svc.GetRobot(ctx, admin, id); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("%s: err = %v", id, err)
			}
		}
	})
}

func TestListAuditTokenID(t *testing.T) {
	t.Parallel()
	e := newRobotEnv(t)
	admin := e.admin()
	bot := e.w.robot(t, "bot")
	tok := e.w.apiToken(t, bot, "-1 hour", "1 hour", "")
	mustExec(t, e.owner, `INSERT INTO audit_events (actor_id, action, target_type, target_id, token_id, occurred_at)
		VALUES ($1, 'with.token', 't', '1', $2, now() + interval '1 minute'), ($1, 'no.token', 't', '2', NULL, now())`, bot, tok)
	p, err := e.svc.ListAudit(e.ctx(), admin, portal.AuditQuery{})
	if err != nil || len(p.Items) < 2 || p.Items[0].Action != "with.token" || p.Items[0].TokenID != tok || p.Items[1].Action != "no.token" || p.Items[1].TokenID != "" {
		t.Fatalf("events = %+v, %v", p.Items, err)
	}
}

func TestUpdateRobot(t *testing.T) {
	t.Parallel()
	e := newRobotEnv(t)
	ctx := e.ctx()
	admin := e.admin()
	alpha := e.initiative("alpha")
	mgr := e.manager(alpha.id, alpha.manager)
	human := e.w.user(t, "active")
	r := e.w.robot(t, "ci")
	e.w.robot(t, "taken")
	managerBot := e.w.robot(t, "manager-bot")
	e.w.grantUser(t, alpha.id, alpha.manager, managerBot, "")

	t.Run("rename, owner change and clear are audited with before and after", func(t *testing.T) {
		got, err := e.svc.UpdateRobot(ctx, admin, r, portal.RobotPatch{Name: ptr(" Renamed "), OwnerUserID: ptr(human)})
		if err != nil || got.Name != "Renamed" || got.OwnerUserID != human || got.OwnerEmail != e.email(human) || got.OwnerName != "n" ||
			got.Status != "active" || !got.Detail {
			t.Fatalf("robot = %+v, %v", got, err)
		}
		a := e.lastAudit()
		if a.Actor != admin || a.Action != "robot.update" || a.TargetType != "robot" || a.TargetID != r ||
			a.Before["name"] != "ci" || a.Before["owner_user_id"] != nil || a.After["name"] != "Renamed" || a.After["owner_user_id"] != human ||
			a.After["tokens_revoked"] != nil {
			t.Fatalf("audit = %+v", a)
		}
		other := e.w.user(t, "active")
		if got, err = e.svc.UpdateRobot(ctx, admin, r, portal.RobotPatch{OwnerUserID: ptr(other)}); err != nil || got.OwnerUserID != other ||
			got.OwnerEmail != e.email(other) || got.OwnerName != "n" {
			t.Fatalf("change owner: %+v, %v", got, err)
		}
		if got, err = e.svc.UpdateRobot(ctx, admin, r, portal.RobotPatch{OwnerUserID: ptr("")}); err != nil || got.OwnerUserID != "" ||
			got.OwnerEmail != "" || got.OwnerName != "" {
			t.Fatalf("clear owner: %+v, %v", got, err)
		}
		if scalar[*string](t, e.owner, `SELECT owner_user_id::text FROM users WHERE id = $1`, r) != nil {
			t.Fatal("the owner was not cleared")
		}
	})

	t.Run("a patch that changes nothing writes no audit row", func(t *testing.T) {
		before := e.auditCount()
		for _, p := range []portal.RobotPatch{{Status: ptr("active")}, {OwnerUserID: ptr("")}} {
			if got, err := e.svc.UpdateRobot(ctx, admin, r, p); err != nil || got.ID != r || !got.Detail {
				t.Fatalf("no-op %+v: %+v, %v", p, got, err)
			}
		}
		if e.auditCount() != before {
			t.Fatal("a no-op wrote an audit row")
		}
	})

	t.Run("names stay unique, but a robot may change the case of its own", func(t *testing.T) {
		if _, err := e.svc.UpdateRobot(ctx, admin, r, portal.RobotPatch{Name: ptr("TAKEN")}); !errors.Is(err, portal.ErrRobotNameTaken) {
			t.Fatalf("err = %v", err)
		}
		if got, err := e.svc.UpdateRobot(ctx, admin, r, portal.RobotPatch{Name: ptr("RENAMED")}); err != nil || got.Name != "RENAMED" {
			t.Fatalf("own case change: %+v, %v", got, err)
		}
	})

	t.Run("the owner must be an active human, unless it does not change", func(t *testing.T) {
		robot := e.w.robot(t, "not-a-human")
		disabled := e.w.user(t, "disabled")
		for name, id := range map[string]string{"robot": robot, "disabled": disabled, "unknown": "01a14296-1449-7be0-957b-4b1ad8d7a998"} {
			_, err := e.svc.UpdateRobot(ctx, admin, r, portal.RobotPatch{OwnerUserID: ptr(id)})
			if p := validationFields(t, err); len(p) != 1 || p[0].Field != "owner_user_id" || p[0].Code != portal.CodeUnknown {
				t.Errorf("%s owner: problems = %v", name, p)
			}
		}
		// The owner leaves (disabled): renaming the robot is still possible, the owner column is untouched.
		leaver := e.w.user(t, "active")
		if _, err := e.svc.UpdateRobot(ctx, admin, r, portal.RobotPatch{OwnerUserID: ptr(leaver)}); err != nil {
			t.Fatal(err)
		}
		mustExec(t, e.owner, `UPDATE users SET status = 'disabled' WHERE id = $1`, leaver)
		if got, err := e.svc.UpdateRobot(ctx, admin, r, portal.RobotPatch{Name: ptr("after-leaver"), OwnerUserID: ptr(leaver)}); err != nil || got.OwnerUserID != leaver {
			t.Fatalf("rename with a disabled owner: %+v, %v", got, err)
		}
	})

	t.Run("validation", func(t *testing.T) {
		tests := []struct {
			name string
			in   portal.RobotPatch
			want []portal.FieldProblem
		}{
			{"empty patch", portal.RobotPatch{}, []portal.FieldProblem{{Field: "name", Code: portal.CodeAtLeastOne}, {Field: "status", Code: portal.CodeAtLeastOne}, {Field: "owner_user_id", Code: portal.CodeAtLeastOne}}},
			{"blank name", portal.RobotPatch{Name: ptr(" ")}, []portal.FieldProblem{{Field: "name", Code: portal.CodeRequired}}},
			{"unknown status", portal.RobotPatch{Status: ptr("deleted")}, []portal.FieldProblem{{Field: "status", Code: portal.CodeInvalid}}},
		}
		for _, tt := range tests {
			_, err := e.svc.UpdateRobot(ctx, admin, r, tt.in)
			if got := validationFields(t, err); !slices.Equal(got, tt.want) {
				t.Errorf("%s: problems = %v, want %v", tt.name, got, tt.want)
			}
		}
	})

	t.Run("only platform admins; a human id or a malformed id is as absent as an unknown one", func(t *testing.T) {
		for name, caller := range map[string]string{"manager": mgr, "robot manager": managerBot} {
			if _, err := e.svc.UpdateRobot(ctx, caller, r, portal.RobotPatch{Status: ptr("disabled")}); !errors.Is(err, portal.ErrForbidden) {
				t.Fatalf("%s: %v", name, err)
			}
		}
		if e.robotStatus(r) != "active" {
			t.Fatal("a manager disabled a robot")
		}
		events := e.auditCount()
		for name, id := range map[string]string{"human": human, "unknown": "01a14296-1449-7be0-957b-4b1ad8d7a998", "malformed": "nope"} {
			if _, err := e.svc.UpdateRobot(ctx, admin, id, portal.RobotPatch{Status: ptr("disabled")}); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("%s: err = %v", name, err)
			}
		}
		if e.robotStatus(human) != "active" || e.auditCount() != events {
			t.Fatal("a refused request wrote something")
		}
	})
}

// Disabling a robot revokes its live tokens in the same transaction and lists their ids in the audit row, like
// disabling a human; nothing else is touched and re-enabling brings nothing back.
func TestDisablingARobotRevokesItsTokens(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	admin := e.admin()
	r, other := e.w.robot(t, "ci"), e.w.robot(t, "other")
	live1 := e.w.apiToken(t, r, "-1 hour", "1 hour", "")
	live2 := e.w.apiToken(t, r, "-1 hour", "10 days", "")
	expired := e.w.apiToken(t, r, "-2 days", "-1 day", "")
	othersLive := e.w.apiToken(t, other, "-1 hour", "1 hour", "")
	revokedAt := func(id string) *string {
		return scalar[*string](t, e.owner, `SELECT revoked_at::text FROM api_tokens WHERE id = $1`, id)
	}

	// The audit insert fails: the revocation and the status roll back together.
	failing := newAdminEnvWith(t, func(s portal.Store) portal.Store { return failingAuditStore{Store: s} })
	fa, fr := failing.admin(), failing.w.robot(t, "ci")
	ft := failing.w.apiToken(t, fr, "-1 hour", "1 hour", "")
	if _, err := failing.svc.UpdateRobot(failing.ctx(), fa, fr, portal.RobotPatch{Status: ptr("disabled")}); !errors.Is(err, errAuditDown) {
		t.Fatalf("err = %v, want the audit failure", err)
	}
	if scalar[*string](t, failing.owner, `SELECT revoked_at::text FROM api_tokens WHERE id = $1`, ft) != nil || failing.robotStatus(fr) != "active" {
		t.Fatal("the disable survived a failed audit")
	}

	if _, err := e.svc.UpdateRobot(ctx, admin, r, portal.RobotPatch{Status: ptr("disabled")}); err != nil {
		t.Fatal(err)
	}
	if revokedAt(live1) == nil || revokedAt(live2) == nil {
		t.Fatal("a live token survived the disable")
	}
	if revokedAt(expired) != nil || revokedAt(othersLive) != nil {
		t.Fatal("the disable touched a dead or foreign token")
	}
	want := []string{live1, live2}
	slices.Sort(want)
	var got []string
	a := e.lastAudit()
	if ids, ok := a.After["tokens_revoked"].([]any); ok {
		for _, id := range ids {
			got = append(got, id.(string))
		}
	}
	if a.Action != "robot.update" || a.After["status"] != "disabled" || a.Before["status"] != "active" || !slices.Equal(got, want) {
		t.Fatalf("audit = %+v, want revoked ids %v", a, want)
	}

	// Disabling again changes nothing; enabling revokes nothing and brings nothing back.
	events := e.auditCount()
	if _, err := e.svc.UpdateRobot(ctx, admin, r, portal.RobotPatch{Status: ptr("disabled")}); err != nil || e.auditCount() != events {
		t.Fatalf("second disable: %v, audit rows %d -> %d", err, events, e.auditCount())
	}
	if _, err := e.svc.UpdateRobot(ctx, admin, r, portal.RobotPatch{Status: ptr("active")}); err != nil {
		t.Fatal(err)
	}
	if revokedAt(live1) == nil {
		t.Fatal("re-enabling un-revoked a token")
	}
	if a := e.lastAudit(); a.After["tokens_revoked"] != nil {
		t.Fatalf("enable audit = %+v", a)
	}
}

func TestCreateRobotToken(t *testing.T) {
	t.Parallel()
	e := newRobotEnv(t)
	ctx := e.ctx()
	admin := e.admin()
	alpha := e.initiative("alpha")
	mgr := e.manager(alpha.id, alpha.manager)
	human := e.w.user(t, "active")
	r := e.w.robot(t, "ci")
	managerBot := e.w.robot(t, "manager-bot")
	e.w.grantUser(t, alpha.id, alpha.manager, managerBot, "")

	t.Run("a platform admin creates a read token owned by the robot; created_by is the admin", func(t *testing.T) {
		before := e.auditCount()
		tok, err := e.svc.CreateRobotToken(ctx, admin, strings.ToUpper(r), portal.NewToken{Name: " deploy ", Scope: "read"})
		if err != nil {
			t.Fatal(err)
		}
		if !apitoken.WellFormed(tok.Secret) || tok.Name != "deploy" || tok.Scope != "read" || tok.UserID != r || tok.CreatedBy != admin {
			t.Fatalf("token = %+v", tok)
		}
		if got := tok.ExpiresAt.Sub(tok.CreatedAt); got != 72*time.Hour { // the robot cap, not the personal one
			t.Fatalf("lifetime = %s, want the robot cap", got)
		}
		if n := e.count(`SELECT count(*) FROM api_tokens WHERE id = $1 AND token_hash = sha256($2::bytea)`, tok.ID, tok.Secret); n != 1 {
			t.Fatal("the stored hash is not the SHA-256 of the secret")
		}
		if got := e.auditCount(); got != before+1 {
			t.Fatalf("audit rows %d -> %d", before, got)
		}
		a := e.lastAudit()
		if a.Actor != admin || a.Action != "token.create" || a.TargetType != "token" || a.TargetID != tok.ID || a.Before != nil ||
			a.After["owner_id"] != r || a.After["name"] != "deploy" || a.After["scope"] != "read" || a.After["expires_at"] == nil {
			t.Fatalf("audit = %+v", a)
		}
		for k := range a.After {
			if k == "token_hash" || k == "secret" {
				t.Fatalf("audit document carries %q", k)
			}
		}
		if e.lastAuditToken() != "" {
			t.Fatal("a session recorded a token id")
		}
	})

	t.Run("expires_in: shorter than the robot cap is honored, between the caps is fine, above or not positive is a 422", func(t *testing.T) {
		tok, err := e.svc.CreateRobotToken(ctx, admin, r, portal.NewToken{Name: "mid", Scope: "read", ExpiresIn: hours(60)}) // above the personal cap
		if err != nil || tok.ExpiresAt.Sub(tok.CreatedAt) != 60*time.Hour {
			t.Fatalf("token = %+v, %v", tok, err)
		}
		if _, err := e.svc.CreateRobotToken(ctx, admin, r, portal.NewToken{Name: "edge", Scope: "read", ExpiresIn: hours(72)}); err != nil {
			t.Fatalf("exactly the cap must be accepted: %v", err)
		}
		for name, d := range map[string]*time.Duration{"over the cap": hours(73), "zero": hours(0), "negative": hours(-1)} {
			_, err := e.svc.CreateRobotToken(ctx, admin, r, portal.NewToken{Name: "x", Scope: "read", ExpiresIn: d})
			if p := validationFields(t, err); len(p) != 1 || p[0].Field != "expires_in" {
				t.Errorf("%s: problems = %v", name, p)
			}
		}
	})

	t.Run("admin scope needs the robot to hold a manages_access grant at that moment", func(t *testing.T) {
		robot := e.w.robot(t, "scoped")
		wantNotAllowed := func(label, robot string) {
			t.Helper()
			_, err := e.svc.CreateRobotToken(ctx, admin, robot, portal.NewToken{Name: "a", Scope: "admin"})
			if p := validationFields(t, err); len(p) != 1 || p[0].Field != "scope" || p[0].Code != portal.CodeNotAllowed {
				t.Fatalf("%s: problems = %v", label, p)
			}
		}
		wantNotAllowed("no grant", robot)
		e.w.grantUser(t, alpha.id, alpha.editor, robot, "")
		wantNotAllowed("a role without manages_access", robot)
		expired := e.w.robot(t, "expired-manager")
		e.w.grantUser(t, alpha.id, alpha.manager, expired, "2000-01-01T00:00:00Z")
		wantNotAllowed("an expired manager grant", expired)
		// The admin's own rights do not count: the token never carries more than its owner can do.
		e.w.grantUser(t, alpha.id, alpha.manager, robot, "")
		tok, err := e.svc.CreateRobotToken(ctx, admin, robot, portal.NewToken{Name: "a", Scope: "admin"})
		if err != nil || tok.Scope != "admin" || tok.UserID != robot {
			t.Fatalf("token = %+v, %v", tok, err)
		}
		// A read token needs no grant at all (the first robot has none).
		if _, err := e.svc.CreateRobotToken(ctx, admin, e.w.robot(t, "reader"), portal.NewToken{Name: "r", Scope: "read"}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("name and scope are validated like a personal token", func(t *testing.T) {
		before := e.count(`SELECT count(*) FROM api_tokens WHERE user_id = $1`, r)
		_, err := e.svc.CreateRobotToken(ctx, admin, r, portal.NewToken{Name: " ", Scope: "root"})
		want := []portal.FieldProblem{{Field: "name", Code: portal.CodeRequired}, {Field: "scope", Code: portal.CodeInvalid}}
		if got := validationFields(t, err); !slices.Equal(got, want) {
			t.Fatalf("problems = %v", got)
		}
		if e.count(`SELECT count(*) FROM api_tokens WHERE user_id = $1`, r) != before {
			t.Fatal("a rejected request stored a token")
		}
	})

	t.Run("a request authenticated by a token never creates one, not even for a robot", func(t *testing.T) {
		before := e.count(`SELECT count(*) FROM api_tokens`)
		_, err := e.svc.CreateRobotToken(portal.WithToken(ctx, "01a14296-1449-7be0-957b-4b1ad8d7a999"), admin, r, portal.NewToken{Name: "x", Scope: "read"})
		if !errors.Is(err, portal.ErrForbidden) || e.count(`SELECT count(*) FROM api_tokens`) != before {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("only platform admins; a human, an unknown or a malformed id is not found; a disabled robot is a conflict", func(t *testing.T) {
		before := e.count(`SELECT count(*) FROM api_tokens`)
		for name, caller := range map[string]string{"manager": mgr, "robot manager": managerBot, "plain user": human} {
			if _, err := e.svc.CreateRobotToken(ctx, caller, r, portal.NewToken{Name: "x", Scope: "read"}); !errors.Is(err, portal.ErrForbidden) {
				t.Errorf("%s: err = %v", name, err)
			}
		}
		for name, id := range map[string]string{"human": human, "unknown": "01a14296-1449-7be0-957b-4b1ad8d7a998", "malformed": "nope"} {
			if _, err := e.svc.CreateRobotToken(ctx, admin, id, portal.NewToken{Name: "x", Scope: "read"}); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("%s: err = %v", name, err)
			}
		}
		off := e.w.robot(t, "off")
		mustExec(t, e.owner, `UPDATE users SET status = 'disabled' WHERE id = $1`, off)
		if _, err := e.svc.CreateRobotToken(ctx, admin, off, portal.NewToken{Name: "x", Scope: "read"}); !errors.Is(err, portal.ErrRobotDisabled) {
			t.Errorf("disabled robot: err = %v", err)
		}
		if e.count(`SELECT count(*) FROM api_tokens`) != before {
			t.Fatal("a refused request stored a token")
		}
	})

	t.Run("a robot's tokens are listed newest first, with the dead ones, for admins only; a human id is not found", func(t *testing.T) {
		robot := e.w.robot(t, "lister")
		var ids []string
		for _, n := range []string{"a", "b", "c"} {
			tok, err := e.svc.CreateRobotToken(ctx, admin, robot, portal.NewToken{Name: n, Scope: "read"})
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, tok.ID)
		}
		e.w.apiToken(t, robot, "-3 days", "-2 days", "") // dead, still listed
		var seen []string
		req := portal.PageRequest{Limit: 2}
		for pages := 0; pages < 4; pages++ {
			p, err := e.svc.ListRobotTokens(ctx, admin, robot, req)
			if err != nil {
				t.Fatal(err)
			}
			for _, tk := range p.Items {
				seen = append(seen, tk.ID)
			}
			if p.NextCursor == "" {
				break
			}
			req.Cursor = p.NextCursor
		}
		if len(seen) != 4 || !slices.IsSortedFunc(seen, func(a, b string) int { return strings.Compare(b, a) }) {
			t.Fatalf("seen %v (created %v)", seen, ids)
		}
		if _, err := e.svc.ListRobotTokens(ctx, mgr, robot, portal.PageRequest{}); !errors.Is(err, portal.ErrForbidden) {
			t.Errorf("manager: %v", err)
		}
		if _, err := e.svc.ListRobotTokens(ctx, admin, human, portal.PageRequest{}); !errors.Is(err, portal.ErrNotFound) {
			t.Errorf("human id: %v", err)
		}
	})
}

// The 25-live-token cap holds for a robot under parallel creations, and the cap is per robot.
func TestRobotTokenLimit(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	admin := e.admin()
	r := e.w.robot(t, "ci")

	var (
		wg   sync.WaitGroup
		errs = make([]error, portal.MaxLiveTokens+1)
	)
	for i := range errs {
		wg.Go(func() { _, errs[i] = e.svc.CreateRobotToken(ctx, admin, r, portal.NewToken{Name: "t", Scope: "read"}) })
	}
	wg.Wait()
	var refused int
	for _, err := range errs {
		switch {
		case err == nil:
		case errors.Is(err, portal.ErrTokenLimit):
			refused++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if got := e.liveTokens(r); got != portal.MaxLiveTokens || refused != 1 {
		t.Fatalf("live tokens = %d, refused = %d; want %d and 1", got, refused, portal.MaxLiveTokens)
	}
	if _, err := e.svc.CreateRobotToken(ctx, admin, e.w.robot(t, "other"), portal.NewToken{Name: "t", Scope: "read"}); err != nil {
		t.Fatalf("another robot: %v", err)
	}
}

// No token survives the disable of its robot, however the creation interleaves with it: the creation takes the
// robot's lock first (LockRobot), so it either commits before the disable (which then revokes the token) or finds
// the robot disabled.
func TestCreateRobotTokenAgainstDisable(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	admin := e.admin()
	var created, refused int
	for i := range 20 {
		r := e.w.robot(t, "race-"+string(rune('a'+i)))
		var (
			wg                  sync.WaitGroup
			createErr, statusEr error
		)
		wg.Go(func() {
			_, createErr = e.svc.CreateRobotToken(ctx, admin, r, portal.NewToken{Name: "race", Scope: "read"})
		})
		wg.Go(func() { _, statusEr = e.svc.UpdateRobot(ctx, admin, r, portal.RobotPatch{Status: ptr("disabled")}) })
		wg.Wait()
		if statusEr != nil {
			t.Fatalf("disable: %v", statusEr)
		}
		switch {
		case createErr == nil:
			created++
		case errors.Is(createErr, portal.ErrRobotDisabled):
			refused++
		default:
			t.Fatalf("create: %v", createErr)
		}
		if n := e.liveTokens(r); n != 0 {
			t.Fatalf("%d live tokens survived the disable (creation err: %v)", n, createErr)
		}
	}
	t.Logf("creations committed before the disable: %d, refused: %d", created, refused)
}

// The existing revocation paths serve robot-owned tokens: an admin by id and by secret, a manager cannot see
// them, and the robot itself revokes its own current token.
func TestRevokingRobotTokens(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	admin := e.admin()
	alpha := e.initiative("alpha")
	mgr := e.manager(alpha.id, alpha.manager)
	r := e.w.robot(t, "ci")
	e.w.grantUser(t, alpha.id, alpha.manager, r, "")
	create := func(scope string) portal.CreatedToken {
		t.Helper()
		tok, err := e.svc.CreateRobotToken(ctx, admin, r, portal.NewToken{Name: "t", Scope: scope})
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	revoked := func(id string) bool {
		return scalar[*string](t, e.owner, `SELECT revoked_at::text FROM api_tokens WHERE id = $1`, id) != nil
	}

	byID, bySecret, current, hidden := create("read"), create("read"), create("admin"), create("read")

	if err := e.svc.RevokeToken(ctx, mgr, hidden.ID); !errors.Is(err, portal.ErrNotFound) || revoked(hidden.ID) {
		t.Fatalf("manager: err = %v, revoked = %v", err, revoked(hidden.ID))
	}
	if err := e.svc.RevokeToken(ctx, admin, byID.ID); err != nil || !revoked(byID.ID) {
		t.Fatalf("admin by id: %v", err)
	}
	if a := e.lastAudit(); a.Actor != admin || a.Action != "token.revoke" || a.TargetID != byID.ID || a.Before["owner_id"] != r {
		t.Fatalf("audit = %+v", a)
	}
	if err := e.svc.RevokeBySecret(ctx, admin, bySecret.Secret); err != nil || !revoked(bySecret.ID) {
		t.Fatalf("admin by secret: %v", err)
	}
	if a := e.lastAudit(); a.Actor != admin || a.Action != "token.revoke" || a.TargetID != bySecret.ID || a.Before["owner_id"] != r {
		t.Fatalf("audit = %+v", a)
	}

	// The robot, authenticated by its own token, revokes it: the audit records the robot and that token.
	robotCtx := portal.WithToken(ctx, current.ID)
	if err := e.svc.RevokeCurrentToken(robotCtx, r); err != nil || !revoked(current.ID) {
		t.Fatalf("self revocation: %v", err)
	}
	if a := e.lastAudit(); a.Actor != r || a.TargetID != current.ID || e.lastAuditToken() != current.ID {
		t.Fatalf("audit = %+v, token_id %q", a, e.lastAuditToken())
	}
	if revoked(hidden.ID) {
		t.Fatal("another token of the robot was revoked")
	}
}

func TestGrantToARobot(t *testing.T) {
	t.Parallel()
	e := newRobotEnv(t)
	ctx := e.ctx()
	admin := e.admin()
	alpha, beta := e.initiative("alpha"), e.initiative("beta")
	mgrA := e.manager(alpha.id, alpha.manager)
	r := e.w.robot(t, "deploy-bot")

	t.Run("an admin and a manager of the initiative grant a role by robot id", func(t *testing.T) {
		g, err := e.svc.CreateGrant(ctx, admin, alpha.id, portal.NewGrant{RobotID: strings.ToUpper(r), RoleID: alpha.editor})
		if err != nil || g.User == nil || g.User.ID != r || g.User.Email != "" || g.User.Name != "deploy-bot" || g.Group != nil || g.GrantedBy != admin {
			t.Fatalf("grant = %+v, %v", g, err)
		}
		a := e.lastAudit()
		if a.Action != "grant.create" || a.Initiative != alpha.id || a.After["user_id"] != r || a.After["user_name"] != "deploy-bot" || a.After["user_email"] != nil {
			t.Fatalf("audit = %+v", a)
		}
		// Natural key: the same grant again is a conflict, not a second row.
		if _, err := e.svc.CreateGrant(ctx, mgrA, alpha.id, portal.NewGrant{RobotID: r, RoleID: alpha.editor}); !errors.Is(err, portal.ErrGrantExists) {
			t.Fatalf("repeat: %v", err)
		}
		other := e.w.robot(t, "second-bot")
		if _, err := e.svc.CreateGrant(ctx, mgrA, alpha.id, portal.NewGrant{RobotID: other, RoleID: alpha.editor}); err != nil {
			t.Fatalf("manager: %v", err)
		}
		// A manager of another initiative learns nothing about this one.
		if _, err := e.svc.CreateGrant(ctx, mgrA, beta.id, portal.NewGrant{RobotID: r, RoleID: beta.editor}); !errors.Is(err, portal.ErrNotFound) {
			t.Fatalf("manager of another initiative: %v", err)
		}
	})

	t.Run("exactly one subject; the robot must exist, be a robot and be active", func(t *testing.T) {
		human := e.w.user(t, "active")
		disabled := e.w.robot(t, "off-bot")
		mustExec(t, e.owner, `UPDATE users SET status = 'disabled' WHERE id = $1`, disabled)
		rows, events := e.count(`SELECT count(*) FROM initiative_access`), e.auditCount()
		tests := []struct {
			name  string
			in    portal.NewGrant
			field string
			code  string
		}{
			{"email and robot", portal.NewGrant{Email: e.email(human), RobotID: r, RoleID: alpha.editor}, "email", portal.CodeExclusive},
			{"group and robot", portal.NewGrant{GroupID: r, RobotID: r, RoleID: alpha.editor}, "email", portal.CodeExclusive},
			{"robot id is not a uuid", portal.NewGrant{RobotID: "nope", RoleID: alpha.editor}, "robot_id", portal.CodeInvalid},
			{"robot that does not exist", portal.NewGrant{RobotID: "01a14296-1449-7be0-957b-4b1ad8d7a998", RoleID: alpha.editor}, "robot_id", portal.CodeUnknown},
			{"a human id", portal.NewGrant{RobotID: human, RoleID: alpha.editor}, "robot_id", portal.CodeUnknown},
			{"disabled robot", portal.NewGrant{RobotID: disabled, RoleID: alpha.editor}, "robot_id", portal.CodeUnknown},
			{"role of another initiative", portal.NewGrant{RobotID: r, RoleID: beta.editor}, "role_id", portal.CodeUnknown},
		}
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

	t.Run("a robot with manages_access can grant in its initiative but is never an effective manager", func(t *testing.T) {
		gamma := e.initiative("gamma")
		human := e.w.user(t, "active")
		bot := e.w.robot(t, "manager-bot")
		if _, err := e.svc.CreateGrant(ctx, admin, gamma.id, portal.NewGrant{Email: e.email(human), RoleID: gamma.manager}); err != nil {
			t.Fatal(err)
		}
		robotGrant, err := e.svc.CreateGrant(ctx, admin, gamma.id, portal.NewGrant{RobotID: bot, RoleID: gamma.manager})
		if err != nil {
			t.Fatal(err)
		}
		// The last human manager cannot go while the robot manager remains.
		humanGrant := e.managerGrantID(gamma, human)
		if err := e.svc.DeleteGrant(ctx, admin, humanGrant); !errors.Is(err, portal.ErrLastManager) {
			t.Fatalf("revoking the last human manager: %v, want ErrLastManager", err)
		}
		// The robot manager manages: it grants in gamma, and only there.
		target := e.w.robot(t, "granted-by-a-robot")
		if _, err := e.svc.CreateGrant(ctx, bot, gamma.id, portal.NewGrant{RobotID: target, RoleID: gamma.editor}); err != nil {
			t.Fatalf("a robot manager granting: %v", err)
		}
		if a := e.lastAudit(); a.Actor != bot || a.Action != "grant.create" {
			t.Fatalf("audit = %+v", a)
		}
		if _, err := e.svc.CreateGrant(ctx, bot, alpha.id, portal.NewGrant{RobotID: target, RoleID: alpha.editor}); !errors.Is(err, portal.ErrNotFound) {
			t.Fatalf("a robot manager granting elsewhere: %v", err)
		}
		// The robot's own grant is not counted: once the human is not needed, the robot's can be revoked.
		if err := e.svc.DeleteGrant(ctx, admin, robotGrant.ID); err != nil {
			t.Fatalf("revoking the robot manager: %v", err)
		}
	})
}

// A robot is a user for everything the API does on behalf of the caller: no email is assumed.
func TestRobotAsCaller(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	alpha := e.initiative("alpha")
	r := e.w.robot(t, "ci")
	e.w.app(t, alpha.id, "web.alpha.test")

	if apps, err := e.svc.HomeApps(ctx, r); err != nil || len(apps) != 0 {
		t.Fatalf("home without grants = %+v, %v", apps, err)
	}
	e.w.grantUser(t, alpha.id, alpha.editor, r, "")
	apps, err := e.svc.HomeApps(ctx, r)
	if err != nil || len(apps) != 1 || apps[0].Hosts[0] != "web.alpha.test" || !slices.Equal(apps[0].Roles, []string{"editor"}) {
		t.Fatalf("home = %+v, %v", apps, err)
	}
	p, err := e.svc.Me(ctx, r)
	if err != nil || p.UserID != r || p.Email != "" || p.Name != "ci" || p.PlatformAdmin {
		t.Fatalf("me = %+v, %v", p, err)
	}
	// A disabled robot is as inactive as a disabled user.
	mustExec(t, e.owner, `UPDATE users SET status = 'disabled' WHERE id = $1`, r)
	if _, err := e.svc.Me(ctx, r); !errors.Is(err, portal.ErrUserInactive) {
		t.Fatalf("disabled robot: %v", err)
	}
	if apps, err := e.svc.HomeApps(ctx, r); err != nil || len(apps) != 0 {
		t.Fatalf("home of a disabled robot = %+v, %v", apps, err)
	}
}

// Robots are never in groups: groups are filled by email (which only a human has) and the schema rejects the id.
func TestRobotsCannotJoinGroups(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	admin := e.admin()
	r := e.w.robot(t, "ci")
	group := e.w.group(t, "ops")

	if _, err := e.svc.AddGroupMember(ctx, admin, group, ""); validationFields(t, err)[0].Field != "email" {
		t.Fatalf("empty email: %v", err)
	}
	err := e.store.WithTx(ctx, func(tx portal.Tx) error {
		_, err := tx.Groups().InsertMember(ctx, group, r)
		return err
	})
	if ref, ok := errors.AsType[*portal.ReferenceError](err); !ok || ref.Field != "user_id" {
		t.Fatalf("InsertMember(robot) = %v, want a reference error on user_id", err)
	}
}
