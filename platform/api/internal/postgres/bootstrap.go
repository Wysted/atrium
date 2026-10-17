package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Wysted/atrium/platform/api/internal/audit"
)

// Reserved names of the portal registration. The initiative key is access.PlatformInitiativeKey; the strings
// repeat the portal service's target types and action names so bootstrap rows read like the admin API's.
const (
	platformInitiativeKey  = "platform"
	platformInitiativeName = "Platform"
	portalAppName          = "portal"
	// bootstrapActor is after.actor of every audit row written here (admin.sh writes "cli").
	bootstrapActor = "bootstrap"
)

// Bootstrap is the optional first-install data written by "platform-api migrate", as the owner role, after the
// migrations. Zero values skip the matching step. PortalAppID and PortalHost are set together or not at all
// (checked by the caller); values are validated by the caller too, the schema CHECKs are the backstop.
type Bootstrap struct {
	// PortalAppID and PortalHost register the portal: initiative "platform", the app, and a deployment of the host.
	PortalAppID string
	PortalHost  string
	// AdminEmail (lowercase) is made the first platform admin, only while platform_admins is empty.
	AdminEmail string
}

// ApplyBootstrap creates what b asks for and is missing, in one transaction. It never modifies an existing row;
// a row that conflicts with b is an error and nothing is written. Re-running it is a no-op.
func ApplyBootstrap(ctx context.Context, pool *pgxpool.Pool, b Bootstrap, log *slog.Logger) (err error) {
	if b == (Bootstrap{}) {
		return nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin bootstrap: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback(context.WithoutCancel(ctx)))
		}
	}()
	// Same key as Migrate: two migrate runs never bootstrap at once (the second then sees the rows of the first).
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationsLockKey); err != nil {
		return fmt.Errorf("lock bootstrap: %w", err)
	}
	changed := 0
	if b.PortalAppID != "" {
		n, err := bootstrapPortal(ctx, tx, b, log)
		if err != nil {
			return err
		}
		changed += n
	}
	if b.AdminEmail != "" {
		n, err := bootstrapAdmin(ctx, tx, b.AdminEmail, log)
		if err != nil {
			return err
		}
		changed += n
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit bootstrap: %w", err)
	}
	if changed == 0 {
		log.Info("bootstrap: nothing to do")
	}
	return nil
}

// bootstrapPortal ensures the initiative, the app and the deployment, returning how many rows it created.
func bootstrapPortal(ctx context.Context, tx pgx.Tx, b Bootstrap, log *slog.Logger) (created int, err error) {
	var initiativeID, initiativeStatus string
	err = tx.QueryRow(ctx, `SELECT id::text, status FROM initiatives WHERE key = $1`, platformInitiativeKey).Scan(&initiativeID, &initiativeStatus)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if err = tx.QueryRow(ctx, `INSERT INTO initiatives (key, name) VALUES ($1, $2) RETURNING id::text`,
			platformInitiativeKey, platformInitiativeName).Scan(&initiativeID); err != nil {
			return 0, fmt.Errorf("create initiative %q: %w", platformInitiativeKey, err)
		}
		if err = auditBootstrap(ctx, tx, initiativeID, "initiative.create", "initiative", initiativeID,
			map[string]any{"key": platformInitiativeKey, "name": platformInitiativeName, "status": "active"}); err != nil {
			return 0, err
		}
		log.Info("bootstrap: created initiative", "key", platformInitiativeKey)
		created++
	case err != nil:
		return 0, fmt.Errorf("read initiative %q: %w", platformInitiativeKey, err)
	case initiativeStatus != "active":
		log.Warn("bootstrap: initiative is not active; not modifying it", "key", platformInitiativeKey, "status", initiativeStatus)
	}

	var appInitiative, appStatus string
	err = tx.QueryRow(ctx, `SELECT initiative_id::text, status FROM apps WHERE id = $1`, b.PortalAppID).Scan(&appInitiative, &appStatus)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if _, err = tx.Exec(ctx, `INSERT INTO apps (id, initiative_id, name) VALUES ($1, $2::uuid, $3)`,
			b.PortalAppID, initiativeID, portalAppName); err != nil {
			return 0, fmt.Errorf("create app %s: %w", b.PortalAppID, err)
		}
		if err = auditBootstrap(ctx, tx, initiativeID, "app.create", "app", b.PortalAppID,
			map[string]any{"initiative_id": initiativeID, "name": portalAppName, "status": "active"}); err != nil {
			return 0, err
		}
		log.Info("bootstrap: created app", "app_id", b.PortalAppID, "initiative", platformInitiativeKey)
		created++
	case err != nil:
		return 0, fmt.Errorf("read app %s: %w", b.PortalAppID, err)
	case appInitiative != initiativeID:
		return 0, fmt.Errorf("app %s already exists in another initiative than %q: refusing to change it", b.PortalAppID, platformInitiativeKey)
	case appStatus != "active":
		log.Warn("bootstrap: portal app is disabled; not modifying it", "app_id", b.PortalAppID, "status", appStatus)
	}

	var hostApp string
	err = tx.QueryRow(ctx, `SELECT app_id FROM deployments WHERE host = $1`, b.PortalHost).Scan(&hostApp)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		var deploymentID string
		if err = tx.QueryRow(ctx, `INSERT INTO deployments (host, app_id) VALUES ($1, $2) RETURNING id::text`,
			b.PortalHost, b.PortalAppID).Scan(&deploymentID); err != nil {
			return 0, fmt.Errorf("create deployment %s: %w", b.PortalHost, err)
		}
		if err = auditBootstrap(ctx, tx, initiativeID, "deployment.create", "deployment", deploymentID,
			map[string]any{"app_id": b.PortalAppID, "host": b.PortalHost}); err != nil {
			return 0, err
		}
		log.Info("bootstrap: created deployment", "host", b.PortalHost, "app_id", b.PortalAppID)
		created++
	case err != nil:
		return 0, fmt.Errorf("read deployment %s: %w", b.PortalHost, err)
	case hostApp != b.PortalAppID:
		return 0, fmt.Errorf("host %s already maps to app %s, not %s: refusing to change it", b.PortalHost, hostApp, b.PortalAppID)
	}
	return created, nil
}

// bootstrapAdmin grants the first platform admin. It only acts while platform_admins is empty, so a deliberate
// revocation is never undone by a re-run. Returns 1 when it granted.
func bootstrapAdmin(ctx context.Context, tx pgx.Tx, email string, log *slog.Logger) (int, error) {
	var admins int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform_admins`).Scan(&admins); err != nil {
		return 0, fmt.Errorf("count platform admins: %w", err)
	}
	if admins > 0 {
		log.Info("bootstrap: platform admins already exist, skipping the bootstrap admin")
		return 0, nil
	}
	// Same predicate as overlays/local-google/admin.sh: an active real person, never a fake-login, robot or
	// disabled user.
	rows, err := tx.Query(ctx, `SELECT id::text FROM users
		WHERE lower(email) = $1 AND kind = 'human' AND google_sub NOT LIKE 'fake:%' AND status = 'active'`, email)
	if err != nil {
		return 0, fmt.Errorf("find bootstrap admin: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, fmt.Errorf("find bootstrap admin: %w", err)
	}
	switch len(ids) {
	case 0:
		log.Warn("bootstrap: the bootstrap admin has not signed in yet or is disabled (no active user with that email); sign in once and run the migrate Job again")
		return 0, nil
	case 1:
	default:
		return 0, fmt.Errorf("%d users match the bootstrap admin email: refusing to guess", len(ids))
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform_admins (user_id) VALUES ($1::uuid)`, ids[0]); err != nil {
		return 0, fmt.Errorf("grant platform admin: %w", err)
	}
	if err := auditBootstrap(ctx, tx, "", "platform_admin.grant", "platform_admin", ids[0],
		map[string]any{"email": email}); err != nil {
		return 0, err
	}
	log.Info("bootstrap: granted platform admin", "user_id", ids[0])
	return 1, nil
}

// auditBootstrap appends one audit row with no actor user; after.actor says who wrote it.
func auditBootstrap(ctx context.Context, tx pgx.Tx, initiativeID, action, targetType, targetID string, after map[string]any) error {
	after["actor"] = bootstrapActor
	return insertAudit(ctx, tx, audit.Event{
		InitiativeID: initiativeID, Action: action, TargetType: targetType, TargetID: targetID, After: after,
	})
}
