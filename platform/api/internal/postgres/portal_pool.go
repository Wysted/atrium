package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PortalRole is the least-privilege role of the portal process (cmd/portal), created by migration 0009.
const PortalRole = "platform_portal"

// Tables the portal role must not touch at all, and tables it may only read. They mirror the grants of
// migration 0009; CheckPortalRole refuses to start if any of them drifts.
var (
	portalNoAccessTables = []string{"signing_keys", "oauth_flows", "login_codes", "schema_migrations"}
	portalReadOnlyTables = []string{"platform_admins", "access_denials"}
	// portalDataTables are every table the portal may touch with the grants of migration 0009. None of them may
	// carry TRUNCATE, TRIGGER or REFERENCES for the portal role, whatever else it may do on them.
	portalDataTables = []string{
		"groups", "group_members", "initiatives", "initiative_roles", "apps", "deployments", "initiative_access",
		"users", "audit_events", "sessions", "app_sessions",
	}
)

// NewPortalPool builds the pool "portal serve" uses from the standard PG* variables, as PortalRole, and
// refuses to return it unless CheckPortalRole passes.
func NewPortalPool(ctx context.Context) (*pgxpool.Pool, RuntimeIdentity, error) {
	cfg, err := pgxpool.ParseConfig("")
	if err != nil {
		return nil, RuntimeIdentity{}, fmt.Errorf("parse pg config: %w", err)
	}
	return OpenPortalPool(ctx, cfg)
}

// OpenPortalPool opens a pool from cfg whose every connection runs as PortalRole (a startup parameter, see
// OpenRuntimePool for why) and refuses to return it unless CheckPortalRole passes.
func OpenPortalPool(ctx context.Context, cfg *pgxpool.Config) (*pgxpool.Pool, RuntimeIdentity, error) {
	return openRolePool(ctx, cfg, PortalRole, CheckPortalRole)
}

// portalPrivilegeSQL lists the privileges the portal role must NOT have. Each row of the result is a
// description of one violation.
//
//	$1 = tables with no access at all, $2 = read-only tables, $3 = every table the portal may use.
//
// users: only status and updated_at are writable, and rows are never inserted or deleted (they are created
// just-in-time by platform-api). sessions and app_sessions: the token hashes are never readable and rows can
// only be deleted (revocation).
const portalPrivilegeSQL = `
SELECT t || ': any access' FROM unnest($1::text[]) AS t
 WHERE has_any_column_privilege(current_user, t, 'SELECT,INSERT,UPDATE,REFERENCES')
    OR has_table_privilege(current_user, t, 'DELETE,TRUNCATE,TRIGGER')
UNION ALL
SELECT t || ': write access' FROM unnest($2::text[]) AS t
 WHERE has_any_column_privilege(current_user, t, 'INSERT,UPDATE')
    OR has_table_privilege(current_user, t, 'DELETE,TRUNCATE,TRIGGER')
UNION ALL
SELECT t || ': TRUNCATE, TRIGGER or REFERENCES' FROM unnest($3::text[]) AS t
 WHERE has_table_privilege(current_user, t, 'TRUNCATE,TRIGGER')
    OR has_any_column_privilege(current_user, t, 'REFERENCES')
UNION ALL
SELECT 'users: write access beyond status and updated_at'
 WHERE has_table_privilege(current_user, 'users', 'INSERT,DELETE,TRUNCATE,TRIGGER')
    OR EXISTS (SELECT 1 FROM pg_attribute a
                WHERE a.attrelid = 'users'::regclass AND a.attnum > 0 AND NOT a.attisdropped
                  AND a.attname NOT IN ('status', 'updated_at')
                  AND has_column_privilege(current_user, 'users', a.attnum, 'INSERT,UPDATE'))
UNION ALL
SELECT t || ': token hash readable or rows writable' FROM unnest(ARRAY['sessions', 'app_sessions']) AS t
 WHERE has_column_privilege(current_user, t, 'token_hash', 'SELECT,INSERT,UPDATE')
    OR has_any_column_privilege(current_user, t, 'INSERT,UPDATE')
    OR has_table_privilege(current_user, t, 'TRUNCATE,TRIGGER')`

// CheckPortalRole verifies that db runs as PortalRole and with nothing beyond the grants of migration 0009:
// the base checks of CheckRuntimeRole (effective role, superuser, row security bypass, table ownership,
// CREATE on public, append-only audit_events) plus the table-level limits of portalPrivilegeSQL.
func CheckPortalRole(ctx context.Context, db querier) (RuntimeIdentity, error) {
	f, err := loadRoleFacts(ctx, db)
	if err != nil {
		return RuntimeIdentity{}, err
	}
	problems := f.problems(PortalRole)

	rows, err := db.Query(ctx, portalPrivilegeSQL, portalNoAccessTables, portalReadOnlyTables, portalDataTables)
	if err != nil {
		return RuntimeIdentity{}, fmt.Errorf("check portal privileges: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var violation string
		if err := rows.Scan(&violation); err != nil {
			return RuntimeIdentity{}, fmt.Errorf("check portal privileges: %w", err)
		}
		problems = append(problems, fmt.Errorf("role %q has extra privileges: %s", f.CurrentUser, violation))
	}
	if err := rows.Err(); err != nil {
		return RuntimeIdentity{}, fmt.Errorf("check portal privileges: %w", err)
	}
	if len(problems) > 0 {
		return f.RuntimeIdentity, refusal(PortalRole, problems)
	}
	return f.RuntimeIdentity, nil
}
