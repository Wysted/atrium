package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"platform/api/internal/access"
	"platform/api/internal/portal"
)

// dbtx is the part of a pool or a transaction the portal repositories use, so the same repository serves
// both a plain read (pool) and a unit of work (pgx.Tx).
type dbtx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var (
	_ dbtx = (*pgxpool.Pool)(nil)
	_ dbtx = pgx.Tx(nil)
)

// PermissionsRepo implements portal.PermissionsReader.
type PermissionsRepo struct{ db dbtx }

// NewPermissionsRepo returns a PermissionsRepo over a pool.
func NewPermissionsRepo(pool *pgxpool.Pool) *PermissionsRepo { return &PermissionsRepo{db: pool} }

var _ portal.PermissionsReader = (*PermissionsRepo)(nil)

// permissionsSQL loads the profile, the platform-admin flag and the managed initiatives in one round trip.
// $1 = user id. A managed initiative is one where a non-expired grant of a role with manages_access exists,
// directly or through a group (the same grant rules as access.Decide). No row means the user does not exist.
const permissionsSQL = `
SELECT u.email, u.name, u.status,
       EXISTS (SELECT 1 FROM platform_admins pa WHERE pa.user_id = u.id),
       COALESCE((
           SELECT jsonb_agg(jsonb_build_object('id', i.id, 'key', i.key, 'name', i.name) ORDER BY i.key)
           FROM initiatives i
           WHERE i.id IN (
               SELECT ia.initiative_id
               FROM initiative_access ia
               JOIN initiative_roles ir ON ir.initiative_id = ia.initiative_id AND ir.id = ia.role_id
               WHERE ir.manages_access
                 AND (ia.expires_at IS NULL OR ia.expires_at > now())
                 AND (ia.user_id = u.id
                      OR ia.group_id IN (SELECT gm.group_id FROM group_members gm WHERE gm.user_id = u.id)))
       ), '[]'::jsonb)
FROM users u
WHERE u.id = $1::uuid`

// initiativeJSON is one element of the jsonb array built by permissionsSQL.
type initiativeJSON struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

// LoadPermissions implements portal.PermissionsReader.
func (r *PermissionsRepo) LoadPermissions(ctx context.Context, userID string) (portal.Permissions, error) {
	p := portal.Permissions{UserID: userID}
	var managed []initiativeJSON
	err := r.db.QueryRow(ctx, permissionsSQL, userID).Scan(&p.Email, &p.Name, &p.Status, &p.PlatformAdmin, &managed)
	if errors.Is(err, pgx.ErrNoRows) {
		return portal.Permissions{}, portal.ErrNotFound
	}
	if err != nil {
		return portal.Permissions{}, fmt.Errorf("query permissions: %w", err)
	}
	p.Managed = make([]portal.InitiativeRef, 0, len(managed))
	for _, m := range managed {
		p.Managed = append(p.Managed, portal.InitiativeRef(m))
	}
	return p, nil
}

// HomeRepo implements portal.HomeReader.
type HomeRepo struct{ db dbtx }

// NewHomeRepo returns a HomeRepo over a pool.
func NewHomeRepo(pool *pgxpool.Pool) *HomeRepo { return &HomeRepo{db: pool} }

var _ portal.HomeReader = (*HomeRepo)(nil)

// homeSQL lists the apps a user can open. It applies exactly the rules of access.Decide (and the grant rules
// of accessFactsSQL): the user is active, the initiative is active, the app is active and a non-expired grant
// exists for the user directly or through a group; roles are the distinct role keys of those grants. A host
// is listed iff /forwardauth would answer 200 for it, with one exception: the portal app ($2), which every
// active user may open but which is not "one of their apps", is left out. The exclusion mirrors the
// Evaluator: it applies only to an app of the reserved initiative ($3), so a mistyped portal app id that points
// to another initiative's app hides nothing. $1 = user id, $2 = portal app id (empty: none), $3 = key of the
// reserved initiative. An app without deployments has no host to open and is not listed.
const homeSQL = `
SELECT a.id, a.name, i.id::text, i.key, i.name, g.roles,
       ARRAY(SELECT d.host FROM deployments d WHERE d.app_id = a.id ORDER BY d.host COLLATE "C")
FROM users u
JOIN initiatives i ON i.status = 'active'
JOIN apps a        ON a.initiative_id = i.id AND a.status = 'active'
CROSS JOIN LATERAL (
    SELECT array_agg(DISTINCT ir.key ORDER BY ir.key) AS roles
    FROM initiative_access ia
    JOIN initiative_roles ir ON ir.initiative_id = ia.initiative_id AND ir.id = ia.role_id
    WHERE ia.initiative_id = i.id
      AND (ia.expires_at IS NULL OR ia.expires_at > now())
      AND (ia.user_id = u.id
           OR ia.group_id IN (SELECT gm.group_id FROM group_members gm WHERE gm.user_id = u.id))
) g
WHERE u.id = $1::uuid
  AND u.status = 'active'
  AND g.roles IS NOT NULL
  AND NOT (a.id = $2 AND i.key = $3)
  AND EXISTS (SELECT 1 FROM deployments d WHERE d.app_id = a.id)
ORDER BY i.key, a.name, a.id`

// HomeApps implements portal.HomeReader.
func (r *HomeRepo) HomeApps(ctx context.Context, userID, excludeAppID string) ([]portal.HomeApp, error) {
	rows, err := r.db.Query(ctx, homeSQL, userID, excludeAppID, access.PlatformInitiativeKey)
	if err != nil {
		return nil, fmt.Errorf("query home apps: %w", err)
	}
	apps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (portal.HomeApp, error) {
		var a portal.HomeApp
		err := row.Scan(&a.ID, &a.Name, &a.Initiative.ID, &a.Initiative.Key, &a.Initiative.Name, &a.Roles, &a.Hosts)
		return a, err
	})
	if err != nil {
		return nil, fmt.Errorf("read home apps: %w", err)
	}
	return apps, nil
}
