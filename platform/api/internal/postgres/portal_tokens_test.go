package postgres_test

import (
	"errors"
	"github.com/jackc/pgx/v5"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Wysted/atrium/platform/api/internal/apitoken"
	"github.com/Wysted/atrium/platform/api/internal/portal"
	"github.com/Wysted/atrium/platform/api/internal/testdb"
)

// lastAuditToken returns the token_id of the newest audit row (empty: NULL).
func (e *adminEnv) lastAuditToken() string {
	e.t.Helper()
	return scalar[string](e.t, e.owner, `SELECT COALESCE(token_id::text, '') FROM audit_events ORDER BY occurred_at DESC, id DESC LIMIT 1`)
}

func (e *adminEnv) liveTokens(owner string) int {
	e.t.Helper()
	return e.count(`SELECT count(*) FROM api_tokens WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()`, owner)
}

func hours(n int) *time.Duration {
	d := time.Duration(n) * time.Hour
	return &d
}

func TestCreateOwnToken(t *testing.T) {
	t.Parallel()
	e := newAdminEnvCfg(t, func(s portal.Store) portal.Store { return s }, func(c *portal.Config) { c.TokenMaxTTLPersonal = 48 * time.Hour })
	ctx := e.ctx()
	alpha := e.initiative("alpha")
	admin := e.admin()
	mgr := e.manager(alpha.id, alpha.manager)
	plain := e.w.user(t, "active")

	t.Run("a user without roles creates a read token; the secret is stored only as its hash", func(t *testing.T) {
		before := e.auditCount()
		tok, err := e.svc.CreateOwnToken(ctx, plain, portal.NewToken{Name: "  laptop ", Scope: "read"})
		if err != nil {
			t.Fatal(err)
		}
		if !apitoken.WellFormed(tok.Secret) || tok.Name != "laptop" || tok.Scope != "read" || tok.UserID != plain || tok.CreatedBy != plain ||
			tok.LastUsedAt != nil || tok.RevokedAt != nil {
			t.Fatalf("token = %+v", tok)
		}
		// No expires_in: the cap (48h here).
		if got := tok.ExpiresAt.Sub(tok.CreatedAt); got != 48*time.Hour {
			t.Fatalf("lifetime = %s, want the cap", got)
		}
		if n := e.count(`SELECT count(*) FROM api_tokens WHERE id = $1 AND token_hash = sha256($2::bytea)`, tok.ID, tok.Secret); n != 1 {
			t.Fatal("the stored hash is not the SHA-256 of the secret")
		}
		if got := e.auditCount(); got != before+1 {
			t.Fatalf("audit rows %d -> %d", before, got)
		}
		a := e.lastAudit()
		if a.Actor != plain || a.Action != "token.create" || a.TargetType != "token" || a.TargetID != tok.ID || a.Initiative != "" ||
			a.Before != nil || a.After["name"] != "laptop" || a.After["scope"] != "read" || a.After["owner_id"] != plain || a.After["expires_at"] == nil {
			t.Fatalf("audit = %+v", a)
		}
		if tid := e.lastAuditToken(); tid != "" {
			t.Fatalf("a browser session recorded token_id %q", tid)
		}
		for k := range a.After {
			if k == "token_hash" || k == "secret" {
				t.Fatalf("audit document carries %q", k)
			}
		}
	})

	t.Run("expires_in shorter than the cap is honored; longer or not positive is a 422", func(t *testing.T) {
		tok, err := e.svc.CreateOwnToken(ctx, plain, portal.NewToken{Name: "short", Scope: "read", ExpiresIn: hours(2)})
		if err != nil || tok.ExpiresAt.Sub(tok.CreatedAt) != 2*time.Hour {
			t.Fatalf("token = %+v, %v", tok, err)
		}
		for name, d := range map[string]*time.Duration{"over the cap": hours(49), "zero": hours(0), "negative": hours(-1)} {
			_, err := e.svc.CreateOwnToken(ctx, plain, portal.NewToken{Name: "x", Scope: "read", ExpiresIn: d})
			if p := validationFields(t, err); len(p) != 1 || p[0].Field != "expires_in" {
				t.Errorf("%s: problems = %v", name, p)
			}
		}
		if _, err := e.svc.CreateOwnToken(ctx, plain, portal.NewToken{Name: "edge", Scope: "read", ExpiresIn: hours(48)}); err != nil {
			t.Fatalf("exactly the cap must be accepted: %v", err)
		}
	})

	t.Run("name and scope are validated", func(t *testing.T) {
		tests := []struct {
			name string
			in   portal.NewToken
			want []portal.FieldProblem
		}{
			{"empty name", portal.NewToken{Name: "  ", Scope: "read"}, []portal.FieldProblem{{Field: "name", Code: portal.CodeRequired}}},
			{"name with NUL", portal.NewToken{Name: "a\x00b", Scope: "read"}, []portal.FieldProblem{{Field: "name", Code: portal.CodeInvalid}}},
			{"name too long", portal.NewToken{Name: string(slices.Repeat([]byte("a"), 65)), Scope: "read"}, []portal.FieldProblem{{Field: "name", Code: portal.CodeInvalid}}},
			{"no scope", portal.NewToken{Name: "x"}, []portal.FieldProblem{{Field: "scope", Code: portal.CodeRequired}}},
			{"unknown scope", portal.NewToken{Name: "x", Scope: "write"}, []portal.FieldProblem{{Field: "scope", Code: portal.CodeInvalid}}},
			{"both wrong", portal.NewToken{Scope: "root"}, []portal.FieldProblem{{Field: "name", Code: portal.CodeRequired}, {Field: "scope", Code: portal.CodeInvalid}}},
		}
		for _, tt := range tests {
			_, err := e.svc.CreateOwnToken(ctx, plain, tt.in)
			if got := validationFields(t, err); !slices.Equal(got, tt.want) {
				t.Errorf("%s: problems = %v, want %v", tt.name, got, tt.want)
			}
		}
		if n := e.count(`SELECT count(*) FROM api_tokens WHERE name = 'x'`); n != 0 {
			t.Fatalf("%d tokens were stored by rejected requests", n)
		}
	})

	t.Run("admin scope only for a platform admin or an initiative manager at that moment", func(t *testing.T) {
		_, err := e.svc.CreateOwnToken(ctx, plain, portal.NewToken{Name: "a", Scope: "admin"})
		if p := validationFields(t, err); len(p) != 1 || p[0].Field != "scope" || p[0].Code != portal.CodeNotAllowed {
			t.Fatalf("plain user: problems = %v", p)
		}
		for name, who := range map[string]string{"admin": admin, "manager": mgr} {
			tok, err := e.svc.CreateOwnToken(ctx, who, portal.NewToken{Name: "a", Scope: "admin"})
			if err != nil || tok.Scope != "admin" {
				t.Errorf("%s: %+v, %v", name, tok, err)
			}
		}
		// A manager whose grant expired is no manager.
		expired := e.w.user(t, "active")
		e.w.grantUser(t, alpha.id, alpha.manager, expired, "2000-01-01T00:00:00Z")
		if _, err := e.svc.CreateOwnToken(ctx, expired, portal.NewToken{Name: "a", Scope: "admin"}); err == nil {
			t.Fatal("a manager with an expired grant got an admin token")
		}
	})

	t.Run("a request authenticated by a token never creates one", func(t *testing.T) {
		before := e.count(`SELECT count(*) FROM api_tokens`)
		_, err := e.svc.CreateOwnToken(portal.WithToken(ctx, "01a14296-1449-7be0-957b-4b1ad8d7a999"), admin, portal.NewToken{Name: "x", Scope: "read"})
		if !errors.Is(err, portal.ErrForbidden) || e.count(`SELECT count(*) FROM api_tokens`) != before {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a disabled user, an unknown id and a robot cannot create", func(t *testing.T) {
		disabled := e.w.user(t, "disabled")
		robot := e.w.robot(t, "ci-bot")
		for name, id := range map[string]string{"disabled": disabled, "robot": robot, "unknown": "01a14296-1449-7be0-957b-4b1ad8d7a998"} {
			if _, err := e.svc.CreateOwnToken(ctx, id, portal.NewToken{Name: "x", Scope: "read"}); !errors.Is(err, portal.ErrUserInactive) {
				t.Errorf("%s: err = %v, want ErrUserInactive", name, err)
			}
		}
	})
}

func TestTokenLimitPerOwner(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	owner := e.w.user(t, "active")

	// 26 creations at once: never more than 25 live tokens, and exactly one is refused.
	var (
		wg   sync.WaitGroup
		errs = make([]error, portal.MaxLiveTokens+1)
	)
	for i := range errs {
		wg.Go(func() {
			_, errs[i] = e.svc.CreateOwnToken(ctx, owner, portal.NewToken{Name: "t", Scope: "read"})
		})
	}
	wg.Wait()
	var refused int
	for _, err := range errs {
		switch {
		case err == nil:
		case errors.Is(err, portal.ErrTokenLimit):
			refused++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if got := e.liveTokens(owner); got != portal.MaxLiveTokens || refused != 1 {
		t.Fatalf("live tokens = %d, refused = %d; want %d and 1", got, refused, portal.MaxLiveTokens)
	}

	// Revoking one frees a place; a dead token (expired or revoked) never counts.
	list, err := e.svc.ListOwnTokens(ctx, owner, portal.PageRequest{Limit: 1})
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err := e.svc.RevokeOwnToken(ctx, owner, list.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	e.w.apiToken(t, owner, "-2 days", "-1 day", "") // expired: does not count
	if _, err := e.svc.CreateOwnToken(ctx, owner, portal.NewToken{Name: "again", Scope: "read"}); err != nil {
		t.Fatalf("after a revocation: %v", err)
	}
	if _, err := e.svc.CreateOwnToken(ctx, owner, portal.NewToken{Name: "over", Scope: "read"}); !errors.Is(err, portal.ErrTokenLimit) {
		t.Fatalf("over the limit: %v", err)
	}
	// The limit is per owner.
	if _, err := e.svc.CreateOwnToken(ctx, e.w.user(t, "active"), portal.NewToken{Name: "other", Scope: "read"}); err != nil {
		t.Fatalf("another owner: %v", err)
	}
}

// No token survives the disable of its owner, however the creation interleaves with it: the creation takes the
// owner's lock first, so it either commits before the disable (which then revokes the token) or finds the user
// disabled.
func TestCreateTokenAgainstDisable(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	admin := e.admin()
	var created, refused int
	for range 20 {
		owner := e.w.user(t, "active")
		var (
			wg                  sync.WaitGroup
			createErr, statusEr error
		)
		wg.Go(func() { _, createErr = e.svc.CreateOwnToken(ctx, owner, portal.NewToken{Name: "race", Scope: "read"}) })
		wg.Go(func() { _, statusEr = e.svc.SetUserStatus(ctx, admin, owner, portal.UserStatusDisabled) })
		wg.Wait()
		if statusEr != nil {
			t.Fatalf("disable: %v", statusEr)
		}
		switch {
		case createErr == nil:
			created++
		case errors.Is(createErr, portal.ErrUserInactive):
			refused++
		default:
			t.Fatalf("create: %v", createErr)
		}
		if n := e.liveTokens(owner); n != 0 {
			t.Fatalf("%d live tokens survived the disable (creation err: %v)", n, createErr)
		}
	}
	t.Logf("creations committed before the disable: %d, refused: %d", created, refused)
}

func TestListOwnTokens(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	owner, other := e.w.user(t, "active"), e.w.user(t, "active")
	for _, n := range []string{"a", "b", "c"} {
		if _, err := e.svc.CreateOwnToken(ctx, owner, portal.NewToken{Name: n, Scope: "read"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.svc.CreateOwnToken(ctx, other, portal.NewToken{Name: "theirs", Scope: "read"}); err != nil {
		t.Fatal(err)
	}
	dead := e.w.apiToken(t, owner, "-3 days", "-1 day", "")

	var names []string
	req := portal.PageRequest{Limit: 2}
	for pages := 0; ; pages++ {
		if pages > 3 {
			t.Fatal("pagination does not end")
		}
		p, err := e.svc.ListOwnTokens(ctx, owner, req)
		if err != nil {
			t.Fatal(err)
		}
		for _, tok := range p.Items {
			if tok.UserID != owner {
				t.Fatalf("listed a token of %s", tok.UserID)
			}
			names = append(names, tok.Name)
			if tok.ID == dead && tok.RevokedAt != nil {
				t.Fatal("dead token reports a revocation")
			}
		}
		if p.NextCursor == "" {
			break
		}
		req.Cursor = p.NextCursor
	}
	// Newest first by id, whatever order the ids came in; the expired token is still listed.
	rows, err := e.owner.Query(ctx, `SELECT name FROM api_tokens WHERE user_id = $1 ORDER BY id DESC`, owner)
	if err != nil {
		t.Fatal(err)
	}
	want, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(want) != 4 || !slices.Equal(names, want) {
		t.Fatalf("names = %v, want %v (%v)", names, want, err)
	}
	if _, err := e.svc.ListOwnTokens(ctx, owner, portal.PageRequest{Cursor: "garbage"}); !errors.Is(err, portal.ErrInvalidCursor) {
		t.Fatalf("garbage cursor: %v", err)
	}
	if _, err := e.svc.ListOwnTokens(ctx, e.w.user(t, "disabled"), portal.PageRequest{}); !errors.Is(err, portal.ErrUserInactive) {
		t.Fatalf("disabled user: %v", err)
	}
}

func TestRevokeTokens(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	admin := e.admin()
	alice, bob := e.w.user(t, "active"), e.w.user(t, "active")
	mk := func(owner string) string {
		tok, err := e.svc.CreateOwnToken(ctx, owner, portal.NewToken{Name: "t", Scope: "admin"})
		if err != nil {
			// Not an admin or manager: a read token will do.
			tok, err = e.svc.CreateOwnToken(ctx, owner, portal.NewToken{Name: "t", Scope: "read"})
		}
		if err != nil {
			t.Fatal(err)
		}
		return tok.ID
	}
	revoked := func(id string) bool {
		return e.count(`SELECT count(*) FROM api_tokens WHERE id = $1 AND revoked_at IS NOT NULL`, id) == 1
	}

	t.Run("the owner revokes their token once; a repeat succeeds without a second audit row", func(t *testing.T) {
		id := mk(alice)
		if err := e.svc.RevokeOwnToken(ctx, alice, id); err != nil || !revoked(id) {
			t.Fatalf("revoke: %v", err)
		}
		n := e.auditCount()
		a := e.lastAudit()
		if a.Actor != alice || a.Action != "token.revoke" || a.TargetID != id || a.After != nil || a.Before["owner_id"] != alice {
			t.Fatalf("audit = %+v", a)
		}
		if err := e.svc.RevokeOwnToken(ctx, alice, id); err != nil || e.auditCount() != n {
			t.Fatalf("repeat: err = %v, audit rows %d -> %d", err, n, e.auditCount())
		}
	})

	t.Run("someone else's token is not found, with the own and the any endpoint alike", func(t *testing.T) {
		id := mk(alice)
		for name, f := range map[string]func() error{
			"own":  func() error { return e.svc.RevokeOwnToken(ctx, bob, id) },
			"any":  func() error { return e.svc.RevokeToken(ctx, bob, id) },
			"gone": func() error { return e.svc.RevokeToken(ctx, bob, "01a14296-1449-7be0-957b-4b1ad8d7a997") },
			"junk": func() error { return e.svc.RevokeToken(ctx, bob, "not-a-uuid") },
		} {
			if err := f(); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("%s: err = %v, want ErrNotFound", name, err)
			}
		}
		if revoked(id) {
			t.Fatal("a stranger revoked the token")
		}
	})

	t.Run("a platform admin revokes any token through the any endpoint only", func(t *testing.T) {
		id := mk(alice)
		if err := e.svc.RevokeOwnToken(ctx, admin, id); !errors.Is(err, portal.ErrNotFound) || revoked(id) {
			t.Fatalf("own endpoint: %v", err)
		}
		if err := e.svc.RevokeToken(ctx, admin, id); err != nil || !revoked(id) {
			t.Fatalf("any endpoint: %v", err)
		}
		if a := e.lastAudit(); a.Actor != admin || a.Action != "token.revoke" || a.Before["owner_id"] != alice {
			t.Fatalf("audit = %+v", a)
		}
	})

	t.Run("a disabled caller revokes nothing", func(t *testing.T) {
		id := mk(alice)
		if err := e.svc.RevokeToken(ctx, e.w.user(t, "disabled"), id); !errors.Is(err, portal.ErrUserInactive) || revoked(id) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("the current token revokes itself; a browser session has none", func(t *testing.T) {
		id := mk(alice)
		if err := e.svc.RevokeCurrentToken(ctx, alice); !errors.Is(err, portal.ErrNotFound) {
			t.Fatalf("no token: %v", err)
		}
		if err := e.svc.RevokeCurrentToken(portal.WithToken(ctx, id), alice); err != nil || !revoked(id) {
			t.Fatalf("current: %v", err)
		}
		if tid := e.lastAuditToken(); tid != id {
			t.Fatalf("audit token_id = %q, want %q", tid, id)
		}
		// Another user presenting that id (a bug upstream) still cannot revoke it through "current".
		other := mk(alice)
		if err := e.svc.RevokeCurrentToken(portal.WithToken(ctx, other), bob); !errors.Is(err, portal.ErrNotFound) || revoked(other) {
			t.Fatalf("foreign current: %v", err)
		}
	})

	t.Run("a token revokes another token of its owner or, when its owner is an admin, of anyone", func(t *testing.T) {
		callerTok, victim := mk(alice), mk(alice)
		if err := e.svc.RevokeOwnToken(portal.WithToken(ctx, callerTok), alice, victim); err != nil || !revoked(victim) {
			t.Fatalf("own: %v", err)
		}
		adminTok := mk(admin)
		stranger := mk(bob)
		if err := e.svc.RevokeToken(portal.WithToken(ctx, adminTok), admin, stranger); err != nil || !revoked(stranger) {
			t.Fatalf("admin's token: %v", err)
		}
		if tid := e.lastAuditToken(); tid != adminTok {
			t.Fatalf("audit token_id = %q, want %q", tid, adminTok)
		}
		// The token of a plain user cannot reach another owner's token.
		other := mk(bob)
		if err := e.svc.RevokeToken(portal.WithToken(ctx, callerTok), alice, other); !errors.Is(err, portal.ErrNotFound) || revoked(other) {
			t.Fatalf("plain user's token: %v", err)
		}
	})
}

// Every mutation made with a token records it, not only the token endpoints: record() reads it from the context.
func TestMutationsMadeWithATokenRecordIt(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	admin := e.admin()
	const tid = "01a14296-1449-7be0-957b-4b1ad8d7a123"

	if _, err := e.svc.CreateGroup(portal.WithToken(ctx, tid), admin, portal.NewGroup{Key: "ops", Name: "Ops"}); err != nil {
		t.Fatal(err)
	}
	if a := e.lastAudit(); a.Action != "group.create" || e.lastAuditToken() != tid {
		t.Fatalf("audit = %+v, token_id %q", a, e.lastAuditToken())
	}
	if _, err := e.svc.CreateGroup(ctx, admin, portal.NewGroup{Key: "ops2", Name: "Ops"}); err != nil {
		t.Fatal(err)
	}
	if tid := e.lastAuditToken(); tid != "" {
		t.Fatalf("a session mutation recorded token_id %q", tid)
	}
}

// The portal role never reads the hash: not as a column, and not to find a token by it either (a WHERE on the
// column needs SELECT too). Matching a secret to a token is therefore the job of revoke_api_token_by_hash (migration 0013).
func TestPortalRoleCannotReadTokenHash(t *testing.T) {
	t.Parallel()
	owner := newTestDB(t)
	pool := testdb.Portal(t, owner)
	w := world{pool: owner}
	id := w.apiToken(t, w.user(t, "active"), "-1 hour", "1 hour", "")
	for _, sql := range []string{
		`SELECT token_hash FROM api_tokens WHERE id = $1`,
		`SELECT id FROM api_tokens WHERE token_hash = sha256('x') AND $1::text IS NOT NULL`,
		`UPDATE api_tokens SET revoked_at = now() WHERE token_hash = sha256('x') AND $1::text IS NOT NULL`,
	} {
		if err := exec(pool, sql, id); pgErrCode(err) != "42501" {
			t.Errorf("%s: err = %v, want insufficient_privilege", sql, err)
		}
	}
}

// secretToken inserts a live token whose hash is that of a fresh secret and returns both.
func (e *adminEnv) secretToken(owner, expires, revoked string) (id, secret string) {
	e.t.Helper()
	secret, err := apitoken.Generate()
	if err != nil {
		e.t.Fatal(err)
	}
	id = scalar[string](e.t, e.owner, `
		INSERT INTO api_tokens (user_id, name, token_hash, scope, created_at, expires_at, revoked_at)
		VALUES ($1, 'leaked', sha256($2::bytea), 'read', now() - interval '2 days', now() + $3::interval,
		        CASE WHEN $4 = '' THEN NULL ELSE now() + $4::interval END)
		RETURNING id::text`, owner, secret, expires, revoked)
	return id, secret
}

func TestRevokeAPITokenByHashFunction(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	owner := e.w.user(t, "active")
	live, liveSecret := e.secretToken(owner, "1 day", "")
	_, expiredSecret := e.secretToken(owner, "-1 hour", "")
	_, revokedSecret := e.secretToken(owner, "1 day", "-1 hour")
	portalPool := testdb.Portal(t, e.owner)
	call := func(secret string) (*string, error) {
		var id *string
		err := portalPool.QueryRow(e.ctx(), `SELECT revoke_api_token_by_hash(sha256($1::bytea))::text`, secret).Scan(&id)
		return id, err
	}

	for name, s := range map[string]string{"expired": expiredSecret, "already revoked": revokedSecret, "unknown": "ptk_unknown"} {
		if id, err := call(s); err != nil || id != nil {
			t.Errorf("%s: id = %v, err = %v; want NULL", name, id, err)
		}
	}
	if e.count(`SELECT count(*) FROM api_tokens WHERE revoked_at IS NOT NULL AND id = $1`, live) != 0 {
		t.Fatal("a dead or unknown secret revoked the live token")
	}
	id, err := call(liveSecret)
	if err != nil || id == nil || *id != live {
		t.Fatalf("live: id = %v, err = %v", id, err)
	}
	if e.liveTokens(owner) != 0 {
		t.Fatal("the token is still live")
	}
	if id, err := call(liveSecret); err != nil || id != nil {
		t.Fatalf("second call: id = %v, err = %v; want NULL", id, err)
	}
	// Dead tokens keep their original revoked_at (the purge grace counts from it).
	if n := e.count(`SELECT count(*) FROM api_tokens WHERE revoked_at < now() - interval '30 minutes' AND revoked_at > now() - interval '90 minutes'`); n != 1 {
		t.Fatalf("an already revoked token was touched: %d rows keep their date", n)
	}

	for name, hash := range map[string][]byte{"31 bytes": make([]byte, 31), "33 bytes": make([]byte, 33), "empty": {}, "NULL": nil} {
		var id *string
		err := portalPool.QueryRow(e.ctx(), `SELECT revoke_api_token_by_hash($1)::text`, hash).Scan(&id)
		if pgErrCode(err) != codeInvalidParameter {
			t.Errorf("%s: err = %v, want invalid_parameter_value", name, err)
		}
	}

	// Only the portal role may call it.
	for _, role := range []string{"platform_runtime", "platform_portal"} {
		want := role == "platform_portal"
		if got := scalar[bool](t, e.owner, `SELECT has_function_privilege($1, 'revoke_api_token_by_hash(bytea)', 'EXECUTE')`, role); got != want {
			t.Errorf("%s EXECUTE = %v, want %v", role, got, want)
		}
	}
	if scalar[bool](t, e.owner, `SELECT EXISTS (SELECT 1 FROM pg_proc p, aclexplode(p.proacl) a
		WHERE p.proname = 'revoke_api_token_by_hash' AND a.grantee = 0)`) {
		t.Error("PUBLIC can EXECUTE revoke_api_token_by_hash")
	}
	rt := runtimePool(t, e.owner)
	if _, err := rt.Exec(e.ctx(), `SELECT revoke_api_token_by_hash(sha256('x'))`); pgErrCode(err) != codeInsufficientPrivs {
		t.Errorf("runtime role: err = %v, want insufficient_privilege", err)
	}
}

func TestRevokeBySecret(t *testing.T) {
	t.Parallel()
	e := newAdminEnv(t)
	ctx := e.ctx()
	admin := e.admin()
	plain := e.w.user(t, "active")
	owner := e.w.user(t, "active")

	t.Run("a platform admin revokes the token; one audit row with the id, never the secret", func(t *testing.T) {
		id, secret := e.secretToken(owner, "1 day", "")
		before := e.auditCount()
		if err := e.svc.RevokeBySecret(ctx, admin, secret); err != nil {
			t.Fatal(err)
		}
		if e.liveTokens(owner) != 0 || e.auditCount() != before+1 {
			t.Fatalf("live = %d, audit rows %d -> %d", e.liveTokens(owner), before, e.auditCount())
		}
		a := e.lastAudit()
		if a.Actor != admin || a.Action != "token.revoke" || a.TargetType != "token" || a.TargetID != id ||
			a.Before["owner_id"] != owner || a.Before["name"] != "leaked" || a.After != nil {
			t.Fatalf("audit = %+v", a)
		}
		for _, v := range a.Before {
			if v == secret {
				t.Fatal("audit holds the secret")
			}
		}
	})

	t.Run("an unknown, expired or revoked secret succeeds without an audit row", func(t *testing.T) {
		_, expired := e.secretToken(owner, "-1 hour", "")
		_, revoked := e.secretToken(owner, "1 day", "-1 hour")
		unknown, err := apitoken.Generate()
		if err != nil {
			t.Fatal(err)
		}
		before := e.auditCount()
		for name, s := range map[string]string{"unknown": unknown, "expired": expired, "revoked": revoked} {
			if err := e.svc.RevokeBySecret(ctx, admin, s); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
		if e.auditCount() != before {
			t.Fatal("a miss wrote an audit row")
		}
	})

	t.Run("a malformed secret is a 422 and never reaches the database function", func(t *testing.T) {
		for name, s := range map[string]string{"empty": "", "garbage": "ptk_nope", "bad checksum": "ptk_" + string(slices.Repeat([]byte("A"), 51))} {
			if p := validationFields(t, e.svc.RevokeBySecret(ctx, admin, s)); len(p) != 1 || p[0].Field != "token" {
				t.Errorf("%s: problems = %v", name, p)
			}
		}
	})

	t.Run("only platform admins; the token survives a refusal", func(t *testing.T) {
		id, secret := e.secretToken(owner, "1 day", "")
		for name, who := range map[string]string{"plain user": plain, "disabled admin": e.w.user(t, "disabled")} {
			if err := e.svc.RevokeBySecret(ctx, who, secret); err == nil {
				t.Errorf("%s revoked by secret", name)
			}
		}
		if e.count(`SELECT count(*) FROM api_tokens WHERE id = $1 AND revoked_at IS NULL`, id) != 1 {
			t.Fatal("a refused request revoked the token")
		}
		// Not even the token's own owner, unless they are an admin.
		if err := e.svc.RevokeBySecret(ctx, owner, secret); !errors.Is(err, portal.ErrForbidden) {
			t.Fatalf("owner: %v", err)
		}
	})

	t.Run("under a token the audit row records the acting token", func(t *testing.T) {
		_, secret := e.secretToken(owner, "1 day", "")
		const actor = "01a14296-1449-7be0-957b-4b1ad8d7a456"
		if err := e.svc.RevokeBySecret(portal.WithToken(ctx, actor), admin, secret); err != nil || e.lastAuditToken() != actor {
			t.Fatalf("err = %v, token_id %q", err, e.lastAuditToken())
		}
	})
}
