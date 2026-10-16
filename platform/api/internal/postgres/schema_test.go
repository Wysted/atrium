package postgres_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"platform/api/internal/postgres"
)

// pgErrCode extracts the SQLSTATE of err ("" if it is not a PostgreSQL error).
func pgErrCode(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

const (
	codeForeignKeyViolation = "23503"
	codeCheckViolation      = "23514"
	codeInsufficientPrivs   = "42501"
	codeInvalidParameter    = "22023"
)

func TestConstraints(t *testing.T) {
	t.Parallel()
	pool := newTestDB(t)
	w := world{pool: pool}

	a := w.initiative(t, "a", "active")
	b := w.initiative(t, "b", "active")
	w.role(t, a, "editor")
	roleB := w.role(t, b, "editor")
	appA := w.app(t, a, "a.example.test")
	u := w.user(t, "active")
	g := w.group(t, "g")

	tests := []struct {
		name string
		sql  string
		args []any
		code string
	}{
		{"composite FK rejects role of another initiative",
			`INSERT INTO initiative_access (initiative_id, role_id, user_id) VALUES ($1, $2, $3)`,
			[]any{a, roleB, u}, codeForeignKeyViolation},
		{"CHECK rejects user and group together",
			`INSERT INTO initiative_access (initiative_id, role_id, user_id, group_id) VALUES ($1, $2, $3, $4)`,
			[]any{b, roleB, u, g}, codeCheckViolation},
		{"CHECK rejects neither user nor group",
			`INSERT INTO initiative_access (initiative_id, role_id) VALUES ($1, $2)`,
			[]any{b, roleB}, codeCheckViolation},
		{"upper case deployment host rejected",
			`INSERT INTO deployments (host, app_id) VALUES ('Upper.example.test', $1)`,
			[]any{appA}, codeCheckViolation},
		{"upper case initiative key rejected",
			`INSERT INTO initiatives (key) VALUES ('Upper')`, nil, codeCheckViolation},
		{"upper case role key rejected",
			`INSERT INTO initiative_roles (initiative_id, key) VALUES ($1, 'Editor')`,
			[]any{a}, codeCheckViolation},
		{"malformed app id rejected",
			`INSERT INTO apps (id, initiative_id) VALUES ('app_123', $1)`,
			[]any{a}, codeCheckViolation},
		{"token hash must be 32 bytes",
			`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ('\x00'::bytea, $1, now())`,
			[]any{u}, codeCheckViolation},
		{"duplicate google_sub rejected",
			`INSERT INTO users (google_sub, email) SELECT google_sub, 'x@example.com' FROM users LIMIT 1`,
			nil, "23505"},
	}
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
}

func TestRuntimeRoleAuditAppendOnly(t *testing.T) {
	t.Parallel()
	pool := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // rollback after failed statements is best effort

	if _, err := tx.Exec(ctx, "SET LOCAL ROLE platform_runtime"); err != nil {
		t.Fatalf("set role: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events (action, target_type, target_id) VALUES ('x', 'y', 'z')`); err != nil {
		t.Fatalf("insert should be allowed: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT count(*) FROM audit_events`); err != nil {
		t.Fatalf("select should be allowed: %v", err)
	}

	for _, stmt := range []string{
		`UPDATE audit_events SET action = 'tampered'`,
		`DELETE FROM audit_events`,
		`TRUNCATE audit_events`,
	} {
		// A failed statement aborts the transaction: use savepoints.
		sp, err := tx.Begin(ctx)
		if err != nil {
			t.Fatalf("savepoint: %v", err)
		}
		_, err = sp.Exec(ctx, stmt)
		_ = sp.Rollback(ctx)
		if got := pgErrCode(err); got != codeInsufficientPrivs {
			t.Fatalf("%s: code = %q (%v), want %s", stmt, got, err, codeInsufficientPrivs)
		}
	}
}

func TestSessionDeleteCascadesToAppSessions(t *testing.T) {
	t.Parallel()
	pool := newTestDB(t)
	w := world{pool: pool}

	in := w.initiative(t, "i", "active")
	app := w.app(t, in, "i.example.test")
	u := w.user(t, "active")

	sid := scalar[string](t, pool,
		`INSERT INTO sessions (token_hash, user_id, expires_at)
		 VALUES (sha256('s'::bytea), $1, now() + interval '8 hours') RETURNING id::text`, u)
	mustExec(t, pool,
		`INSERT INTO app_sessions (token_hash, session_id, app_id, host, expires_at)
		 VALUES (sha256('a'::bytea), $1, $2, 'i.example.test', now() + interval '8 hours')`, sid, app)
	if n := scalar[int](t, pool, `SELECT count(*) FROM app_sessions`); n != 1 {
		t.Fatalf("app_sessions before = %d, want 1", n)
	}
	mustExec(t, pool, `DELETE FROM sessions WHERE id = $1`, sid)
	if n := scalar[int](t, pool, `SELECT count(*) FROM app_sessions`); n != 0 {
		t.Fatalf("app_sessions after = %d, want 0", n)
	}
}

func TestAccessChangeNotifies(t *testing.T) {
	t.Parallel()
	pool := newTestDB(t)
	w := world{pool: pool}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	in := w.initiative(t, "i", "active")
	role := w.role(t, in, "editor")
	u := w.user(t, "active")

	listener, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer listener.Release()
	if _, err := listener.Exec(ctx, "LISTEN access_changed"); err != nil {
		t.Fatalf("listen: %v", err)
	}

	w.grantUser(t, in, role, u, "")

	waitCtx, waitCancel := context.WithTimeout(ctx, 5*time.Second)
	defer waitCancel()
	n, err := listener.Conn().WaitForNotification(waitCtx)
	if err != nil {
		t.Fatalf("no notification: %v", err)
	}
	if n.Channel != "access_changed" || n.Payload != "initiative_access" {
		t.Fatalf("notification = %+v", n)
	}
}

func TestAccessDenialsPartitions(t *testing.T) {
	t.Parallel()
	pool := newTestDB(t)

	// Current month exists after migration, so inserting works.
	mustExec(t, pool, `INSERT INTO access_denials (host, reason) VALUES ('h.example.test', 'no_grant')`)

	// Creating a partition is idempotent and returns its name.
	month := time.Now().UTC().AddDate(0, 6, 0)
	monthStart := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	want := "access_denials_" + monthStart.Format("2006_01")
	first := scalar[string](t, pool, `SELECT create_access_denials_partition($1::date)`, monthStart.AddDate(0, 0, 16))
	second := scalar[string](t, pool, `SELECT create_access_denials_partition($1::date)`, monthStart)
	if first != want || second != first {
		t.Fatalf("partition names = %q, %q, want %q", first, second, want)
	}
	mustExec(t, pool, `INSERT INTO access_denials (occurred_at, host, reason) VALUES ($1, 'h.example.test', 'no_grant')`,
		monthStart.AddDate(0, 1, 0).Add(-time.Second))

	// A month without partition fails: the job must create partitions ahead.
	err := exec(pool, `INSERT INTO access_denials (occurred_at, host, reason) VALUES ($1, 'h.example.test', 'no_grant')`,
		monthStart.AddDate(0, 2, 0))
	if err == nil || !strings.Contains(err.Error(), "no partition") {
		t.Fatalf("expected missing partition error, got %v", err)
	}
}

// The partition function only accepts [current month - 1, current month + 12] (UTC), even for the runtime role.
func TestAccessDenialsPartitionRange(t *testing.T) {
	t.Parallel()
	pool := newTestDB(t)
	const monthAt = `SELECT create_access_denials_partition((date_trunc('month', now() AT TIME ZONE 'UTC') + make_interval(months => $1))::date)`
	for _, tc := range []struct {
		offset int
		ok     bool
	}{{-1, true}, {0, true}, {1, true}, {12, true}, {-2, false}, {13, false}, {-120, false}, {600, false}} {
		err := exec(pool, monthAt, tc.offset)
		if tc.ok && err != nil {
			t.Errorf("offset %+d months rejected: %v", tc.offset, err)
		}
		if !tc.ok && pgErrCode(err) != codeInvalidParameter {
			t.Errorf("offset %+d months: err = %v, want SQLSTATE %s", tc.offset, err, codeInvalidParameter)
		}
	}
	// Rejected months leave no partition behind.
	if n := scalar[int](t, pool, `SELECT count(*) FROM pg_inherits WHERE inhparent = 'access_denials'::regclass`); n != 4 {
		t.Fatalf("partitions = %d, want exactly the 4 accepted months", n)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	t.Parallel()
	pool := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	applied, err := postgres.Migrate(ctx, pool)
	if err != nil || len(applied) != 0 {
		t.Fatalf("second migrate applied=%v err=%v", applied, err)
	}
}

// L7: the values copied into the Platform-Assertion are length-bounded by the schema.
func TestAssertionFieldLengthCaps(t *testing.T) {
	t.Parallel()
	pool := newTestDB(t)
	w := world{pool: pool}
	init := w.initiative(t, "caps", "active")

	tests := []struct {
		name string
		sql  string
		args []any
		code string // "" = must succeed
	}{
		{"role key at 32", `INSERT INTO initiative_roles (initiative_id, key) VALUES ($1, $2)`, []any{init, strings.Repeat("r", 32)}, ""},
		{"role key at 33", `INSERT INTO initiative_roles (initiative_id, key) VALUES ($1, $2)`, []any{init, strings.Repeat("r", 33)}, codeCheckViolation},
		{"email at 254", `INSERT INTO users (google_sub, email) VALUES ('s-email-ok', $1)`, []any{strings.Repeat("e", 254)}, ""},
		{"email at 255", `INSERT INTO users (google_sub, email) VALUES ('s-email-bad', $1)`, []any{strings.Repeat("e", 255)}, codeCheckViolation},
		{"name at 200 characters", `INSERT INTO users (google_sub, email, name) VALUES ('s-name-ok', 'a@example.test', $1)`, []any{strings.Repeat("\U0001F600", 200)}, ""},
		{"name at 201 characters", `INSERT INTO users (google_sub, email, name) VALUES ('s-name-bad', 'b@example.test', $1)`, []any{strings.Repeat("n", 201)}, codeCheckViolation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := exec(pool, tt.sql, tt.args...)
			if got := pgErrCode(err); got != tt.code {
				t.Fatalf("code = %q (%v), want %q", got, err, tt.code)
			}
		})
	}
}

// L8: the runtime role may only update the lifecycle columns of signing_keys.
func TestRuntimeRoleSigningKeysUpdateIsColumnScoped(t *testing.T) {
	t.Parallel()
	pool := newTestDB(t)
	mustExec(t, pool, `INSERT INTO signing_keys (kid, public_jwk, private_enc, kek_id, state)
		VALUES (repeat('a', 43), '{}', '\x01', repeat('k', 22), 'next')`)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // best effort
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE platform_runtime"); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		sql  string
		want string
	}{
		{"state", `UPDATE signing_keys SET state = 'active', activated_at = now()`, ""},
		{"retired_at", `UPDATE signing_keys SET retired_at = now()`, ""},
		{"removed_at and private_enc", `UPDATE signing_keys SET removed_at = now(), private_enc = NULL, state = 'removed'`, ""},
		{"public_jwk", `UPDATE signing_keys SET public_jwk = '{"x":"forged"}'`, codeInsufficientPrivs},
		{"kid", `UPDATE signing_keys SET kid = repeat('b', 43)`, codeInsufficientPrivs},
		{"created_at", `UPDATE signing_keys SET created_at = now()`, codeInsufficientPrivs},
	}
	for _, tt := range tests {
		sp, err := tx.Begin(ctx) // a failed statement aborts the transaction: use savepoints
		if err != nil {
			t.Fatal(err)
		}
		_, err = sp.Exec(ctx, tt.sql)
		_ = sp.Rollback(ctx)
		if got := pgErrCode(err); got != tt.want {
			t.Errorf("%s: code = %q (%v), want %q", tt.name, got, err, tt.want)
		}
	}
}

func TestPortalRolePrivileges(t *testing.T) {
	t.Parallel()
	pool := newTestDB(t)
	w := world{pool: pool}
	init := w.initiative(t, "platform", "active")
	w.app(t, init, "portal.example.test")
	mustExec(t, pool, `INSERT INTO signing_keys (kid, public_jwk, private_enc, kek_id, state)
		VALUES (repeat('a', 43), '{}', '\x01', repeat('k', 22), 'next')`)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // best effort
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE platform_portal"); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		sql  string
		want string
	}{
		{"select users", `SELECT count(*) FROM users`, ""},
		{"update user status", `UPDATE users SET status = 'disabled'`, ""},
		{"update user updated_at", `UPDATE users SET updated_at = now()`, ""},
		{"update user google_sub", `UPDATE users SET google_sub = 'forged'`, codeInsufficientPrivs},
		{"update user email", `UPDATE users SET email = 'x@example.com'`, codeInsufficientPrivs},
		{"insert user", `INSERT INTO users (google_sub, email, name) VALUES ('s', 'e@example.com', 'n')`, codeInsufficientPrivs},
		{"delete users", `DELETE FROM users`, codeInsufficientPrivs},
		{"insert group", `INSERT INTO groups (key) VALUES ('g')`, ""},
		{"update initiative", `UPDATE initiatives SET status = 'inactive'`, ""},
		{"update apps status", `UPDATE apps SET status = 'disabled'`, ""},
		{"update deployments probe", `UPDATE deployments SET last_probe_status = 'ok', last_probe_at = now(), last_seen_at = now()`, ""},
		{"delete deployments", `DELETE FROM deployments`, ""},
		{"select platform_admins", `SELECT count(*) FROM platform_admins`, ""},
		{"insert platform_admins", `INSERT INTO platform_admins (user_id) VALUES (uuidv7())`, codeInsufficientPrivs},
		{"insert audit", `INSERT INTO audit_events (action, target_type, target_id, initiative_id) VALUES ('x', 'y', 'z', uuidv7())`, ""},
		{"select audit", `SELECT count(*) FROM audit_events`, ""},
		{"update audit", `UPDATE audit_events SET action = 'tampered'`, codeInsufficientPrivs},
		{"delete audit", `DELETE FROM audit_events`, codeInsufficientPrivs},
		{"truncate audit", `TRUNCATE audit_events`, codeInsufficientPrivs},
		{"select denials", `SELECT count(*) FROM access_denials`, ""},
		{"insert denials", `INSERT INTO access_denials (host, reason) VALUES ('h', 'r')`, codeInsufficientPrivs},
		{"select sessions", `SELECT id, user_id, created_at, last_seen_at, expires_at, ip, user_agent FROM sessions`, ""},
		{"select sessions token_hash", `SELECT token_hash FROM sessions`, codeInsufficientPrivs},
		{"select sessions star", `SELECT * FROM sessions`, codeInsufficientPrivs},
		{"delete sessions by id", `DELETE FROM sessions WHERE id = uuidv7()`, ""},
		{"delete sessions by user", `DELETE FROM sessions WHERE user_id = uuidv7() RETURNING id`, ""},
		{"delete sessions by token_hash", `DELETE FROM sessions WHERE token_hash = '\x00'`, codeInsufficientPrivs},
		{"select app_sessions", `SELECT id, session_id, app_id, host, created_at, expires_at FROM app_sessions`, ""},
		{"select app_sessions token_hash", `SELECT token_hash FROM app_sessions`, codeInsufficientPrivs},
		{"delete app_sessions by app", `DELETE FROM app_sessions WHERE app_id = 'app_X'`, ""},
		{"delete app_sessions by session", `DELETE FROM app_sessions WHERE session_id = uuidv7()`, ""},
		{"update sessions", `UPDATE sessions SET last_seen_at = now()`, codeInsufficientPrivs},
		{"select signing_keys", `SELECT count(*) FROM signing_keys`, codeInsufficientPrivs},
		{"update signing_keys", `UPDATE signing_keys SET state = 'active'`, codeInsufficientPrivs},
		{"select oauth_flows", `SELECT count(*) FROM oauth_flows`, codeInsufficientPrivs},
		{"select login_codes", `SELECT count(*) FROM login_codes`, codeInsufficientPrivs},
		{"select schema_migrations", `SELECT count(*) FROM schema_migrations`, codeInsufficientPrivs},
		{"create table", `CREATE TABLE rogue (x int)`, codeInsufficientPrivs},
	}
	for _, tt := range tests {
		sp, err := tx.Begin(ctx) // a failed statement aborts the transaction: use savepoints
		if err != nil {
			t.Fatal(err)
		}
		_, err = sp.Exec(ctx, tt.sql)
		_ = sp.Rollback(ctx)
		if got := pgErrCode(err); got != tt.want {
			t.Errorf("%s: code = %q (%v), want %q", tt.name, got, err, tt.want)
		}
	}
}

func TestPortalSchemaChanges(t *testing.T) {
	t.Parallel()
	pool := newTestDB(t)
	w := world{pool: pool}
	init := w.initiative(t, "platform", "active")
	app := w.app(t, init, "portal.example.test")

	if got := scalar[string](t, pool, `SELECT status FROM apps WHERE id = $1`, app); got != "active" {
		t.Fatalf("default app status = %q", got)
	}
	if err := exec(pool, `UPDATE apps SET status = 'deleted' WHERE id = $1`, app); pgErrCode(err) != "23514" {
		t.Fatalf("invalid app status: err = %v, want check_violation", err)
	}
	for _, status := range []string{"ok", "unprotected", "unexpected", "unreachable"} {
		mustExec(t, pool, `UPDATE deployments SET last_probe_status = $1, last_probe_at = now()`, status)
	}
	mustExec(t, pool, `UPDATE deployments SET last_probe_status = NULL`)
	if err := exec(pool, `UPDATE deployments SET last_probe_status = 'weird'`); pgErrCode(err) != "23514" {
		t.Fatalf("invalid probe status: err = %v, want check_violation", err)
	}
}

// Every table the portal role is not meant to touch is denied by default, so a future table is covered
// until someone grants it explicitly. PUBLIC has no table privileges and no function can be executed.
func TestPortalRoleDeniedByDefault(t *testing.T) {
	t.Parallel()
	pool := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	// Tables with any grant to the portal, and the privileges it may hold on each table-wide.
	allowed := map[string][]string{
		"groups": {"SELECT", "INSERT", "UPDATE", "DELETE"}, "group_members": {"SELECT", "INSERT", "UPDATE", "DELETE"},
		"initiatives": {"SELECT", "INSERT", "UPDATE", "DELETE"}, "initiative_roles": {"SELECT", "INSERT", "UPDATE", "DELETE"},
		"apps": {"SELECT", "INSERT", "UPDATE", "DELETE"}, "deployments": {"SELECT", "INSERT", "UPDATE", "DELETE"},
		"initiative_access": {"SELECT", "INSERT", "UPDATE", "DELETE"},
		"users":             {"SELECT"}, "platform_admins": {"SELECT"}, "audit_events": {"SELECT", "INSERT"},
		"access_denials": {"SELECT"}, "sessions": {"DELETE"}, "app_sessions": {"DELETE"},
	}
	rows, err := pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = 'public'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	if len(tables) == 0 {
		t.Fatal("no tables found")
	}
	for _, tbl := range tables {
		for _, priv := range []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"} {
			got := scalar[bool](t, pool, `SELECT has_table_privilege('platform_portal', $1, $2)`, tbl, priv)
			want := slices.Contains(allowed[tbl], priv)
			if got != want {
				t.Errorf("platform_portal %s on %s = %v, want %v", priv, tbl, got, want)
			}
			// PUBLIC (a role that is a member of nothing) has no table privileges either.
			if pub := scalar[bool](t, pool,
				`SELECT EXISTS (SELECT 1 FROM pg_class c, aclexplode(c.relacl) a
				   WHERE c.oid = $1::regclass AND a.grantee = 0 AND a.privilege_type = $2)`, tbl, priv); pub {
				t.Errorf("PUBLIC has %s on %s", priv, tbl)
			}
		}
	}
	for _, fn := range []string{"create_access_denials_partition(date)", "drop_expired_access_denials_partitions(integer)"} {
		if scalar[bool](t, pool, `SELECT has_function_privilege('platform_portal', $1, 'EXECUTE')`, fn) {
			t.Errorf("platform_portal can EXECUTE %s", fn)
		}
	}
}

// Telemetry columns of deployments must not flush the ForwardAuth caches; decision-relevant changes must.
func TestDeploymentsNotifyOnlyOnDecisionChanges(t *testing.T) {
	t.Parallel()
	pool := newTestDB(t)
	w := world{pool: pool}
	in := w.initiative(t, "i", "active")
	w.app(t, in, "a.example.test")
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	listener, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer listener.Release()
	if _, err := listener.Exec(ctx, "LISTEN access_changed"); err != nil {
		t.Fatalf("listen: %v", err)
	}
	// Notifications arrive in commit order and carry the table name. A sentinel on apps after the telemetry
	// update makes the negative check deterministic: the first notification must be the sentinel's.
	next := func() string {
		wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
		defer wcancel()
		n, err := listener.Conn().WaitForNotification(wctx)
		if err != nil {
			t.Fatalf("no notification: %v", err)
		}
		return n.Payload
	}
	mustExec(t, pool, `UPDATE deployments SET last_seen_at = now(), last_probe_at = now(), last_probe_status = 'ok'`)
	mustExec(t, pool, `UPDATE apps SET name = name`)
	if got := next(); got != "apps" {
		t.Fatalf("first notification payload = %q, want apps: telemetry update notified", got)
	}
	mustExec(t, pool, `UPDATE deployments SET host = 'b.example.test'`)
	if got := next(); got != "deployments" {
		t.Fatalf("host change payload = %q, want deployments", got)
	}
}
