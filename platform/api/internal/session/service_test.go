package session_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Wysted/atrium/platform/api/internal/postgres"
	"github.com/Wysted/atrium/platform/api/internal/session"
	"github.com/Wysted/atrium/platform/api/internal/testdb"
)

const (
	testTimeout = 30 * time.Second
	hostA       = "a.example.com"
	hostA2      = "a2.example.com"
	hostB       = "b.example.com"
	ulidDigits  = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
)

type rig struct {
	pool         *pgxpool.Pool
	svc          *session.Service
	appA, appB   string
	userX, userY string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	pool := testdb.New(t)
	repo := postgres.NewSessionRepo(pool)
	svc, err := session.NewService(session.Config{Sessions: repo, AppSessions: repo, Codes: repo, Hosts: repo})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{pool: pool, svc: svc}
	ctx := r.ctx(t)
	var initiative string
	if err := pool.QueryRow(ctx, `INSERT INTO initiatives (key, status) VALUES ('init', 'active') RETURNING id::text`).Scan(&initiative); err != nil {
		t.Fatal(err)
	}
	mkApp := func(n int, hosts ...string) string {
		id := "app_" + strings.Repeat("0", 25) + string(ulidDigits[n])
		r.exec(t, `INSERT INTO apps (id, initiative_id) VALUES ($1, $2)`, id, initiative)
		for _, h := range hosts {
			r.exec(t, `INSERT INTO deployments (host, app_id) VALUES ($1, $2)`, h, id)
		}
		return id
	}
	r.appA = mkApp(1, hostA, hostA2)
	r.appB = mkApp(2, hostB)
	mkUser := func(sub string) string {
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO users (google_sub, email, name) VALUES ($1, $1 || '@example.com', 'n') RETURNING id::text`, sub).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	r.userX, r.userY = mkUser("x"), mkUser("y")
	return r
}

func (r *rig) ctx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	return ctx
}

func (r *rig) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := r.pool.Exec(r.ctx(t), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func count(t *testing.T, r *rig, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := r.pool.QueryRow(r.ctx(t), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

func (r *rig) login(t *testing.T, user string) (string, session.Session) {
	t.Helper()
	tok, s, err := r.svc.CreateSession(r.ctx(t), session.CreateSessionInput{UserID: user})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return tok, s
}

func TestCreateSessionAlwaysIssuesFreshIdentity(t *testing.T) {
	r := newRig(t)
	ip := netip.MustParseAddr("203.0.113.9")
	tok1, s1, err := r.svc.CreateSession(r.ctx(t), session.CreateSessionInput{UserID: r.userX, IP: ip, UserAgent: "UA"})
	if err != nil {
		t.Fatal(err)
	}
	tok2, s2 := r.login(t, r.userX)
	if tok1 == tok2 || s1.ID == s2.ID {
		t.Fatalf("a second login reused identity: tokens equal=%v ids equal=%v", tok1 == tok2, s1.ID == s2.ID)
	}
	if s1.IP != ip || s1.UserAgent != "UA" || s2.IP.IsValid() {
		t.Fatalf("ip/user agent not stored as given: %+v %+v", s1, s2)
	}
	if got := s1.ExpiresAt.Sub(s1.CreatedAt); got != session.AbsoluteLifetime {
		t.Fatalf("absolute lifetime = %s", got)
	}
	// Only the SHA-256 is stored, and the session id is not the token.
	hash, _ := session.HashToken(tok1)
	if n := count(t, r, `SELECT count(*) FROM sessions WHERE token_hash = $1`, hash); n != 1 {
		t.Fatalf("rows with the token hash = %d", n)
	}
	if s1.ID == tok1 {
		t.Fatal("public sid equals the token")
	}
	long := strings.Repeat("é", 5000) + "\x00\xff"
	_, s3, err := r.svc.CreateSession(r.ctx(t), session.CreateSessionInput{UserID: r.userX, UserAgent: long})
	if err != nil || len(s3.UserAgent) > session.MaxUserAgentLength {
		t.Fatalf("user agent not bounded: len %d err %v", len(s3.UserAgent), err)
	}
}

func TestValidateSessionLimits(t *testing.T) {
	r := newRig(t)
	ctx := r.ctx(t)

	t.Run("valid", func(t *testing.T) {
		tok, s := r.login(t, r.userX)
		got, err := r.svc.ValidateSession(ctx, tok)
		if err != nil || got.ID != s.ID || got.UserID != r.userX {
			t.Fatalf("Validate = %+v, %v", got, err)
		}
	})
	t.Run("unknown and malformed tokens", func(t *testing.T) {
		other, _, _ := session.NewToken()
		for _, tok := range []string{other, "", "garbage"} {
			if _, err := r.svc.ValidateSession(ctx, tok); !errors.Is(err, session.ErrInvalidSession) {
				t.Fatalf("Validate(%q) = %v", tok, err)
			}
		}
	})
	t.Run("idle expired", func(t *testing.T) {
		tok, s := r.login(t, r.userX)
		r.exec(t, `UPDATE sessions SET last_seen_at = now() - interval '46 minutes' WHERE id = $1`, s.ID)
		if _, err := r.svc.ValidateSession(ctx, tok); !errors.Is(err, session.ErrInvalidSession) {
			t.Fatalf("idle session accepted: %v", err)
		}
		r.exec(t, `UPDATE sessions SET last_seen_at = now() - interval '44 minutes' WHERE id = $1`, s.ID)
		if _, err := r.svc.ValidateSession(ctx, tok); err != nil {
			t.Fatalf("session within idle timeout rejected: %v", err)
		}
	})
	t.Run("configurable idle", func(t *testing.T) {
		repo := postgres.NewSessionRepo(r.pool)
		short, err := session.NewService(session.Config{Sessions: repo, AppSessions: repo, Codes: repo, Hosts: repo, IdleTimeout: session.MinIdleTimeout})
		if err != nil {
			t.Fatal(err)
		}
		tok, s := r.login(t, r.userX)
		r.exec(t, `UPDATE sessions SET last_seen_at = now() - interval '31 minutes' WHERE id = $1`, s.ID)
		if _, err := short.ValidateSession(ctx, tok); !errors.Is(err, session.ErrInvalidSession) {
			t.Fatalf("minimum idle not enforced: %v", err)
		}
		if _, err := r.svc.ValidateSession(ctx, tok); err != nil {
			t.Fatalf("default idle should still accept: %v", err)
		}
	})
	t.Run("absolute expiry wins over activity", func(t *testing.T) {
		tok, s := r.login(t, r.userX)
		r.exec(t, `UPDATE sessions SET expires_at = now() - interval '1 second', last_seen_at = now() WHERE id = $1`, s.ID)
		if _, err := r.svc.ValidateSession(ctx, tok); !errors.Is(err, session.ErrInvalidSession) {
			t.Fatalf("session past its absolute lifetime accepted: %v", err)
		}
	})
	t.Run("activity never extends the absolute expiry", func(t *testing.T) {
		tok, s := r.login(t, r.userX)
		r.exec(t, `UPDATE sessions SET last_seen_at = now() - interval '10 minutes' WHERE id = $1`, s.ID)
		got, err := r.svc.ValidateSession(ctx, tok)
		if err != nil || !got.ExpiresAt.Equal(s.ExpiresAt) {
			t.Fatalf("expires_at moved: %v -> %v (%v)", s.ExpiresAt, got.ExpiresAt, err)
		}
	})
}

// installTouchLog records every UPDATE of sessions so tests can count real writes.
func (r *rig) installTouchLog(t *testing.T) {
	t.Helper()
	r.exec(t, `CREATE TABLE touch_log (session_id uuid, at timestamptz DEFAULT clock_timestamp())`)
	r.exec(t, `CREATE FUNCTION log_touch() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN INSERT INTO touch_log (session_id) VALUES (NEW.id); RETURN NULL; END $$`)
	r.exec(t, `CREATE TRIGGER sessions_touch AFTER UPDATE ON sessions FOR EACH ROW EXECUTE FUNCTION log_touch()`)
}

func TestTouchIsThrottled(t *testing.T) {
	r := newRig(t)
	r.installTouchLog(t)
	ctx := r.ctx(t)
	tok, s := r.login(t, r.userX)

	// Fresh session: last_seen_at is recent, nothing to write.
	for range 3 {
		if _, err := r.svc.ValidateSession(ctx, tok); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, r, `SELECT count(*) FROM touch_log`); n != 0 {
		t.Fatalf("writes on a fresh session = %d, want 0", n)
	}

	// Stale beyond the window: the first validation writes, the next ones in the window do not.
	r.exec(t, `UPDATE sessions SET last_seen_at = now() - interval '10 minutes' WHERE id = $1`, s.ID)
	r.exec(t, `DELETE FROM touch_log`)
	first, err := r.svc.ValidateSession(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.svc.ValidateSession(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if n := count(t, r, `SELECT count(*) FROM touch_log`); n != 1 {
		t.Fatalf("two consecutive validations wrote %d times, want 1", n)
	}
	if !first.LastSeenAt.Equal(second.LastSeenAt) || time.Since(first.LastSeenAt) > time.Minute {
		t.Fatalf("last_seen_at not refreshed once: %v then %v", first.LastSeenAt, second.LastSeenAt)
	}

	// Concurrent validations of a stale session also collapse into one write.
	r.exec(t, `UPDATE sessions SET last_seen_at = now() - interval '10 minutes' WHERE id = $1`, s.ID)
	r.exec(t, `DELETE FROM touch_log`)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if _, err := r.svc.ValidateSession(ctx, tok); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if n := count(t, r, `SELECT count(*) FROM touch_log`); n != 1 {
		t.Fatalf("16 concurrent validations wrote %d times, want 1", n)
	}
}

func TestRevocation(t *testing.T) {
	r := newRig(t)
	ctx := r.ctx(t)
	tokX1, sX1 := r.login(t, r.userX)
	tokX2, sX2 := r.login(t, r.userX)
	tokY, sY := r.login(t, r.userY)
	for _, s := range []session.Session{sX1, sX2, sY} {
		if _, _, err := r.svc.CreateAppSession(ctx, s.ID, r.appA, hostA); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("a user cannot close another user's session", func(t *testing.T) {
		if ok, err := r.svc.RevokeUserSession(ctx, r.userY, sX1.ID); err != nil || ok {
			t.Fatalf("RevokeUserSession across users = %v, %v", ok, err)
		}
		if _, err := r.svc.ValidateSession(ctx, tokX1); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("list shows only the user's active sessions", func(t *testing.T) {
		_, idle := r.login(t, r.userX)
		r.exec(t, `UPDATE sessions SET last_seen_at = now() - interval '2 hours' WHERE id = $1`, idle.ID)
		_, expired := r.login(t, r.userX)
		r.exec(t, `UPDATE sessions SET expires_at = now() - interval '1 second' WHERE id = $1`, expired.ID)
		got, err := r.svc.ListActive(ctx, r.userX)
		if err != nil || len(got) != 2 {
			t.Fatalf("ListActive = %+v, %v; want the 2 live sessions", got, err)
		}
		for _, s := range got {
			if s.UserID != r.userX || (s.ID != sX1.ID && s.ID != sX2.ID) {
				t.Fatalf("unexpected session %+v", s)
			}
		}
	})
	t.Run("closing one session cascades to its app sessions only", func(t *testing.T) {
		if ok, err := r.svc.RevokeUserSession(ctx, r.userX, sX2.ID); err != nil || !ok {
			t.Fatalf("RevokeUserSession = %v, %v", ok, err)
		}
		if _, err := r.svc.ValidateSession(ctx, tokX2); !errors.Is(err, session.ErrInvalidSession) {
			t.Fatal("revoked session still valid")
		}
		if n := count(t, r, `SELECT count(*) FROM app_sessions WHERE session_id = $1`, sX2.ID); n != 0 {
			t.Fatalf("app sessions of the revoked session = %d", n)
		}
	})
	t.Run("revoking a user deletes every central and app session", func(t *testing.T) {
		n, err := r.svc.RevokeUser(ctx, r.userX)
		if err != nil || n < 1 {
			t.Fatalf("RevokeUser = %d, %v", n, err)
		}
		if c := count(t, r, `SELECT count(*) FROM sessions WHERE user_id = $1`, r.userX); c != 0 {
			t.Fatalf("sessions left for the user: %d", c)
		}
		if c := count(t, r, `SELECT count(*) FROM app_sessions a JOIN sessions s ON s.id = a.session_id WHERE s.user_id = $1`, r.userX); c != 0 {
			t.Fatal("app sessions left")
		}
		if c := count(t, r, `SELECT count(*) FROM app_sessions`); c != 1 {
			t.Fatalf("app sessions of other users = %d, want the 1 of user Y", c)
		}
		if _, err := r.svc.ValidateSession(ctx, tokY); err != nil {
			t.Fatalf("another user's session was affected: %v", err)
		}
	})
	t.Run("revoking by token and unknown tokens", func(t *testing.T) {
		rev, err := r.svc.RevokeSession(ctx, tokY)
		if err != nil || rev != (session.Revoked{SessionID: sY.ID, UserID: r.userY}) {
			t.Fatalf("RevokeSession = %+v, %v; want session %s of user %s", rev, err, sY.ID, r.userY)
		}
		if again, err := r.svc.RevokeSession(ctx, tokY); err != nil || again != (session.Revoked{}) {
			t.Fatalf("second RevokeSession = %+v, %v; want the zero value", again, err)
		}
		if _, err := r.svc.ValidateSession(ctx, tokY); !errors.Is(err, session.ErrInvalidSession) {
			t.Fatal("revoked session still valid")
		}
		if rev, err := r.svc.RevokeSession(ctx, "garbage"); err != nil || rev != (session.Revoked{}) {
			t.Fatalf("revoking garbage must be a no-op: %v", err)
		}
	})
}

func TestAppSessions(t *testing.T) {
	r := newRig(t)
	ctx := r.ctx(t)
	central, s := r.login(t, r.userX)

	appTok, app, err := r.svc.CreateAppSession(ctx, s.ID, r.appA, hostA)
	if err != nil {
		t.Fatal(err)
	}
	if !app.ExpiresAt.Equal(s.ExpiresAt) || app.UserID != r.userX || app.SessionID != s.ID {
		t.Fatalf("app session = %+v, central expires %v", app, s.ExpiresAt)
	}
	hash, _ := session.HashToken(appTok)
	if n := count(t, r, `SELECT count(*) FROM app_sessions WHERE token_hash = $1`, hash); n != 1 {
		t.Fatal("app token hash not stored")
	}

	t.Run("validates only on its own host", func(t *testing.T) {
		got, err := r.svc.ValidateAppSession(ctx, appTok, hostA)
		if err != nil || got.AppID != r.appA || got.UserID != r.userX {
			t.Fatalf("Validate = %+v, %v", got, err)
		}
		for _, host := range []string{hostA2, hostB, ""} {
			if _, err := r.svc.ValidateAppSession(ctx, appTok, host); !errors.Is(err, session.ErrInvalidSession) {
				t.Fatalf("app token accepted on %q", host)
			}
		}
		if _, err := r.svc.ValidateAppSession(ctx, central, hostA); !errors.Is(err, session.ErrInvalidSession) {
			t.Fatal("the central token must not authenticate as an app session")
		}
	})
	t.Run("validation refreshes the central idle clock, throttled", func(t *testing.T) {
		r.installTouchLog(t)
		r.exec(t, `UPDATE sessions SET last_seen_at = now() - interval '30 minutes' WHERE id = $1`, s.ID)
		r.exec(t, `DELETE FROM touch_log`) // the setup UPDATE above fired the trigger too
		for range 2 {
			if _, err := r.svc.ValidateAppSession(ctx, appTok, hostA); err != nil {
				t.Fatal(err)
			}
		}
		if n := count(t, r, `SELECT count(*) FROM touch_log`); n != 1 {
			t.Fatalf("central writes = %d, want 1", n)
		}
		if n := count(t, r, `SELECT count(*) FROM sessions WHERE id = $1 AND last_seen_at > now() - interval '1 minute'`, s.ID); n != 1 {
			t.Fatal("central last_seen_at not refreshed")
		}
	})
	t.Run("does not survive an idle central session", func(t *testing.T) {
		r.exec(t, `UPDATE sessions SET last_seen_at = now() - interval '46 minutes' WHERE id = $1`, s.ID)
		if _, err := r.svc.ValidateAppSession(ctx, appTok, hostA); !errors.Is(err, session.ErrInvalidSession) {
			t.Fatal("app session survived its idle central session")
		}
		r.exec(t, `UPDATE sessions SET last_seen_at = now() WHERE id = $1`, s.ID)
	})
	t.Run("does not survive an expired central session", func(t *testing.T) {
		// Even if its own expires_at were somehow later, the central session decides.
		r.exec(t, `UPDATE sessions SET expires_at = now() - interval '1 second' WHERE id = $1`, s.ID)
		r.exec(t, `UPDATE app_sessions SET expires_at = now() + interval '1 hour' WHERE id = $1`, app.ID)
		if _, err := r.svc.ValidateAppSession(ctx, appTok, hostA); !errors.Is(err, session.ErrInvalidSession) {
			t.Fatal("app session survived its expired central session")
		}
		r.exec(t, `UPDATE sessions SET expires_at = now() + interval '1 hour' WHERE id = $1`, s.ID)
		r.exec(t, `UPDATE app_sessions SET expires_at = now() - interval '1 second' WHERE id = $1`, app.ID)
		if _, err := r.svc.ValidateAppSession(ctx, appTok, hostA); !errors.Is(err, session.ErrInvalidSession) {
			t.Fatal("expired app session accepted")
		}
	})
	t.Run("dies with the central session", func(t *testing.T) {
		tok2, s2 := r.login(t, r.userY)
		at, _, err := r.svc.CreateAppSession(ctx, s2.ID, r.appB, hostB)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.svc.RevokeSession(ctx, tok2); err != nil {
			t.Fatal(err)
		}
		if _, err := r.svc.ValidateAppSession(ctx, at, hostB); !errors.Is(err, session.ErrInvalidSession) {
			t.Fatal("app session outlived the central session")
		}
	})
	t.Run("creation requires a live central session and a host of the app", func(t *testing.T) {
		_, live := r.login(t, r.userY)
		if _, _, err := r.svc.CreateAppSession(ctx, live.ID, r.appA, hostB); !errors.Is(err, session.ErrInvalidSession) {
			t.Fatalf("host of another app accepted: %v", err)
		}
		if _, _, err := r.svc.CreateAppSession(ctx, live.ID, r.appA, "unknown.example.com"); !errors.Is(err, session.ErrInvalidSession) {
			t.Fatalf("unregistered host accepted: %v", err)
		}
		r.exec(t, `UPDATE sessions SET expires_at = now() - interval '1 second' WHERE id = $1`, live.ID)
		if _, _, err := r.svc.CreateAppSession(ctx, live.ID, r.appA, hostA); !errors.Is(err, session.ErrInvalidSession) {
			t.Fatalf("expired central accepted: %v", err)
		}
	})
	t.Run("revoke by app", func(t *testing.T) {
		_, live := r.login(t, r.userY)
		tA, _, _ := r.svc.CreateAppSession(ctx, live.ID, r.appA, hostA2)
		tB, _, _ := r.svc.CreateAppSession(ctx, live.ID, r.appB, hostB)
		n, err := r.svc.RevokeApp(ctx, r.appA)
		if err != nil || n < 1 {
			t.Fatalf("RevokeApp = %d, %v", n, err)
		}
		if _, err := r.svc.ValidateAppSession(ctx, tA, hostA2); !errors.Is(err, session.ErrInvalidSession) {
			t.Fatal("revoked app session still valid")
		}
		if _, err := r.svc.ValidateAppSession(ctx, tB, hostB); err != nil {
			t.Fatalf("other app's session affected: %v", err)
		}
		if _, err := r.svc.ValidateSession(ctx, central); err != nil {
			t.Fatalf("central session affected by app revocation: %v", err)
		}
	})
}

func (r *rig) bind(t *testing.T) (string, []byte) {
	t.Helper()
	v, h, err := session.NewBind()
	if err != nil {
		t.Fatal(err)
	}
	return v, h
}

func (r *rig) code(t *testing.T, sessionID string, bindHash []byte) string {
	t.Helper()
	code, err := r.svc.CreateLoginCode(r.ctx(t), session.LoginCodeInput{
		SessionID: sessionID, AppID: r.appA, Host: hostA, RD: "https://A.example.com/path?q=1", BindHash: bindHash,
	})
	if err != nil {
		t.Fatalf("CreateLoginCode: %v", err)
	}
	return code
}

func TestLoginCodes(t *testing.T) {
	r := newRig(t)
	ctx := r.ctx(t)
	_, s := r.login(t, r.userX)

	t.Run("redeems once and yields the grant", func(t *testing.T) {
		bind, bh := r.bind(t)
		code := r.code(t, s.ID, bh)
		hash, _ := session.HashToken(code)
		if n := count(t, r, `SELECT count(*) FROM login_codes WHERE code_hash = $1 AND expires_at - created_at = interval '30 seconds'`, hash); n != 1 {
			t.Fatal("code not stored by hash with a 30 s lifetime")
		}
		g, err := r.svc.ConsumeLoginCode(ctx, code, hostA, bind)
		want := session.LoginGrant{SessionID: s.ID, AppID: r.appA, Host: hostA, RD: "https://a.example.com/path?q=1"}
		if err != nil || g != want {
			t.Fatalf("grant = %+v, %v; want %+v", g, err, want)
		}
		if _, err := r.svc.ConsumeLoginCode(ctx, code, hostA, bind); !errors.Is(err, session.ErrInvalidCode) {
			t.Fatalf("reuse accepted: %v", err)
		}
	})
	t.Run("expired", func(t *testing.T) {
		bind, bh := r.bind(t)
		code := r.code(t, s.ID, bh)
		hash, _ := session.HashToken(code)
		r.exec(t, `UPDATE login_codes SET expires_at = now() - interval '1 second' WHERE code_hash = $1`, hash)
		if _, err := r.svc.ConsumeLoginCode(ctx, code, hostA, bind); !errors.Is(err, session.ErrInvalidCode) {
			t.Fatalf("expired code accepted: %v", err)
		}
	})
	t.Run("unknown and malformed codes", func(t *testing.T) {
		bind, _ := r.bind(t)
		other, _, _ := session.NewToken()
		for _, c := range []string{other, "", "x"} {
			if _, err := r.svc.ConsumeLoginCode(ctx, c, hostA, bind); !errors.Is(err, session.ErrInvalidCode) {
				t.Fatalf("code %q accepted: %v", c, err)
			}
		}
	})
	t.Run("wrong host burns the code", func(t *testing.T) {
		bind, bh := r.bind(t)
		code := r.code(t, s.ID, bh)
		if _, err := r.svc.ConsumeLoginCode(ctx, code, hostB, bind); !errors.Is(err, session.ErrInvalidCode) {
			t.Fatalf("code accepted on another host: %v", err)
		}
		if _, err := r.svc.ConsumeLoginCode(ctx, code, hostA, bind); !errors.Is(err, session.ErrInvalidCode) {
			t.Fatal("code still usable after a failed host check")
		}
	})
	t.Run("another host of the same app is still a wrong host", func(t *testing.T) {
		bind, bh := r.bind(t)
		code := r.code(t, s.ID, bh)
		if _, err := r.svc.ConsumeLoginCode(ctx, code, hostA2, bind); !errors.Is(err, session.ErrInvalidCode) {
			t.Fatalf("code accepted on a sibling host: %v", err)
		}
	})
	t.Run("wrong or missing bind burns the code", func(t *testing.T) {
		for name, presented := range map[string]func() string{
			"another browser's bind": func() string { v, _ := r.bind(t); return v },
			"empty":                  func() string { return "" },
			"malformed":              func() string { return "not-a-bind" },
		} {
			bind, bh := r.bind(t)
			code := r.code(t, s.ID, bh)
			if _, err := r.svc.ConsumeLoginCode(ctx, code, hostA, presented()); !errors.Is(err, session.ErrInvalidCode) {
				t.Fatalf("%s: code accepted: %v", name, err)
			}
			if _, err := r.svc.ConsumeLoginCode(ctx, code, hostA, bind); !errors.Is(err, session.ErrInvalidCode) {
				t.Fatalf("%s: code still usable after a failed bind check", name)
			}
		}
	})
	t.Run("concurrent redemption has exactly one winner", func(t *testing.T) {
		bind, bh := r.bind(t)
		code := r.code(t, s.ID, bh)
		var wins, invalid atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for range 32 {
			wg.Go(func() {
				<-start
				switch _, err := r.svc.ConsumeLoginCode(ctx, code, hostA, bind); {
				case err == nil:
					wins.Add(1)
				case errors.Is(err, session.ErrInvalidCode):
					invalid.Add(1)
				default:
					t.Error(err)
				}
			})
		}
		close(start)
		wg.Wait()
		if wins.Load() != 1 || invalid.Load() != 31 {
			t.Fatalf("wins=%d invalid=%d, want 1 and 31", wins.Load(), invalid.Load())
		}
	})
	t.Run("codes die with the session", func(t *testing.T) {
		tok, s2 := r.login(t, r.userY)
		bind, bh := r.bind(t)
		code, err := r.svc.CreateLoginCode(ctx, session.LoginCodeInput{SessionID: s2.ID, AppID: r.appA, Host: hostA, RD: "https://a.example.com/", BindHash: bh})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.svc.RevokeSession(ctx, tok); err != nil {
			t.Fatal(err)
		}
		if _, err := r.svc.ConsumeLoginCode(ctx, code, hostA, bind); !errors.Is(err, session.ErrInvalidCode) {
			t.Fatalf("code of a revoked session accepted: %v", err)
		}
	})
}

func TestCreateLoginCodeValidation(t *testing.T) {
	r := newRig(t)
	ctx := r.ctx(t)
	_, s := r.login(t, r.userX)
	_, bh := r.bind(t)
	base := session.LoginCodeInput{SessionID: s.ID, AppID: r.appA, Host: hostA, RD: "https://a.example.com/x", BindHash: bh}

	rejectedRD := []string{
		"http://a.example.com/x",
		"https://evil.example.org/x",
		"https://a.example.com@evil.com/",
		"https://a.example.com//evil.com",
		`https://a.example.com/\evil.com`,
		"//evil.com",
		`\\evil.com`,
		"javascript:alert(1)",
		"https://b.example.com/x", // registered, but not the code's destination host
	}
	for _, rd := range rejectedRD {
		in := base
		in.RD = rd
		if _, err := r.svc.CreateLoginCode(ctx, in); !errors.Is(err, session.ErrInvalidRD) {
			t.Fatalf("rd %q: err = %v, want ErrInvalidRD", rd, err)
		}
	}
	if n := count(t, r, `SELECT count(*) FROM login_codes`); n != 0 {
		t.Fatalf("codes stored for rejected rd: %d", n)
	}

	t.Run("bind hash must be a SHA-256", func(t *testing.T) {
		in := base
		in.BindHash = []byte("short")
		if _, err := r.svc.CreateLoginCode(ctx, in); err == nil {
			t.Fatal("short bind hash accepted")
		}
	})
	t.Run("host must belong to the app", func(t *testing.T) {
		in := base
		in.AppID = r.appB
		if _, err := r.svc.CreateLoginCode(ctx, in); !errors.Is(err, session.ErrInvalidSession) {
			t.Fatalf("host/app mismatch accepted: %v", err)
		}
	})
	t.Run("session must be live", func(t *testing.T) {
		r.exec(t, `UPDATE sessions SET expires_at = now() - interval '1 second' WHERE id = $1`, s.ID)
		if _, err := r.svc.CreateLoginCode(ctx, base); !errors.Is(err, session.ErrInvalidSession) {
			t.Fatalf("code for an expired session: %v", err)
		}
	})
}

func TestBindIsIndependentOfTheCookieValueInTransit(t *testing.T) {
	v, h, err := session.NewBind()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := session.HashToken(v)
	if !ok || string(got) != string(h) || len(h) != sha256.Size {
		t.Fatal("bind hash is not the SHA-256 of the bind value")
	}
}
