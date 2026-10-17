package postgres_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Wysted/atrium/platform/api/internal/postgres"
)

const bootstrapHost = "portal.apps.example.test"

func applyBootstrap(t *testing.T, pool *pgxpool.Pool, b postgres.Bootstrap) (logs string, err error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	var buf bytes.Buffer
	err = postgres.ApplyBootstrap(ctx, pool, b, slog.New(slog.NewTextHandler(&buf, nil)))
	return buf.String(), err
}

func count(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	return scalar[int](t, pool, `SELECT count(*) FROM `+table)
}

func TestBootstrapPortal(t *testing.T) {
	t.Parallel()
	b := postgres.Bootstrap{PortalAppID: appID(9001), PortalHost: bootstrapHost}

	t.Run("fresh database creates the registration, then re-runs are a no-op", func(t *testing.T) {
		t.Parallel()
		pool := newTestDB(t)
		if _, err := applyBootstrap(t, pool, b); err != nil {
			t.Fatal(err)
		}
		if got := scalar[string](t, pool, `SELECT i.key FROM apps a JOIN initiatives i ON i.id = a.initiative_id WHERE a.id = $1`, b.PortalAppID); got != "platform" {
			t.Fatalf("app initiative = %q", got)
		}
		if got := scalar[string](t, pool, `SELECT app_id FROM deployments WHERE host = $1`, bootstrapHost); got != b.PortalAppID {
			t.Fatalf("deployment app = %q", got)
		}
		if got := count(t, pool, "audit_events"); got != 3 {
			t.Fatalf("audit rows = %d, want 3", got)
		}
		if got := scalar[string](t, pool, `SELECT after->>'actor' FROM audit_events WHERE action = 'deployment.create'`); got != "bootstrap" {
			t.Fatalf("audit actor = %q", got)
		}
		logs, err := applyBootstrap(t, pool, b)
		if err != nil || !strings.Contains(logs, "nothing to do") {
			t.Fatalf("re-run: err=%v logs=%q", err, logs)
		}
		if count(t, pool, "audit_events") != 3 || count(t, pool, "deployments") != 1 || count(t, pool, "apps") != 1 {
			t.Fatal("re-run changed rows")
		}
	})

	t.Run("existing initiative and extra deployments of the app are fine", func(t *testing.T) {
		t.Parallel()
		pool := newTestDB(t)
		w := world{pool: pool}
		ini := w.initiative(t, "platform", "active")
		mustExec(t, pool, `INSERT INTO apps (id, initiative_id, name) VALUES ($1, $2, 'mine')`, b.PortalAppID, ini)
		mustExec(t, pool, `INSERT INTO deployments (host, app_id) VALUES ('other.apps.example.test', $1)`, b.PortalAppID)
		if _, err := applyBootstrap(t, pool, b); err != nil {
			t.Fatal(err)
		}
		if got := scalar[string](t, pool, `SELECT name FROM apps WHERE id = $1`, b.PortalAppID); got != "mine" {
			t.Fatalf("existing app was modified: name = %q", got)
		}
		if got := count(t, pool, "deployments"); got != 2 {
			t.Fatalf("deployments = %d, want 2", got)
		}
	})

	t.Run("host mapped to another app is an error and writes nothing", func(t *testing.T) {
		t.Parallel()
		pool := newTestDB(t)
		w := world{pool: pool}
		other := w.app(t, w.initiative(t, "demo", "active"), bootstrapHost)
		_, err := applyBootstrap(t, pool, b)
		if err == nil || !strings.Contains(err.Error(), "already maps to app "+other) {
			t.Fatalf("error = %v", err)
		}
		if count(t, pool, "audit_events") != 0 || scalar[int](t, pool, `SELECT count(*) FROM initiatives WHERE key = 'platform'`) != 0 {
			t.Fatal("a failed bootstrap left rows behind")
		}
	})

	t.Run("disabled app and inactive initiative warn and are left alone", func(t *testing.T) {
		t.Parallel()
		pool := newTestDB(t)
		w := world{pool: pool}
		ini := w.initiative(t, "platform", "inactive")
		mustExec(t, pool, `INSERT INTO apps (id, initiative_id, name, status) VALUES ($1, $2, 'portal', 'disabled')`, b.PortalAppID, ini)
		mustExec(t, pool, `INSERT INTO deployments (host, app_id) VALUES ($1, $2)`, bootstrapHost, b.PortalAppID)
		logs, err := applyBootstrap(t, pool, b)
		if err != nil || !strings.Contains(logs, "portal app is disabled") || !strings.Contains(logs, "initiative is not active") {
			t.Fatalf("err=%v logs=%q", err, logs)
		}
		if scalar[string](t, pool, `SELECT status FROM apps`) != "disabled" || scalar[string](t, pool, `SELECT status FROM initiatives`) != "inactive" ||
			count(t, pool, "audit_events") != 0 {
			t.Fatal("bootstrap modified or wrote rows")
		}
	})

	t.Run("app in another initiative is an error", func(t *testing.T) {
		t.Parallel()
		pool := newTestDB(t)
		w := world{pool: pool}
		mustExec(t, pool, `INSERT INTO apps (id, initiative_id, name) VALUES ($1, $2, 'x')`, b.PortalAppID, w.initiative(t, "demo", "active"))
		_, err := applyBootstrap(t, pool, b)
		if err == nil || !strings.Contains(err.Error(), "another initiative") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestBootstrapAdmin(t *testing.T) {
	t.Parallel()
	const email = "root@example.com"
	b := postgres.Bootstrap{AdminEmail: email}
	addUser := func(t *testing.T, pool *pgxpool.Pool, sub, mail string) string {
		t.Helper()
		return scalar[string](t, pool, `INSERT INTO users (google_sub, email) VALUES ($1, $2) RETURNING id::text`, sub, mail)
	}

	t.Run("empty table and one user grants, once", func(t *testing.T) {
		t.Parallel()
		pool := newTestDB(t)
		uid := addUser(t, pool, "sub-root", "Root@Example.com")
		if _, err := applyBootstrap(t, pool, b); err != nil {
			t.Fatal(err)
		}
		if got := scalar[string](t, pool, `SELECT user_id::text FROM platform_admins`); got != uid {
			t.Fatalf("admin = %q, want %q", got, uid)
		}
		if got := scalar[string](t, pool, `SELECT target_id || ' ' || (after->>'actor') FROM audit_events WHERE action = 'platform_admin.grant'`); got != uid+" bootstrap" {
			t.Fatalf("audit = %q", got)
		}
		if _, err := applyBootstrap(t, pool, b); err != nil || count(t, pool, "audit_events") != 1 {
			t.Fatalf("re-run: err=%v audit=%d", err, count(t, pool, "audit_events"))
		}
	})

	t.Run("no such user warns and succeeds", func(t *testing.T) {
		t.Parallel()
		pool := newTestDB(t)
		logs, err := applyBootstrap(t, pool, b)
		if err != nil || !strings.Contains(logs, "level=WARN") || count(t, pool, "platform_admins") != 0 {
			t.Fatalf("err=%v logs=%q", err, logs)
		}
	})

	t.Run("fake-login users are ignored", func(t *testing.T) {
		t.Parallel()
		pool := newTestDB(t)
		addUser(t, pool, "fake:"+email, email)
		logs, err := applyBootstrap(t, pool, b)
		if err != nil || !strings.Contains(logs, "level=WARN") || count(t, pool, "platform_admins") != 0 {
			t.Fatalf("err=%v logs=%q", err, logs)
		}
	})

	t.Run("disabled user is not granted", func(t *testing.T) {
		t.Parallel()
		pool := newTestDB(t)
		mustExec(t, pool, `INSERT INTO users (google_sub, email, status) VALUES ('sub-root', $1, 'disabled')`, email)
		logs, err := applyBootstrap(t, pool, b)
		if err != nil || !strings.Contains(logs, "level=WARN") || count(t, pool, "platform_admins") != 0 {
			t.Fatalf("err=%v logs=%q", err, logs)
		}
	})

	t.Run("non-empty platform_admins is left alone", func(t *testing.T) {
		t.Parallel()
		pool := newTestDB(t)
		addUser(t, pool, "sub-root", email)
		other := addUser(t, pool, "sub-other", "other@example.com")
		mustExec(t, pool, `INSERT INTO platform_admins (user_id) VALUES ($1)`, other)
		logs, err := applyBootstrap(t, pool, b)
		if err != nil || !strings.Contains(logs, "skipping") || count(t, pool, "platform_admins") != 1 {
			t.Fatalf("err=%v logs=%q", err, logs)
		}
	})

	t.Run("two matching users is an error", func(t *testing.T) {
		t.Parallel()
		pool := newTestDB(t)
		addUser(t, pool, "sub-1", email)
		addUser(t, pool, "sub-2", email)
		if _, err := applyBootstrap(t, pool, b); err == nil || !strings.Contains(err.Error(), "2 users match") {
			t.Fatalf("error = %v", err)
		}
	})
}
