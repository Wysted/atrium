package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DeploymentRepo resolves hosts to apps.
type DeploymentRepo struct {
	pool *pgxpool.Pool
}

// NewDeploymentRepo returns a DeploymentRepo over pool.
func NewDeploymentRepo(pool *pgxpool.Pool) *DeploymentRepo { return &DeploymentRepo{pool: pool} }

// AppForHost returns the app id of the deployment on host (exact, lowercase), or found=false.
func (r *DeploymentRepo) AppForHost(ctx context.Context, host string) (appID string, found bool, err error) {
	err = r.pool.QueryRow(ctx, `SELECT app_id FROM deployments WHERE host = $1`, host).Scan(&appID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("resolve host: %w", err)
	}
	return appID, true, nil
}
