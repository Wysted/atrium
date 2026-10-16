// Package jwks serves the public signing keys at /.well-known/jwks.json.
package jwks

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"platform/api/internal/httpx"
	"platform/api/internal/keys"
)

const (
	// Path is the JWKS endpoint; Route is its ServeMux pattern.
	Path  = "/.well-known/jwks.json"
	Route = http.MethodGet + " " + Path

	// CacheMaxAge is the max-age apps may cache the set for; it bounds how long a revoked key is still accepted.
	CacheMaxAge = 5 * time.Minute

	contentType = "application/jwk-set+json"
)

// Source provides the published public keys.
type Source interface {
	PublicKeys() []keys.PublicKey
}

type document struct {
	Keys []keys.JWK `json:"keys"`
}

// Handler serves the key set: next, active and retired keys, public members only.
// An empty set is a platform failure (apps would cache "no keys"), so it answers 503 and is not cacheable.
func Handler(src Source) http.Handler {
	cacheControl := fmt.Sprintf("public, max-age=%d", int(CacheMaxAge/time.Second))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		published := src.PublicKeys()
		if len(published) == 0 {
			w.Header().Set(httpx.HeaderCacheControl, httpx.CacheNoStore)
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		doc := document{Keys: make([]keys.JWK, 0, len(published))}
		for _, k := range published {
			jwk := k.JWK
			jwk.Kid = k.KID
			doc.Keys = append(doc.Keys, jwk)
		}
		body, err := json.Marshal(doc)
		if err != nil {
			slog.Error("marshal jwks", "error", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		w.Header().Set(httpx.HeaderContentType, contentType)
		w.Header().Set(httpx.HeaderCacheControl, cacheControl)
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
	})
}
