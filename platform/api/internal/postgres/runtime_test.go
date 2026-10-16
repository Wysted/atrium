package postgres_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"platform/api/internal/audit"
	"platform/api/internal/postgres"
	"platform/api/internal/testdb"
)

// runtimePool opens a runtime-role pool over the same test database as owner.
func runtimePool(t *testing.T, owner *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	pool, _, err := postgres.OpenRuntimePool(ctx, owner.Config())
	if err != nil {
		t.Fatalf("open runtime pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// scalarStrings runs a query returning one text column and collects it.
func scalarStrings(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRuntimePoolRunsAsTheRuntimeRole(t *testing.T) {
	t.Parallel()
	owner := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	// The test connection (owner role, superuser in the compose database) must be refused.
	if _, err := postgres.CheckRuntimeRole(ctx, owner); err == nil || !strings.Contains(err.Error(), "refusing to run") {
		t.Fatalf("owner pool accepted as runtime: %v", err)
	}

	pool, id, err := postgres.OpenRuntimePool(ctx, owner.Config())
	if err != nil {
		t.Fatalf("OpenRuntimePool: %v", err)
	}
	defer pool.Close()
	if id.CurrentUser != postgres.RuntimeRole {
		t.Fatalf("identity = %+v", id)
	}
	// Every connection of the pool, including ones created later, has the role from the start.
	for range 3 {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var cur string
		if err := conn.QueryRow(ctx, "SELECT current_user::text").Scan(&cur); err != nil || cur != postgres.RuntimeRole {
			t.Fatalf("current_user = %q, %v", cur, err)
		}
		defer conn.Release()
	}

	// The effective role cannot touch audit_events beyond append, nor create objects.
	if err := audit.Recorder(postgres.NewAuditRepo(pool)).RecordAudit(ctx, audit.Event{
		Action: "test.event", TargetType: "test", TargetID: "1", After: map[string]string{"k": "v"},
	}); err != nil {
		t.Fatalf("runtime role cannot append to audit_events: %v", err)
	}
	for _, stmt := range []string{
		`UPDATE audit_events SET action = 'tampered'`,
		`DELETE FROM audit_events`,
		`TRUNCATE audit_events`,
		`CREATE TABLE rogue (x int)`,
		`UPDATE signing_keys SET public_jwk = '{}'`,
		`SELECT * FROM schema_migrations`,
	} {
		if _, err := pool.Exec(ctx, stmt); pgErrCode(err) != codeInsufficientPrivs {
			t.Errorf("%q as runtime: err = %v, want insufficient_privilege", stmt, err)
		}
	}
	if n := scalar[int](t, owner, `SELECT count(*) FROM audit_events WHERE action = 'test.event'`); n != 1 {
		t.Fatalf("audit rows = %d", n)
	}
}

// A runtime role whose privileges drift from the expected set, in either direction, is refused at startup.
func TestCheckRuntimeRoleRefusesDrift(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, sql, want string
	}{
		{"update on audit_events", `GRANT UPDATE ON audit_events TO platform_runtime`, "audit_events"},
		{"create in public", `GRANT CREATE ON SCHEMA public TO platform_runtime`, "create objects"},
		{"update of users.status", `GRANT UPDATE (status) ON users TO platform_runtime`, "UPDATE ON users.status"},
		{"table-wide update on users", `GRANT UPDATE ON users TO platform_runtime`, "UPDATE ON users"},
		{"delete on users", `GRANT DELETE ON users TO platform_runtime`, "DELETE ON users"},
		{"insert on initiatives", `GRANT INSERT ON initiatives TO platform_runtime`, "INSERT ON initiatives"},
		{"update on initiative_access", `GRANT UPDATE ON initiative_access TO platform_runtime`, "UPDATE ON initiative_access"},
		{"delete on group_members", `GRANT DELETE ON group_members TO platform_runtime`, "DELETE ON group_members"},
		{"select on groups", `GRANT SELECT ON groups TO platform_runtime`, "SELECT ON groups"},
		{"select on platform_admins", `GRANT SELECT ON platform_admins TO platform_runtime`, "SELECT ON platform_admins"},
		{"insert on platform_admins", `GRANT INSERT ON platform_admins TO platform_runtime`, "INSERT ON platform_admins"},
		{"update of deployments.host", `GRANT UPDATE (host) ON deployments TO platform_runtime`, "UPDATE ON deployments.host"},
		{"insert on deployments", `GRANT INSERT ON deployments TO platform_runtime`, "INSERT ON deployments"},
		{"delete on apps", `GRANT DELETE ON apps TO platform_runtime`, "DELETE ON apps"},
		{"update of sessions.expires_at", `GRANT UPDATE (expires_at) ON sessions TO platform_runtime`, "UPDATE ON sessions.expires_at"},
		{"update on app_sessions", `GRANT UPDATE ON app_sessions TO platform_runtime`, "UPDATE ON app_sessions"},
		{"update of login_codes.expires_at", `GRANT UPDATE (expires_at) ON login_codes TO platform_runtime`, "UPDATE ON login_codes.expires_at"},
		{"references column on users", `GRANT REFERENCES (id) ON users TO platform_runtime`, "REFERENCES ON users.id"},
		{"truncate on sessions", `GRANT TRUNCATE ON sessions TO platform_runtime`, "TRUNCATE ON sessions"},
		{"trigger on apps", `GRANT TRIGGER ON apps TO platform_runtime`, "TRIGGER ON apps"},
		{"delete on signing_keys", `GRANT DELETE ON signing_keys TO platform_runtime`, "DELETE ON signing_keys"},
		{"update of signing_keys.kid", `GRANT UPDATE (kid) ON signing_keys TO platform_runtime`, "UPDATE ON signing_keys.kid"},
		{"delete on access_denials", `GRANT DELETE ON access_denials TO platform_runtime`, "DELETE ON access_denials"},
		{"select on schema_migrations", `GRANT SELECT ON schema_migrations TO platform_runtime`, "SELECT ON schema_migrations"},
		{"truncate on access_denials", `GRANT TRUNCATE ON access_denials TO platform_runtime`, "TRUNCATE ON access_denials"},
		{"maintain on sessions", `GRANT MAINTAIN ON sessions TO platform_runtime`, "MAINTAIN ON sessions"},
		{"grant option on apps", `GRANT SELECT ON apps TO platform_runtime WITH GRANT OPTION`, "SELECT WITH GRANT OPTION ON apps"},
		{"grant option on a column", `GRANT UPDATE (last_seen_at) ON sessions TO platform_runtime WITH GRANT OPTION`,
			"UPDATE WITH GRANT OPTION ON sessions"},
		{"select on a view", `CREATE VIEW rogue_view AS SELECT 1 AS x; GRANT SELECT ON rogue_view TO platform_runtime`, "SELECT ON rogue_view"},
		{"select on a materialized view", `CREATE MATERIALIZED VIEW rogue_mv AS SELECT 1 AS x; GRANT SELECT ON rogue_mv TO platform_runtime`,
			"SELECT ON rogue_mv"},
		{"usage on a sequence", `CREATE SEQUENCE rogue_seq; GRANT USAGE ON SEQUENCE rogue_seq TO platform_runtime`, "USAGE ON rogue_seq"},
		{"select on a sequence", `CREATE SEQUENCE rogue_seq; GRANT SELECT ON SEQUENCE rogue_seq TO platform_runtime`, "SELECT ON rogue_seq"},
		{"select on an unknown table", `CREATE TABLE rogue (x int); GRANT SELECT ON rogue TO platform_runtime`, "SELECT ON rogue"},
		{"delete on a denials partition", `SELECT create_access_denials_partition((current_date + interval '2 months')::date);
			DO $$ BEGIN EXECUTE format('GRANT DELETE ON %I TO platform_runtime',
				'access_denials_' || to_char(current_date + interval '2 months', 'YYYY_MM')); END $$`,
			"DELETE ON access_denials_"},
		{"missing select on apps", `REVOKE SELECT ON apps FROM platform_runtime`, "missing privilege SELECT ON apps"},
		{"missing update of sessions.last_seen_at", `REVOKE UPDATE (last_seen_at) ON sessions FROM platform_runtime`,
			"missing privilege UPDATE ON sessions.last_seen_at"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner := newTestDB(t)
			mustExec(t, owner, tt.sql)
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			_, _, err := postgres.OpenRuntimePool(ctx, owner.Config())
			if err == nil || !strings.Contains(err.Error(), "refusing to run") || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want a refusal mentioning %q", err, tt.want)
			}
		})
	}
}

// withRoleTx runs fn on a transaction of owner where setup has been applied as the superuser and the session then
// assumed role. Role membership is cluster-wide, so doing it inside a transaction that is rolled back keeps
// the parallel tests (which all connect as the runtime and portal roles) unaffected.
func withRoleTx(t *testing.T, owner *pgxpool.Pool, role string, setup []string, fn func(context.Context, pgx.Tx)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	tx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // best effort
	for _, stmt := range setup {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+role); err != nil {
		t.Fatal(err)
	}
	fn(ctx, tx)
}

// Membership of a predefined pg_* role, direct or inherited through another role, is refused for both roles.
func TestRoleChecksRefusePredefinedRoleMembership(t *testing.T) {
	t.Parallel()
	owner := newTestDB(t)
	checks := map[string]func(context.Context, pgx.Tx) error{
		postgres.RuntimeRole: func(ctx context.Context, tx pgx.Tx) error { _, err := postgres.CheckRuntimeRole(ctx, tx); return err },
		postgres.PortalRole:  func(ctx context.Context, tx pgx.Tx) error { _, err := postgres.CheckPortalRole(ctx, tx); return err },
	}
	for role, check := range checks {
		for _, tt := range []struct {
			name  string
			setup []string
			want  string
		}{
			{"none", nil, ""},
			{"pg_maintain", []string{"GRANT pg_maintain TO " + role}, "pg_maintain"},
			{"pg_read_all_data", []string{"GRANT pg_read_all_data TO " + role}, "pg_read_all_data"},
			{"pg_write_all_data", []string{"GRANT pg_write_all_data TO " + role}, "pg_write_all_data"},
			{"pg_read_server_files", []string{"GRANT pg_read_server_files TO " + role}, "pg_read_server_files"},
			{"pg_execute_server_program", []string{"GRANT pg_execute_server_program TO " + role}, "pg_execute_server_program"},
			{"pg_signal_backend", []string{"GRANT pg_signal_backend TO " + role}, "pg_signal_backend"},
			{"pg_monitor", []string{"GRANT pg_monitor TO " + role}, "pg_monitor"},
			{"inherited", []string{"CREATE ROLE tmp_intermediate NOLOGIN", "GRANT pg_write_all_data TO tmp_intermediate",
				"GRANT tmp_intermediate TO " + role}, "pg_write_all_data"},
		} {
			t.Run(role+"/"+tt.name, func(t *testing.T) {
				withRoleTx(t, owner, role, tt.setup, func(ctx context.Context, tx pgx.Tx) {
					err := check(ctx, tx)
					switch {
					case tt.want == "" && err != nil:
						t.Fatalf("baseline refused: %v", err)
					case tt.want != "" && (err == nil || !strings.Contains(err.Error(), "predefined roles") || !strings.Contains(err.Error(), tt.want)):
						t.Fatalf("err = %v, want a refusal naming %s", err, tt.want)
					}
				})
			})
		}
	}
}

// The expected privilege set of platform_runtime, written out independently of the check: every table x
// privilege (table-wide and per column) against the catalog, and what the role may and may not do by statement.
func TestRuntimeRolePrivileges(t *testing.T) {
	t.Parallel()
	owner := newTestDB(t)
	w := world{pool: owner}
	in := w.initiative(t, "platform", "active")
	w.app(t, in, "portal.example.test")

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	conn, err := owner.Acquire(ctx)
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

	// allowed: the statement must get past the privilege check (it may still fail on data, which is fine);
	// denied: it must fail with insufficient_privilege.
	tests := []struct {
		name    string
		sql     string
		allowed bool
	}{
		{"select users", `SELECT id, status, email FROM users`, true},
		{"insert user", `INSERT INTO users (google_sub, email, name) VALUES ('s-1', 'a@example.test', 'n')`, true},
		{"login upsert", `INSERT INTO users (google_sub, email, name) VALUES ('s-1', 'a@example.test', 'n')
			ON CONFLICT (google_sub) DO UPDATE SET email = EXCLUDED.email, name = EXCLUDED.name, updated_at = now()
			RETURNING id, status`, true},
		{"update users.email", `UPDATE users SET email = 'x@example.test'`, true},
		{"update users.name", `UPDATE users SET name = 'x'`, true},
		{"update users.updated_at", `UPDATE users SET updated_at = now()`, true},
		{"update users.status", `UPDATE users SET status = 'disabled'`, false},
		{"update users.google_sub", `UPDATE users SET google_sub = 'forged'`, false},
		{"update users.email with a where on status", `UPDATE users SET email = 'x@example.test' WHERE status = 'active'`, true},
		{"delete users", `DELETE FROM users`, false},

		{"select groups", `SELECT * FROM groups`, false},
		{"insert groups", `INSERT INTO groups (key) VALUES ('g')`, false},
		{"select platform_admins", `SELECT * FROM platform_admins`, false},
		{"insert platform_admins", `INSERT INTO platform_admins (user_id) VALUES (uuidv7())`, false},

		{"select group_members", `SELECT * FROM group_members`, true},
		{"select initiatives", `SELECT * FROM initiatives`, true},
		{"select initiative_roles", `SELECT * FROM initiative_roles`, true},
		{"select apps", `SELECT * FROM apps`, true},
		{"select initiative_access", `SELECT * FROM initiative_access`, true},
		{"insert group_members", `INSERT INTO group_members (group_id, user_id) VALUES (uuidv7(), uuidv7())`, false},
		{"update group_members", `UPDATE group_members SET user_id = user_id`, false},
		{"delete group_members", `DELETE FROM group_members`, false},
		{"insert initiatives", `INSERT INTO initiatives (key, name) VALUES ('i', 'I')`, false},
		{"update initiatives", `UPDATE initiatives SET status = 'inactive'`, false},
		{"delete initiatives", `DELETE FROM initiatives`, false},
		{"insert initiative_roles", `INSERT INTO initiative_roles (initiative_id, key) VALUES (uuidv7(), 'r')`, false},
		{"update initiative_roles", `UPDATE initiative_roles SET key = 'r'`, false},
		{"delete initiative_roles", `DELETE FROM initiative_roles`, false},
		{"update apps", `UPDATE apps SET status = 'disabled'`, false},
		{"delete apps", `DELETE FROM apps`, false},
		{"update initiative_access", `UPDATE initiative_access SET expires_at = now()`, false},
		{"delete initiative_access", `DELETE FROM initiative_access`, false},

		{"select deployments", `SELECT id, host, app_id, last_seen_at FROM deployments`, true},
		{"update deployments.last_seen_at", `UPDATE deployments SET last_seen_at = now()`, true},
		{"lock deployments skip locked", `SELECT 1 FROM deployments FOR NO KEY UPDATE SKIP LOCKED`, true},
		{"update deployments.host", `UPDATE deployments SET host = 'other.example.test'`, false},
		{"update deployments.app_id", `UPDATE deployments SET app_id = app_id`, false},
		{"update deployments.last_probe_status", `UPDATE deployments SET last_probe_status = 'ok'`, false},
		{"update deployments.last_probe_at", `UPDATE deployments SET last_probe_at = now()`, false},
		{"insert deployments", `INSERT INTO deployments (host, app_id) VALUES ('x.example.test', 'app_x')`, false},
		{"delete deployments", `DELETE FROM deployments`, false},

		{"select sessions", `SELECT token_hash, id, last_seen_at FROM sessions`, true},
		{"insert sessions", `INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ('\x00'::bytea, uuidv7(), now())`, true},
		{"update sessions.last_seen_at", `UPDATE sessions SET last_seen_at = now()`, true},
		{"update sessions.expires_at", `UPDATE sessions SET expires_at = now()`, false},
		{"update sessions.user_id", `UPDATE sessions SET user_id = user_id`, false},
		{"delete sessions", `DELETE FROM sessions`, true},
		{"select app_sessions", `SELECT * FROM app_sessions`, true},
		{"insert app_sessions", `INSERT INTO app_sessions (token_hash, session_id, app_id, host, expires_at)
			VALUES ('\x00'::bytea, uuidv7(), 'app_x', 'x.example.test', now())`, true},
		{"update app_sessions", `UPDATE app_sessions SET expires_at = now()`, false},
		{"delete app_sessions", `DELETE FROM app_sessions`, true},

		{"select login_codes", `SELECT * FROM login_codes`, true},
		{"update login_codes.consumed_at", `UPDATE login_codes SET consumed_at = now()`, true},
		{"update login_codes.expires_at", `UPDATE login_codes SET expires_at = now()`, false},
		{"delete login_codes", `DELETE FROM login_codes`, true},
		{"select oauth_flows", `SELECT * FROM oauth_flows`, true},
		{"update oauth_flows.consumed_at", `UPDATE oauth_flows SET consumed_at = now()`, true},
		{"update oauth_flows.expires_at", `UPDATE oauth_flows SET expires_at = now()`, false},
		{"delete oauth_flows", `DELETE FROM oauth_flows`, true},

		{"select signing_keys", `SELECT * FROM signing_keys`, true},
		{"update signing_keys.state", `UPDATE signing_keys SET state = 'active'`, true},
		{"update signing_keys.public_jwk", `UPDATE signing_keys SET public_jwk = '{}'`, false},
		{"delete signing_keys", `DELETE FROM signing_keys`, false},
		{"select audit_events", `SELECT * FROM audit_events`, true},
		{"insert audit_events", `INSERT INTO audit_events (action, target_type, target_id) VALUES ('x', 'y', 'z')`, true},
		{"update audit_events", `UPDATE audit_events SET action = 'tampered'`, false},
		{"insert access_denials", `INSERT INTO access_denials (host, reason) VALUES ('h', 'r')`, true},
		{"delete access_denials", `DELETE FROM access_denials`, false},
		{"select schema_migrations", `SELECT * FROM schema_migrations`, false},
		{"create table", `CREATE TABLE rogue (x int)`, false},
	}
	for _, tt := range tests {
		sp, err := tx.Begin(ctx) // a failed statement aborts the transaction: use savepoints
		if err != nil {
			t.Fatal(err)
		}
		_, err = sp.Exec(ctx, tt.sql)
		_ = sp.Rollback(ctx)
		if denied := pgErrCode(err) == codeInsufficientPrivs; denied == tt.allowed {
			t.Errorf("%s: err = %v, want allowed=%v", tt.name, err, tt.allowed)
		}
	}
}

// Every table x privilege and every column x privilege of schema public, against an independently written
// expectation. A table that is not listed must be denied everything, so a future table is covered until it is
// granted explicitly.
func TestRuntimeRoleDeniedByDefault(t *testing.T) {
	t.Parallel()
	pool := newTestDB(t)

	type grant struct {
		table   []string            // table-wide privileges
		columns map[string][]string // privilege -> columns granted one by one
	}
	allowed := map[string]grant{
		"group_members": {table: []string{"SELECT"}}, "initiatives": {table: []string{"SELECT"}},
		"initiative_roles": {table: []string{"SELECT"}}, "apps": {table: []string{"SELECT"}},
		"initiative_access": {table: []string{"SELECT"}},
		"users":             {table: []string{"SELECT", "INSERT"}, columns: map[string][]string{"UPDATE": {"email", "name", "updated_at"}}},
		"sessions":          {table: []string{"SELECT", "INSERT", "DELETE"}, columns: map[string][]string{"UPDATE": {"last_seen_at"}}},
		"app_sessions":      {table: []string{"SELECT", "INSERT", "DELETE"}},
		"login_codes":       {table: []string{"SELECT", "INSERT", "DELETE"}, columns: map[string][]string{"UPDATE": {"consumed_at"}}},
		"oauth_flows":       {table: []string{"SELECT", "INSERT", "DELETE"}, columns: map[string][]string{"UPDATE": {"consumed_at"}}},
		"deployments":       {table: []string{"SELECT"}, columns: map[string][]string{"UPDATE": {"last_seen_at"}}},
		"signing_keys": {table: []string{"SELECT", "INSERT"},
			columns: map[string][]string{"UPDATE": {"state", "activated_at", "retired_at", "removed_at", "private_enc"}}},
		"audit_events": {table: []string{"SELECT", "INSERT"}}, "access_denials": {table: []string{"SELECT", "INSERT"}},
	}

	tables := scalarStrings(t, pool, `SELECT tablename::text FROM pg_tables WHERE schemaname = 'public'`)
	if len(tables) == 0 {
		t.Fatal("no tables found")
	}
	for tbl := range allowed {
		if !slices.Contains(tables, tbl) {
			t.Errorf("expected table %s does not exist", tbl)
		}
	}
	for _, tbl := range tables {
		g := allowed[tbl]
		for _, priv := range []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"} {
			got := scalar[bool](t, pool, `SELECT has_table_privilege('platform_runtime', $1, $2)`, tbl, priv)
			if want := slices.Contains(g.table, priv); got != want {
				t.Errorf("platform_runtime %s on %s = %v, want %v", priv, tbl, got, want)
			}
		}
		for _, col := range scalarStrings(t, pool,
			`SELECT attname::text FROM pg_attribute WHERE attrelid = $1::regclass AND attnum > 0 AND NOT attisdropped`, tbl) {
			for _, priv := range []string{"SELECT", "INSERT", "UPDATE", "REFERENCES"} {
				got := scalar[bool](t, pool, `SELECT has_column_privilege('platform_runtime', $1, $2, $3)`, tbl, col, priv)
				want := slices.Contains(g.table, priv) || slices.Contains(g.columns[priv], col)
				if got != want {
					t.Errorf("platform_runtime %s on %s.%s = %v, want %v", priv, tbl, col, got, want)
				}
			}
		}
		// PUBLIC has no table privileges either.
		if scalar[bool](t, pool, `SELECT EXISTS (SELECT 1 FROM pg_class c, aclexplode(c.relacl) a
			WHERE c.oid = $1::regclass AND a.grantee = 0)`, tbl) {
			t.Errorf("PUBLIC holds a privilege on %s", tbl)
		}
	}
	// Only the two partition functions may be executed, and only they exist as callable surface.
	for _, fn := range []string{"create_access_denials_partition(date)", "drop_expired_access_denials_partitions(integer)"} {
		if !scalar[bool](t, pool, `SELECT has_function_privilege('platform_runtime', $1, 'EXECUTE')`, fn) {
			t.Errorf("platform_runtime cannot EXECUTE %s", fn)
		}
	}
}

// Retention (365 days) runs under the runtime role through the SECURITY DEFINER function.
func TestHousekeepingDropsExpiredDenialPartitions(t *testing.T) {
	t.Parallel()
	owner := newTestDB(t)
	rt := runtimePool(t, owner)

	now := time.Now().UTC()
	month := func(offset int) time.Time {
		return time.Date(now.Year(), now.Month()+time.Month(offset), 1, 0, 0, 0, 0, time.UTC)
	}
	// Partitions created by hand as the owner (the runtime function refuses months this old on purpose).
	partition := func(name string, from, to time.Time) {
		mustExec(t, owner, fmt.Sprintf(`CREATE TABLE %s PARTITION OF access_denials FOR VALUES FROM ('%s') TO ('%s')`,
			name, from.Format(time.RFC3339), to.Format(time.RFC3339)))
	}
	oldName := "access_denials_" + month(-14).Format("2006_01")
	keptName := "access_denials_" + month(-10).Format("2006_01")
	partition(oldName, month(-14), month(-13))
	partition(keptName, month(-10), month(-9))
	partition("access_denials_legacy", time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC))
	mustExec(t, owner, `INSERT INTO access_denials (occurred_at, host, reason) VALUES ($1, 'old', 'r'), ($2, 'kept', 'r'), (now(), 'now', 'r')`,
		month(-14).Add(time.Hour), month(-10).Add(time.Hour))

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	rep, err := newJob(t, rt, 0).RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce as runtime role: %v", err)
	}
	if len(rep.PartitionsDropped) != 1 || rep.PartitionsDropped[0] != oldName {
		t.Fatalf("dropped = %v, want [%s]", rep.PartitionsDropped, oldName)
	}
	exists := func(name string) bool {
		return scalar[bool](t, owner, `SELECT to_regclass($1) IS NOT NULL`, "public."+name)
	}
	if exists(oldName) || !exists(keptName) || !exists("access_denials_legacy") {
		t.Fatalf("old exists=%v kept exists=%v legacy exists=%v", exists(oldName), exists(keptName), exists("access_denials_legacy"))
	}
	if n := scalar[int](t, owner, `SELECT count(*) FROM access_denials WHERE host IN ('kept', 'now')`); n != 2 {
		t.Fatalf("surviving rows = %d, want 2", n)
	}
	again, err := newJob(t, rt, 0).RunOnce(ctx)
	if err != nil || len(again.PartitionsDropped) != 0 {
		t.Fatalf("second run = %+v, %v", again, err)
	}
}

func TestDropExpiredPartitionsRefusesShortRetention(t *testing.T) {
	t.Parallel()
	rt := runtimePool(t, newTestDB(t))
	for _, days := range []int{0, 1, 29, -5} {
		err := exec(rt, `SELECT drop_expired_access_denials_partitions($1)`, days)
		if pgErrCode(err) != codeInvalidParameter {
			t.Errorf("retention %d days: err = %v, want invalid_parameter_value", days, err)
		}
	}
}

// If dropping fails (here: a reader holds a lock on the old partition), the purges still commit and the
// error is reported; the next run retries.
func TestHousekeepingRetentionFailureKeepsThePurges(t *testing.T) {
	t.Parallel()
	owner := newTestDB(t)
	f := seedHousekeeping(t, owner)
	rt := runtimePool(t, owner)

	old := time.Date(time.Now().UTC().Year()-2, 1, 1, 0, 0, 0, 0, time.UTC)
	mustExec(t, owner, fmt.Sprintf(`CREATE TABLE access_denials_%s PARTITION OF access_denials FOR VALUES FROM ('%s') TO ('%s')`,
		old.Format("2006_01"), old.Format(time.RFC3339), old.AddDate(0, 1, 0).Format(time.RFC3339)))

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	holder, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // best effort
	if _, err := holder.Exec(ctx, fmt.Sprintf(`LOCK TABLE access_denials_%s IN ACCESS SHARE MODE`, old.Format("2006_01"))); err != nil {
		t.Fatal(err)
	}

	_, err = newJob(t, rt, 0).RunOnce(ctx)
	if err == nil || !strings.Contains(err.Error(), "drop expired partitions") {
		t.Fatalf("err = %v, want the retention failure", err)
	}
	if n := scalar[int](t, owner, `SELECT count(*) FROM sessions WHERE id IN ($1, $2)`, f.expiredSession, f.idleSession); n != 0 {
		t.Fatalf("purges rolled back with the retention failure: %d dead sessions left", n)
	}
	_ = holder.Rollback(ctx) // release the lock
	if rep, err := newJob(t, rt, 0).RunOnce(ctx); err != nil || len(rep.PartitionsDropped) != 1 {
		t.Fatalf("retry = %+v, %v", rep, err)
	}
}

// The login role (session_user) is held to the same rule: RESET ROLE would hand its memberships back. The
// legitimate membership (login role -> group role) is not flagged.
func TestLoginRoleProblemsPredefinedRoleMembership(t *testing.T) {
	t.Parallel()
	owner := newTestDB(t)
	checks := map[string]func(context.Context, pgx.Tx) (postgres.RuntimeIdentity, error){
		postgres.RuntimeRole: func(ctx context.Context, tx pgx.Tx) (postgres.RuntimeIdentity, error) {
			return postgres.CheckRuntimeRole(ctx, tx)
		},
		postgres.PortalRole: func(ctx context.Context, tx pgx.Tx) (postgres.RuntimeIdentity, error) {
			return postgres.CheckPortalRole(ctx, tx)
		},
	}
	for role, check := range checks {
		for _, tt := range []struct {
			name  string
			extra []string
			want  string
		}{
			{"group role only", nil, ""},
			{"direct", []string{"GRANT pg_read_all_data TO tmp_login"}, "pg_read_all_data"},
			{"inherited", []string{"CREATE ROLE tmp_mid NOLOGIN", "GRANT pg_monitor TO tmp_mid", "GRANT tmp_mid TO tmp_login"}, "pg_monitor"},
		} {
			t.Run(role+"/"+tt.name, func(t *testing.T) {
				// Superuser setup first; the session user only changes once every grant is in place.
				setup := slices.Concat([]string{"CREATE ROLE tmp_login LOGIN", "GRANT " + role + " TO tmp_login"}, tt.extra,
					[]string{"SET LOCAL SESSION AUTHORIZATION tmp_login"})
				withRoleTx(t, owner, role, setup, func(ctx context.Context, tx pgx.Tx) {
					id, err := check(ctx, tx)
					if err != nil {
						t.Fatalf("check: %v", err)
					}
					got := strings.Join(id.LoginRoleProblems(), "; ")
					switch {
					case tt.want == "" && got != "":
						t.Fatalf("problems = %q, want none", got)
					case tt.want != "" && !strings.Contains(got, tt.want):
						t.Fatalf("problems = %q, want one naming %s", got, tt.want)
					}
				})
			})
		}
	}
}

// Membership of the table owner that cannot inherit its privileges (INHERIT FALSE) still allows SET ROLE to it,
// so it is refused both for the effective role and for the login role.
func TestRoleChecksRefuseNonInheritingOwnerMembership(t *testing.T) {
	t.Parallel()
	owner := newTestDB(t)
	tableOwner := scalar[string](t, owner, `SELECT tableowner FROM pg_tables WHERE schemaname = 'public' AND tablename = 'apps'`)
	const grant = "GRANT %s TO %s WITH INHERIT FALSE, SET TRUE"
	for _, role := range []string{postgres.RuntimeRole, postgres.PortalRole} {
		check := func(ctx context.Context, tx pgx.Tx) (postgres.RuntimeIdentity, error) {
			if role == postgres.RuntimeRole {
				return postgres.CheckRuntimeRole(ctx, tx)
			}
			return postgres.CheckPortalRole(ctx, tx)
		}
		t.Run(role+"/effective", func(t *testing.T) {
			withRoleTx(t, owner, role, []string{fmt.Sprintf(grant, tableOwner, role)}, func(ctx context.Context, tx pgx.Tx) {
				_, err := check(ctx, tx)
				if err == nil || !strings.Contains(err.Error(), "refusing to run") || !strings.Contains(err.Error(), "owns (or inherits the owner of)") {
					t.Fatalf("err = %v, want a refusal for owner membership", err)
				}
			})
		})
		t.Run(role+"/login", func(t *testing.T) {
			setup := []string{"CREATE ROLE tmp_login LOGIN", "GRANT " + role + " TO tmp_login", fmt.Sprintf(grant, tableOwner, "tmp_login"),
				"SET LOCAL SESSION AUTHORIZATION tmp_login"}
			withRoleTx(t, owner, role, setup, func(ctx context.Context, tx pgx.Tx) {
				id, err := check(ctx, tx)
				if err != nil {
					t.Fatalf("check: %v", err)
				}
				if got := strings.Join(id.LoginRoleProblems(), "; "); !strings.Contains(got, "owns (or inherits the owner of)") {
					t.Fatalf("problems = %q, want the owner membership flagged", got)
				}
			})
		})
	}
}

// Partitions have no ACL of their own (create_access_denials_partition, hand-made ones, sub-partitioned ones):
// they must not count as drift of either role.
func TestRoleChecksAcceptPartitionsWithoutOwnACL(t *testing.T) {
	t.Parallel()
	owner := newTestDB(t)
	mustExec(t, owner, `SELECT create_access_denials_partition((current_date + interval '1 month')::date)`)
	start := time.Now().UTC().AddDate(5, 0, 0)
	from := time.Date(start.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	mid := from.AddDate(0, 6, 0)
	mustExec(t, owner, fmt.Sprintf(`CREATE TABLE access_denials_sub PARTITION OF access_denials
		FOR VALUES FROM ('%s') TO ('%s') PARTITION BY RANGE (occurred_at);
		CREATE TABLE access_denials_sub_leaf PARTITION OF access_denials_sub FOR VALUES FROM ('%s') TO ('%s')`,
		from.Format(time.RFC3339), from.AddDate(1, 0, 0).Format(time.RFC3339), from.Format(time.RFC3339), mid.Format(time.RFC3339)))

	rt := testdb.Runtime(t, owner)
	pt := testdb.Portal(t, owner)
	if err := rt.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := pt.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}
