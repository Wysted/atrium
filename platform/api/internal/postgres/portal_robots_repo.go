package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"platform/api/internal/adminz"
	"platform/api/internal/portal"
)

// RobotsRepo implements portal.RobotRepo. Every statement filters kind = 'robot' in SQL, so a human id is as
// absent as an unknown one (the counterpart of UsersRepo, which sees humans only).
type RobotsRepo struct{ db dbtx }

var _ portal.RobotRepo = RobotsRepo{}

// robotColumns are the columns of a robot without its last use; lastUsedSQL is the newest use of any of the
// robot's tokens (the portal role may select it), computed only where it is returned.
const (
	robotColumns = `u.id::text, u.name, u.status, COALESCE(u.owner_user_id::text, ''), COALESCE(o.email, ''), COALESCE(o.name, ''), u.created_at, NULL::timestamptz`
	ownerJoin    = ` LEFT JOIN users o ON o.id = u.owner_user_id`
	lastUsedSQL  = `(SELECT max(t.last_used_at) FROM api_tokens t WHERE t.user_id = u.id)`
)

func scanRobot(row pgx.CollectableRow) (portal.Robot, error) {
	var r portal.Robot
	err := row.Scan(&r.ID, &r.Name, &r.Status, &r.OwnerUserID, &r.OwnerEmail, &r.OwnerName, &r.CreatedAt, &r.LastUsedAt)
	return r, err
}

// ListRobots implements portal.RobotRepo.
func (r RobotsRepo) ListRobots(ctx context.Context, scope adminz.Scope, q portal.PageQuery) ([]portal.Robot, error) {
	if !scope.All {
		return nil, nil
	}
	rows, err := r.db.Query(ctx, `SELECT u.id::text, u.name, u.status, COALESCE(u.owner_user_id::text, ''), COALESCE(o.email, ''), COALESCE(o.name, ''), u.created_at, `+lastUsedSQL+`
FROM users u`+ownerJoin+` WHERE u.kind = 'robot' AND ($1::uuid IS NULL OR u.id > $1::uuid) AND ($3::text = '' OR u.status = $3::text) ORDER BY u.id LIMIT $2`, afterID(q.After), q.Limit, q.Status)
	if err != nil {
		return nil, fmt.Errorf("query robots: %w", err)
	}
	robots, err := pgx.CollectRows(rows, scanRobot)
	if err != nil {
		return nil, fmt.Errorf("read robots: %w", err)
	}
	return robots, nil
}

func (r RobotsRepo) one(ctx context.Context, suffix, id string) (portal.Robot, error) {
	rows, err := r.db.Query(ctx, `SELECT `+robotColumns+` FROM users u`+ownerJoin+` WHERE u.id = $1::uuid AND u.kind = 'robot'`+suffix, id)
	if err != nil {
		return portal.Robot{}, fmt.Errorf("query robot: %w", err)
	}
	rb, err := pgx.CollectOneRow(rows, scanRobot)
	if errors.Is(err, pgx.ErrNoRows) {
		return portal.Robot{}, portal.ErrNotFound
	}
	if err != nil {
		return portal.Robot{}, fmt.Errorf("read robot: %w", err)
	}
	return rb, nil
}

// LastUsed implements portal.RobotRepo.
func (r RobotsRepo) LastUsed(ctx context.Context, id string) (*time.Time, error) {
	var at *time.Time
	if err := r.db.QueryRow(ctx, `SELECT `+lastUsedSQL+` FROM users u WHERE u.id = $1::uuid`, id).Scan(&at); err != nil {
		return nil, fmt.Errorf("query robot last use: %w", err)
	}
	return at, nil
}

// GetRobot implements portal.RobotRepo.
func (r RobotsRepo) GetRobot(ctx context.Context, id string) (portal.Robot, error) {
	return r.one(ctx, "", id)
}

// LockRobot implements portal.RobotRepo; OF u keeps the lock on the user row, not on the tokens of the subquery.
func (r RobotsRepo) LockRobot(ctx context.Context, id string) (portal.Robot, error) {
	return r.one(ctx, " FOR NO KEY UPDATE OF u", id)
}

// InsertRobot implements portal.RobotRepo. The email is ” (users.email is NOT NULL and the CHECK demands it),
// there is no google_sub: the column list is what the portal role may insert.
func (r RobotsRepo) InsertRobot(ctx context.Context, name, ownerID string) (portal.Robot, error) {
	var id string
	err := r.db.QueryRow(ctx, `INSERT INTO users (kind, name, email, owner_user_id)
VALUES ('robot', $1, '', NULLIF($2, '')::uuid) RETURNING id::text`, name, ownerID).Scan(&id)
	if err != nil {
		return portal.Robot{}, writeError("insert robot", err)
	}
	return r.one(ctx, "", id)
}

// UpdateRobot implements portal.RobotRepo; the portal role may update name, owner_user_id, status and updated_at.
func (r RobotsRepo) UpdateRobot(ctx context.Context, rb portal.Robot) error {
	_, err := r.db.Exec(ctx, `UPDATE users SET name = $2, status = $3, owner_user_id = NULLIF($4, '')::uuid, updated_at = now()
WHERE id = $1::uuid AND kind = 'robot'`, rb.ID, rb.Name, rb.Status, rb.OwnerUserID)
	if err != nil {
		return writeError("update robot", err)
	}
	return nil
}
