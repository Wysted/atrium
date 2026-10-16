package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"platform/api/internal/portal"
	"platform/api/internal/postgres"
	"platform/api/internal/testdb"
)

func TestPortalPoolRunsAsThePortalRole(t *testing.T) {
	t.Parallel()
	owner := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	// The test connection (owner role, superuser in the compose database) must be refused.
	if _, err := postgres.CheckPortalRole(ctx, owner); err == nil || !strings.Contains(err.Error(), "refusing to run") {
		t.Fatalf("owner pool accepted as portal: %v", err)
	}
	// The runtime role is a valid role but not the portal one.
	rt := runtimePool(t, owner)
	if _, err := postgres.CheckPortalRole(ctx, rt); err == nil || !strings.Contains(err.Error(), "refusing to run") {
		t.Fatalf("runtime pool accepted as portal: %v", err)
	}

	pool := testdb.Portal(t, owner)
	for range 3 { // every connection of the pool, including ones created later, has the role from the start
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Release()
		var cur string
		if err := conn.QueryRow(ctx, `SELECT current_user::text`).Scan(&cur); err != nil || cur != postgres.PortalRole {
			t.Fatalf("current_user = %q, %v", cur, err)
		}
	}

	// What the portal may do.
	uid := scalar[string](t, owner, `INSERT INTO users (google_sub, email) VALUES ('s-1', 'a@example.com') RETURNING id::text`)
	for _, stmt := range []string{
		`UPDATE users SET status = 'disabled', updated_at = now()`,
		`INSERT INTO groups (key) VALUES ('g1')`,
		`INSERT INTO audit_events (action, target_type, target_id) VALUES ('x', 'y', 'z')`,
		`DELETE FROM sessions`,
		`DELETE FROM app_sessions`,
		`SELECT id, user_id, expires_at FROM sessions`,
		`SELECT * FROM access_denials`,
		`SELECT * FROM platform_admins`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Errorf("%q as portal: %v", stmt, err)
		}
	}
	// What it must not.
	for _, stmt := range []string{
		`UPDATE users SET email = 'x@example.com'`,
		`UPDATE users SET google_sub = 'other'`,
		`INSERT INTO users (google_sub, email) VALUES ('s-2', 'b@example.com')`,
		`DELETE FROM users`,
		`SELECT token_hash FROM sessions`,
		`SELECT token_hash FROM app_sessions`,
		`SELECT * FROM signing_keys`,
		`SELECT * FROM oauth_flows`,
		`SELECT * FROM login_codes`,
		`SELECT * FROM schema_migrations`,
		`INSERT INTO platform_admins (user_id) SELECT id FROM users`,
		`INSERT INTO access_denials (host, reason) VALUES ('h', 'r')`,
		`UPDATE audit_events SET action = 'tampered'`,
		`DELETE FROM audit_events`,
		`TRUNCATE audit_events`,
		`CREATE TABLE rogue (x int)`,
	} {
		if _, err := pool.Exec(ctx, stmt); pgErrCode(err) != codeInsufficientPrivs {
			t.Errorf("%q as portal: err = %v, want insufficient_privilege", stmt, err)
		}
	}
	if st := scalar[string](t, owner, `SELECT status FROM users WHERE id = $1`, uid); st != "disabled" {
		t.Errorf("status = %q, want disabled", st)
	}
}

// A portal role that was granted more than the schema intends is refused at startup.
func TestCheckPortalRoleRefusesExtraPrivileges(t *testing.T) {
	t.Parallel()
	for name, grant := range map[string]string{
		"select on signing_keys":     `GRANT SELECT ON signing_keys TO platform_portal`,
		"column select on oauth":     `GRANT SELECT (token_hash) ON oauth_flows TO platform_portal`,
		"delete on login_codes":      `GRANT DELETE ON login_codes TO platform_portal`,
		"update of user email":       `GRANT UPDATE (email) ON users TO platform_portal`,
		"insert on users":            `GRANT INSERT ON users TO platform_portal`,
		"delete on users":            `GRANT DELETE ON users TO platform_portal`,
		"token hash of sessions":     `GRANT SELECT (token_hash) ON sessions TO platform_portal`,
		"token hash of app sessions": `GRANT SELECT (token_hash) ON app_sessions TO platform_portal`,
		"write on platform_admins":   `GRANT INSERT ON platform_admins TO platform_portal`,
		"write on access_denials":    `GRANT UPDATE ON access_denials TO platform_portal`,
		"update on audit_events":     `GRANT UPDATE ON audit_events TO platform_portal`,
		"create in public":           `GRANT CREATE ON SCHEMA public TO platform_portal`,
		"truncate on apps":           `GRANT TRUNCATE ON apps TO platform_portal`,
		"truncate on initiatives":    `GRANT TRUNCATE ON initiatives TO platform_portal`,
		"trigger on groups":          `GRANT TRIGGER ON groups TO platform_portal`,
		"trigger on audit_events":    `GRANT TRIGGER ON audit_events TO platform_portal`,
		"references on deployments":  `GRANT REFERENCES ON deployments TO platform_portal`,
		"references column on users": `GRANT REFERENCES (id) ON users TO platform_portal`,
		"truncate on sessions":       `GRANT TRUNCATE ON sessions TO platform_portal`,
	} {
		t.Run(name, func(t *testing.T) {
			owner := newTestDB(t)
			mustExec(t, owner, grant)
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			_, _, err := postgres.OpenPortalPool(ctx, owner.Config())
			if err == nil || !strings.Contains(err.Error(), "refusing to run") {
				t.Fatalf("err = %v, want a refusal", err)
			}
		})
	}
}

// portalWorld builds scenarios on the owner connection.
type portalWorld struct {
	t     *testing.T
	owner world
}

func newPortalWorld(t *testing.T, owner world) portalWorld { return portalWorld{t: t, owner: owner} }

// managerRole creates a role with manages_access in the initiative.
func (w portalWorld) managerRole(initiativeID, key string) string {
	w.t.Helper()
	return scalar[string](w.t, w.owner.pool,
		`INSERT INTO initiative_roles (initiative_id, key, manages_access) VALUES ($1, $2, true) RETURNING id::text`, initiativeID, key)
}

func (w portalWorld) platformAdmin(userID string) {
	w.t.Helper()
	mustExec(w.t, w.owner.pool, `INSERT INTO platform_admins (user_id) VALUES ($1)`, userID)
}

func TestLoadPermissions(t *testing.T) {
	t.Parallel()
	owner := newTestDB(t)
	pool := testdb.Portal(t, owner)
	w := world{pool: owner}
	pw := newPortalWorld(t, w)

	initA := w.initiative(t, "alpha", "active")
	initB := w.initiative(t, "beta", "active")
	initC := w.initiative(t, "gamma", "inactive")
	initD := w.initiative(t, "delta", "active")
	mgrA, mgrB, mgrC := pw.managerRole(initA, "owner"), pw.managerRole(initB, "owner"), pw.managerRole(initC, "owner")
	mgrD := pw.managerRole(initD, "owner")
	editorA := w.role(t, initA, "editor")

	admin := w.user(t, "active")
	pw.platformAdmin(admin)

	direct := w.user(t, "active") // manager of beta and alpha through different paths, plus an editor role elsewhere
	w.grantUser(t, initB, mgrB, direct, "")
	grp := w.group(t, "ops")
	w.member(t, grp, direct)
	w.grantGroup(t, initA, mgrA, grp)
	w.grantUser(t, initD, mgrD, direct, "2000-01-01T00:00:00Z") // expired: not managed

	editorOnly := w.user(t, "active")
	w.grantUser(t, initA, editorA, editorOnly, "")

	inactiveInit := w.user(t, "active") // managing an inactive initiative still counts as managing it
	w.grantUser(t, initC, mgrC, inactiveInit, "")

	disabled := w.user(t, "disabled")
	w.grantUser(t, initA, mgrA, disabled, "")

	repo := postgres.NewPermissionsRepo(pool)
	tests := []struct {
		name    string
		userID  string
		admin   bool
		status  string
		managed []string // initiative keys, sorted
	}{
		{"platform admin", admin, true, "active", nil},
		{"direct and group grants, expired ignored", direct, false, "active", []string{"alpha", "beta"}},
		{"role without manages_access", editorOnly, false, "active", nil},
		{"inactive initiative", inactiveInit, false, "active", []string{"gamma"}},
		{"disabled user is reported as such", disabled, false, "disabled", []string{"alpha"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			got, err := repo.LoadPermissions(ctx, tt.userID)
			if err != nil {
				t.Fatal(err)
			}
			var keys []string
			for _, m := range got.Managed {
				keys = append(keys, m.Key)
			}
			if got.PlatformAdmin != tt.admin || got.Status != tt.status || strings.Join(keys, ",") != strings.Join(tt.managed, ",") {
				t.Fatalf("permissions = %+v (keys %v), want admin=%v status=%s managed=%v", got, keys, tt.admin, tt.status, tt.managed)
			}
			if got.Managed == nil || got.Email == "" || got.UserID != tt.userID {
				t.Fatalf("incomplete permissions: %+v", got)
			}
		})
	}

	t.Run("unknown user", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()
		_, err := repo.LoadPermissions(ctx, "00000000-0000-0000-0000-000000000000")
		if !errors.Is(err, portal.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

func TestStoreWithTx(t *testing.T) {
	t.Parallel()
	owner := newTestDB(t)
	pool := testdb.Portal(t, owner)
	store := postgres.NewPortalStore(pool)
	w := world{pool: owner}
	uid := w.user(t, "active")

	t.Run("repositories read through the transaction", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()
		err := store.WithTx(ctx, func(tx portal.Tx) error {
			p, err := tx.Permissions().LoadPermissions(ctx, uid)
			if err != nil {
				return err
			}
			if p.UserID != uid {
				return fmt.Errorf("permissions of %q, want %q", p.UserID, uid)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("an error from fn rolls back and is returned as is", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()
		boom := errors.New("boom")
		var calls int
		err := store.WithTx(ctx, func(portal.Tx) error {
			calls++
			return boom
		})
		if !errors.Is(err, boom) || calls != 1 {
			t.Fatalf("err = %v after %d calls, want boom after 1", err, calls)
		}
	})
}

// A panic inside fn rolls the transaction back (nothing it wrote survives, no connection is left in a
// transaction) and propagates to the caller.
func TestStoreRollsBackOnPanic(t *testing.T) {
	t.Parallel()
	owner := newTestDB(t)
	pool := testdb.Portal(t, owner)
	store := postgres.NewPortalStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	func() {
		defer func() {
			if r := recover(); r != "boom" {
				t.Fatalf("recovered %v, want the original panic", r)
			}
		}()
		_ = store.WithTx(ctx, func(tx portal.Tx) error {
			if err := postgres.TxExec(ctx, tx, `INSERT INTO groups (key) VALUES ('ghost')`); err != nil {
				return err
			}
			panic("boom")
		})
		t.Fatal("WithTx returned after a panic")
	}()

	if n := scalar[int](t, owner, `SELECT count(*) FROM groups WHERE key = 'ghost'`); n != 0 {
		t.Fatalf("the write of a panicking unit of work survived (%d rows)", n)
	}
	if open := scalar[int](t, owner, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND state LIKE 'idle in transaction%'`); open != 0 {
		t.Fatalf("%d connections left in a transaction", open)
	}
}

// A real deadlock: two units of work lock the same two rows in opposite order. PostgreSQL aborts one with
// 40P01 after deadlock_timeout (1 s by default); WithTx retries it in a new transaction and both finish.
func TestStoreRetriesARealDeadlock(t *testing.T) {
	t.Parallel()
	owner := newTestDB(t)
	pool := testdb.Portal(t, owner)
	store := postgres.NewPortalStore(pool)
	w := world{pool: owner}
	g1, g2 := w.group(t, "g1"), w.group(t, "g2")

	var (
		bothHoldOne sync.WaitGroup // each worker holds its first lock before asking for the second
		attempts    atomic.Int64
	)
	bothHoldOne.Add(2)
	worker := func(first, second string) error {
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()
		firstAttempt := true
		return store.WithTx(ctx, func(tx portal.Tx) error {
			attempts.Add(1)
			if err := postgres.TxExec(ctx, tx, `SELECT id FROM groups WHERE id = $1 FOR UPDATE`, first); err != nil {
				return err
			}
			if firstAttempt {
				firstAttempt = false
				bothHoldOne.Done()
				bothHoldOne.Wait()
			}
			return postgres.TxExec(ctx, tx, `SELECT id FROM groups WHERE id = $1 FOR UPDATE`, second)
		})
	}

	errs := make(chan error, 2)
	go func() { errs <- worker(g1, g2) }()
	go func() { errs <- worker(g2, g1) }()
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("worker: %v", err)
		}
	}
	// Two first attempts, one of them aborted by the deadlock detector and run again.
	if n := attempts.Load(); n < 3 {
		t.Fatalf("attempts = %d, want at least 3 (the victim must have been retried)", n)
	}
}

func TestIsRetryable(t *testing.T) {
	t.Parallel()
	pgErr := func(code string) error { return &pgconn.PgError{Code: code} }
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"deadlock", pgErr("40P01"), true},
		{"serialization failure", pgErr("40001"), true},
		{"wrapped deadlock", fmt.Errorf("lock grant: %w", pgErr("40P01")), true},
		{"serialization failure at commit", fmt.Errorf("commit transaction: %w", pgErr("40001")), true},
		{"joined", errors.Join(errors.New("x"), pgErr("40001")), true},
		{"unique violation", pgErr("23505"), false},
		{"insufficient privilege", pgErr("42501"), false},
		{"plain error", errors.New("40001"), false},
		{"context canceled", context.Canceled, false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := postgres.IsRetryable(tt.err); got != tt.want {
				t.Fatalf("IsRetryable(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestWithRetry(t *testing.T) {
	t.Parallel()
	retryable := &pgconn.PgError{Code: "40001"}
	fatal := errors.New("fatal")

	tests := []struct {
		name      string
		results   []error // result of each attempt; the last one repeats
		wantCalls int
		wantErr   error // nil: success
		exhausted bool
	}{
		{"first attempt succeeds", []error{nil}, 1, nil, false},
		{"retries then succeeds", []error{retryable, retryable, nil}, 3, nil, false},
		{"a commit-time failure is retried like any other", []error{fmt.Errorf("commit transaction: %w", retryable), nil}, 2, nil, false},
		{"gives up after the maximum", []error{retryable}, postgres.MaxTxAttempts, retryable, true},
		{"does not retry other errors", []error{fatal}, 1, fatal, false},
		{"stops at the first non-retryable error", []error{retryable, fatal}, 2, fatal, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var calls int
			var waits []time.Duration
			sleep := func(_ context.Context, d time.Duration) error {
				waits = append(waits, d)
				return nil
			}
			err := postgres.WithRetry(context.Background(), postgres.MaxTxAttempts, 10*time.Millisecond, sleep, func() error {
				r := tt.results[min(calls, len(tt.results)-1)]
				calls++
				return r
			})
			if calls != tt.wantCalls {
				t.Errorf("calls = %d, want %d", calls, tt.wantCalls)
			}
			if !errors.Is(err, tt.wantErr) && tt.wantErr != nil || err != nil && tt.wantErr == nil {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.exhausted && (err == nil || !strings.Contains(err.Error(), "giving up") || !errors.Is(err, portal.ErrContention)) {
				t.Errorf("exhausted error = %v, want a 'giving up' annotation wrapping ErrContention", err)
			}
			if !tt.exhausted && errors.Is(err, portal.ErrContention) {
				t.Errorf("err = %v must not be contention", err)
			}
			// Linear backoff between attempts, none before the first.
			for i, d := range waits {
				if want := time.Duration(i+1) * 10 * time.Millisecond; d != want {
					t.Errorf("wait %d = %s, want %s", i, d, want)
				}
			}
			if len(waits) != tt.wantCalls-1 && !tt.exhausted {
				t.Errorf("waits = %d, want %d", len(waits), tt.wantCalls-1)
			}
		})
	}

	t.Run("a canceled context stops the retries", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		var calls int
		sleep := func(ctx context.Context, _ time.Duration) error {
			cancel()
			return ctx.Err()
		}
		err := postgres.WithRetry(ctx, postgres.MaxTxAttempts, time.Millisecond, sleep, func() error {
			calls++
			return retryable
		})
		if calls != 1 || !errors.Is(err, context.Canceled) || !errors.Is(err, retryable) {
			t.Fatalf("err = %v after %d calls", err, calls)
		}
	})
}
