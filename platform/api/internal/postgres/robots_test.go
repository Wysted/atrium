package postgres_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"platform/api/internal/adminz"
	"platform/api/internal/audit"
	"platform/api/internal/portal"
	"platform/api/internal/postgres"
	"platform/api/internal/testdb"
)

const (
	codeUniqueViolation = "23505"
	robotMigration      = "0012_robots_and_api_tokens.sql"
)

// robot inserts a robot (no google_sub, no email) and returns its id.
func (w world) robot(t *testing.T, name string) string {
	t.Helper()
	return scalar[string](t, w.pool, `INSERT INTO users (kind, name, email) VALUES ('robot', $1, '') RETURNING id::text`, name)
}

// apiToken inserts a token of the user. The intervals are relative to now(): createdAt and expiresAt are
// required, revokedAt may be empty (not revoked). It returns the token id.
func (w world) apiToken(t *testing.T, userID, createdAt, expiresAt, revokedAt string) string {
	t.Helper()
	return scalar[string](t, w.pool, `
		INSERT INTO api_tokens (user_id, name, token_hash, scope, created_at, expires_at, revoked_at)
		VALUES ($1, 'tok', sha256(gen_random_uuid()::text::bytea), 'read', now() + $2::interval, now() + $3::interval,
		        CASE WHEN $4 = '' THEN NULL ELSE now() + $4::interval END)
		RETURNING id::text`, userID, createdAt, expiresAt, revokedAt)
}

// The migration applies on a database that already holds users, admins and group members: they all become
// humans, keep working with the composite foreign keys, and robots stay out of both tables.
func TestRobotMigrationOnExistingData(t *testing.T) {
	t.Parallel()
	pool := testdb.Empty(t)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	files, err := filepath.Glob("migrations/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("migrations: %v, %v", files, err)
	}
	slices.Sort(files)
	apply := func(file string) {
		body, err := os.ReadFile(file) //nolint:gosec // file comes from a glob of the migrations directory
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(body), pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatalf("apply %s: %v", file, err)
		}
	}
	// Created by the migrator before the first file; 0008 revokes privileges on it.
	mustExec(t, pool, `CREATE TABLE schema_migrations (version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`)
	var last string
	for _, f := range files {
		if filepath.Base(f) == robotMigration {
			last = f
			break
		}
		apply(f)
	}
	if last == "" {
		t.Fatalf("%s not found", robotMigration)
	}

	mustExec(t, pool, `INSERT INTO users (google_sub, email, name) VALUES ('s-admin', 'admin@example.com', 'a'), ('s-member', 'm@example.com', 'm')`)
	mustExec(t, pool, `INSERT INTO platform_admins (user_id) SELECT id FROM users WHERE google_sub = 's-admin'`)
	mustExec(t, pool, `INSERT INTO groups (key) VALUES ('g')`)
	mustExec(t, pool, `INSERT INTO group_members (group_id, user_id) SELECT g.id, u.id FROM groups g, users u WHERE u.google_sub = 's-member'`)

	apply(last)

	if n := scalar[int](t, pool, `SELECT count(*) FROM users WHERE kind = 'human' AND google_sub IS NOT NULL`); n != 2 {
		t.Fatalf("%d humans after the migration, want 2", n)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM platform_admins WHERE kind = 'human'`) +
		scalar[int](t, pool, `SELECT count(*) FROM group_members WHERE kind = 'human'`); n != 2 {
		t.Fatalf("admins and members kept: %d, want 2", n)
	}
	w := world{pool: pool}
	r := w.robot(t, "ci")
	for _, sql := range []string{`INSERT INTO platform_admins (user_id) VALUES ($1)`,
		`INSERT INTO group_members (group_id, user_id) SELECT id, $1::uuid FROM groups`} {
		if err := exec(pool, sql, r); pgErrCode(err) != codeForeignKeyViolation {
			t.Fatalf("%s for a robot: err = %v, want a foreign key violation", sql, err)
		}
	}
	// The cascade of the composite keys still removes the rows of a deleted human.
	mustExec(t, pool, `DELETE FROM users WHERE google_sub IN ('s-admin', 's-member')`)
	if n := scalar[int](t, pool, `SELECT count(*) FROM platform_admins`) + scalar[int](t, pool, `SELECT count(*) FROM group_members`); n != 0 {
		t.Fatalf("%d rows survived the deletion of their user", n)
	}
}

func TestRobotAndTokenConstraints(t *testing.T) {
	t.Parallel()
	pool := newTestDB(t)
	w := world{pool: pool}
	human := w.user(t, "active")
	robot := w.robot(t, "Deploy-Bot")
	mustExec(t, pool, `INSERT INTO api_tokens (user_id, name, token_hash, scope, expires_at)
		VALUES ($1, 't', sha256('dup'), 'read', now() + interval '1 day')`, human)

	tests := []struct {
		name string
		sql  string
		args []any
		code string
	}{
		{"robot with an email", `INSERT INTO users (kind, name, email) VALUES ('robot', 'a', 'r@example.com')`, nil, codeCheckViolation},
		{"robot with a google_sub", `INSERT INTO users (kind, name, email, google_sub) VALUES ('robot', 'b', '', 's-r')`, nil, codeCheckViolation},
		{"human without a google_sub", `INSERT INTO users (email) VALUES ('h@example.com')`, nil, codeCheckViolation},
		{"unknown kind", `INSERT INTO users (kind, name, email, google_sub) VALUES ('bot', 'c', '', 's-b')`, nil, codeCheckViolation},
		{"robot without a name", `INSERT INTO users (kind, email) VALUES ('robot', '')`, nil, codeCheckViolation},
		{"robot with a 65 character name", `INSERT INTO users (kind, name, email) VALUES ('robot', repeat('x', 65), '')`, nil, codeCheckViolation},
		{"robot name unique ignoring case", `INSERT INTO users (kind, name, email) VALUES ('robot', 'deploy-bot', '')`, nil, codeUniqueViolation},
		{"robot in platform_admins", `INSERT INTO platform_admins (user_id) VALUES ($1)`, []any{robot}, codeForeignKeyViolation},
		{"robot in platform_admins with its kind", `INSERT INTO platform_admins (user_id, kind) VALUES ($1, 'robot')`, []any{robot}, codeCheckViolation},
		{"robot in group_members", `INSERT INTO group_members (group_id, user_id) SELECT id, $1::uuid FROM groups WHERE key = 'g'`,
			[]any{robot}, codeForeignKeyViolation},
		{"robot in group_members with its kind", `INSERT INTO group_members (group_id, user_id, kind) SELECT id, $1::uuid, 'robot' FROM groups WHERE key = 'g'`,
			[]any{robot}, codeCheckViolation},
		{"token scope", `INSERT INTO api_tokens (user_id, name, token_hash, scope, expires_at) VALUES ($1, 't', sha256('1'), 'write', now() + interval '1 day')`,
			[]any{human}, codeCheckViolation},
		{"token without a name", `INSERT INTO api_tokens (user_id, name, token_hash, scope, expires_at) VALUES ($1, '', sha256('2'), 'read', now() + interval '1 day')`,
			[]any{human}, codeCheckViolation},
		{"token with a 65 character name", `INSERT INTO api_tokens (user_id, name, token_hash, scope, expires_at) VALUES ($1, repeat('x', 65), sha256('3'), 'read', now() + interval '1 day')`,
			[]any{human}, codeCheckViolation},
		{"token hash of the wrong length", `INSERT INTO api_tokens (user_id, name, token_hash, scope, expires_at) VALUES ($1, 't', '\x00', 'read', now() + interval '1 day')`,
			[]any{human}, codeCheckViolation},
		{"token expiring before its creation", `INSERT INTO api_tokens (user_id, name, token_hash, scope, expires_at) VALUES ($1, 't', sha256('4'), 'read', now() - interval '1 second')`,
			[]any{human}, codeCheckViolation},
		{"token without an expiry", `INSERT INTO api_tokens (user_id, name, token_hash, scope) VALUES ($1, 't', sha256('5'), 'read')`,
			[]any{human}, "23502"},
		{"duplicate token hash", `INSERT INTO api_tokens (user_id, name, token_hash, scope, expires_at) VALUES ($1, 't', sha256('dup'), 'admin', now() + interval '1 day')`,
			[]any{robot}, codeUniqueViolation},
		{"token of an unknown user", `INSERT INTO api_tokens (user_id, name, token_hash, scope, expires_at) VALUES (uuidv7(), 't', sha256('6'), 'read', now() + interval '1 day')`,
			nil, codeForeignKeyViolation},
	}
	w.group(t, "g")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := exec(pool, tt.sql, tt.args...)
			if err == nil {
				t.Fatalf("expected error with code %s, got nil", tt.code)
			}
			if got := pgErrCode(err); got != tt.code {
				t.Fatalf("code = %q (%v), want %q", got, err, tt.code)
			}
		})
	}

	t.Run("valid shapes", func(t *testing.T) {
		mustExec(t, pool, `INSERT INTO users (kind, name, email) VALUES ('robot', repeat('x', 64), '')`)
		mustExec(t, pool, `INSERT INTO users (google_sub, email, name) VALUES ('s-same-1', 'same@example.com', 'n'), ('s-same-2', 'same@example.com', 'n')`)
		mustExec(t, pool, `INSERT INTO users (kind, name, email) VALUES ('robot', 'Deploy-Bot-2', '')`)
	})

	t.Run("owner and tokens follow the user", func(t *testing.T) {
		owner := w.user(t, "active")
		r := w.robot(t, "owned-bot")
		mustExec(t, pool, `UPDATE users SET owner_user_id = $2 WHERE id = $1`, r, owner)
		w.apiToken(t, r, "-1 hour", "1 hour", "")
		mustExec(t, pool, `DELETE FROM users WHERE id = $1`, owner)
		if scalar[bool](t, pool, `SELECT owner_user_id IS NOT NULL FROM users WHERE id = $1`, r) {
			t.Fatal("the owner column survived the deletion of the owner")
		}
		mustExec(t, pool, `DELETE FROM users WHERE id = $1`, r)
		if n := scalar[int](t, pool, `SELECT count(*) FROM api_tokens WHERE user_id = $1`, r); n != 0 {
			t.Fatalf("%d tokens survived the deletion of their owner", n)
		}
	})
}

// purge_dead_api_tokens deletes only rows dead for longer than the grace period, counting from the earlier of
// expires_at and revoked_at, and refuses a short grace. The housekeeping job calls it as the runtime role.
func TestPurgeDeadAPITokensRespectsTheGrace(t *testing.T) {
	t.Parallel()
	owner := newTestDB(t)
	w := world{pool: owner}
	rt := testdb.Runtime(t, owner)
	u := w.user(t, "active")
	r := w.robot(t, "ci")

	kept := map[string]string{
		"live":                    w.apiToken(t, u, "-1 day", "29 days", ""),
		"expired recently":        w.apiToken(t, u, "-40 days", "-10 days", ""),
		"revoked recently":        w.apiToken(t, r, "-40 days", "50 days", "-10 days"),
		"expiry just inside":      w.apiToken(t, u, "-60 days", "-29 days", ""),
		"revoked, expires later":  w.apiToken(t, u, "-5 days", "20 days", "-1 hour"),
		"robot token still valid": w.apiToken(t, r, "-1 day", "89 days", ""),
	}
	gone := map[string]string{
		"expired long ago":               w.apiToken(t, u, "-100 days", "-40 days", ""),
		"revoked long ago":               w.apiToken(t, r, "-90 days", "60 days", "-40 days"),
		"revoked after it had expired":   w.apiToken(t, u, "-100 days", "-40 days", "-5 days"),
		"expired after being revoked":    w.apiToken(t, u, "-100 days", "-35 days", "-45 days"),
		"expiry just outside the period": w.apiToken(t, u, "-60 days", "-31 days", ""),
	}

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	for _, grace := range []string{"6 days 23 hours", "0 seconds", "-1 day"} {
		_, err := rt.Exec(ctx, `SELECT purge_dead_api_tokens($1::interval)`, grace)
		if pgErrCode(err) != codeInvalidParameter {
			t.Errorf("grace %q: err = %v, want invalid_parameter_value", grace, err)
		}
	}
	if _, err := rt.Exec(ctx, `SELECT purge_dead_api_tokens(NULL)`); pgErrCode(err) != codeInvalidParameter {
		t.Errorf("NULL grace: err = %v, want invalid_parameter_value", err)
	}
	if n := scalar[int](t, owner, `SELECT count(*) FROM api_tokens`); n != len(kept)+len(gone) {
		t.Fatalf("a refused call deleted rows: %d left", n)
	}

	rep, err := newJob(t, rt, 0).RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.APITokens != int64(len(gone)) {
		t.Fatalf("purged %d tokens, want %d", rep.APITokens, len(gone))
	}
	for label, id := range kept {
		if scalar[int](t, owner, `SELECT count(*) FROM api_tokens WHERE id = $1`, id) != 1 {
			t.Errorf("%s was purged", label)
		}
	}
	for label, id := range gone {
		if scalar[int](t, owner, `SELECT count(*) FROM api_tokens WHERE id = $1`, id) != 0 {
			t.Errorf("%s survived", label)
		}
	}
	if again, err := newJob(t, rt, 0).RunOnce(ctx); err != nil || again.APITokens != 0 {
		t.Fatalf("second run = %+v, %v", again, err)
	}
}

// The login upsert still works under the narrowed INSERT of the runtime role, with the nullable google_sub.
func TestLoginUpsertWithRobotsPresent(t *testing.T) {
	t.Parallel()
	owner := newTestDB(t)
	w := world{pool: owner}
	w.robot(t, "ci")
	repo := postgres.NewUserRepo(testdb.Runtime(t, owner))
	ctx := t.Context()

	id, active, err := repo.UpsertUser(ctx, "google-sub-1", "a@example.com", "A")
	if err != nil || !active {
		t.Fatalf("first login = %q, %v, %v", id, active, err)
	}
	again, _, err := repo.UpsertUser(ctx, "google-sub-1", "b@example.com", "B")
	if err != nil || again != id {
		t.Fatalf("second login = %q, %v, want %q", again, err, id)
	}
	if kind := scalar[string](t, owner, `SELECT kind FROM users WHERE id = $1`, id); kind != "human" {
		t.Fatalf("kind = %q", kind)
	}
	if email := scalar[string](t, owner, `SELECT email FROM users WHERE id = $1`, id); email != "b@example.com" {
		t.Fatalf("email = %q", email)
	}
}

// Disabling a user revokes their live tokens in the same transaction, through the shared function, and the audit
// row lists the ids.
func TestDisablingRevokesTheTokens(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	admin := e.admin()
	u := e.w.user(t, "active")
	other := e.w.user(t, "active")

	live1 := e.w.apiToken(t, u, "-1 hour", "1 hour", "")
	live2 := e.w.apiToken(t, u, "-1 hour", "10 days", "")
	expired := e.w.apiToken(t, u, "-2 days", "-1 day", "")
	revoked := e.w.apiToken(t, u, "-2 days", "10 days", "-1 day")
	othersLive := e.w.apiToken(t, other, "-1 hour", "1 hour", "")
	revokedAt := func(id string) *string {
		return scalar[*string](t, e.owner, `SELECT revoked_at::text FROM api_tokens WHERE id = $1`, id)
	}
	revokedBefore := *revokedAt(revoked)

	// The audit insert fails: the revocation rolls back with the status.
	failing := newAdminEnvWith(t, func(s portal.Store) portal.Store { return failingAuditStore{Store: s} })
	fa, fu := failing.admin(), failing.w.user(t, "active")
	ft := failing.w.apiToken(t, fu, "-1 hour", "1 hour", "")
	if _, err := failing.svc.SetUserStatus(failing.ctx(), fa, fu, "disabled"); !errors.Is(err, errAuditDown) {
		t.Fatalf("err = %v, want the audit failure", err)
	}
	if scalar[*string](t, failing.owner, `SELECT revoked_at::text FROM api_tokens WHERE id = $1`, ft) != nil {
		t.Fatal("the revocation survived a rolled back disable")
	}

	if _, err := e.svc.SetUserStatus(ctx, u, u, "disabled"); !errors.Is(err, portal.ErrForbidden) && !errors.Is(err, portal.ErrSelfDisable) {
		t.Fatalf("self-service disable: %v", err)
	}
	if revokedAt(live1) != nil {
		t.Fatal("a refused disable revoked a token")
	}

	if _, err := e.svc.SetUserStatus(ctx, admin, u, "disabled"); err != nil {
		t.Fatal(err)
	}
	for label, id := range map[string]string{"first": live1, "second": live2} {
		if revokedAt(id) == nil {
			t.Errorf("live token %s not revoked", label)
		}
	}
	if revokedAt(expired) != nil {
		t.Error("an expired token got a revoked_at")
	}
	if got := *revokedAt(revoked); got != revokedBefore {
		t.Errorf("revoked_at of a revoked token changed: %s -> %s", revokedBefore, got)
	}
	if revokedAt(othersLive) != nil {
		t.Error("another user's token was revoked")
	}
	a := e.lastAudit()
	want := []string{live1, live2}
	slices.Sort(want)
	var got []string
	if ids, ok := a.After["tokens_revoked"].([]any); ok {
		for _, id := range ids {
			got = append(got, id.(string))
		}
	}
	if a.Action != "user.disable" || !slices.Equal(got, want) {
		t.Fatalf("audit = %+v, want revoked ids %v", a, want)
	}

	// Enabling brings nothing back.
	if _, err := e.svc.SetUserStatus(ctx, admin, u, "active"); err != nil {
		t.Fatal(err)
	}
	if revokedAt(live1) == nil {
		t.Fatal("re-enabling un-revoked a token")
	}
	if a := e.lastAudit(); a.Action != "user.enable" || a.After["tokens_revoked"] != nil {
		t.Fatalf("enable audit = %+v", a)
	}
}

// The revoke function is the one the robot path will call: it serves any owner, returns the sorted ids of the
// tokens it revoked and is idempotent.
func TestRevokeTokensFunction(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	r := e.w.robot(t, "ci")
	a := e.w.apiToken(t, r, "-1 hour", "1 hour", "")
	b := e.w.apiToken(t, r, "-1 hour", "2 hours", "")
	e.w.apiToken(t, r, "-2 days", "-1 day", "")

	revoke := func() []string {
		var ids []string
		err := e.store.WithTx(e.ctx(), func(tx portal.Tx) error {
			var err error
			ids, err = tx.Users().RevokeTokens(e.ctx(), r)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return ids
	}
	want := []string{a, b}
	slices.Sort(want)
	if got := revoke(); !slices.Equal(got, want) {
		t.Fatalf("revoked %v, want %v", got, want)
	}
	if got := revoke(); len(got) != 0 {
		t.Fatalf("second call revoked %v", got)
	}
	if n := e.count(`SELECT count(*) FROM api_tokens WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()`, r); n != 0 {
		t.Fatalf("%d live tokens left", n)
	}
}

// /users/* is for humans: a robot id is as absent as an unknown id, and nothing is written or audited.
func TestUsersEndpointsDoNotSeeRobots(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	admin := e.admin()
	r := e.w.robot(t, "ci")
	events := e.auditCount()

	for _, status := range []string{"disabled", "active"} {
		if _, err := e.svc.SetUserStatus(ctx, admin, r, status); !errors.Is(err, portal.ErrNotFound) {
			t.Errorf("SetUserStatus(%s) on a robot: %v, want not found", status, err)
		}
	}
	if _, err := e.svc.RevokeUserSessions(ctx, admin, r); !errors.Is(err, portal.ErrNotFound) {
		t.Errorf("RevokeUserSessions on a robot: %v, want not found", err)
	}
	if e.auditCount() != events || e.count(`SELECT count(*) FROM users WHERE id = $1 AND status = 'active'`, r) != 1 {
		t.Fatal("a refused request wrote something")
	}

	err := e.store.WithTx(ctx, func(tx portal.Tx) error {
		if _, err := tx.Users().GetUser(ctx, r); !errors.Is(err, portal.ErrNotFound) {
			t.Errorf("GetUser: %v", err)
		}
		if _, err := tx.Users().LockUser(ctx, r); !errors.Is(err, portal.ErrNotFound) {
			t.Errorf("LockUser: %v", err)
		}
		if users, err := tx.Users().FindActiveByEmail(ctx, ""); err != nil || len(users) != 0 {
			t.Errorf("FindActiveByEmail(\"\") = %v, %v", users, err)
		}
		if users, err := tx.Users().ListUsersByEmail(ctx, adminz.Scope{All: true}, "", 10); err != nil || len(users) != 0 {
			t.Errorf("ListUsersByEmail(\"\") = %v, %v", users, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A robot may hold a manages_access role but is never an effective manager: the last human manager cannot be
// revoked while a robot manager remains, and the integration health counts agree.
func TestRobotManagersAreNotEffective(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	admin := e.admin()
	r := e.w.robot(t, "ci")

	alpha := e.initiative("alpha")
	human := e.w.user(t, "active")
	e.w.grantUser(t, alpha.id, alpha.manager, human, "")
	e.w.grantUser(t, alpha.id, alpha.manager, r, "")
	humanGrant, robotGrant := e.managerGrantID(alpha, human), e.managerGrantID(alpha, r)

	counts := func(initiativeID string) portal.GrantCounts {
		var c portal.GrantCounts
		err := e.store.WithTx(ctx, func(tx portal.Tx) error {
			var err error
			c, err = tx.Health().GrantCounts(ctx, initiativeID)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if c := counts(alpha.id); c.Grants != 2 || c.EffectiveManagers != 1 {
		t.Fatalf("counts = %+v, want 2 grants and 1 effective manager", c)
	}
	if err := e.svc.DeleteGrant(ctx, admin, humanGrant); !errors.Is(err, portal.ErrLastManager) {
		t.Fatalf("revoking the last human manager: %v, want ErrLastManager", err)
	}
	if err := e.svc.DeleteGrant(ctx, admin, robotGrant); err != nil {
		t.Fatalf("revoking the robot manager: %v", err)
	}

	// Only a robot manager: nothing is effective, so its grant can go.
	beta := e.initiative("beta")
	e.w.grantUser(t, beta.id, beta.manager, r, "")
	if c := counts(beta.id); c.EffectiveManagers != 0 {
		t.Fatalf("a robot counted as an effective manager: %+v", c)
	}
	if err := e.svc.DeleteGrant(ctx, admin, e.managerGrantID(beta, r)); err != nil {
		t.Fatalf("revoking a robot-only manager grant: %v", err)
	}
}

// Permissions are loaded for a robot like for any user (its name, its managed initiatives) but it is never a
// platform admin.
func TestRobotPermissions(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	alpha := e.initiative("alpha")
	r := e.w.robot(t, "ci")
	e.w.grantUser(t, alpha.id, alpha.manager, r, "")

	p, err := e.svc.Me(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "ci" || p.Email != "" || p.PlatformAdmin || len(p.Managed) != 1 || p.Managed[0].ID != alpha.id {
		t.Fatalf("permissions = %+v", p)
	}
	if err := exec(e.owner, `INSERT INTO platform_admins (user_id) VALUES ($1)`, r); pgErrCode(err) != codeForeignKeyViolation {
		t.Fatalf("a robot became a platform admin: %v", err)
	}
}

// Audit and grant listings show a robot by its name where they show the email of a human.
func TestRobotNameInListingsAndDocuments(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	admin := e.admin()
	alpha := e.initiative("alpha")
	r := e.w.robot(t, "deploy-bot")
	e.w.grantUser(t, alpha.id, alpha.editor, r, "")
	mustExec(t, e.owner, `INSERT INTO audit_events (actor_id, action, target_type, target_id) VALUES ($1, 'x.y', 'thing', 't')`, r)

	grants, err := e.svc.ListGrants(ctx, admin, alpha.id, portal.PageRequest{})
	if err != nil || len(grants.Items) != 1 || grants.Items[0].User == nil ||
		grants.Items[0].User.Email != "" || grants.Items[0].User.Name != "deploy-bot" {
		t.Fatalf("grants = %+v, %v", grants, err)
	}
	log, err := e.svc.ListAudit(ctx, admin, portal.AuditQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, ev := range log.Items {
		if ev.ActorID == r {
			seen = true
			if ev.ActorEmail != "" || ev.ActorName != "deploy-bot" {
				t.Errorf("actor of the robot event = %q / %q", ev.ActorEmail, ev.ActorName)
			}
		}
	}
	if !seen {
		t.Fatal("the robot event is not listed")
	}

	if err := e.svc.DeleteGrant(ctx, admin, grants.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	if a := e.lastAudit(); a.Action != "grant.delete" || a.Before["user_name"] != "deploy-bot" || a.Before["user_email"] != nil {
		t.Fatalf("grant audit = %+v", a)
	}
}

// audit.Event carries the token that made the request; without one the column stays NULL.
func TestAuditRecordsTheToken(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	tok := scalar[string](t, e.owner, `SELECT uuidv7()::text`)
	for _, ev := range []audit.Event{
		{Action: "a.with", TargetType: "t", TargetID: "1", TokenID: tok},
		{Action: "a.without", TargetType: "t", TargetID: "2"},
	} {
		if err := e.store.WithTx(ctx, func(tx portal.Tx) error { return tx.Audit().RecordAudit(ctx, ev) }); err != nil {
			t.Fatal(err)
		}
	}
	if got := scalar[string](t, e.owner, `SELECT token_id::text FROM audit_events WHERE action = 'a.with'`); got != tok {
		t.Fatalf("token_id = %q, want %q", got, tok)
	}
	if scalar[*string](t, e.owner, `SELECT token_id::text FROM audit_events WHERE action = 'a.without'`) != nil {
		t.Fatal("token_id is not NULL without a token")
	}
}
