// Package platformauth validates the Platform-Assertion JWT injected by the platform (contract block 1).
// It is the only place apps should implement that validation.
package platformauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
	// JWKSURL is the fixed in-cluster JWKS URL; jku/x5u in tokens are ignored. It normally travels over
	// plain http inside the cluster: its integrity depends on the NetworkPolicy allowing apps to reach
	// only platform-api. Redirects are never followed.
	JWKSURL string
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
		Issuer:    wc.Issuer,
		Audience:  wc.Audience[0],
		Subject:   wc.Subject,
		Email:     wc.Email,
		Name:      wc.Name,
		SessionID: wc.SID,
		TokenID:   wc.ID,
		Roles:     wc.Roles,
		IssuedAt:  numericDate(wc.IssuedAt),
		NotBefore: numericDate(wc.NotBefore),
		ExpiresAt: numericDate(wc.ExpiresAt),
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
