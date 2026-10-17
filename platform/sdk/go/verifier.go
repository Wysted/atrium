// Package platformauth validates the Platform-Assertion JWT injected by the platform (contract block 1).
// It is the only place apps should implement that validation.
package platformauth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// HeaderName is the request header carrying the assertion.
	HeaderName = "Platform-Assertion"

	signingAlgorithm = "ES256"
	assertionType    = "platform-assertion+jwt"

	// Defaults for Config zero values.
	DefaultLeeway             = 30 * time.Second
	DefaultMinRefetchInterval = time.Minute
	DefaultHealthPath         = "/healthz"
	defaultHTTPTimeout        = 5 * time.Second

	// Transport settings used when RootCAs needs a transport of its own (same values as net/http's default).
	minTLSVersion         = tls.VersionTLS13
	dialTimeout           = 30 * time.Second
	dialKeepAlive         = 30 * time.Second
	maxIdleConns          = 100
	idleConnTimeout       = 90 * time.Second
	tlsHandshakeTimeout   = 10 * time.Second
	expectContinueTimeout = time.Second

	// maxTokenSize bounds what we parse; real assertions are well under 4 KiB.
	maxTokenSize  = 8 << 10
	tokenSegments = 3

	// AssertionTTL is the lifetime the platform gives an assertion (contract: exp = iat + 90 s).
	// A token whose exp - iat exceeds it (plus leeway) was not issued by the platform.
	AssertionTTL = 90 * time.Second
)

// ErrInvalidAssertion wraps every rejection caused by the token itself.
var ErrInvalidAssertion = errors.New("invalid platform assertion")

// Config configures a Verifier.
type Config struct {
	Issuer   string // expected iss, exact match
	Audience string // this app's id (aud), exact match
	// JWKSURL is the fixed in-cluster JWKS URL; jku/x5u in tokens are ignored. Prefer https: over plain
	// http the integrity of the keys depends on the NetworkPolicy allowing apps to reach only platform-api.
	// Redirects are never followed.
	JWKSURL string
	// RootCAs is the optional trust store for an https JWKSURL whose certificate is signed by a private CA
	// (see LoadRootCAs). Nil means the system roots. It requires the HTTPClient, if any, to have a nil or
	// *http.Transport transport without DialTLS/DialTLSContext (NewVerifier returns an error otherwise, since
	// RootCAs would be silently ignored). The transport used enforces TLS 1.3 as the minimum version.
	RootCAs *x509.CertPool
	// HTTPClient is optional. It is copied, and the copy never follows redirects whatever the original
	// CheckRedirect says; a 3xx answer counts as a JWKS failure.
	HTTPClient *http.Client

	Leeway             time.Duration // clock skew for exp/nbf/iat; default DefaultLeeway
	MinRefetchInterval time.Duration // minimum spacing of JWKS fetches; default DefaultMinRefetchInterval
	HealthPath         string        // exempt from validation; default DefaultHealthPath

	Now func() time.Time // injectable clock; default time.Now
}

// Verifier validates assertions against the platform JWKS.
type Verifier struct {
	issuer, audience string
	healthPath       string
	leeway           time.Duration
	parser           *jwt.Parser
	keys             *keyCache
}

// wireClaims mirrors the assertion payload.
type wireClaims struct {
	jwt.RegisteredClaims
	Email string   `json:"email"`
	Name  string   `json:"name"`
	SID   string   `json:"sid"`
	Roles []string `json:"roles"`
	// Present only on assertions issued for an API token (contract block 1).
	SubType string `json:"sub_type"`
	TID     string `json:"tid"`
	Scope   string `json:"scope"`
}

// NewVerifier validates cfg and applies defaults.
func NewVerifier(cfg Config) (*Verifier, error) {
	if cfg.Issuer == "" || cfg.Audience == "" {
		return nil, errors.New("platformauth: issuer and audience are required")
	}
	u, err := url.Parse(cfg.JWKSURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("platformauth: JWKSURL must be an absolute http(s) URL")
	}
	if cfg.Leeway <= 0 {
		cfg.Leeway = DefaultLeeway
	}
	if cfg.MinRefetchInterval <= 0 {
		cfg.MinRefetchInterval = DefaultMinRefetchInterval
	}
	if cfg.HealthPath == "" {
		cfg.HealthPath = DefaultHealthPath
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	client := &http.Client{Timeout: defaultHTTPTimeout}
	if cfg.HTTPClient != nil {
		clone := *cfg.HTTPClient
		client = &clone
	}
	if cfg.RootCAs != nil {
		if client.Transport, err = transportWithRoots(client.Transport, cfg.RootCAs); err != nil {
			return nil, err
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse // the JWKS URL is fixed; never follow redirects
	}
	return &Verifier{
		issuer:     cfg.Issuer,
		audience:   cfg.Audience,
		healthPath: cfg.HealthPath,
		leeway:     cfg.Leeway,
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{signingAlgorithm}),
			jwt.WithLeeway(cfg.Leeway),
			jwt.WithTimeFunc(cfg.Now),
			jwt.WithIssuer(cfg.Issuer),
			jwt.WithAudience(cfg.Audience),
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
		),
		keys: &keyCache{url: cfg.JWKSURL, client: client, now: cfg.Now, minRefetch: cfg.MinRefetchInterval, fetchTimeout: defaultFetchTimeout},
	}, nil
}

// transportWithRoots returns a copy of base trusting only roots. A nil base gets a fresh transport built here
// (not a clone of http.DefaultTransport, which anything in the process may have replaced). TLS 1.3 is the
// minimum, unless the copied TLSClientConfig already demands more.
func transportWithRoots(base http.RoundTripper, roots *x509.CertPool) (http.RoundTripper, error) {
	var t *http.Transport
	switch b := base.(type) {
	case nil:
		t = newBaseTransport()
	case *http.Transport:
		t = b.Clone()
	default:
		return nil, errors.New("platformauth: RootCAs needs an HTTPClient whose Transport is nil or *http.Transport")
	}
	if t.DialTLS != nil || t.DialTLSContext != nil { //nolint:staticcheck // SA1019: DialTLS is checked precisely because it would bypass RootCAs
		return nil, errors.New("platformauth: RootCAs cannot be combined with a Transport that sets DialTLS or DialTLSContext")
	}
	if t.TLSClientConfig == nil {
		t.TLSClientConfig = &tls.Config{MinVersion: minTLSVersion}
	}
	if t.TLSClientConfig.MinVersion < minTLSVersion {
		t.TLSClientConfig.MinVersion = minTLSVersion
	}
	t.TLSClientConfig.RootCAs = roots
	return t, nil
}

// newBaseTransport mirrors the net/http default transport settings.
func newBaseTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: dialTimeout, KeepAlive: dialKeepAlive}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          maxIdleConns,
		IdleConnTimeout:       idleConnTimeout,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ExpectContinueTimeout: expectContinueTimeout,
	}
}

// LoadRootCAs reads PEM certificates from path into a pool for Config.RootCAs. A file without a valid
// certificate is an error, so a wrong path or an empty mount fails at start-up.
func LoadRootCAs(path string) (*x509.CertPool, error) {
	pemBytes, err := os.ReadFile(path) //nolint:gosec // G304: operator-configured CA bundle location
	if err != nil {
		return nil, fmt.Errorf("platformauth: read CA file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, errors.New("platformauth: CA file has no valid PEM certificate")
	}
	return pool, nil
}

// Verify validates token and returns its claims. Rejections caused by the token wrap ErrInvalidAssertion;
// a JWKS outage wraps ErrKeysUnavailable. The token is never included in errors.
func (v *Verifier) Verify(ctx context.Context, token string) (*Claims, error) {
	kid, err := checkHeader(token)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidAssertion, err)
	}

	var wc wireClaims
	_, err = v.parser.ParseWithClaims(token, &wc, func(t *jwt.Token) (any, error) {
		return v.keys.lookup(ctx, kid)
	})
	if err != nil {
		if errors.Is(err, ErrKeysUnavailable) {
			return nil, fmt.Errorf("verify assertion: %w", ErrKeysUnavailable)
		}
		return nil, fmt.Errorf("%w: %w", ErrInvalidAssertion, err)
	}

	// The parser accepts any matching entry of an aud array; the contract requires exactly our app id.
	if len(wc.Audience) != 1 || wc.Audience[0] != v.audience {
		return nil, fmt.Errorf("%w: audience mismatch", ErrInvalidAssertion)
	}
	if wc.Subject == "" {
		return nil, fmt.Errorf("%w: missing sub", ErrInvalidAssertion)
	}
	// The platform always sets iat and exp = iat + AssertionTTL; anything longer-lived is foreign.
	if wc.IssuedAt == nil {
		return nil, fmt.Errorf("%w: missing iat", ErrInvalidAssertion)
	}
	if wc.ExpiresAt.Sub(wc.IssuedAt.Time) > AssertionTTL+v.leeway {
		return nil, fmt.Errorf("%w: lifetime exceeds %s", ErrInvalidAssertion, AssertionTTL)
	}
	claims := &Claims{
		Issuer:      wc.Issuer,
		Audience:    wc.Audience[0],
		Subject:     wc.Subject,
		Email:       wc.Email,
		Name:        wc.Name,
		SessionID:   wc.SID,
		TokenID:     wc.ID,
		Roles:       wc.Roles,
		SubjectType: wc.SubType,
		APITokenID:  wc.TID,
		Scope:       wc.Scope,
		IssuedAt:    numericDate(wc.IssuedAt),
		NotBefore:   numericDate(wc.NotBefore),
		ExpiresAt:   numericDate(wc.ExpiresAt),
	}
	return claims, nil
}

func numericDate(d *jwt.NumericDate) time.Time {
	if d == nil {
		return time.Time{}
	}
	return d.Time
}

// checkHeader enforces alg, typ and kid before any key lookup, so malformed tokens never cost a JWKS fetch.
func checkHeader(token string) (kid string, err error) {
	if token == "" || len(token) > maxTokenSize {
		return "", errors.New("token missing or too large")
	}
	parts := strings.Split(token, ".")
	if len(parts) != tokenSegments {
		return "", errors.New("malformed token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", errors.New("malformed header")
	}
	var header map[string]any
	if err := json.Unmarshal(raw, &header); err != nil {
		return "", errors.New("malformed header")
	}
	if _, ok := header["crit"]; ok {
		return "", errors.New("unsupported crit header")
	}
	if alg, _ := header["alg"].(string); alg != signingAlgorithm {
		return "", errors.New("unexpected alg")
	}
	if typ, _ := header["typ"].(string); typ != assertionType {
		return "", errors.New("unexpected typ")
	}
	kid, _ = header["kid"].(string)
	if kid == "" {
		return "", errors.New("missing kid")
	}
	return kid, nil
}
