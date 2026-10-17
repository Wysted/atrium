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

// AreasRepo implements portal.AreaRepo.
type AreasRepo struct{ db dbtx }

var _ portal.AreaRepo = AreasRepo{}

const areaColumns = `id::text, key, name, created_at`

func scanArea(row pgx.CollectableRow) (portal.Area, error) {
	var a portal.Area
	err := row.Scan(&a.ID, &a.Key, &a.Name, &a.CreatedAt)
	return a, err
}

// areaPeopleTable maps a relation to its table; the name is never built from input.
var areaPeopleTable = map[portal.AreaRelation]string{
	portal.AreaMembers:     "area_members",
	portal.AreaSupervisors: "area_supervisors",
}

// ListAreas implements portal.AreaRepo. Areas are global, so only Scope.All lists them.
func (r AreasRepo) ListAreas(ctx context.Context, scope adminz.Scope, q portal.PageQuery) ([]portal.Area, error) {
	if !scope.All {
		return nil, nil
	}
	rows, err := r.db.Query(ctx, `SELECT `+areaColumns+` FROM areas WHERE ($1::uuid IS NULL OR id > $1::uuid) ORDER BY id LIMIT $2`,
		afterID(q.After), q.Limit)
	if err != nil {
		return nil, fmt.Errorf("query areas: %w", err)
	}
	areas, err := pgx.CollectRows(rows, scanArea)
	if err != nil {
		return nil, fmt.Errorf("read areas: %w", err)
	}
	return areas, nil
}

// GetArea implements portal.AreaRepo.
func (r AreasRepo) GetArea(ctx context.Context, id string) (portal.Area, error) {
	rows, err := r.db.Query(ctx, `SELECT `+areaColumns+` FROM areas WHERE id = $1::uuid`, id)
	if err != nil {
		return portal.Area{}, fmt.Errorf("query area: %w", err)
	}
	a, err := pgx.CollectOneRow(rows, scanArea)
	if errors.Is(err, pgx.ErrNoRows) {
		return portal.Area{}, portal.ErrNotFound
	}
	if err != nil {
		return portal.Area{}, fmt.Errorf("read area: %w", err)
	}
	return a, nil
}

// InsertArea implements portal.AreaRepo.
func (r AreasRepo) InsertArea(ctx context.Context, key, name string) (portal.Area, error) {
	rows, err := r.db.Query(ctx, `INSERT INTO areas (key, name) VALUES ($1, $2) RETURNING `+areaColumns, key, name)
	if err != nil {
		return portal.Area{}, writeError("insert area", err)
	}
	a, err := pgx.CollectOneRow(rows, scanArea)
	if err != nil {
		return portal.Area{}, writeError("insert area", err)
	}
	return a, nil
}

// DeleteArea implements portal.AreaRepo: a plain DELETE guarded by ON DELETE RESTRICT (the portal role cannot lock
// the row, it has no UPDATE on areas). The violation of area_members_area_id_fkey or area_supervisors_area_id_fkey
// is a 23001 (restrict_violation, not the 23503 of the inserts) and comes back as portal.ErrInUse.
func (r AreasRepo) DeleteArea(ctx context.Context, id string) (bool, error) {
	tag, err := r.db.Exec(ctx, `DELETE FROM areas WHERE id = $1::uuid`, id)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == sqlStateRestrictViolation {
		return false, fmt.Errorf("delete area: %w", portal.ErrInUse)
	}
	if err != nil {
		return false, fmt.Errorf("delete area: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ListPeople implements portal.AreaRepo.
func (r AreasRepo) ListPeople(ctx context.Context, rel portal.AreaRelation, scope adminz.Scope, areaID string, q portal.PageQuery) ([]portal.AreaPerson, error) {
	if !scope.All {
		return nil, nil
	}
	rows, err := r.db.Query(ctx, `
SELECT u.id::text, u.email, u.name, u.status, ap.added_at
FROM `+areaPeopleTable[rel]+` ap JOIN users u ON u.id = ap.user_id
WHERE ap.area_id = $1::uuid AND ($2::uuid IS NULL OR ap.user_id > $2::uuid)
ORDER BY ap.user_id
LIMIT $3`, areaID, afterID(q.After), q.Limit)
	if err != nil {
		return nil, fmt.Errorf("query area %s: %w", rel, err)
	}
	people, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (portal.AreaPerson, error) {
		var p portal.AreaPerson
		err := row.Scan(&p.UserID, &p.Email, &p.Name, &p.Status, &p.AddedAt)
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("read area %s: %w", rel, err)
	}
	return people, nil
}

// InsertPerson implements portal.AreaRepo.
func (r AreasRepo) InsertPerson(ctx context.Context, rel portal.AreaRelation, areaID, userID string) (time.Time, error) {
	var at time.Time
	err := r.db.QueryRow(ctx, `INSERT INTO `+areaPeopleTable[rel]+` (area_id, user_id) VALUES ($1::uuid, $2::uuid) RETURNING added_at`,
		areaID, userID).Scan(&at)
	if err != nil {
		return time.Time{}, writeError("insert area "+string(rel), err)
	}
	return at, nil
}

// DeletePerson implements portal.AreaRepo.
func (r AreasRepo) DeletePerson(ctx context.Context, rel portal.AreaRelation, areaID, userID string) (bool, error) {
	tag, err := r.db.Exec(ctx, `DELETE FROM `+areaPeopleTable[rel]+` WHERE area_id = $1::uuid AND user_id = $2::uuid`, areaID, userID)
	if err != nil {
		return false, fmt.Errorf("delete area %s: %w", rel, err)
	}
	return tag.RowsAffected() > 0, nil
}
