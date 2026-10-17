// Package assertion issues the Platform-Assertion JWT defined in docs/reference/assertion.md.
package assertion

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Wysted/atrium/platform/api/internal/keys"
)

const (
	// HeaderType is the explicit JOSE typ (RFC 8725 section 3.11).
	HeaderType = "platform-assertion+jwt"
	// TTL is the lifetime of an assertion; it is re-issued on every request.
	TTL = 90 * time.Second
	// MaxRoles caps the roles claim so the assertion stays small enough for header limits.
	MaxRoles = 32
	// MaxSize caps the serialized assertion (header, payload and signature, base64url). It travels in a
	// request header and proxies commonly limit those to 8 KiB. The DB caps on email, name and role keys
	// bound LENGTH only: ordinary text, even 4-byte runes at the caps, fits (see the worst-case test), but
	// email/name values full of <, > or & expand to 6 bytes each under JSON escaping and can exceed it.
	// That is not a provable bound; the outcome in that case is ErrTooLarge from Issue (fail closed,
	// nothing is truncated).
	MaxSize = 4096
	// jtiBytes is the entropy of the token id (128 bits).
	jtiBytes = 16
)

// ErrTooManyRoles is returned when a request carries more than MaxRoles roles; roles are never truncated.
var ErrTooManyRoles = fmt.Errorf("assertion: more than %d roles", MaxRoles)

// ErrTooLarge is returned when the serialized assertion would exceed MaxSize.
var ErrTooLarge = fmt.Errorf("assertion: larger than %d bytes", MaxSize)

// SignerSource provides the active signing key.
type SignerSource interface {
	Signer() (keys.Signer, error)
}

// Values of the token claims (docs/reference/assertion.md, "Claims added for API tokens and robots").
const (
	SubjectHuman = "human"
	SubjectRobot = "robot"
	ScopeRead    = "read"
	ScopeAdmin   = "admin"
)

// Request is the data of one assertion. It is either a session assertion (SessionID) or a token assertion
// (APITokenID, SubjectType and Scope, no SessionID); never both, never neither.
type Request struct {
	Audience  string // opaque app id (app_<ulid>)
	Subject   string // internal user UUID
	Email     string // "" for robots
	Name      string
	SessionID string   // public sid; empty exactly when APITokenID is set
	Roles     []string // role keys of the initiative owning Audience
	// Token fields: tid, sub_type and scope claims.
	APITokenID  string
	SubjectType string // SubjectHuman or SubjectRobot
	Scope       string // ScopeRead or ScopeAdmin
}

// validate checks the shape of the request; it reports a programming error, never user input.
func (r Request) validate() error {
	if r.Audience == "" || r.Subject == "" {
		return errors.New("assertion: audience and subject are required")
	}
	if (r.SessionID == "") == (r.APITokenID == "") {
		return errors.New("assertion: exactly one of session id and api token id is required")
	}
	if r.APITokenID == "" {
		if r.SubjectType != "" || r.Scope != "" {
			return errors.New("assertion: subject type and scope are only for api token assertions")
		}
		return nil
	}
	if len(r.Roles) > 0 {
		return errors.New("assertion: api token assertions carry no roles")
	}
	if r.SubjectType != SubjectHuman && r.SubjectType != SubjectRobot || r.Scope != ScopeRead && r.Scope != ScopeAdmin {
		return errors.New("assertion: api token assertion needs a valid subject type and scope")
	}
	return nil
}

// Issued is a signed assertion plus the metadata callers log.
type Issued struct {
	Token     string
	JTI       string
	ExpiresAt time.Time
}

// Config wires an Issuer. Now and Random are optional.
type Config struct {
	Issuer  string // exact iss value, https://auth.apps.<domain>
	Signers SignerSource
	Now     func() time.Time
	Random  io.Reader
}

// Issuer signs assertions with the active key.
type Issuer struct {
	issuer  string
	signers SignerSource
	now     func() time.Time
	random  io.Reader
}

// NewIssuer validates cfg and applies defaults.
func NewIssuer(cfg Config) (*Issuer, error) {
	if cfg.Issuer == "" || cfg.Signers == nil {
		return nil, errors.New("assertion: issuer and signers are required")
	}
	i := &Issuer{issuer: cfg.Issuer, signers: cfg.Signers, now: cfg.Now, random: cfg.Random}
	if i.now == nil {
		i.now = time.Now
	}
	if i.random == nil {
		i.random = rand.Reader
	}
	return i, nil
}

// Issue signs an assertion for req.
func (i *Issuer) Issue(req Request) (Issued, error) {
	if err := req.validate(); err != nil {
		return Issued{}, err
	}
	if len(req.Roles) > MaxRoles {
		return Issued{}, ErrTooManyRoles
	}
	signer, err := i.signers.Signer()
	if err != nil {
		return Issued{}, fmt.Errorf("assertion: %w", err)
	}
	jti, err := i.newJTI()
	if err != nil {
		return Issued{}, err
	}

	iat := i.now().UTC().Truncate(time.Second)
	exp := iat.Add(TTL)
	roles := req.Roles
	if roles == nil {
		roles = []string{} // always an array, never null
	}
	claims := jwt.MapClaims{
		"iss":   i.issuer,
		"aud":   req.Audience,
		"sub":   req.Subject,
		"email": req.Email,
		"name":  req.Name,
		"iat":   iat.Unix(),
		"nbf":   iat.Unix(),
		"exp":   exp.Unix(),
		"jti":   jti,
		"roles": roles,
	}
	if req.APITokenID != "" {
		claims["tid"], claims["sub_type"], claims["scope"] = req.APITokenID, req.SubjectType, req.Scope
	} else {
		claims["sid"] = req.SessionID
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = signer.KID
	token.Header["typ"] = HeaderType
	signed, err := token.SignedString(signer.Key)
	if err != nil {
		return Issued{}, fmt.Errorf("assertion: sign: %w", err)
	}
	if len(signed) > MaxSize {
		return Issued{}, ErrTooLarge
	}
	return Issued{Token: signed, JTI: jti, ExpiresAt: exp}, nil
}

func (i *Issuer) newJTI() (string, error) {
	b := make([]byte, jtiBytes)
	if _, err := io.ReadFull(i.random, b); err != nil {
		return "", fmt.Errorf("assertion: random jti: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
