package platformauth

import (
	"context"
	"time"
)

// Claims is the verified identity carried by a Platform-Assertion. Only Subject identifies the user;
// Email and Name are informational.
type Claims struct {
	Issuer    string
	Audience  string
	Subject   string // internal user UUID
	Email     string
	Name      string
	SessionID string
	TokenID   string // jti, for log correlation only
	Roles     []string
	IssuedAt  time.Time
	NotBefore time.Time
	ExpiresAt time.Time
}

type claimsKey struct{}

// ContextWithClaims returns a context carrying claims (used by the middleware and by tests).
func ContextWithClaims(ctx context.Context, c *Claims) context.Context {
	return context.WithValue(ctx, claimsKey{}, c)
}

// ClaimsFrom returns the verified claims stored by the middleware.
func ClaimsFrom(ctx context.Context) (*Claims, bool) {
	c, ok := ctx.Value(claimsKey{}).(*Claims)
	return c, ok && c != nil
}
