package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"platform/api/internal/adminz"
	"platform/api/internal/portal"
)

// CatalogRepo implements portal.CatalogRepo.
type CatalogRepo struct{ db dbtx }

var _ portal.CatalogRepo = CatalogRepo{}

// affected turns the result of an UPDATE of one row into an error: a row that vanished while the statement
// waited (it matched nothing) is portal.ErrNotFound, never a silent success.
func affected(tag pgconn.CommandTag, err error) error {
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return portal.ErrNotFound
	}
	return nil
}

// ---- Initiatives ----

const initiativeColumns = `id::text, key, name, status, created_at`

func scanInitiative(row pgx.CollectableRow) (portal.Initiative, error) {
	var i portal.Initiative
	err := row.Scan(&i.ID, &i.Key, &i.Name, &i.Status, &i.CreatedAt)
	return i, err
}

// ListInitiatives implements portal.CatalogRepo: oldest first by id, restricted to the scope.
func (r CatalogRepo) ListInitiatives(ctx context.Context, scope adminz.Scope, q portal.PageQuery) ([]portal.Initiative, error) {
	if scope.Empty() {
		return nil, nil
	}
	all, ids := scopeArgs(scope)
	rows, err := r.db.Query(ctx, `SELECT `+initiativeColumns+` FROM initiatives
WHERE ($1::bool OR id = ANY($2::uuid[])) AND ($3::uuid IS NULL OR id > $3::uuid)
ORDER BY id LIMIT $4`, all, ids, afterID(q.After), q.Limit)
	if err != nil {
		return nil, fmt.Errorf("query initiatives: %w", err)
	}
	list, err := pgx.CollectRows(rows, scanInitiative)
	if err != nil {
		return nil, fmt.Errorf("read initiatives: %w", err)
	}
	return list, nil
}

// GetInitiative implements portal.CatalogRepo.
func (r CatalogRepo) GetInitiative(ctx context.Context, id string) (portal.Initiative, error) {
	rows, err := r.db.Query(ctx, `SELECT `+initiativeColumns+` FROM initiatives WHERE id = $1::uuid`, id)
	if err != nil {
		return portal.Initiative{}, fmt.Errorf("query initiative: %w", err)
	}
	i, err := pgx.CollectOneRow(rows, scanInitiative)
	if errors.Is(err, pgx.ErrNoRows) {
		return portal.Initiative{}, portal.ErrNotFound
	}
	if err != nil {
		return portal.Initiative{}, fmt.Errorf("read initiative: %w", err)
	}
	return i, nil
}

// InsertInitiative implements portal.CatalogRepo.
func (r CatalogRepo) InsertInitiative(ctx context.Context, key, name string) (portal.Initiative, error) {
	rows, err := r.db.Query(ctx, `INSERT INTO initiatives (key, name) VALUES ($1, $2) RETURNING `+initiativeColumns, key, name)
	if err != nil {
		return portal.Initiative{}, writeError("insert initiative", err)
	}
	i, err := pgx.CollectOneRow(rows, scanInitiative)
	if err != nil {
		return portal.Initiative{}, writeError("insert initiative", err)
	}
	return i, nil
}

// UpdateInitiative implements portal.CatalogRepo.
func (r CatalogRepo) UpdateInitiative(ctx context.Context, id, name, status string) error {
	return affected(r.db.Exec(ctx, `UPDATE initiatives SET name = $2, status = $3 WHERE id = $1::uuid`, id, name, status))
}

// ---- Roles ----

const roleColumns = `id::text, initiative_id::text, key, manages_access`

func scanRole(row pgx.CollectableRow) (portal.Role, error) {
	var r portal.Role
	err := row.Scan(&r.ID, &r.InitiativeID, &r.Key, &r.ManagesAccess)
	return r, err
}

// ListRoles implements portal.CatalogRepo: oldest first by id, restricted to the scope.
func (r CatalogRepo) ListRoles(ctx context.Context, scope adminz.Scope, q portal.PageQuery) ([]portal.Role, error) {
	if scope.Empty() {
		return nil, nil
	}
	all, ids := scopeArgs(scope)
	rows, err := r.db.Query(ctx, `SELECT `+roleColumns+` FROM initiative_roles
WHERE ($1::bool OR initiative_id = ANY($2::uuid[])) AND ($3::uuid IS NULL OR id > $3::uuid)
ORDER BY id LIMIT $4`, all, ids, afterID(q.After), q.Limit)
	if err != nil {
		return nil, fmt.Errorf("query roles: %w", err)
	}
	list, err := pgx.CollectRows(rows, scanRole)
	if err != nil {
		return nil, fmt.Errorf("read roles: %w", err)
	}
	return list, nil
}

// GetRole implements portal.CatalogRepo.
func (r CatalogRepo) GetRole(ctx context.Context, id string) (portal.Role, error) {
	rows, err := r.db.Query(ctx, `SELECT `+roleColumns+` FROM initiative_roles WHERE id = $1::uuid`, id)
	if err != nil {
		return portal.Role{}, fmt.Errorf("query role: %w", err)
	}
	role, err := pgx.CollectOneRow(rows, scanRole)
	if errors.Is(err, pgx.ErrNoRows) {
		return portal.Role{}, portal.ErrNotFound
	}
	if err != nil {
		return portal.Role{}, fmt.Errorf("read role: %w", err)
	}
	return role, nil
}

// InsertRole implements portal.CatalogRepo.
func (r CatalogRepo) InsertRole(ctx context.Context, initiativeID, key string, managesAccess bool) (portal.Role, error) {
	rows, err := r.db.Query(ctx, `INSERT INTO initiative_roles (initiative_id, key, manages_access)
VALUES ($1::uuid, $2, $3) RETURNING `+roleColumns, initiativeID, key, managesAccess)
	if err != nil {
		return portal.Role{}, writeError("insert role", err)
	}
	role, err := pgx.CollectOneRow(rows, scanRole)
	if err != nil {
		return portal.Role{}, writeError("insert role", err)
	}
	return role, nil
}

// SetRoleManagesAccess implements portal.CatalogRepo.
func (r CatalogRepo) SetRoleManagesAccess(ctx context.Context, id string, managesAccess bool) error {
	return affected(r.db.Exec(ctx, `UPDATE initiative_roles SET manages_access = $2 WHERE id = $1::uuid`, id, managesAccess))
}

// DeleteRole implements portal.CatalogRepo. The foreign key of the grants refuses a role in use; writeError
// reports it as a *portal.ReferenceError.
func (r CatalogRepo) DeleteRole(ctx context.Context, id string) (bool, error) {
	tag, err := r.db.Exec(ctx, `DELETE FROM initiative_roles WHERE id = $1::uuid`, id)
	if err != nil {
		return false, writeError("delete role", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ---- Apps and deployments ----

// appSelectSQL reads apps with the key of their initiative and their deployments (ordered by host, bytewise) as
// a jsonb array, so a page of apps is one query.
const appSelectSQL = `
SELECT a.id, a.initiative_id::text, i.key, a.name, a.status, a.created_at,
       COALESCE((SELECT jsonb_agg(jsonb_build_object('id', d.id, 'host', d.host, 'created_at', d.created_at,
                                                     'last_seen_at', d.last_seen_at, 'last_probe_at', d.last_probe_at,
                                                     'last_probe_status', d.last_probe_status)
                                  ORDER BY d.host COLLATE "C")
                 FROM deployments d WHERE d.app_id = a.id), '[]'::jsonb)
FROM apps a JOIN initiatives i ON i.id = a.initiative_id`

// deploymentJSON is one element of the jsonb array of appSelectSQL.
type deploymentJSON struct {
	ID              string     `json:"id"`
	Host            string     `json:"host"`
	CreatedAt       time.Time  `json:"created_at"`
	LastSeenAt      *time.Time `json:"last_seen_at"`
	LastProbeAt     *time.Time `json:"last_probe_at"`
	LastProbeStatus *string    `json:"last_probe_status"`
}

func scanApp(row pgx.CollectableRow) (portal.App, error) {
	var (
		a           portal.App
		deployments []deploymentJSON
	)
	if err := row.Scan(&a.ID, &a.InitiativeID, &a.InitiativeKey, &a.Name, &a.Status, &a.CreatedAt, &deployments); err != nil {
		return portal.App{}, err
	}
	a.Deployments = make([]portal.Deployment, 0, len(deployments))
	for _, d := range deployments {
		a.Deployments = append(a.Deployments, portal.Deployment{
			ID: d.ID, AppID: a.ID, InitiativeID: a.InitiativeID, Host: d.Host, CreatedAt: d.CreatedAt,
			LastSeenAt: d.LastSeenAt, LastProbeAt: d.LastProbeAt, LastProbeStatus: deref(d.LastProbeStatus),
		})
	}
	return a, nil
}

// ListApps implements portal.CatalogRepo: oldest first by id (a ULID, so by creation time), restricted to the scope.
func (r CatalogRepo) ListApps(ctx context.Context, scope adminz.Scope, q portal.PageQuery) ([]portal.App, error) {
	if scope.Empty() {
		return nil, nil
	}
	all, ids := scopeArgs(scope)
	rows, err := r.db.Query(ctx, appSelectSQL+`
WHERE ($1::bool OR a.initiative_id = ANY($2::uuid[])) AND ($3::text IS NULL OR a.id COLLATE "C" > $3::text)
ORDER BY a.id COLLATE "C" LIMIT $4`, all, ids, afterID(q.After), q.Limit)
	if err != nil {
		return nil, fmt.Errorf("query apps: %w", err)
	}
	list, err := pgx.CollectRows(rows, scanApp)
	if err != nil {
		return nil, fmt.Errorf("read apps: %w", err)
	}
	return list, nil
}

func (r CatalogRepo) oneApp(ctx context.Context, suffix, id string) (portal.App, error) {
	rows, err := r.db.Query(ctx, appSelectSQL+` WHERE a.id = $1`+suffix, id)
	if err != nil {
		return portal.App{}, fmt.Errorf("query app: %w", err)
	}
	a, err := pgx.CollectOneRow(rows, scanApp)
	if errors.Is(err, pgx.ErrNoRows) {
		return portal.App{}, portal.ErrNotFound
	}
	if err != nil {
		return portal.App{}, fmt.Errorf("read app: %w", err)
	}
	return a, nil
}

// GetApp implements portal.CatalogRepo.
func (r CatalogRepo) GetApp(ctx context.Context, id string) (portal.App, error) {
	return r.oneApp(ctx, "", id)
}

// LockApp implements portal.CatalogRepo. Only the apps row is locked (not the initiative whose key is joined).
func (r CatalogRepo) LockApp(ctx context.Context, id string) (portal.App, error) {
	return r.oneApp(ctx, " FOR NO KEY UPDATE OF a", id)
}

// InsertApp implements portal.CatalogRepo.
func (r CatalogRepo) InsertApp(ctx context.Context, a portal.App) error {
	_, err := r.db.Exec(ctx, `INSERT INTO apps (id, initiative_id, name, status) VALUES ($1, $2::uuid, $3, $4)`,
		a.ID, a.InitiativeID, a.Name, a.Status)
	if err != nil {
		return writeError("insert app", err)
	}
	return nil
}

// UpdateApp implements portal.CatalogRepo.
func (r CatalogRepo) UpdateApp(ctx context.Context, id, name, status string) error {
	return affected(r.db.Exec(ctx, `UPDATE apps SET name = $2, status = $3 WHERE id = $1`, id, name, status))
}

// GetDeployment implements portal.CatalogRepo.
func (r CatalogRepo) GetDeployment(ctx context.Context, id string) (portal.Deployment, error) {
	var d portal.Deployment
	err := r.db.QueryRow(ctx, `
SELECT d.id::text, d.app_id, a.initiative_id::text, d.host, d.created_at
FROM deployments d JOIN apps a ON a.id = d.app_id
WHERE d.id = $1::uuid`, id).Scan(&d.ID, &d.AppID, &d.InitiativeID, &d.Host, &d.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return portal.Deployment{}, portal.ErrNotFound
	}
	if err != nil {
		return portal.Deployment{}, fmt.Errorf("query deployment: %w", err)
	}
	return d, nil
}

// CountDeployments implements portal.CatalogRepo.
func (r CatalogRepo) CountDeployments(ctx context.Context, appID string) (int, error) {
	var n int
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM deployments WHERE app_id = $1`, appID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count deployments: %w", err)
	}
	return n, nil
}

// InsertDeployment implements portal.CatalogRepo.
func (r CatalogRepo) InsertDeployment(ctx context.Context, appID, host string) (portal.Deployment, error) {
	d := portal.Deployment{AppID: appID, Host: host}
	err := r.db.QueryRow(ctx, `INSERT INTO deployments (host, app_id) VALUES ($1, $2) RETURNING id::text, created_at`,
		host, appID).Scan(&d.ID, &d.CreatedAt)
	if err != nil {
		return portal.Deployment{}, writeError("insert deployment", err)
	}
	return d, nil
}

// DeleteDeployment implements portal.CatalogRepo.
func (r CatalogRepo) DeleteDeployment(ctx context.Context, id string) (bool, error) {
	tag, err := r.db.Exec(ctx, `DELETE FROM deployments WHERE id = $1::uuid`, id)
	if err != nil {
		return false, fmt.Errorf("delete deployment: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
