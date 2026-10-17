package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PortalRole is the least-privilege role of the portal process (cmd/portal), created by migration 0009.
const PortalRole = "platform_portal"

// portalGrants is the complete privilege set of PortalRole on the relations of schema public, as granted by
// migrations 0009, 0012 and 0014. A relation that is not listed must carry no privilege at all.
//
// users: humans are created just-in-time by platform-api, so the portal inserts only without google_sub (robots,
// 0012) and updates status, updated_at, name and owner_user_id; rows are never deleted. api_tokens: inserted and
// revoked here, never read with the hash and never deleted. sessions and app_sessions: the token hashes are never readable and rows can
// only be deleted (revocation). areas, area_members and area_supervisors (0014): no UPDATE, keys are immutable.
// audit_events is append-only; platform_admins and access_denials are read-only.
var portalGrants = map[string]tableGrant{
	"groups":            {Table: []string{"SELECT", "INSERT", "UPDATE", "DELETE"}},
	"group_members":     {Table: []string{"SELECT", "INSERT", "UPDATE", "DELETE"}},
	"areas":             {Table: []string{"SELECT", "INSERT", "DELETE"}},
	"area_members":      {Table: []string{"SELECT", "INSERT", "DELETE"}},
	"area_supervisors":  {Table: []string{"SELECT", "INSERT", "DELETE"}},
	"initiatives":       {Table: []string{"SELECT", "INSERT", "UPDATE", "DELETE"}},
	"initiative_roles":  {Table: []string{"SELECT", "INSERT", "UPDATE", "DELETE"}},
	"apps":              {Table: []string{"SELECT", "INSERT", "UPDATE", "DELETE"}},
	"deployments":       {Table: []string{"SELECT", "INSERT", "UPDATE", "DELETE"}},
	"initiative_access": {Table: []string{"SELECT", "INSERT", "UPDATE", "DELETE"}},
	"users": {Table: []string{"SELECT"}, Columns: map[string][]string{
		"INSERT": {"kind", "name", "email", "owner_user_id", "status"},
		"UPDATE": {"status", "updated_at", "name", "owner_user_id"}}},
	"api_tokens": {Table: []string{"INSERT"}, Columns: map[string][]string{
		"SELECT": {"id", "user_id", "name", "scope", "created_by", "created_at", "expires_at", "last_used_at", "revoked_at"},
		"UPDATE": {"revoked_at"}}},
	"platform_admins": {Table: []string{"SELECT"}},
	"audit_events":    {Table: []string{"SELECT", "INSERT"}},
	"access_denials":  {Table: []string{"SELECT"}},
	"sessions": {Table: []string{"DELETE"}, Columns: map[string][]string{
		"SELECT": {"id", "user_id", "created_at", "last_seen_at", "expires_at", "ip", "user_agent"}}},
	"app_sessions": {Table: []string{"DELETE"}, Columns: map[string][]string{
		"SELECT": {"id", "session_id", "app_id", "host", "created_at", "expires_at"}}},
}

// MinPortalPoolConns is the smallest pool "portal serve" runs with. A probe round pins one connection for its
// whole duration (the session-level advisory lock), so the default of pgx (the number of CPUs, at least 4) could
// leave a 1-CPU pod with three connections for every request of the API. The pool is raised to this minimum; a
// larger configured size is kept.
const MinPortalPoolConns = 8

// withMinConns raises cfg.MaxConns to MinPortalPoolConns.
func withMinConns(cfg *pgxpool.Config) {
	cfg.MaxConns = max(cfg.MaxConns, MinPortalPoolConns)
}

// NewPortalPool builds the pool "portal serve" uses from the standard PG* variables, as PortalRole, and
// refuses to return it unless CheckPortalRole passes.
func NewPortalPool(ctx context.Context) (*pgxpool.Pool, RuntimeIdentity, error) {
	cfg, err := pgxpool.ParseConfig("")
	if err != nil {
		return nil, RuntimeIdentity{}, fmt.Errorf("parse pg config: %w", err)
	}
	withMinConns(cfg)
	return OpenPortalPool(ctx, cfg)
}

// OpenPortalPool opens a pool from cfg whose every connection runs as PortalRole (a startup parameter, see
// OpenRuntimePool for why) and refuses to return it unless CheckPortalRole passes.
func OpenPortalPool(ctx context.Context, cfg *pgxpool.Config) (*pgxpool.Pool, RuntimeIdentity, error) {
	return openRolePool(ctx, cfg, PortalRole, CheckPortalRole)
}

// CheckPortalRole verifies that db runs as PortalRole and with exactly the grants of migrations 0009, 0012 and 0014: the base
// checks of CheckRuntimeRole (effective role, superuser, row security bypass, table ownership, CREATE on
// public, append-only audit_events, no predefined pg_* roles) plus the same two-way comparison of table, column
// and sequence privileges against portalGrants. The check runs once per pool, at start-up.
func CheckPortalRole(ctx context.Context, db querier) (RuntimeIdentity, error) {
	f, err := loadRoleFacts(ctx, db)
	if err != nil {
		return RuntimeIdentity{}, err
	}
	problems := f.problems(PortalRole)
	drift, err := privilegeDrift(ctx, db, portalGrants)
	if err != nil {
		return RuntimeIdentity{}, err
	}
	for _, d := range drift {
		problems = append(problems, fmt.Errorf("role %q: %s", f.CurrentUser, d))
	}
	if len(problems) > 0 {
		return f.RuntimeIdentity, refusal(PortalRole, problems)
	}
	return f.RuntimeIdentity, nil
}
