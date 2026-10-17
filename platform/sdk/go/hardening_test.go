package platformauth_test

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	platformauth "github.com/Wysted/atrium/platform/sdk/go"
)

const asyncTimeout = 5 * time.Second

// gatedJWKS fronts a jwksServer and holds every request until release is called, so a test can
// control exactly when an in-flight fetch finishes.
type gatedJWKS struct {
	*httptest.Server
	gate    chan struct{}
	once    sync.Once
	arrived chan struct{} // receives one value per request that reached the gate
}

func newGatedJWKS(t *testing.T, backend *jwksServer) *gatedJWKS {
	t.Helper()
	g := &gatedJWKS{gate: make(chan struct{}), arrived: make(chan struct{}, 16)}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.arrived <- struct{}{}
		select {
		case <-g.gate:
		case <-r.Context().Done():
			return
		}
		backend.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { g.release(); g.Close() })
	return g
}

func (g *gatedJWKS) release() { g.once.Do(func() { close(g.gate) }) }

func (g *gatedJWKS) waitArrived(t *testing.T) {
	t.Helper()
	select {
	case <-g.arrived:
	case <-time.After(asyncTimeout):
		t.Fatal("no JWKS request reached the server")
	}
}

func verifierFor(t *testing.T, url string, clock *fakeClock) *platformauth.Verifier {
	t.Helper()
	v, err := platformauth.NewVerifier(platformauth.Config{Issuer: testIssuer, Audience: testAudience, JWKSURL: url, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// H1: a client that cancels while it triggers the post-expiry fetch must not poison the cache for everybody else.
func TestCanceledRequestDoesNotPoisonJWKSFetch(t *testing.T) {
	clock := newClock()
	key := newKey(t)
	srv := newJWKSServer(t, map[string]*ecdsa.PrivateKey{testKID: key})
	v := newVerifier(t, srv, clock)
	tok := func() string { return mint(t, key, nil, baseClaims(clock.Now())) }

	if _, err := v.Verify(context.Background(), tok()); err != nil {
		t.Fatal(err)
	}
	// Past expiry AND past the stale window, so stale keys cannot mask the poisoning.
	clock.Advance(platformauth.MaxCacheTTL + platformauth.MaxStaleness + time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := v.Verify(ctx, tok()); err == nil {
		t.Fatal("canceled request succeeded")
	} else if errors.Is(err, platformauth.ErrInvalidAssertion) {
		t.Fatalf("a canceled request must not be reported as an invalid assertion: %v", err)
	}

	clock.Advance(time.Second) // inside FailureBackoff: a memoized cancellation would still be served
	if _, err := v.Verify(context.Background(), tok()); err != nil {
		t.Fatalf("healthy request after a canceled one: %v", err)
	}
	if got := srv.hits.Load(); got != 2 {
		t.Fatalf("hits = %d, want 2 (warm-up + one shared refetch)", got)
	}
}

// H1/L9: waiters share one fetch, the leader leaving does not abort it, and each waiter abandons on its own context.
func TestFetchIsSharedAndWaitersUseTheirOwnContext(t *testing.T) {
	clock := newClock()
	key := newKey(t)
	backend := newJWKSServer(t, map[string]*ecdsa.PrivateKey{testKID: key})
	gate := newGatedJWKS(t, backend)
	v := verifierFor(t, gate.URL, clock)
	tok := mint(t, key, nil, baseClaims(clock.Now()))

	type result struct{ err error }
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leader := make(chan result, 1)
	go func() { _, err := v.Verify(leaderCtx, tok); leader <- result{err} }()
	gate.waitArrived(t) // the leader's fetch is in flight

	follower := make(chan result, 1)
	go func() { _, err := v.Verify(context.Background(), tok); follower <- result{err} }()

	// A waiter with a short deadline abandons the wait while the fetch is blocked (L9).
	shortCtx, cancelShort := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelShort()
	impatient := make(chan result, 1)
	go func() { _, err := v.Verify(shortCtx, tok); impatient <- result{err} }()
	select {
	case r := <-impatient:
		if !errors.Is(r.err, platformauth.ErrKeysUnavailable) || errors.Is(r.err, platformauth.ErrInvalidAssertion) {
			t.Fatalf("impatient waiter err = %v", r.err)
		}
	case <-time.After(asyncTimeout):
		t.Fatal("waiter ignored its own context while the fetch was blocked")
	}

	cancelLeader() // the request that started the fetch goes away
	select {
	case r := <-leader:
		if r.err == nil {
			t.Fatal("canceled leader succeeded")
		}
	case <-time.After(asyncTimeout):
		t.Fatal("leader ignored its canceled context")
	}

	gate.release()
	select {
	case r := <-follower:
		if r.err != nil {
			t.Fatalf("follower failed because the leader was canceled: %v", r.err)
		}
	case <-time.After(asyncTimeout):
		t.Fatal("follower never finished")
	}
	if got := backend.hits.Load(); got != 1 {
		t.Fatalf("JWKS fetched %d times, want 1 shared fetch", got)
	}
}

// M2: stale-if-error, short failure backoff, and 503 vs 401 in the middleware.
func TestStaleIfErrorAndShortBackoff(t *testing.T) {
	setup := func(t *testing.T) (*platformauth.Verifier, *jwksServer, *fakeClock, func() string) {
		clock := newClock()
		key := newKey(t)
		srv := newJWKSServer(t, map[string]*ecdsa.PrivateKey{testKID: key})
		v := newVerifier(t, srv, clock)
		tok := func() string { return mint(t, key, nil, baseClaims(clock.Now())) }
		if _, err := v.Verify(context.Background(), tok()); err != nil {
			t.Fatal(err)
		}
		return v, srv, clock, tok
	}

	t.Run("expired cache keeps serving while the JWKS is down, up to the cap", func(t *testing.T) {
		v, srv, clock, tok := setup(t)
		srv.setStatus(http.StatusServiceUnavailable)

		clock.Advance(platformauth.MaxCacheTTL + time.Second)
		if _, err := v.Verify(context.Background(), tok()); err != nil {
			t.Fatalf("stale keys not used during an outage: %v", err)
		}
		clock.Advance(platformauth.MaxStaleness - 2*time.Second) // still inside the cap
		if _, err := v.Verify(context.Background(), tok()); err != nil {
			t.Fatalf("stale keys dropped before the cap: %v", err)
		}
		clock.Advance(5 * time.Second) // beyond expiry + MaxStaleness
		if _, err := v.Verify(context.Background(), tok()); !errors.Is(err, platformauth.ErrKeysUnavailable) {
			t.Fatalf("err = %v, want ErrKeysUnavailable past the cap", err)
		}
	})

	t.Run("failures are remembered only for the short backoff", func(t *testing.T) {
		v, srv, clock, tok := setup(t)
		srv.setStatus(http.StatusServiceUnavailable)
		clock.Advance(platformauth.MaxCacheTTL + platformauth.MaxStaleness + time.Second) // no stale fallback

		if _, err := v.Verify(context.Background(), tok()); !errors.Is(err, platformauth.ErrKeysUnavailable) {
			t.Fatalf("err = %v", err)
		}
		hits := srv.hits.Load()
		srv.setStatus(http.StatusOK)
		clock.Advance(platformauth.FailureBackoff - time.Millisecond)
		if _, err := v.Verify(context.Background(), tok()); !errors.Is(err, platformauth.ErrKeysUnavailable) {
			t.Fatalf("inside the backoff err = %v", err)
		}
		if srv.hits.Load() != hits {
			t.Fatal("refetched inside the backoff")
		}
		clock.Advance(time.Millisecond)
		if _, err := v.Verify(context.Background(), tok()); err != nil {
			t.Fatalf("not recovered after the backoff: %v", err)
		}
	})

	t.Run("stale keys do not hide a key the JWKS no longer has once it answers", func(t *testing.T) {
		v, srv, clock, tok := setup(t)
		srv.dropKey(testKID)
		clock.Advance(platformauth.MaxCacheTTL + time.Second)
		// Stale-while-revalidate: this request is served from the stale key and triggers the refresh...
		if _, err := v.Verify(context.Background(), tok()); err != nil {
			t.Fatalf("stale key not served while revalidating: %v", err)
		}
		waitFetchDone(t, v)
		// ...and once the JWKS has answered, the removed key is rejected.
		if _, err := v.Verify(context.Background(), tok()); !errors.Is(err, platformauth.ErrInvalidAssertion) {
			t.Fatalf("err = %v, want rejection of the removed key", err)
		}
	})
}

func waitFetchDone(t *testing.T, v *platformauth.Verifier) {
	t.Helper()
	deadline := time.Now().Add(asyncTimeout)
	for platformauth.FetchInflight(v) {
		if time.Now().After(deadline) {
			t.Fatal("JWKS fetch goroutine never finished")
		}
		time.Sleep(time.Millisecond)
	}
}

// hangingJWKS wraps a jwksServer and, once hang is set, accepts requests and never answers them.
type hangingJWKS struct {
	*httptest.Server
	hang    atomic.Bool
	arrived atomic.Int64
}

func newHangingJWKS(t *testing.T, backend *jwksServer) *hangingJWKS {
	t.Helper()
	h := &hangingJWKS{}
	stop := make(chan struct{})
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.hang.Load() {
			h.arrived.Add(1)
			select {
			case <-r.Context().Done():
			case <-stop:
			}
			return
		}
		backend.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { close(stop); h.Close() })
	return h
}

// M1 (round 2): a JWKS that HANGS (not one that answers 503) must not stall requests that hold a usable
// stale key; the hung fetch ends at its timeout, frees the single flight, and its backoff counts from its END.
func TestStaleWhileRevalidateWithHangingJWKS(t *testing.T) {
	clock := newClock()
	key := newKey(t)
	backend := newJWKSServer(t, map[string]*ecdsa.PrivateKey{testKID: key})
	srv := newHangingJWKS(t, backend)
	v := verifierFor(t, srv.URL, clock)
	const hungFetchTimeout = 150 * time.Millisecond
	platformauth.SetFetchTimeout(v, hungFetchTimeout)
	tok := func() string { return mint(t, key, nil, baseClaims(clock.Now())) }

	if _, err := v.Verify(context.Background(), tok()); err != nil {
		t.Fatal(err)
	}
	srv.hang.Store(true)
	clock.Advance(platformauth.MaxCacheTTL + time.Second) // expired, inside the stale window

	// Many concurrent requests with a stale key: all answer at once, far below the fetch timeout.
	const requests = 20
	start := time.Now()
	var wg sync.WaitGroup
	errs := make(chan error, requests)
	for range requests {
		wg.Go(func() {
			_, err := v.Verify(context.Background(), tok())
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("request with a stale key failed during a hang: %v", err)
		}
	}
	if elapsed := time.Since(start); elapsed >= hungFetchTimeout {
		t.Fatalf("requests took %v, they waited for the hung fetch (timeout %v)", elapsed, hungFetchTimeout)
	}
	if !platformauth.FetchInflight(v) {
		t.Fatal("no background refresh was started")
	}

	// A request WITHOUT a usable key (unknown kid) does wait, and gives up on its own context.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	other := mint(t, key, map[string]any{"kid": "other"}, baseClaims(clock.Now()))
	if _, err := v.Verify(ctx, other); !errors.Is(err, platformauth.ErrKeysUnavailable) {
		t.Fatalf("unknown kid during a hang: err = %v, want ErrKeysUnavailable", err)
	}

	// The fetch goroutine ends at its timeout and releases the single flight.
	waitFetchDone(t, v)
	if got := srv.arrived.Load(); got != 1 {
		t.Fatalf("hung requests reached the server %d times, want 1 single-flight fetch", got)
	}

	// Backoff counts from the END of the failed fetch: the clock has not moved since, so no new fetch.
	if _, err := v.Verify(context.Background(), tok()); err != nil {
		t.Fatalf("stale key after the failed refresh: %v", err)
	}
	if platformauth.FetchInflight(v) || srv.arrived.Load() != 1 {
		t.Fatal("refetched inside the backoff that follows a failed fetch")
	}
	clock.Advance(platformauth.FailureBackoff)
	if _, err := v.Verify(context.Background(), tok()); err != nil {
		t.Fatal(err)
	}
	waitFetchDone(t, v)
	if got := srv.arrived.Load(); got != 2 {
		t.Fatalf("server hits = %d, want a retry once the backoff passed", got)
	}
}

// L1 (round 2): a fresh cache without the kid plus a failed refetch is an outage (503), not an unknown kid (401).
func TestFailedRefetchForUnknownKIDIsAnOutage(t *testing.T) {
	clock := newClock()
	key := newKey(t)
	srv := newJWKSServer(t, map[string]*ecdsa.PrivateKey{testKID: key})
	v := newVerifier(t, srv, clock)
	if _, err := v.Verify(context.Background(), mint(t, key, nil, baseClaims(clock.Now()))); err != nil {
		t.Fatal(err)
	}
	srv.setStatus(http.StatusServiceUnavailable)
	clock.Advance(platformauth.DefaultMinRefetchInterval) // still fresh (< MaxCacheTTL), refetch allowed
	other := func() string { return mint(t, key, map[string]any{"kid": "other"}, baseClaims(clock.Now())) }

	for _, step := range []string{"the failing refetch", "inside the backoff"} {
		_, err := v.Verify(context.Background(), other())
		if !errors.Is(err, platformauth.ErrKeysUnavailable) || errors.Is(err, platformauth.ErrInvalidAssertion) {
			t.Fatalf("%s: err = %v, want ErrKeysUnavailable only", step, err)
		}
	}

	// Once the JWKS answers and still lacks the kid, it is a plain invalid assertion again.
	srv.setStatus(http.StatusOK)
	clock.Advance(platformauth.FailureBackoff)
	if _, err := v.Verify(context.Background(), other()); !errors.Is(err, platformauth.ErrInvalidAssertion) {
		t.Fatalf("err = %v, want ErrInvalidAssertion", err)
	}
}

func TestMiddlewareAnswers503WhenKeysAreUnavailable(t *testing.T) {
	clock := newClock()
	key := newKey(t)
	srv := newJWKSServer(t, map[string]*ecdsa.PrivateKey{testKID: key})
	srv.setStatus(http.StatusInternalServerError)
	v := newVerifier(t, srv, clock)
	called := false
	h := v.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))

	do := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/dashboard", nil)
		req.Header.Set(platformauth.HeaderName, token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := do(mint(t, key, nil, baseClaims(clock.Now())))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("valid token during an outage: status %d, content-type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	// A token that is invalid on its own is a 401 even during the outage: it never costs a fetch.
	if rec := do("not.a.jwt"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token status = %d, want 401", rec.Code)
	}
	if called {
		t.Fatal("handler reached without a verified assertion")
	}
}

// L3: the exempt health path must not let a client-sent assertion reach the handler.
func TestHealthPathStripsClientAssertion(t *testing.T) {
	clock := newClock()
	srv := newJWKSServer(t, nil)
	v := newVerifier(t, srv, clock)
	var seen string
	h := v.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get(platformauth.HeaderName)
	}))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil)
	req.Header.Set(platformauth.HeaderName, "forged")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if seen != "" {
		t.Fatalf("handler saw the client-sent assertion %q on the health path", seen)
	}
}

// L4: header crit, missing iat, over-long lifetimes and future timestamps.
func TestVerifyHardenedClaims(t *testing.T) {
	signer := newKey(t)
	const leeway = platformauth.DefaultLeeway
	tests := []struct {
		name    string
		header  map[string]any
		mutate  func(jwt.MapClaims)
		wantErr bool
	}{
		{name: "baseline"},
		{name: "crit header", header: map[string]any{"crit": []string{"exp"}}, wantErr: true},
		{name: "crit header naming an unknown parameter", header: map[string]any{"crit": []string{"x-foo"}, "x-foo": 1}, wantErr: true},
		{name: "missing iat", mutate: func(c jwt.MapClaims) { delete(c, "iat") }, wantErr: true},
		{name: "lifetime at the limit", mutate: func(c jwt.MapClaims) {
			c["exp"] = baseTime.Add(platformauth.AssertionTTL + leeway).Unix()
		}},
		{name: "lifetime over the limit", mutate: func(c jwt.MapClaims) {
			c["exp"] = baseTime.Add(platformauth.AssertionTTL + leeway + time.Second).Unix()
		}, wantErr: true},
		{name: "one year lifetime", mutate: func(c jwt.MapClaims) { c["exp"] = baseTime.Add(365 * 24 * time.Hour).Unix() }, wantErr: true},
		{name: "iat in the future beyond leeway", mutate: func(c jwt.MapClaims) {
			c["iat"] = baseTime.Add(leeway + time.Second).Unix()
			c["exp"] = baseTime.Add(leeway + time.Second + platformauth.AssertionTTL).Unix()
		}, wantErr: true},
		{name: "nbf in the future beyond leeway", mutate: func(c jwt.MapClaims) { c["nbf"] = baseTime.Add(leeway + time.Second).Unix() }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newJWKSServer(t, map[string]*ecdsa.PrivateKey{testKID: signer})
			clock := newClock()
			v := newVerifier(t, srv, clock)
			claims := baseClaims(clock.Now())
			if tt.mutate != nil {
				tt.mutate(claims)
			}
			_, err := v.Verify(context.Background(), mint(t, signer, tt.header, claims))
			if (err != nil) != tt.wantErr {
				t.Fatalf("Verify() err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, platformauth.ErrInvalidAssertion) {
				t.Errorf("error %v does not wrap ErrInvalidAssertion", err)
			}
		})
	}
}

// L5: redirects from the JWKS endpoint are never followed, with the default client or a provided one.
func TestJWKSRedirectsAreNeverFollowed(t *testing.T) {
	key := newKey(t)
	for name, client := range map[string]*http.Client{
		"default client":                             nil,
		"provided client that follows":               {Timeout: time.Second},
		"provided client with its own CheckRedirect": {CheckRedirect: func(*http.Request, []*http.Request) error { return nil }},
	} {
		t.Run(name, func(t *testing.T) {
			real := newJWKSServer(t, map[string]*ecdsa.PrivateKey{testKID: key})
			var redirected atomic.Int64
			redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				redirected.Add(1)
				http.Redirect(w, r, real.URLPath(), http.StatusFound)
			}))
			t.Cleanup(redirector.Close)

			clock := newClock()
			v, err := platformauth.NewVerifier(platformauth.Config{
				Issuer: testIssuer, Audience: testAudience, JWKSURL: redirector.URL, HTTPClient: client, Now: clock.Now,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = v.Verify(context.Background(), mint(t, key, nil, baseClaims(clock.Now())))
			if !errors.Is(err, platformauth.ErrKeysUnavailable) {
				t.Fatalf("err = %v, want ErrKeysUnavailable", err)
			}
			if redirected.Load() == 0 || real.hits.Load() != 0 {
				t.Fatalf("redirector hits = %d, redirect target hits = %d (must be 1+ and 0)", redirected.Load(), real.hits.Load())
			}
		})
	}
}
