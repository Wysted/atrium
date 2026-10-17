package postgres

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RuntimeRole is the least-privilege role "serve" runs as (see the grants of migrations 0003, 0005, 0007, 0008, 0010).
const RuntimeRole = "platform_runtime"

// RuntimeIdentity is what the database reports about the connection a runtime pool actually got.
type RuntimeIdentity struct {
	CurrentUser     string // effective role (after SET ROLE)
	SessionUser     string // role that authenticated
	SessionIsSuper  bool   // the login role could RESET ROLE back to superuser powers
	SessionOwnsData bool   // the login role owns (or inherits the owner of) tables

	SessionBypassRLS bool // the login role bypasses row-level security
	SessionCanCreate bool // the login role can create objects in schema public

	SessionPredefinedRoles []string // pg_* roles the login role is a member of, directly or not
}

// NewRuntimePool builds the pool "serve" uses from the standard PG* variables, as RuntimeRole, and refuses
// to return it unless CheckRuntimeRole passes. Migrations keep using NewPool (the owner role).
func NewRuntimePool(ctx context.Context) (*pgxpool.Pool, RuntimeIdentity, error) {
	cfg, err := pgxpool.ParseConfig("")
	if err != nil {
		return nil, RuntimeIdentity{}, fmt.Errorf("parse pg config: %w", err)
	}
	return OpenRuntimePool(ctx, cfg)
}

// OpenRuntimePool opens a pool from cfg whose every connection runs as RuntimeRole.
//
// The role is a startup parameter (the libpq equivalent of options=-c role=platform_runtime), so the server
// applies it while the connection is being established: there is no window in which a connection exists
// with the login role's powers, it also holds for connections the pool re-creates and for the hijacked
// LISTEN connections, and a login role that may not SET ROLE fails to connect with a clear error. An
// AfterConnect "SET ROLE" would run after the connection is already usable and, if forgotten on one code
// path (Hijack, a second pool), silently leave a privileged connection.
func OpenRuntimePool(ctx context.Context, cfg *pgxpool.Config) (*pgxpool.Pool, RuntimeIdentity, error) {
	return openRolePool(ctx, cfg, RuntimeRole, CheckRuntimeRole)
}

// openRolePool opens a pool whose connections assume role as a startup parameter and refuses to return it
// unless check passes.
func openRolePool(ctx context.Context, cfg *pgxpool.Config, role string,
	check func(context.Context, querier) (RuntimeIdentity, error),
) (*pgxpool.Pool, RuntimeIdentity, error) {
	cfg = cfg.Copy()
	params := make(map[string]string, len(cfg.ConnConfig.RuntimeParams)+1)
	maps.Copy(params, cfg.ConnConfig.RuntimeParams)
	params["role"] = role
	cfg.ConnConfig.RuntimeParams = params

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, RuntimeIdentity{}, err
	}
	id, err := check(ctx, pool)
	if err != nil {
		pool.Close()
		return nil, RuntimeIdentity{}, err
	}
	return pool, id, nil
}

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// querier is a rowQuerier that can also return several rows (satisfied by *pgxpool.Pool).
type querier interface {
	rowQuerier
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// CheckRuntimeRole verifies that db runs with the least-privilege runtime role and refuses anything that
// would silently void the grants of the schema: a different effective role, a superuser, a role that
// bypasses row security, a role that owns (or inherits the owner of) tables, one that can create objects in
// schema public, one that can modify audit_events (append-only) or keep its history by truncating it, one that is a
// member (direct or inherited) of any predefined pg_* role, or one whose table, column and sequence privileges (grant options included, partitions held to their parent)
// differ from runtimeGrants in any direction. The check runs once per pool, at start-up.
func CheckRuntimeRole(ctx context.Context, db querier) (RuntimeIdentity, error) {
	f, err := loadRoleFacts(ctx, db)
	if err != nil {
		return RuntimeIdentity{}, err
	}
	problems := f.problems(RuntimeRole)
	drift, err := privilegeDrift(ctx, db, runtimeGrants)
	if err != nil {
		return RuntimeIdentity{}, err
	}
	for _, d := range drift {
		problems = append(problems, fmt.Errorf("role %q: %s", f.CurrentUser, d))
	}
	if len(problems) > 0 {
		return f.RuntimeIdentity, refusal(RuntimeRole, problems)
	}
	return f.RuntimeIdentity, nil
}

// tableGrant is the exact set of privileges a role holds on one table.
type tableGrant struct {
	// Table lists the table-wide privileges (SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER, MAINTAIN).
	Table []string
	// Columns maps a privilege (SELECT, INSERT, UPDATE, REFERENCES) to the columns it is granted on
	// column by column, without being table-wide.
	Columns map[string][]string
}

// runtimeGrants is the complete privilege set of RuntimeRole on the tables of schema public, as granted by
// migrations 0003, 0005, 0008, 0010 and 0012 and used by the repositories of this package. A table that is not
// listed must carry no privilege at all. Administration of the authorization tables belongs to PortalRole.
var runtimeGrants = map[string]tableGrant{
	// Read-only ForwardAuth facts.
	"group_members":     {Table: []string{"SELECT"}},
	"initiatives":       {Table: []string{"SELECT"}},
	"initiative_roles":  {Table: []string{"SELECT"}},
	"apps":              {Table: []string{"SELECT"}},
	"initiative_access": {Table: []string{"SELECT"}},
	// Login upsert: INSERT narrowed to its columns (so this role cannot create a robot or set a status, 0012) and
	// UPDATE of the informational fields only, never status or google_sub.
	"users": {Table: []string{"SELECT"}, Columns: map[string][]string{
		"INSERT": {"google_sub", "email", "name"}, "UPDATE": {"email", "name", "updated_at"}}},
	// API tokens (0012): ForwardAuth reads them and records last use; it cannot insert, revoke or delete them.
	"api_tokens": {Table: []string{"SELECT"}, Columns: map[string][]string{"UPDATE": {"last_used_at"}}},
	// Sessions are touched, never rewritten.
	"sessions":     {Table: []string{"SELECT", "INSERT", "DELETE"}, Columns: map[string][]string{"UPDATE": {"last_seen_at"}}},
	"app_sessions": {Table: []string{"SELECT", "INSERT", "DELETE"}},
	// One-time login state.
	"login_codes": {Table: []string{"SELECT", "INSERT", "DELETE"}, Columns: map[string][]string{"UPDATE": {"consumed_at"}}},
	"oauth_flows": {Table: []string{"SELECT", "INSERT", "DELETE"}, Columns: map[string][]string{"UPDATE": {"consumed_at"}}},
	// Last-seen telemetry; the row lock of SKIP LOCKED needs UPDATE on at least one column.
	"deployments": {Table: []string{"SELECT"}, Columns: map[string][]string{"UPDATE": {"last_seen_at"}}},
	// Keys: the rotator rewrites the lifecycle columns only (0005).
	"signing_keys": {Table: []string{"SELECT", "INSERT"}, Columns: map[string][]string{
		"UPDATE": {"state", "activated_at", "retired_at", "removed_at", "private_enc"}}},
	// Append-only (0008).
	"audit_events":   {Table: []string{"SELECT", "INSERT"}},
	"access_denials": {Table: []string{"SELECT", "INSERT"}},
}

// effectivePrivilegesSQL returns every privilege the current role holds on the relations of schema public:
// tables, partitioned tables, views, materialized views, foreign tables and sequences. Each row is
// (relation, root, column, privilege): table-wide privileges have an empty column, column-only ones (granted
// per column, not table-wide) carry the column name. A partition is reported under its own name with its
// top-level parent as root, so the caller can hold it to the parent's expectation. Every privilege is also
// probed WITH GRANT OPTION, which no role may hold. MAINTAIN needs PostgreSQL 17 or later.
const effectivePrivilegesSQL = `
WITH RECURSIVE rel AS (
    SELECT c.oid, c.relname::text AS name, c.relkind, c.relname::text AS root
      FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S') AND NOT c.relispartition AND n.nspname = 'public'
    UNION ALL
    SELECT ch.oid, ch.relname::text, ch.relkind, rel.root
      FROM rel JOIN pg_inherits i ON i.inhparent = rel.oid JOIN pg_class ch ON ch.oid = i.inhrelid
     WHERE ch.relispartition
)
SELECT r.name, r.root, ''::text, p
  FROM rel r
  CROSS JOIN unnest(ARRAY['SELECT', 'INSERT', 'UPDATE', 'DELETE', 'TRUNCATE', 'REFERENCES', 'TRIGGER', 'MAINTAIN']) AS q
  CROSS JOIN unnest(ARRAY[q, q || ' WITH GRANT OPTION']) AS p
 WHERE r.relkind <> 'S' AND has_table_privilege(current_user, r.oid, p)
UNION ALL
SELECT r.name, r.root, a.attname::text, p
  FROM rel r
  JOIN pg_attribute a ON a.attrelid = r.oid AND a.attnum > 0 AND NOT a.attisdropped
  CROSS JOIN unnest(ARRAY['SELECT', 'INSERT', 'UPDATE', 'REFERENCES']) AS q
  CROSS JOIN unnest(ARRAY[q, q || ' WITH GRANT OPTION']) AS p
 WHERE r.relkind <> 'S'
   AND NOT has_table_privilege(current_user, r.oid, p)
   AND has_column_privilege(current_user, r.oid, a.attnum, p)
UNION ALL
SELECT r.name, r.root, ''::text, p
  FROM rel r
  CROSS JOIN unnest(ARRAY['USAGE', 'SELECT', 'UPDATE']) AS q
  CROSS JOIN unnest(ARRAY[q, q || ' WITH GRANT OPTION']) AS p
 WHERE r.relkind = 'S' AND has_sequence_privilege(current_user, r.oid, p)`

// privilegeDrift compares the privileges the current role holds with want, in both directions: an extra grant
// (say UPDATE on users.status) and a missing one (the service would fail at runtime) are both drift. A relation
// that is not in want must carry nothing, whatever its kind (view, sequence, foreign table...). A partition is
// held to the expectation of its parent: it may carry at most what the parent has, so a direct grant on a leaf
// beyond that is drift (partitions get no ACL of their own from the parent, so holding less is normal).
func privilegeDrift(ctx context.Context, db querier, want map[string]tableGrant) ([]string, error) {
	rows, err := db.Query(ctx, effectivePrivilegesSQL)
	if err != nil {
		return nil, fmt.Errorf("check privileges: %w", err)
	}
	defer rows.Close()
	type leaf struct{ table, root, column, priv string }
	have := map[string]struct{}{}
	var leaves []leaf
	for rows.Next() {
		var table, root, column, priv string
		if err := rows.Scan(&table, &root, &column, &priv); err != nil {
			return nil, fmt.Errorf("check privileges: %w", err)
		}
		if table != root {
			leaves = append(leaves, leaf{table, root, column, priv})
			continue
		}
		have[privilegeKey(table, column, priv)] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("check privileges: %w", err)
	}

	expected := map[string]struct{}{}
	for table, g := range want {
		for _, priv := range g.Table {
			expected[privilegeKey(table, "", priv)] = struct{}{}
		}
		for priv, columns := range g.Columns {
			for _, column := range columns {
				expected[privilegeKey(table, column, priv)] = struct{}{}
			}
		}
	}

	var drift []string
	for _, l := range leaves {
		if _, ok := expected[privilegeKey(l.root, l.column, l.priv)]; !ok {
			drift = append(drift, fmt.Sprintf("extra privilege %s (partition of %s)", privilegeKey(l.table, l.column, l.priv), l.root))
		}
	}
	for _, k := range slices.Sorted(maps.Keys(have)) {
		if _, ok := expected[k]; !ok {
			drift = append(drift, "extra privilege "+k)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(expected)) {
		if _, ok := have[k]; !ok {
			drift = append(drift, "missing privilege "+k)
		}
	}
	return drift, nil
}

// privilegeKey renders a privilege as "PRIV ON table" or "PRIV ON table.column" (column-only grant).
func privilegeKey(table, column, priv string) string {
	var b strings.Builder
	b.WriteString(priv + " ON " + table)
	if column != "" {
		b.WriteString("." + column + " (column-level)")
	}
	return b.String()
}

// refusal builds the start-up error shared by the runtime and portal roles.
func refusal(role string, problems []error) error {
	return fmt.Errorf("refusing to run: the database role is not the least-privilege role %s "+
		"(connect with a login role that is a member of %s, not the owner of the schema): %w",
		role, role, errors.Join(problems...))
}

// LoginRoleProblems lists why the login role (session_user) is unsafe: it could undo the assumed role with
// RESET ROLE / SET ROLE. Empty means the login role is fine.
func (id RuntimeIdentity) LoginRoleProblems() []string {
	var problems []string
	if id.SessionIsSuper {
		problems = append(problems, "is a superuser")
	}
	if id.SessionBypassRLS {
		problems = append(problems, "bypasses row-level security")
	}
	if id.SessionOwnsData {
		problems = append(problems, "owns (or inherits the owner of) tables in schema public")
	}
	if id.SessionCanCreate {
		problems = append(problems, "can create objects in schema public")
	}
	// A superuser is a member of every pg_* role; it is already reported above, so skip the noise.
	if len(id.SessionPredefinedRoles) > 0 && !id.SessionIsSuper {
		problems = append(problems, "is a member of predefined roles "+strings.Join(id.SessionPredefinedRoles, ", "))
	}
	return problems
}

// roleFacts is what the database reports about the effective role of a connection.
type roleFacts struct {
	RuntimeIdentity
	super, bypassRLS, ownsTables, canCreate bool
	auditUpdate, auditDelete, auditTruncate bool
	predefinedRoles                         []string // pg_* roles the effective role is a member of, directly or not
}

func loadRoleFacts(ctx context.Context, db rowQuerier) (roleFacts, error) {
	var (
		f                                  roleFacts
		sessionSuper, sessionOwns          bool
		sessionBypassRLS, sessionCanCreate bool
	)
	err := db.QueryRow(ctx, `
		SELECT current_user::text, session_user::text,
		       r.rolsuper, r.rolbypassrls,
		       EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		                WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S') AND n.nspname = 'public' AND pg_has_role(current_user, c.relowner, 'MEMBER')),
		       has_schema_privilege(current_user, 'public', 'CREATE'),
		       has_table_privilege(current_user, 'audit_events', 'UPDATE'),
		       has_table_privilege(current_user, 'audit_events', 'DELETE'),
		       has_table_privilege(current_user, 'audit_events', 'TRUNCATE'),
		       s.rolsuper,
		       EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		                WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S') AND n.nspname = 'public' AND pg_has_role(session_user, c.relowner, 'MEMBER')),
		       s.rolbypassrls,
		       has_schema_privilege(session_user, 'public', 'CREATE'),
		       COALESCE((SELECT array_agg(g.rolname::text ORDER BY g.rolname) FROM pg_roles g
		                  WHERE g.rolname LIKE 'pg\_%' AND pg_has_role(current_user, g.oid, 'MEMBER')), '{}'),
			       COALESCE((SELECT array_agg(g.rolname::text ORDER BY g.rolname) FROM pg_roles g
			                  WHERE g.rolname LIKE 'pg\_%' AND pg_has_role(session_user, g.oid, 'MEMBER')), '{}')
		FROM pg_roles r, pg_roles s
		WHERE r.rolname = current_user AND s.rolname = session_user`).Scan(
		&f.CurrentUser, &f.SessionUser, &f.super, &f.bypassRLS, &f.ownsTables, &f.canCreate,
		&f.auditUpdate, &f.auditDelete, &f.auditTruncate, &sessionSuper, &sessionOwns, &sessionBypassRLS, &sessionCanCreate,
		&f.predefinedRoles, &f.SessionPredefinedRoles)
	if err != nil {
		return roleFacts{}, fmt.Errorf("check database role: %w", err)
	}
	f.SessionIsSuper, f.SessionOwnsData = sessionSuper, sessionOwns
	f.SessionBypassRLS, f.SessionCanCreate = sessionBypassRLS, sessionCanCreate
	return f, nil
}

// problems lists everything that voids the grants of the schema for the effective role; want is the role the
// connection must be running as.
func (f roleFacts) problems(want string) []error {
	var problems []error
	add := func(format string, args ...any) { problems = append(problems, fmt.Errorf(format, args...)) }
	if f.CurrentUser != want {
		add("effective role is %q, want %q", f.CurrentUser, want)
	}
	if f.super {
		add("role %q is a superuser", f.CurrentUser)
	}
	if f.bypassRLS {
		add("role %q bypasses row-level security", f.CurrentUser)
	}
	if f.ownsTables {
		add("role %q owns (or inherits the owner of) tables in schema public", f.CurrentUser)
	}
	if f.canCreate {
		add("role %q can create objects in schema public", f.CurrentUser)
	}
	if f.auditUpdate || f.auditDelete || f.auditTruncate {
		add("role %q can modify or truncate audit_events (must be append-only)", f.CurrentUser)
	}
	if len(f.predefinedRoles) > 0 {
		add("role %q is a member of predefined roles %s (pg_* roles grant data, maintenance or server access)",
			f.CurrentUser, strings.Join(f.predefinedRoles, ", "))
	}
	return problems
}
