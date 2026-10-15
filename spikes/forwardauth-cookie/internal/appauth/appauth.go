// Package appauth validates X-Portal-Token on the app side.
package appauth

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"spike/forwardauth/internal/token"
)

type ctxKey struct{}

// Verifier validates portal tokens for a single app.
type Verifier struct {
	appID string
	keys  map[string]*ecdsa.PublicKey
	clock func() time.Time
}

// NewVerifier builds a verifier from a JWKS; keys that fail to decode are an error.
func NewVerifier(appID string, set token.Set, clock func() time.Time) (*Verifier, error) {
	keys := make(map[string]*ecdsa.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		pub, err := k.PublicKey()
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", k.Kid, err)
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, errors.New("empty jwks")
	}
	return &Verifier{appID: appID, keys: keys, clock: clock}, nil
}

// Verify checks signature (ES256 only), issuer, audience and expiry.
func (v *Verifier) Verify(raw string) (*token.Claims, error) {
	claims := &token.Claims{}
	_, err := jwt.ParseWithClaims(raw, claims, v.keyFunc,
		jwt.WithValidMethods([]string{token.Algorithm}),
		jwt.WithAudience(v.appID),
		jwt.WithIssuer(token.Issuer),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(v.clock),
	)
	if err != nil {
		return nil, fmt.Errorf("verify token: %w", err)
	}
	return claims, nil
}

func (v *Verifier) keyFunc(t *jwt.Token) (any, error) {
	kid, _ := t.Header["kid"].(string)
	key, ok := v.keys[kid]
	if !ok {
		return nil, errors.New("unknown kid")
	}
	return key, nil
}

// Middleware rejects requests without a valid token and stores claims in the context.
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get(token.HeaderName)
		if raw == "" {
			unauthorized(w)
			return
		}
		claims, err := v.Verify(raw)
		if err != nil {
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, claims)))
	})
}

// ClaimsFrom returns the claims stored by Middleware.
func ClaimsFrom(ctx context.Context) (*token.Claims, bool) {
	c, ok := ctx.Value(ctxKey{}).(*token.Claims)
	return c, ok
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
}
