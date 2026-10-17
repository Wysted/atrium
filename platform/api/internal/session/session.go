// Package session implements the central sessions, per-app sessions and one-time login codes of
// docs/reference/login.md. Time decisions (expiry, idle, throttling) are taken by PostgreSQL's now() inside the
// store, never by replica clocks; this package owns the rules, tokens and validation.
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

// Cookie names (docs/reference/login.md). All are host-only, Secure, HttpOnly, Path=/ and carry a 256-bit random value.
const (
	CookieSession = "__Host-Http-platform_session" // auth host: central session
	CookieApp     = "__Host-Http-platform_app"     // each app host: per-app session
	CookieLogin   = "__Host-Http-platform_login"   // auth host: OAuth flow in progress
	CookieBind    = "__Host-Http-platform_bind"    // app host: binds a login code to the browser that started the login
)

const (
	// TokenBytes is the entropy of every cookie value and login code (256 bits).
	TokenBytes = 32
	// AbsoluteLifetime is the maximum age of a central session, whatever the activity.
	AbsoluteLifetime = 8 * time.Hour
	// DefaultIdleTimeout is how long a central session survives without any request from any app.
	DefaultIdleTimeout = 45 * time.Minute
	// MinIdleTimeout and MaxIdleTimeout are the range of docs/reference/login.md for the idle timeout (30-60 min).
	// The minimum also keeps the idle window well above TouchInterval, so the throttled last_seen_at write
	// can never make an active session look idle.
	MinIdleTimeout = 30 * time.Minute
	MaxIdleTimeout = 60 * time.Minute
	// TouchInterval is the throttling window: last_seen_at is written at most once per window per session.
	TouchInterval = 2 * time.Minute
	// LoginCodeTTL is the lifetime of a one-time login code.
	LoginCodeTTL = 30 * time.Second
	// MaxUserAgentLength bounds the stored User-Agent.
	MaxUserAgentLength = 512
)

var (
	// ErrInvalidSession covers every reason a session cannot be used (unknown, expired, idle, host/app mismatch)
	// so callers and attackers cannot tell them apart.
	ErrInvalidSession = errors.New("invalid session")
	// ErrInvalidCode covers every reason a login code is refused (unknown, reused, expired, wrong host, wrong bind).
	ErrInvalidCode = errors.New("invalid login code")
	// ErrInvalidRD means the post-login redirect target is not acceptable.
	ErrInvalidRD = errors.New("invalid redirect target")
)

// Session is a central session. It never contains the token.
type Session struct {
	ID         string // public sid (JWT "sid"); cannot authenticate
	UserID     string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time // absolute
	IP         netip.Addr
	UserAgent  string
}

// Revoked identifies a central session that RevokeSession just deleted (for audit and logs). The zero
// value means there was nothing to delete.
type Revoked struct {
	SessionID string
	UserID    string
}

// AppSession is a session on one app host. It never outlives its central session.
type AppSession struct {
	ID        string
	SessionID string
	UserID    string
	AppID     string
	Host      string
	ExpiresAt time.Time
}

// LoginGrant is what a consumed login code authorizes: an app session for SessionID on Host, then a redirect to RD.
type LoginGrant struct {
	SessionID string
	AppID     string
	Host      string
	RD        string
}

// Policy carries the time rules every store query applies with the database clock.
type Policy struct {
	IdleTimeout   time.Duration
	TouchInterval time.Duration
}

// NewSession is the input of SessionStore.InsertSession. Only the token hash reaches the store.
type NewSession struct {
	TokenHash []byte
	UserID    string
	Lifetime  time.Duration
	IP        netip.Addr // zero value: unknown
	UserAgent string
}

// NewAppSession is the input of AppSessionStore.InsertAppSession.
type NewAppSession struct {
	TokenHash []byte
	SessionID string
	AppID     string
	Host      string
}

// NewLoginCode is the input of LoginCodeStore.InsertLoginCode.
type NewLoginCode struct {
	CodeHash  []byte
	SessionID string
	AppID     string
	Host      string
	BindHash  []byte
	RD        string
	TTL       time.Duration
}

// ConsumedCode is the row of a login code that was just burnt.
type ConsumedCode struct {
	LoginGrant
	BindHash []byte
}

// SessionStore persists central sessions.
type SessionStore interface {
	InsertSession(ctx context.Context, s NewSession) (Session, error)
	// ValidateSession returns the session if it is neither expired nor idle, touching last_seen_at at most
	// once per Policy.TouchInterval. Otherwise ErrInvalidSession.
	ValidateSession(ctx context.Context, tokenHash []byte, p Policy) (Session, error)
	// DeleteSession deletes the session with that token hash and returns it; the zero Revoked means none.
	DeleteSession(ctx context.Context, tokenHash []byte) (Revoked, error)
	DeleteUserSession(ctx context.Context, userID, sessionID string) (bool, error)
	DeleteUserSessions(ctx context.Context, userID string) (int64, error)
	ListActiveSessions(ctx context.Context, userID string, idle time.Duration) ([]Session, error)
}

// AppSessionStore persists per-app sessions.
type AppSessionStore interface {
	// InsertAppSession fails with ErrInvalidSession unless the central session is valid and host belongs to app.
	InsertAppSession(ctx context.Context, s NewAppSession, p Policy) (AppSession, error)
	// ValidateAppSession also refreshes the central session's idle clock (same throttling).
	ValidateAppSession(ctx context.Context, tokenHash []byte, host string, p Policy) (AppSession, error)
	DeleteAppSessions(ctx context.Context, appID string) (int64, error)
}

// LoginCodeStore persists one-time login codes.
type LoginCodeStore interface {
	// InsertLoginCode fails with ErrInvalidSession unless the central session is valid and host belongs to app.
	InsertLoginCode(ctx context.Context, c NewLoginCode, p Policy) error
	// ConsumeLoginCode atomically burns the code. Unknown, used or expired: ErrInvalidCode.
	ConsumeLoginCode(ctx context.Context, codeHash []byte) (ConsumedCode, error)
}

// HostRegistry answers whether a host is registered in deployments.
type HostRegistry interface {
	HostRegistered(ctx context.Context, host string) (bool, error)
}

// Config wires a Service. IdleTimeout defaults to DefaultIdleTimeout.
type Config struct {
	Sessions    SessionStore
	AppSessions AppSessionStore
	Codes       LoginCodeStore
	Hosts       HostRegistry
	IdleTimeout time.Duration
}

// Service applies the session rules over the stores.
type Service struct {
	sessions    SessionStore
	appSessions AppSessionStore
	codes       LoginCodeStore
	hosts       HostRegistry
	policy      Policy
}

// NewService validates cfg and applies defaults.
func NewService(cfg Config) (*Service, error) {
	if cfg.Sessions == nil || cfg.AppSessions == nil || cfg.Codes == nil || cfg.Hosts == nil {
		return nil, errors.New("session service: every store is required")
	}
	idle := cfg.IdleTimeout
	if idle == 0 {
		idle = DefaultIdleTimeout
	}
	if idle < MinIdleTimeout || idle > MaxIdleTimeout {
		return nil, fmt.Errorf("session service: idle timeout must be within [%s, %s]", MinIdleTimeout, MaxIdleTimeout)
	}
	return &Service{
		sessions: cfg.Sessions, appSessions: cfg.AppSessions, codes: cfg.Codes, hosts: cfg.Hosts,
		policy: Policy{IdleTimeout: idle, TouchInterval: TouchInterval},
	}, nil
}

// IdleTimeout is the configured idle timeout (also needed by the housekeeping job).
func (s *Service) IdleTimeout() time.Duration { return s.policy.IdleTimeout }

// tokenEncoding is canonical base64url: Strict rejects non-zero trailing bits, so one value has one encoding.
var tokenEncoding = base64.RawURLEncoding.Strict()

// NewToken returns a fresh 256-bit random value (what goes in a cookie or URL) and its SHA-256 (what is stored).
func NewToken() (value string, hash []byte, err error) {
	raw := make([]byte, TokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("random token: %w", err)
	}
	sum := sha256.Sum256(raw)
	return tokenEncoding.EncodeToString(raw), sum[:], nil
}

// HashToken returns the SHA-256 stored for a presented token value, or false if the value is malformed.
// Malformed values are rejected before any database access.
func HashToken(value string) ([]byte, bool) {
	if len(value) != tokenEncoding.EncodedLen(TokenBytes) {
		return nil, false
	}
	raw, err := tokenEncoding.DecodeString(value)
	if err != nil || len(raw) != TokenBytes {
		return nil, false
	}
	sum := sha256.Sum256(raw)
	return sum[:], true
}

// NewBind generates the value for the __Host-Http-platform_bind cookie of the app host and its hash,
// which is the only part that travels in the login flow and is stored with the login code.
func NewBind() (value string, hash []byte, err error) { return NewToken() }

// CreateSessionInput describes a successful login.
type CreateSessionInput struct {
	UserID    string
	IP        netip.Addr
	UserAgent string
}

// CreateSession starts a central session. It always generates a new token and a new id: there is no way
// to supply either, which rules out session fixation. The caller sets the cookie and should discard any
// session cookie the browser presented before the login.
func (s *Service) CreateSession(ctx context.Context, in CreateSessionInput) (token string, sess Session, err error) {
	token, hash, err := NewToken()
	if err != nil {
		return "", Session{}, err
	}
	sess, err = s.sessions.InsertSession(ctx, NewSession{
		TokenHash: hash, UserID: in.UserID, Lifetime: AbsoluteLifetime, IP: in.IP, UserAgent: cleanUserAgent(in.UserAgent),
	})
	if err != nil {
		return "", Session{}, fmt.Errorf("create session: %w", err)
	}
	return token, sess, nil
}

// ValidateSession authenticates a central session cookie value (absolute and idle limits, throttled touch).
func (s *Service) ValidateSession(ctx context.Context, token string) (Session, error) {
	hash, ok := HashToken(token)
	if !ok {
		return Session{}, ErrInvalidSession
	}
	return s.sessions.ValidateSession(ctx, hash, s.policy)
}

// RevokeSession deletes the central session (and, by cascade, its app sessions and login codes) and
// returns which one it was; the zero Revoked means the token was malformed or unknown.
func (s *Service) RevokeSession(ctx context.Context, token string) (Revoked, error) {
	hash, ok := HashToken(token)
	if !ok {
		return Revoked{}, nil
	}
	rev, err := s.sessions.DeleteSession(ctx, hash)
	if err != nil {
		return Revoked{}, fmt.Errorf("revoke session: %w", err)
	}
	return rev, nil
}

// RevokeUserSession closes one session of a user by its public id; it reports whether one was deleted.
func (s *Service) RevokeUserSession(ctx context.Context, userID, sessionID string) (bool, error) {
	ok, err := s.sessions.DeleteUserSession(ctx, userID, sessionID)
	if err != nil {
		return false, fmt.Errorf("revoke user session: %w", err)
	}
	return ok, nil
}

// RevokeUser deletes every session of a user (cascading to app sessions) and returns how many centrals were removed.
func (s *Service) RevokeUser(ctx context.Context, userID string) (int64, error) {
	n, err := s.sessions.DeleteUserSessions(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("revoke user: %w", err)
	}
	return n, nil
}

// ListActive returns the user's sessions that are neither expired nor idle, newest first.
func (s *Service) ListActive(ctx context.Context, userID string) ([]Session, error) {
	out, err := s.sessions.ListActiveSessions(ctx, userID, s.policy.IdleTimeout)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return out, nil
}

// CreateAppSession opens a session on an app host tied to a central session. Its expiry equals the central
// session's absolute expiry, so it can never outlive it.
func (s *Service) CreateAppSession(ctx context.Context, sessionID, appID, host string) (token string, sess AppSession, err error) {
	token, hash, err := NewToken()
	if err != nil {
		return "", AppSession{}, err
	}
	sess, err = s.appSessions.InsertAppSession(ctx, NewAppSession{TokenHash: hash, SessionID: sessionID, AppID: appID, Host: host}, s.policy)
	if err != nil {
		return "", AppSession{}, fmt.Errorf("create app session: %w", err)
	}
	return token, sess, nil
}

// ValidateAppSession authenticates an app cookie value for the request host and refreshes the central idle clock.
func (s *Service) ValidateAppSession(ctx context.Context, token, host string) (AppSession, error) {
	hash, ok := HashToken(token)
	if !ok {
		return AppSession{}, ErrInvalidSession
	}
	return s.appSessions.ValidateAppSession(ctx, hash, host, s.policy)
}

// RevokeApp deletes every app session of an app and returns how many were removed.
func (s *Service) RevokeApp(ctx context.Context, appID string) (int64, error) {
	n, err := s.appSessions.DeleteAppSessions(ctx, appID)
	if err != nil {
		return 0, fmt.Errorf("revoke app sessions: %w", err)
	}
	return n, nil
}

// LoginCodeInput describes the code to issue after a successful login or when an existing session visits an app.
type LoginCodeInput struct {
	SessionID string
	AppID     string
	Host      string // destination app host (lowercase); must be the host of RD and belong to AppID
	RD        string // raw redirect target, validated here
	BindHash  []byte // SHA-256 of the app host's bind cookie value, as received in the login URL
}

// CreateLoginCode issues a single-use code valid for LoginCodeTTL, bound to the session, app, host, validated
// rd and bind hash. It returns the code value (it appears only in the callback URL; just its hash is stored).
func (s *Service) CreateLoginCode(ctx context.Context, in LoginCodeInput) (string, error) {
	if len(in.BindHash) != sha256.Size {
		return "", errors.New("create login code: bind hash must be a SHA-256")
	}
	rd, err := s.ValidateRD(ctx, in.RD)
	if err != nil {
		return "", err
	}
	if rd.Host != in.Host {
		return "", fmt.Errorf("%w: host differs from the code's destination", ErrInvalidRD)
	}
	code, hash, err := NewToken()
	if err != nil {
		return "", err
	}
	err = s.codes.InsertLoginCode(ctx, NewLoginCode{
		CodeHash: hash, SessionID: in.SessionID, AppID: in.AppID, Host: in.Host, BindHash: in.BindHash, RD: rd.URL, TTL: LoginCodeTTL,
	}, s.policy)
	if err != nil {
		return "", fmt.Errorf("create login code: %w", err)
	}
	return code, nil
}

// ConsumeLoginCode redeems a code on the app host that received it. The code is burnt atomically before
// host and bind are verified (in constant time), so a failed attempt still consumes it. Every failure is
// ErrInvalidCode. bind is the raw value of the __Host-Http-platform_bind cookie.
func (s *Service) ConsumeLoginCode(ctx context.Context, code, host, bind string) (LoginGrant, error) {
	codeHash, ok := HashToken(code)
	if !ok {
		return LoginGrant{}, ErrInvalidCode
	}
	row, err := s.codes.ConsumeLoginCode(ctx, codeHash)
	if err != nil {
		return LoginGrant{}, err
	}
	bindHash, bindOK := HashToken(bind)
	hostOK := subtle.ConstantTimeCompare([]byte(host), []byte(row.Host)) == 1
	bindMatch := bindOK && subtle.ConstantTimeCompare(bindHash, row.BindHash) == 1
	if !hostOK || !bindMatch {
		return LoginGrant{}, ErrInvalidCode
	}
	return row.LoginGrant, nil
}

// cleanUserAgent makes a header value storable (valid UTF-8, no NUL) and bounds it to MaxUserAgentLength bytes.
func cleanUserAgent(s string) string {
	s = strings.ReplaceAll(strings.ToValidUTF8(s, "\uFFFD"), "\x00", "")
	return truncate(s, MaxUserAgentLength)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && s[cut]&0xC0 == 0x80 { // do not split a UTF-8 sequence
		cut--
	}
	return s[:cut]
}
