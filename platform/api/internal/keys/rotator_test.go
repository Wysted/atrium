package keys_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Wysted/atrium/platform/api/internal/keys"
	"github.com/Wysted/atrium/platform/api/internal/postgres"
	"github.com/Wysted/atrium/platform/api/internal/testdb"
)

const testTimeout = 30 * time.Second

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// fakeClock is a settable clock for the rotator.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type rig struct {
	pool    *pgxpool.Pool
	store   *postgres.KeyStore
	kek     *keys.KEK
	clock   *fakeClock
	rotator *keys.Rotator
	ring    *keys.Keyring
}

func newRig(t *testing.T) *rig {
	t.Helper()
	pool := testdb.New(t)
	kek, _, err := keys.GenerateKEK()
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{t: t0}
	store := postgres.NewKeyStore(pool, postgres.WithClock(clock.Now))
	rotator, err := keys.NewRotator(keys.RotatorConfig{Store: store, KEK: kek})
	if err != nil {
		t.Fatal(err)
	}
	ring, err := keys.NewKeyring(keys.KeyringConfig{Store: store, KEK: kek})
	if err != nil {
		t.Fatal(err)
	}
	return &rig{pool: pool, store: store, kek: kek, clock: clock, rotator: rotator, ring: ring}
}

func (r *rig) ctx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	return ctx
}

// tick advances the clock by d, runs one tick and reloads the keyring.
func (r *rig) tick(t *testing.T, d time.Duration) keys.TickResult {
	t.Helper()
	r.clock.Advance(d)
	ctx := r.ctx(t)
	res, err := r.rotator.Tick(ctx)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if err := r.ring.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	return res
}

type published struct {
	KID   string
	State keys.State
}

func (r *rig) published() []published {
	var out []published
	for _, k := range r.ring.PublicKeys() {
		out = append(out, published{k.KID, k.State})
	}
	return out
}

func kinds(res keys.TickResult) []keys.ActionKind {
	var out []keys.ActionKind
	for _, e := range res.Events {
		out = append(out, e.Action)
	}
	return out
}

func (r *rig) activeKID(t *testing.T) string {
	t.Helper()
	s, err := r.ring.Signer()
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return s.KID
}

func TestRotationLifecycle(t *testing.T) {
	r := newRig(t)
	ctx := r.ctx(t)

	// Phase 1: bootstrap creates an active key directly plus a next key published from the start.
	res := r.tick(t, 0)
	if !slices.Equal(kinds(res), []keys.ActionKind{keys.ActionCreateActive, keys.ActionCreateNext}) {
		t.Fatalf("bootstrap events = %v", res.Events)
	}
	a := r.activeKID(t)
	b := r.nextKID(t)
	if got := r.published(); !slices.Equal(got, []published{{a, keys.StateActive}, {b, keys.StateNext}}) {
		t.Fatalf("after bootstrap published = %v", got)
	}

	// Idempotent: a second tick changes nothing.
	if res := r.tick(t, time.Minute); len(res.Events) != 0 {
		t.Fatalf("idempotent tick produced %v", res.Events)
	}

	// Phase 2: the next key is long ready, but the active key is not 30 days old yet.
	if res := r.tick(t, keys.ActiveDuration-2*time.Minute-time.Second); len(res.Events) != 0 {
		t.Fatalf("early tick produced %v", res.Events)
	}
	if r.activeKID(t) != a {
		t.Fatal("next key must not sign")
	}

	// Phase 3: active key is 30 days old: swap, and a replacement next is created in the same step.
	res = r.tick(t, time.Minute+time.Second) // exactly activated_at + 30d
	if !slices.Equal(kinds(res), []keys.ActionKind{keys.ActionRetire, keys.ActionPromote, keys.ActionCreateNext}) {
		t.Fatalf("swap events = %v", res.Events)
	}
	c := r.nextKID(t)
	if got := r.published(); !slices.Equal(got, []published{{b, keys.StateActive}, {c, keys.StateNext}, {a, keys.StateRetired}}) {
		t.Fatalf("after swap published = %v", got)
	}
	if r.activeKID(t) != b {
		t.Fatal("promoted key must sign")
	}

	// Retired key stays published for 1h.
	if res := r.tick(t, 59*time.Minute); len(res.Events) != 0 {
		t.Fatalf("retired key removed early: %v", res.Events)
	}

	// Phase 4: removed from the JWKS, row kept, private material deleted.
	res = r.tick(t, time.Minute)
	if !slices.Equal(kinds(res), []keys.ActionKind{keys.ActionRemove}) {
		t.Fatalf("remove events = %v", res.Events)
	}
	if got := r.published(); !slices.Equal(got, []published{{b, keys.StateActive}, {c, keys.StateNext}}) {
		t.Fatalf("after removal published = %v", got)
	}
	var state string
	var hasPrivate bool
	var removedAt *time.Time
	err := r.pool.QueryRow(ctx, "SELECT state, private_enc IS NOT NULL, removed_at FROM signing_keys WHERE kid = $1", a).Scan(&state, &hasPrivate, &removedAt)
	if err != nil {
		t.Fatalf("removed row must be kept: %v", err)
	}
	if state != "removed" || hasPrivate || removedAt == nil || !removedAt.Equal(r.clock.Now()) {
		t.Fatalf("removed row = state %q private %v removed_at %v", state, hasPrivate, removedAt)
	}
}

func (r *rig) nextKID(t *testing.T) string {
	t.Helper()
	for _, p := range r.published() {
		if p.State == keys.StateNext {
			return p.KID
		}
	}
	t.Fatal("no next key published")
	return ""
}

// L1: with the default store, rotation timestamps come from the database clock; a replica cannot set them.
func TestRotationTimestampsComeFromTheDatabaseClock(t *testing.T) {
	pool := testdb.New(t)
	kek, _, err := keys.GenerateKEK()
	if err != nil {
		t.Fatal(err)
	}
	rotator, err := keys.NewRotator(keys.RotatorConfig{Store: postgres.NewKeyStore(pool), KEK: kek})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if _, err := rotator.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var drift time.Duration
	err = pool.QueryRow(ctx, "SELECT max(abs(extract(epoch FROM clock_timestamp() - created_at))) * interval '1 second' FROM signing_keys").Scan(&drift)
	if err != nil {
		t.Fatal(err)
	}
	if drift > time.Minute {
		t.Fatalf("created_at is %s away from the database clock", drift)
	}
}

// L1: the rotator takes every timestamp from Tx.Now (the store's clock), not from the replica.
func TestRotatorStampsWithTheStoreClock(t *testing.T) {
	r := newRig(t)
	r.tick(t, 0)
	var created, activated time.Time
	err := r.pool.QueryRow(r.ctx(t), "SELECT created_at, activated_at FROM signing_keys WHERE state = 'active'").Scan(&created, &activated)
	if err != nil {
		t.Fatal(err)
	}
	if !created.Equal(t0) || !activated.Equal(t0) {
		t.Fatalf("created_at %v activated_at %v, want the store clock %v", created, activated, t0)
	}
}

func TestRotationOverdueActiveKeepsSigningUntilNextIsReady(t *testing.T) {
	r := newRig(t)
	r.tick(t, 0)
	a := r.activeKID(t)
	b := r.nextKID(t)

	// The rotator was down for 40 days: the next key has been published all along, so the swap is immediate.
	res := r.tick(t, 40*24*time.Hour)
	if !slices.Equal(kinds(res), []keys.ActionKind{keys.ActionRetire, keys.ActionPromote, keys.ActionCreateNext}) {
		t.Fatalf("events = %v", res.Events)
	}
	if r.activeKID(t) != b || r.activeKID(t) == a {
		t.Fatal("overdue active key must be replaced by the published next")
	}
}

func TestConcurrentRotatorsAreSafe(t *testing.T) {
	r := newRig(t)
	second, err := keys.NewRotator(keys.RotatorConfig{Store: postgres.NewKeyStore(r.pool, postgres.WithClock(r.clock.Now)), KEK: r.kek})
	if err != nil {
		t.Fatal(err)
	}
	rotators := []*keys.Rotator{r.rotator, second}

	round := func(t *testing.T, name string, wantCreated int) {
		t.Helper()
		ctx := r.ctx(t)
		var (
			wg      sync.WaitGroup
			start   = make(chan struct{})
			mu      sync.Mutex
			created int
		)
		for _, rot := range rotators {
			wg.Go(func() {
				<-start
				res, err := rot.Tick(ctx)
				if err != nil {
					t.Errorf("%s: tick: %v", name, err)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				for _, e := range res.Events {
					if e.Action == keys.ActionCreateActive || e.Action == keys.ActionCreateNext {
						created++
					}
				}
			})
		}
		close(start)
		wg.Wait()
		if created != wantCreated {
			t.Fatalf("%s: keys created = %d, want %d", name, created, wantCreated)
		}
	}

	round(t, "bootstrap", 2) // active + next
	if n, m := countKeys(t, r, "active"), countKeys(t, r, "next"); n != 1 || m != 1 {
		t.Fatalf("after bootstrap active=%d next=%d", n, m)
	}
	r.clock.Advance(keys.ActiveDuration)
	round(t, "swap", 1) // replacement next
	if n, m, k := countKeys(t, r, "active"), countKeys(t, r, "retired"), countKeys(t, r, "next"); n != 1 || m != 1 || k != 1 {
		t.Fatalf("after swap active=%d retired=%d next=%d", n, m, k)
	}
}

func countKeys(t *testing.T, r *rig, state string) int {
	t.Helper()
	var n int
	if err := r.pool.QueryRow(r.ctx(t), "SELECT count(*) FROM signing_keys WHERE state = $1", state).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTickSkipsWhenLockIsHeld(t *testing.T) {
	r := newRig(t)
	ctx := r.ctx(t)

	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := r.store.Locked(ctx, true, func(keys.Tx) error {
			close(held)
			<-release
			return nil
		})
		done <- err
	}()
	<-held
	res, err := r.rotator.Tick(ctx)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err != nil || !res.Skipped || len(res.Events) != 0 {
		t.Fatalf("Tick() = %+v, %v; want Skipped", res, err)
	}
}

func TestRevoke(t *testing.T) {
	bootstrap := func(t *testing.T, r *rig) string { r.tick(t, 0); return r.activeKID(t) }
	withNext := func(t *testing.T, r *rig) (active, next string) {
		active = bootstrap(t, r)
		return active, r.nextKID(t)
	}

	tests := []struct {
		name  string
		setup func(t *testing.T, r *rig) (revoke string, wantActive func(old string) bool)
	}{
		{"active with a published next promotes the next", func(t *testing.T, r *rig) (string, func(string) bool) {
			a, n := withNext(t, r)
			return a, func(string) bool { return r.activeKID(t) == n }
		}},
		{"active without next creates a fresh active key (emergency window)", func(t *testing.T, r *rig) (string, func(string) bool) {
			a := bootstrap(t, r)
			r.dropNexts(t)
			return a, func(old string) bool { return r.activeKID(t) != old }
		}},
		{"next is removed, active untouched", func(t *testing.T, r *rig) (string, func(string) bool) {
			a, n := withNext(t, r)
			return n, func(string) bool { return r.activeKID(t) == a }
		}},
		{"retired is removed, active untouched", func(t *testing.T, r *rig) (string, func(string) bool) {
			a, n := withNext(t, r)
			r.tick(t, keys.ActiveDuration) // swap: a retired, n active
			return a, func(string) bool { return r.activeKID(t) == n }
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			kid, wantActive := tt.setup(t, r)

			if err := r.rotator.Revoke(r.ctx(t), kid); err != nil {
				t.Fatalf("Revoke: %v", err)
			}
			if err := r.ring.Reload(r.ctx(t)); err != nil {
				t.Fatal(err)
			}
			for _, p := range r.published() {
				if p.KID == kid {
					t.Fatalf("revoked key %s still published", kid)
				}
			}
			if !wantActive(kid) {
				t.Fatalf("unexpected active key %s", r.activeKID(t))
			}
			if n := countKeys(t, r, "active"); n != 1 {
				t.Fatalf("active keys = %d, want 1", n)
			}
			if n := countKeys(t, r, "next"); n != 1 {
				t.Fatalf("next keys = %d, want a replacement next after every revoke", n)
			}
			var hasPrivate bool
			if err := r.pool.QueryRow(r.ctx(t), "SELECT private_enc IS NOT NULL FROM signing_keys WHERE kid = $1", kid).Scan(&hasPrivate); err != nil || hasPrivate {
				t.Fatalf("revoked row must remain without private material (private=%v err=%v)", hasPrivate, err)
			}
		})
	}

	t.Run("unknown or already removed kid", func(t *testing.T) {
		r := newRig(t)
		kid := bootstrap(t, r)
		if err := r.rotator.Revoke(r.ctx(t), "does-not-exist"); err == nil {
			t.Fatal("unknown kid accepted")
		}
		if err := r.rotator.Revoke(r.ctx(t), kid); err != nil {
			t.Fatal(err)
		}
		if err := r.rotator.Revoke(r.ctx(t), kid); err == nil {
			t.Fatal("second revoke of a removed key accepted")
		}
	})

	// M3: with several next keys, the oldest (published the longest ago) is promoted.
	t.Run("promotes the oldest next", func(t *testing.T) {
		r := newRig(t)
		a, _ := withNext(t, r)
		older := r.insertNext(t, t0.Add(-keys.PrepublishDuration))
		if err := r.rotator.Revoke(r.ctx(t), a); err != nil {
			t.Fatal(err)
		}
		if err := r.ring.Reload(r.ctx(t)); err != nil {
			t.Fatal(err)
		}
		if got := r.activeKID(t); got != older {
			t.Fatalf("active = %s, want the oldest next %s", got, older)
		}
	})
}

// dropNexts removes every next key behind the rotator's back (an unreachable state through the API).
func (r *rig) dropNexts(t *testing.T) {
	t.Helper()
	_, err := r.pool.Exec(r.ctx(t), "UPDATE signing_keys SET state = 'removed', removed_at = now(), private_enc = NULL WHERE state = 'next'")
	if err != nil {
		t.Fatal(err)
	}
}

// insertNext adds a valid next key created at the given time and returns its kid.
func (r *rig) insertNext(t *testing.T, created time.Time) string {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwk, err := keys.NewJWK(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	scalar, _ := priv.Bytes()
	sealed, err := r.kek.Seal(jwk.Kid, scalar)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.store.Locked(r.ctx(t), true, func(tx keys.Tx) error {
		return tx.Insert(r.ctx(t), keys.Record{KID: jwk.Kid, State: keys.StateNext, JWK: jwk, PrivateEnc: sealed, KEKID: r.kek.ID(), CreatedAt: created})
	})
	if err != nil {
		t.Fatal(err)
	}
	return jwk.Kid
}

// corrupt makes the stored private key of kid undecryptable under the KEK.
func (r *rig) corrupt(t *testing.T, kid string) {
	t.Helper()
	if _, err := r.pool.Exec(r.ctx(t), "UPDATE signing_keys SET private_enc = '\\x00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff' WHERE kid = $1", kid); err != nil {
		t.Fatal(err)
	}
}

func (r *rig) stateOf(t *testing.T, kid string) string {
	t.Helper()
	var state string
	if err := r.pool.QueryRow(r.ctx(t), "SELECT state FROM signing_keys WHERE kid = $1", kid).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

// M1 (a): a promotion whose key cannot be unsealed is never committed. Revoke no longer fails because of it:
// it tries the other nexts and, with none usable, creates a fresh active key (the compromised one is never left active).
func TestPromotionRequiresUnsealableKey(t *testing.T) {
	t.Run("Revoke with every next broken creates an active key directly", func(t *testing.T) {
		r := newRig(t)
		r.tick(t, 0)
		a, n := r.activeKID(t), r.nextKID(t)
		r.corrupt(t, n)
		logs := &syncBuffer{}
		rotator, err := keys.NewRotator(keys.RotatorConfig{Store: r.store, KEK: r.kek, Logger: slog.New(slog.NewTextHandler(logs, nil))})
		if err != nil {
			t.Fatal(err)
		}

		if err := rotator.Revoke(r.ctx(t), a); err != nil {
			t.Fatalf("Revoke: %v", err)
		}
		if err := r.ring.Reload(r.ctx(t)); err != nil {
			t.Fatal(err)
		}
		if got := r.stateOf(t, a); got != "removed" {
			t.Fatalf("compromised key state = %q, want removed", got)
		}
		if got := r.stateOf(t, n); got != "next" {
			t.Fatalf("broken next state = %q, want untouched", got)
		}
		if got := r.activeKID(t); got == a || got == n {
			t.Fatalf("active = %s, want a freshly created key", got)
		}
		if n := countKeys(t, r, "active"); n != 1 {
			t.Fatalf("active keys = %d", n)
		}
		if n := countKeys(t, r, "next"); n != 2 { // the broken one plus a usable replacement
			t.Fatalf("next keys = %d, want the broken one and a replacement", n)
		}
		if out := logs.String(); !strings.Contains(out, "level=ERROR") || !strings.Contains(out, n) {
			t.Fatalf("no ERROR log naming the broken next %s:\n%s", n, out)
		}
	})
	t.Run("Revoke skips a broken next and promotes the next usable one", func(t *testing.T) {
		r := newRig(t)
		r.tick(t, 0)
		a, broken := r.activeKID(t), r.nextKID(t)
		good := r.insertNext(t, t0.Add(time.Minute)) // younger than the broken one, so tried second
		r.corrupt(t, broken)

		if err := r.rotator.Revoke(r.ctx(t), a); err != nil {
			t.Fatalf("Revoke: %v", err)
		}
		if err := r.ring.Reload(r.ctx(t)); err != nil {
			t.Fatal(err)
		}
		if got := r.activeKID(t); got != good {
			t.Fatalf("active = %s, want the usable next %s", got, good)
		}
		if got := r.stateOf(t, broken); got != "next" {
			t.Fatalf("broken next state = %q", got)
		}
		if n := countKeys(t, r, "next"); n != 2 { // broken + replacement
			t.Fatalf("next keys = %d, want a replacement because the only other next is broken", n)
		}
	})
	t.Run("Revoke prefers a matured next over a younger one", func(t *testing.T) {
		r := newRig(t)
		r.tick(t, 0)
		a := r.activeKID(t)
		r.insertNext(t, t0.Add(30*time.Minute)) // younger than the bootstrap next, not matured
		matured := r.insertNext(t, t0.Add(-keys.PrepublishDuration))
		if err := r.rotator.Revoke(r.ctx(t), a); err != nil {
			t.Fatal(err)
		}
		if err := r.ring.Reload(r.ctx(t)); err != nil {
			t.Fatal(err)
		}
		if got := r.activeKID(t); got != matured {
			t.Fatalf("active = %s, want the matured next %s", got, matured)
		}
	})
	t.Run("Revoke with no matured next promotes the oldest", func(t *testing.T) {
		r := newRig(t)
		r.tick(t, 0)
		a, boot := r.activeKID(t), r.nextKID(t) // created at t0
		r.insertNext(t, t0.Add(10*time.Minute))
		if err := r.rotator.Revoke(r.ctx(t), a); err != nil {
			t.Fatal(err)
		}
		if err := r.ring.Reload(r.ctx(t)); err != nil {
			t.Fatal(err)
		}
		if got := r.activeKID(t); got != boot {
			t.Fatalf("active = %s, want the oldest next %s", got, boot)
		}
	})
	t.Run("rotator swap never promotes a key that cannot be unsealed", func(t *testing.T) {
		r := newRig(t)
		r.tick(t, 0)
		a, n := r.activeKID(t), r.nextKID(t)
		r.corrupt(t, n)

		r.clock.Advance(keys.ActiveDuration)
		if _, err := r.rotator.Tick(r.ctx(t)); err != nil {
			t.Fatalf("Tick: %v", err)
		}
		if got := r.stateOf(t, a); got != "active" {
			t.Fatalf("old active state = %q, want active (no swap onto a broken key)", got)
		}
		if got := r.stateOf(t, n); got != "removed" {
			t.Fatalf("corrupt next state = %q, want removed", got)
		}
	})
}

func TestKeyringSignerAndSecrecy(t *testing.T) {
	r := newRig(t)
	if _, err := r.ring.Signer(); err == nil {
		t.Fatal("signer available before any key exists")
	}
	r.tick(t, 0)
	s, err := r.ring.Signer()
	if err != nil {
		t.Fatal(err)
	}

	// The signer works and matches the published public key.
	digest := sha256.Sum256([]byte("payload"))
	sig, err := ecdsa.SignASN1(randReader{}, s.Key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	pub := r.ring.PublicKeys()
	if len(pub) != 2 || pub[0].KID != s.KID || pub[0].JWK.Kid != s.KID || pub[1].State != keys.StateNext {
		t.Fatalf("published = %+v", pub)
	}
	if !ecdsa.VerifyASN1(&s.Key.PublicKey, digest[:], sig) {
		t.Fatal("signature does not verify")
	}

	// The stored private material is ciphertext, not the raw scalar.
	var enc []byte
	if err := r.pool.QueryRow(r.ctx(t), "SELECT private_enc FROM signing_keys WHERE kid = $1", s.KID).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	scalar, _ := s.Key.Bytes()
	if len(enc) == 0 || bytes.Contains(enc, scalar) {
		t.Fatal("private key stored in clear")
	}
}

func TestKeyringRejectsWrongKEKAndMovedCiphertext(t *testing.T) {
	r := newRig(t)
	r.tick(t, 0)
	active := r.activeKID(t)
	r.tick(t, keys.ActiveDuration-keys.PrepublishDuration) // creates next

	t.Run("wrong KEK", func(t *testing.T) {
		other, _, _ := keys.GenerateKEK()
		ring, err := keys.NewKeyring(keys.KeyringConfig{Store: r.store, KEK: other})
		if err != nil {
			t.Fatal(err)
		}
		if err := ring.Reload(r.ctx(t)); err == nil {
			t.Fatal("reload succeeded with the wrong KEK")
		}
		if _, err := ring.Signer(); err == nil {
			t.Fatal("signer available after failed reload")
		}
	})

	t.Run("ciphertext moved to another kid fails the AAD check", func(t *testing.T) {
		var next string
		for _, p := range r.published() {
			if p.State == keys.StateNext {
				next = p.KID
			}
		}
		// Give the next key's ciphertext to the active row.
		ctx := r.ctx(t)
		if _, err := r.pool.Exec(ctx, "UPDATE signing_keys SET private_enc = (SELECT private_enc FROM signing_keys WHERE kid = $1) WHERE kid = $2", next, active); err != nil {
			t.Fatal(err)
		}
		ring, _ := keys.NewKeyring(keys.KeyringConfig{Store: r.store, KEK: r.kek})
		if err := ring.Reload(ctx); err == nil {
			t.Fatal("reload accepted a ciphertext bound to a different kid")
		}
	})
}

// M1 (b): when the database's active key cannot be loaded, the replica stops signing and stops
// publishing a key the database no longer publishes, instead of keeping the old snapshot.
func TestKeyringFailsClosedWhenNewActiveCannotBeLoaded(t *testing.T) {
	r := newRig(t)
	r.tick(t, 0)
	a, n := r.activeKID(t), r.nextKID(t)
	if _, err := r.ring.Signer(); err != nil {
		t.Fatal(err)
	}

	// Behind the rotator's back (it would refuse): revoke a, activate a next key with garbage ciphertext.
	r.corrupt(t, n)
	ctx := r.ctx(t)
	if _, err := r.pool.Exec(ctx, "UPDATE signing_keys SET state = 'removed', removed_at = now(), private_enc = NULL WHERE kid = $1", a); err != nil {
		t.Fatal(err)
	}
	if _, err := r.pool.Exec(ctx, "UPDATE signing_keys SET state = 'active', activated_at = now() WHERE kid = $1", n); err != nil {
		t.Fatal(err)
	}

	if err := r.ring.Reload(ctx); err == nil {
		t.Fatal("Reload reported success although the active key cannot be loaded")
	}
	if s, err := r.ring.Signer(); err == nil {
		t.Fatalf("still signing with %s after the database moved to another active key", s.KID)
	}
	for _, p := range r.ring.PublicKeys() {
		if p.KID == a {
			t.Fatal("the revoked key is still published")
		}
	}
}

func TestKeyringSkipsKeysWhoseJWKDoesNotMatchKID(t *testing.T) {
	r := newRig(t)
	r.tick(t, 0)
	if _, err := r.pool.Exec(r.ctx(t), "UPDATE signing_keys SET public_jwk = public_jwk || '{\"x\":\"tampered\"}'"); err != nil {
		t.Fatal(err)
	}
	// Tampered public JWK no longer matches the kid: the key is skipped, not trusted.
	if err := r.ring.Reload(r.ctx(t)); err != nil {
		t.Fatal(err)
	}
	if len(r.ring.PublicKeys()) != 0 {
		t.Fatal("key with a JWK not matching its kid was published")
	}
}

func TestKeyringReloadsOnNotify(t *testing.T) {
	r := newRig(t)
	// A long refresh interval proves the reload comes from NOTIFY, not from the timer.
	ring, err := keys.NewKeyring(keys.KeyringConfig{Store: r.store, KEK: r.kek, RefreshInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { ring.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	eventually := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", what)
	}

	// Let the initial reload and LISTEN settle on an empty table, then change it.
	time.Sleep(200 * time.Millisecond)
	if _, err := r.rotator.Tick(r.ctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually("bootstrap key to appear", func() bool { _, err := ring.Signer(); return err == nil })
	first, _ := ring.Signer()

	if err := r.rotator.Revoke(r.ctx(t), first.KID); err != nil {
		t.Fatal(err)
	}
	eventually("emergency rotation to be picked up", func() bool {
		s, err := ring.Signer()
		return err == nil && s.KID != first.KID
	})
}

// L8: the rotator works with the least-privilege runtime role (column-scoped UPDATE on signing_keys).
func TestRotationWorksUnderRuntimeRole(t *testing.T) {
	r := newRig(t)
	cfg := r.pool.Config().Copy()
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET ROLE platform_runtime")
		return err
	}
	pool, err := pgxpool.NewWithConfig(r.ctx(t), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	rotator, err := keys.NewRotator(keys.RotatorConfig{Store: postgres.NewKeyStore(pool, postgres.WithClock(r.clock.Now)), KEK: r.kek})
	if err != nil {
		t.Fatal(err)
	}
	ctx := r.ctx(t)
	if _, err := rotator.Tick(ctx); err != nil {
		t.Fatalf("bootstrap as runtime role: %v", err)
	}
	r.clock.Advance(keys.ActiveDuration)
	if _, err := rotator.Tick(ctx); err != nil {
		t.Fatalf("swap as runtime role: %v", err)
	}
	r.clock.Advance(keys.RetiredDuration)
	if _, err := rotator.Tick(ctx); err != nil {
		t.Fatalf("removal as runtime role: %v", err)
	}
	if err := rotator.Revoke(ctx, r.currentActive(t)); err != nil {
		t.Fatalf("revoke as runtime role: %v", err)
	}
}

func (r *rig) currentActive(t *testing.T) string {
	t.Helper()
	var kid string
	if err := r.pool.QueryRow(r.ctx(t), "SELECT kid FROM signing_keys WHERE state = 'active'").Scan(&kid); err != nil {
		t.Fatal(err)
	}
	return kid
}
