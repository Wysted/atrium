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
	cfg = cfg.Copy()
	params := make(map[string]string, len(cfg.ConnConfig.RuntimeParams)+1)
	maps.Copy(params, cfg.ConnConfig.RuntimeParams)
	params["role"] = RuntimeRole
	cfg.ConnConfig.RuntimeParams = params

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, RuntimeIdentity{}, err
	}
	id, err := CheckRuntimeRole(ctx, pool)
	if err != nil {
		pool.Close()
		return nil, RuntimeIdentity{}, err
	}
	return pool, id, nil
}

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// CheckRuntimeRole verifies that db runs with the least-privilege runtime role and refuses anything that
// would silently void the grants of the schema: a different effective role, a superuser, a role that
// bypasses row security, a role that owns (or inherits the owner of) tables, one that can create objects in
// schema public, or one that can modify audit_events (append-only) or keep its history by truncating it.
func CheckRuntimeRole(ctx context.Context, db rowQuerier) (RuntimeIdentity, error) {
	var (
		id                                      RuntimeIdentity
		super, bypassRLS, ownsTables, canCreate bool
		auditUpdate, auditDelete, auditTruncate bool
		sessionSuper, sessionOwns               bool
		sessionBypassRLS, sessionCanCreate      bool
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
		&id.CurrentUser, &id.SessionUser, &super, &bypassRLS, &ownsTables, &canCreate,
		&auditUpdate, &auditDelete, &auditTruncate, &sessionSuper, &sessionOwns, &sessionBypassRLS, &sessionCanCreate)
	if err != nil {
		return RuntimeIdentity{}, fmt.Errorf("check runtime role: %w", err)
	}
	id.SessionIsSuper, id.SessionOwnsData = sessionSuper, sessionOwns
	id.SessionBypassRLS, id.SessionCanCreate = sessionBypassRLS, sessionCanCreate

	var problems []error
	add := func(format string, args ...any) { problems = append(problems, fmt.Errorf(format, args...)) }
	if id.CurrentUser != RuntimeRole {
		add("effective role is %q, want %q", id.CurrentUser, RuntimeRole)
	}
	if super {
		add("role %q is a superuser", id.CurrentUser)
	}
	if bypassRLS {
		add("role %q bypasses row-level security", id.CurrentUser)
	}
	if ownsTables {
		add("role %q owns (or inherits the owner of) tables in schema public", id.CurrentUser)
	}
	if canCreate {
		add("role %q can create objects in schema public", id.CurrentUser)
	}
	if auditUpdate || auditDelete || auditTruncate {
		add("role %q can modify or truncate audit_events (must be append-only)", id.CurrentUser)
	}
	if len(problems) > 0 {
		return id, fmt.Errorf("refusing to run: the database role is not the least-privilege runtime role "+
			"(connect with a login role that is a member of %s, not the owner of the schema): %w",
			RuntimeRole, errors.Join(problems...))
	}
	return id, nil
}
