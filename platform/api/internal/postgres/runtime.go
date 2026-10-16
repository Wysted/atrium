package postgres

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RuntimeRole is the least-privilege role "serve" runs as (see the grants of migrations 0003, 0005, 0007, 0008).
const RuntimeRole = "platform_runtime"

// RuntimeIdentity is what the database reports about the connection a runtime pool actually got.
type RuntimeIdentity struct {
	CurrentUser     string // effective role (after SET ROLE)
	SessionUser     string // role that authenticated
	SessionIsSuper  bool   // the login role could RESET ROLE back to superuser powers
	SessionOwnsData bool   // the login role owns (or inherits the owner of) tables

	SessionBypassRLS bool // the login role bypasses row-level security
	SessionCanCreate bool // the login role can create objects in schema public
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
	return openRolePool(ctx, cfg, RuntimeRole, func(ctx context.Context, db querier) (RuntimeIdentity, error) {
		return CheckRuntimeRole(ctx, db)
	})
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
// schema public, or one that can modify audit_events (append-only) or keep its history by truncating it.
func CheckRuntimeRole(ctx context.Context, db rowQuerier) (RuntimeIdentity, error) {
	f, err := loadRoleFacts(ctx, db)
	if err != nil {
		return RuntimeIdentity{}, err
	}
	if problems := f.problems(RuntimeRole); len(problems) > 0 {
		return f.RuntimeIdentity, refusal(RuntimeRole, problems)
	}
	return f.RuntimeIdentity, nil
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
	return problems
}

// roleFacts is what the database reports about the effective role of a connection.
type roleFacts struct {
	RuntimeIdentity
	super, bypassRLS, ownsTables, canCreate bool
	auditUpdate, auditDelete, auditTruncate bool
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
		                WHERE c.relkind IN ('r', 'p') AND n.nspname = 'public' AND pg_has_role(current_user, c.relowner, 'USAGE')),
		       has_schema_privilege(current_user, 'public', 'CREATE'),
		       has_table_privilege(current_user, 'audit_events', 'UPDATE'),
		       has_table_privilege(current_user, 'audit_events', 'DELETE'),
		       has_table_privilege(current_user, 'audit_events', 'TRUNCATE'),
		       s.rolsuper,
		       EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		                WHERE c.relkind IN ('r', 'p') AND n.nspname = 'public' AND pg_has_role(session_user, c.relowner, 'USAGE')),
		       s.rolbypassrls,
		       has_schema_privilege(session_user, 'public', 'CREATE')
		FROM pg_roles r, pg_roles s
		WHERE r.rolname = current_user AND s.rolname = session_user`).Scan(
		&f.CurrentUser, &f.SessionUser, &f.super, &f.bypassRLS, &f.ownsTables, &f.canCreate,
		&f.auditUpdate, &f.auditDelete, &f.auditTruncate, &sessionSuper, &sessionOwns, &sessionBypassRLS, &sessionCanCreate)
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
	return problems
}
