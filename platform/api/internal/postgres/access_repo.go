package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"platform/api/internal/access"
)

// AccessRepository implements access.FactsReader.
type AccessRepository struct {
	pool *pgxpool.Pool
}

// NewAccessRepository builds the repository over a pool.
func NewAccessRepository(pool *pgxpool.Pool) *AccessRepository {
	return &AccessRepository{pool: pool}
}

var _ access.FactsReader = (*AccessRepository)(nil)

// accessFactsSQL loads everything the decision needs in one round trip.
// $1 = user id, $2 = host (already lowercase). No row means the host has no deployment.
// Roles are the distinct keys of non-expired grants on the app's initiative, direct or via groups.
const accessFactsSQL = `
SELECT a.id,
       i.id::text,
       i.status,
       u.status,
       ARRAY(
           SELECT DISTINCT ir.key
           FROM initiative_access ia
           JOIN initiative_roles ir ON ir.initiative_id = ia.initiative_id AND ir.id = ia.role_id
           WHERE ia.initiative_id = i.id
             AND (ia.expires_at IS NULL OR ia.expires_at > now())
             AND (ia.user_id = $1::uuid
                  OR ia.group_id IN (SELECT gm.group_id FROM group_members gm WHERE gm.user_id = $1::uuid))
           ORDER BY ir.key
       )
FROM deployments dep
JOIN apps a        ON a.id = dep.app_id
JOIN initiatives i ON i.id = a.initiative_id
LEFT JOIN users u  ON u.id = $1::uuid
WHERE dep.host = $2`

// AccessFacts implements access.FactsReader.
func (r *AccessRepository) AccessFacts(ctx context.Context, userID, host string) (access.Facts, error) {
	var (
		f          access.Facts
		userStatus *string
	)
	err := r.pool.QueryRow(ctx, accessFactsSQL, userID, host).
		Scan(&f.AppID, &f.InitiativeID, &f.InitiativeStatus, &userStatus, &f.Roles)
	if errors.Is(err, pgx.ErrNoRows) {
		return access.Facts{}, nil
	}
	if err != nil {
		return access.Facts{}, fmt.Errorf("query access facts: %w", err)
	}
	f.HostKnown = true
	if userStatus != nil {
		f.UserFound = true
		f.UserStatus = *userStatus
	}
	return f, nil
}
