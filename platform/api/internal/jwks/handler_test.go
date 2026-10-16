package jwks_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"platform/api/internal/jwks"
	"platform/api/internal/keys"
)

type fakeSource []keys.PublicKey

func (f fakeSource) PublicKeys() []keys.PublicKey { return f }

func jwk(kid string) keys.JWK {
	return keys.JWK{Kty: "EC", Crv: "P-256", X: "x-" + kid, Y: "y-" + kid, Kid: kid, Use: "sig", Alg: "ES256"}
}

func newMux(src jwks.Source) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle(jwks.Route, jwks.Handler(src))
	return mux
}

func TestHandlerServesPublicSet(t *testing.T) {
	src := fakeSource{
		{KID: "a", State: keys.StateActive, JWK: jwk("a")},
		{KID: "n", State: keys.StateNext, JWK: jwk("n")},
		{KID: "r", State: keys.StateRetired, JWK: jwk("r")},
	}
	rec := httptest.NewRecorder()
	newMux(src).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, jwks.Path, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/jwk-set+json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=300" {
		t.Errorf("Cache-Control = %q", got)
	}

	var doc struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Keys) != 3 {
		t.Fatalf("keys = %d", len(doc.Keys))
	}
	allowed := map[string]bool{"kty": true, "crv": true, "x": true, "y": true, "kid": true, "use": true, "alg": true}
	for _, k := range doc.Keys {
		for member := range k {
			if !allowed[member] {
				t.Errorf("JWKS exposes member %q", member)
			}
		}
	}
}

func TestHandlerEmptySetIsUnavailableAndUncacheable(t *testing.T) {
	rec := httptest.NewRecorder()
	newMux(fakeSource{}).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, jwks.Path, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
}

func TestRouteMethods(t *testing.T) {
	src := fakeSource{{KID: "a", State: keys.StateActive, JWK: jwk("a")}}
	tests := []struct {
		method   string
		want     int
		wantBody bool
	}{
		{http.MethodGet, http.StatusOK, true},
		{http.MethodHead, http.StatusOK, false},
		{http.MethodPost, http.StatusMethodNotAllowed, false},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			rec := httptest.NewRecorder()
			newMux(src).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), tt.method, jwks.Path, strings.NewReader("")))
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
			if tt.method == http.MethodHead && rec.Body.Len() != 0 {
				t.Error("HEAD returned a body")
			}
		})
	}
}
