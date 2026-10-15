package portal

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"spike/forwardauth/internal/token"
)

const (
	hdrForwardedHost   = "X-Forwarded-Host"
	hdrForwardedURI    = "X-Forwarded-Uri"
	hdrForwardedMethod = "X-Forwarded-Method"
	jwksCacheControl   = "public, max-age=300"
	httpsScheme        = "https"
)

// session maps an opaque secret to a user (and an app for per-app sessions).
type session struct {
	user string
	app  AppID
	exp  time.Time
}

// loginCode is a one-time code bound to user + app host + the original URL.
type loginCode struct {
	user string
	host string
	rd   string
	exp  time.Time
}

// Server is the authz HTTP service. All state is in memory (spike).
type Server struct {
	cfg  Config
	kid  string
	jwks token.Set

	mu          sync.Mutex
	sessions    map[string]session   // central sessions, keyed by hash(secret)
	appSessions map[string]session   // per-app sessions, keyed by hash(secret)
	codes       map[string]loginCode // one-time codes, keyed by hash(secret)
}

// New builds a Server and precomputes the JWKS document.
func New(cfg Config) (*Server, error) {
	// An empty auth host would match a missing X-Forwarded-Host in forwardAuth.
	cfg.AuthHost = strings.ToLower(cfg.AuthHost)
	if cfg.AuthHost == "" {
		return nil, errors.New("auth host must not be empty")
	}
	// The auth host must never be an app: forwardAuth would otherwise be able to mint tokens for it.
	for h := range cfg.Apps {
		if strings.ToLower(h) == cfg.AuthHost {
			return nil, fmt.Errorf("auth host %q must not be listed in apps", cfg.AuthHost)
		}
	}
	kid, err := token.KeyID(&cfg.Key.PublicKey)
	if err != nil {
		return nil, err
	}
	jwk, err := token.NewJWK(kid, &cfg.Key.PublicKey)
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg: cfg, kid: kid, jwks: token.Set{Keys: []token.JWK{jwk}},
		sessions:    map[string]session{},
		appSessions: map[string]session{},
		codes:       map[string]loginCode{},
	}, nil
}

// Handler returns the routes. /forwardauth accepts every method on purpose: Traefik
// replays the original method (OPTIONS preflights included) against the auth server.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/forwardauth", s.forwardAuth)
	mux.HandleFunc("GET /login", s.login)
	mux.HandleFunc("GET /.well-known/jwks.json", s.serveJWKS)
	return mux
}

func (s *Server) now() time.Time { return s.cfg.Clock() }

// ---- ForwardAuth ----

func (s *Server) forwardAuth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	// The app is derived ONLY from X-Forwarded-Host (exact match, lowercase).
	// X-Forwarded-Prefix/-Port/-Proto are deliberately ignored.
	host := strings.ToLower(r.Header.Get(hdrForwardedHost))

	// The middleware is attached at the entrypoint, so the auth host (/login, JWKS) also
	// lands here. It protects itself with the central session: let it through and never
	// mint a token for it.
	if host == s.cfg.AuthHost {
		w.WriteHeader(http.StatusOK)
		return
	}
	app, ok := s.cfg.Apps[host]
	if !ok {
		writeJSON(w, http.StatusForbidden, "unknown host")
		return
	}
	method := r.Header.Get(hdrForwardedMethod)
	if method == "" {
		method = r.Method
	}

	// Preflight criterion: a CORS preflight (OPTIONS + Origin + Access-Control-Request-Method)
	// never carries credentials by spec, so it cannot be authenticated. It is let through
	// without a token and carries no data; the real request that follows is fully checked.
	if method == http.MethodOptions && r.Header.Get("Origin") != "" &&
		r.Header.Get("Access-Control-Request-Method") != "" {
		w.WriteHeader(http.StatusOK)
		return
	}

	uri := r.Header.Get(hdrForwardedURI)
	if uri == "" {
		uri = "/"
	}
	u, err := url.ParseRequestURI(uri)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, "invalid forwarded uri")
		return
	}
	if u.Path == CallbackPath {
		s.callback(w, host, u)
		return
	}

	sess, ok := s.lookup(s.appSessions, r, AppCookie)
	if !ok || sess.app != app {
		s.unauthenticated(w, r, method, host, uri)
		return
	}
	user, ok := s.cfg.Users[sess.user]
	if !ok || !Decide(AccessRequest{Subject: user, Resource: app, Action: method}) {
		writeJSON(w, http.StatusForbidden, "forbidden")
		return
	}
	signed, err := s.mint(user, app)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set(token.HeaderName, signed)
	w.WriteHeader(http.StatusOK)
}

// unauthenticated answers 401 JSON for API/XHR traffic and 302 to login for HTML navigation.
func (s *Server) unauthenticated(w http.ResponseWriter, r *http.Request, method, host, uri string) {
	accept := r.Header.Get("Accept")
	apiLike := strings.HasPrefix(uri, apiPrefix) ||
		strings.Contains(accept, "application/json") ||
		r.Header.Get("X-Requested-With") == "XMLHttpRequest"
	navigation := r.Header.Get("Sec-Fetch-Mode") == "navigate" || strings.Contains(accept, "text/html")
	safeMethod := method == http.MethodGet || method == http.MethodHead

	if apiLike || !navigation || !safeMethod {
		writeJSON(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	original := httpsScheme + "://" + host + uri
	loc := httpsScheme + "://" + s.cfg.AuthHost + "/login?rd=" + url.QueryEscape(original)
	http.Redirect(w, r, loc, http.StatusFound)
}

// callback consumes the one-time code and plants the per-app cookie via a 302.
func (s *Server) callback(w http.ResponseWriter, host string, u *url.URL) {
	code := u.Query().Get("code")
	if code == "" {
		writeJSON(w, http.StatusBadRequest, "missing code")
		return
	}
	entry, ok := s.consumeCode(code)
	// Code is already burned here, whatever the outcome (single use).
	if !ok || entry.host != host || !s.now().Before(entry.exp) {
		writeJSON(w, http.StatusForbidden, "invalid code")
		return
	}
	secret, err := s.newSecret()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	app := s.cfg.Apps[host]
	s.mu.Lock()
	s.appSessions[hashSecret(secret)] = session{user: entry.user, app: app, exp: s.now().Add(AppSessionTTL)}
	s.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name: AppCookie, Value: secret, Path: "/", // no Domain: required by __Host-
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: int(AppSessionTTL / time.Second),
	})
	w.Header().Set("Location", entry.rd)
	w.WriteHeader(http.StatusFound)
}

func (s *Server) consumeCode(code string) (loginCode, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := hashSecret(code)
	entry, ok := s.codes[key]
	delete(s.codes, key)
	return entry, ok
}

// ---- Fake login on the auth host ----

var chooser = template.Must(template.New("chooser").Parse(
	`<!doctype html><title>Fake login</title><h1>Pick a user</h1><ul>
{{range .Users}}<li><a href="{{$.Base}}&user={{.}}">{{.}}</a></li>{{end}}</ul>`))

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	rd, err := s.validateRD(r.URL.Query().Get("rd"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, "invalid rd")
		return
	}
	name := r.URL.Query().Get("user")
	if name == "" {
		// Reuse the central session (SSO) when present; otherwise show the chooser.
		sess, ok := s.lookup(s.sessions, r, SessionCookie)
		if !ok {
			s.renderChooser(w, rd)
			return
		}
		name = sess.user
	} else {
		if _, ok := s.cfg.Users[name]; !ok {
			writeJSON(w, http.StatusBadRequest, "unknown user")
			return
		}
		secret, err := s.newSecret()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, "internal error")
			return
		}
		s.mu.Lock()
		s.sessions[hashSecret(secret)] = session{user: name, exp: s.now().Add(SessionTTL)}
		s.mu.Unlock()
		http.SetCookie(w, &http.Cookie{
			Name: SessionCookie, Value: secret, Path: "/",
			Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
			MaxAge: int(SessionTTL / time.Second),
		})
	}

	code, err := s.newSecret()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.mu.Lock()
	s.codes[hashSecret(code)] = loginCode{user: name, host: rd.Host, rd: rd.String(), exp: s.now().Add(CodeTTL)}
	s.mu.Unlock()

	loc := httpsScheme + "://" + rd.Host + CallbackPath + "?code=" + url.QueryEscape(code)
	http.Redirect(w, r, loc, http.StatusFound)
}

// validateRD is the anti open-redirect check: https scheme, no userinfo, known app host.
func (s *Server) validateRD(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, errors.New("empty rd")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse rd: %w", err)
	}
	if u.Scheme != httpsScheme || u.User != nil || u.Opaque != "" {
		return nil, errors.New("rd must be an absolute https URL")
	}
	if _, ok := s.cfg.Apps[strings.ToLower(u.Host)]; !ok {
		return nil, errors.New("rd host is not a known app")
	}
	u.Host = strings.ToLower(u.Host)
	if u.Path == "" {
		u.Path = "/"
	}
	return u, nil
}

func (s *Server) renderChooser(w http.ResponseWriter, rd *url.URL) {
	names := make([]string, 0, len(s.cfg.Users))
	for n := range s.cfg.Users {
		names = append(names, n)
	}
	sort.Strings(names)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = chooser.Execute(w, map[string]any{
		"Users": names,
		"Base":  template.URL("/login?rd=" + url.QueryEscape(rd.String())),
	})
}

// ---- JWKS and tokens ----

func (s *Server) serveJWKS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", jwksCacheControl)
	_ = json.NewEncoder(w).Encode(s.jwks)
}

func (s *Server) mint(user User, app AppID) (string, error) {
	now := s.now()
	return token.Sign(s.cfg.Key, s.kid, token.Claims{
		Roles: user.Roles,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    token.Issuer,
			Subject:   user.ID,
			Audience:  jwt.ClaimStrings{string(app)},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(TokenTTL)),
		},
	})
}

// ---- helpers ----

func (s *Server) newSecret() (string, error) {
	buf := make([]byte, secretBytes)
	if _, err := io.ReadFull(s.cfg.Rand, buf); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashSecret keys the stores by hash so lookups never compare raw secrets.
func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func (s *Server) lookup(store map[string]session, r *http.Request, cookie string) (session, bool) {
	c, err := r.Cookie(cookie)
	if err != nil || c.Value == "" {
		return session{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := hashSecret(c.Value)
	sess, ok := store[key]
	if !ok {
		return session{}, false
	}
	if !s.now().Before(sess.exp) {
		delete(store, key)
		return session{}, false
	}
	return sess, true
}

func writeJSON(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
