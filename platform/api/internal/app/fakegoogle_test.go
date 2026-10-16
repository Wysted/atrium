package app_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"platform/api/internal/googlelogin"
)

const (
	googleClientID = "test-client.apps.googleusercontent.com"
	googleSecret   = "test-client-secret"
	googleHD       = "example.test"
	googleKID      = "test-key-1"
	callbackURL    = "https://" + authHost + "/oauth2/callback"

	idTokenTTL = time.Hour
	rsaBits    = 2048
)

// account is the identity the fake provider signs into the ID token.
type account struct {
	Sub           string
	Email         string
	Name          string
	HD            string // empty: the claim is omitted (personal account)
	EmailVerified bool
}

func defaultAccount() account {
	return account{Sub: "100000000000000000001", Email: "ana@example.test", Name: "Ana Test", HD: googleHD, EmailVerified: true}
}

// issuedCode is an authorization code waiting to be exchanged.
type issuedCode struct {
	challenge string
	nonce     string
	account   account
}

// fakeGoogle is an OpenID provider on httptest: discovery, JWKS, authorization and token endpoints. The token
// endpoint verifies the PKCE code_verifier against the challenge like Google does, and ID tokens are RS256.
type fakeGoogle struct {
	t   *testing.T
	srv *httptest.Server
	key *rsa.PrivateKey

	down        atomic.Bool  // every endpoint answers 503
	jwksDown    atomic.Bool  // only the JWKS endpoint answers 503
	tokenStatus atomic.Int64 // when non-zero, the token endpoint answers this status with an empty body
	tokenHang   atomic.Bool  // the token endpoint never answers (until the client gives up)
	tokenCalls  atomic.Int64 // requests that reached the token endpoint
	// tokenRedirect, when set, makes the token endpoint answer 307 to that URL.
	tokenRedirect atomic.Pointer[string]

	mu       sync.Mutex
	acct     account
	codes    map[string]issuedCode
	auths    []url.Values        // authorization requests as received
	secrets  []string            // every code, access token and ID token ever issued
	mutate   func(jwt.MapClaims) // applied to the claims before signing
	signWith *rsa.PrivateKey     // nil: the published key
	kid      string              // key id of the published key
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		t.Fatal(err)
	}
	fg := &fakeGoogle{t: t, key: key, kid: googleKID, acct: defaultAccount(), codes: map[string]issuedCode{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", fg.discovery)
	mux.HandleFunc("GET /jwks", fg.jwks)
	mux.HandleFunc("GET /authorize", fg.authorize)
	mux.HandleFunc("POST /token", fg.token)
	fg.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fg.down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(fg.srv.Close)
	return fg
}

// config is the app.Config.Google of a platform-api that trusts this provider.
func (fg *fakeGoogle) config() *googlelogin.Config {
	return &googlelogin.Config{
		Issuer: fg.srv.URL, ClientID: googleClientID, ClientSecret: googleSecret, AllowedHD: []string{googleHD},
		InitialBackoff: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond,
	}
}

func (fg *fakeGoogle) setAccount(a account) {
	fg.mu.Lock()
	defer fg.mu.Unlock()
	fg.acct = a
}

func (fg *fakeGoogle) setMutate(f func(jwt.MapClaims)) {
	fg.mu.Lock()
	defer fg.mu.Unlock()
	fg.mutate = f
}

func (fg *fakeGoogle) setSigner(k *rsa.PrivateKey) {
	fg.mu.Lock()
	defer fg.mu.Unlock()
	fg.signWith = k
}

// rotateKey publishes a new signing key under a new kid and stops publishing the old one, like a provider
// rotating its keys. ID tokens issued from now on are signed with the new key.
func (fg *fakeGoogle) rotateKey() {
	key, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		fg.t.Fatal(err)
	}
	fg.mu.Lock()
	defer fg.mu.Unlock()
	fg.key, fg.kid = key, "test-key-"+randomHex(4)
}

func (fg *fakeGoogle) issuedSecrets() []string {
	fg.mu.Lock()
	defer fg.mu.Unlock()
	return append([]string(nil), fg.secrets...)
}

func (fg *fakeGoogle) lastAuth() url.Values {
	fg.mu.Lock()
	defer fg.mu.Unlock()
	if len(fg.auths) == 0 {
		fg.t.Fatal("no authorization request reached the provider")
	}
	return fg.auths[len(fg.auths)-1]
}

func (fg *fakeGoogle) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                fg.srv.URL,
		"authorization_endpoint":                fg.srv.URL + "/authorize",
		"token_endpoint":                        fg.srv.URL + "/token",
		"jwks_uri":                              fg.srv.URL + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_post", "client_secret_basic"},
	})
}

func (fg *fakeGoogle) jwks(w http.ResponseWriter, _ *http.Request) {
	if fg.jwksDown.Load() {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	fg.mu.Lock()
	pub, kid := fg.key.PublicKey, fg.kid
	fg.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "alg": "RS256", "use": "sig", "kid": kid,
		"n": b64(pub.N.Bytes()), "e": b64(big.NewInt(int64(pub.E)).Bytes()),
	}}})
}

// authorize plays Google after the user picked an account: it answers with the redirect to the callback.
func (fg *fakeGoogle) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fg.mu.Lock()
	fg.auths = append(fg.auths, q)
	acct := fg.acct
	code := randomHex(16)
	fg.codes[code] = issuedCode{challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), account: acct}
	fg.secrets = append(fg.secrets, code)
	fg.mu.Unlock()
	loc := url.Values{"code": {code}, "state": {q.Get("state")}}
	// The fake IdP redirects to whatever redirect_uri the code under test registered; that is the contract it simulates.
	http.Redirect(w, r, q.Get("redirect_uri")+"?"+loc.Encode(), http.StatusFound) //nolint:gosec // G710: test fake, input comes from the test itself
}

func (fg *fakeGoogle) token(w http.ResponseWriter, r *http.Request) {
	fg.tokenCalls.Add(1)
	if fg.tokenHang.Load() {
		_, _ = io.Copy(io.Discard, r.Body) // net/http only notices a closed connection once the body is consumed
		<-r.Context().Done()
		return
	}
	if to := fg.tokenRedirect.Load(); to != nil {
		http.Redirect(w, r, *to, http.StatusTemporaryRedirect)
		return
	}
	if status := fg.tokenStatus.Load(); status != 0 {
		w.WriteHeader(int(status))
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	id, secret, basic := r.BasicAuth()
	if !basic {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	} else {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	}
	if id != googleClientID || secret != googleSecret {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}
	if r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("redirect_uri") != callbackURL {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	fg.mu.Lock()
	issued, ok := fg.codes[r.PostForm.Get("code")]
	delete(fg.codes, r.PostForm.Get("code")) // single use
	mutate, signWith, signKey, kid := fg.mutate, fg.signWith, fg.key, fg.kid
	fg.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !ok || b64(sum[:]) != issued.challenge {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "bad code or code_verifier"})
		return
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"iss": fg.srv.URL, "aud": googleClientID, "sub": issued.account.Sub, "iat": now.Unix(), "exp": now.Add(idTokenTTL).Unix(),
		"nonce": issued.nonce, "email": issued.account.Email, "email_verified": issued.account.EmailVerified, "name": issued.account.Name,
	}
	if issued.account.HD != "" {
		claims["hd"] = issued.account.HD
	}
	if mutate != nil {
		mutate(claims)
	}
	key := signKey
	if signWith != nil {
		key = signWith
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	idToken, err := tok.SignedString(key)
	if err != nil {
		fg.t.Errorf("sign ID token: %v", err)
		http.Error(w, "sign", http.StatusInternalServerError)
		return
	}
	access := "ya29." + randomHex(16)
	fg.mu.Lock()
	fg.secrets = append(fg.secrets, idToken, access)
	fg.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 3599, "id_token": idToken})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
