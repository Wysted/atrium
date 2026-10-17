package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Wysted/atrium/platform/api/internal/access"
)

// UserRepo reads and provisions users.
type UserRepo struct {
	pool *pgxpool.Pool
}

// NewUserRepo returns a UserRepo over pool.
func NewUserRepo(pool *pgxpool.Pool) *UserRepo { return &UserRepo{pool: pool} }

// UpsertUser creates the user on first login (just-in-time) or refreshes its informational email and name.
// Identity is googleSub; status is never changed here. It returns the internal id and whether the user is active.
func (r *UserRepo) UpsertUser(ctx context.Context, googleSub, email, name string) (id string, active bool, err error) {
	var status string
	err = r.pool.QueryRow(ctx, `
		INSERT INTO users (google_sub, email, name) VALUES ($1, $2, $3)
		ON CONFLICT (google_sub) DO UPDATE SET email = EXCLUDED.email, name = EXCLUDED.name, updated_at = now()
		RETURNING id::text, status`, googleSub, email, name).Scan(&id, &status)
	if err != nil {
		return "", false, fmt.Errorf("upsert user: %w", err)
	}
	return id, status == access.UserStatusActive, nil
}

// Profile returns the informational data of a user.
func (r *UserRepo) Profile(ctx context.Context, userID string) (access.Profile, error) {
	var p access.Profile
	if err := r.pool.QueryRow(ctx, `SELECT email, name FROM users WHERE id = $1::uuid`, userID).Scan(&p.Email, &p.Name); err != nil {
		return access.Profile{}, fmt.Errorf("load user profile: %w", err)
	}
	return p, nil
}
