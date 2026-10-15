package platformauth

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

// Middleware validates the Platform-Assertion header, stores the claims in the request context
// (see ClaimsFrom) and answers application/problem+json otherwise: 401 when the assertion is missing or
// invalid, 503 when the signing keys cannot be obtained (a platform failure, not a bad token).
// The health path is exempt from validation. The assertion header is removed from every request handed
// to next, exempt or not, so handlers cannot forward it. The token is never logged.
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return v.MiddlewareWithLogger(slog.Default(), next)
}

// MiddlewareWithLogger is Middleware with an explicit logger for rejection reasons.
func (v *Verifier) MiddlewareWithLogger(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values(HeaderName)
		r2 := r.Clone(r.Context())
		r2.Header.Del(HeaderName)

		if r.URL.Path == v.healthPath {
			next.ServeHTTP(w, r2)
			return
		}
		if len(values) != 1 { // missing or ambiguous
			log.Warn("platform assertion rejected", "reason", "missing or repeated header")
			writeProblem(w, http.StatusUnauthorized, "missing or invalid platform assertion")
			return
		}
		claims, err := v.Verify(r.Context(), values[0])
		if err != nil {
			if errors.Is(err, ErrKeysUnavailable) {
				log.Error("platform signing keys unavailable", "reason", err.Error())
				writeProblem(w, http.StatusServiceUnavailable, "signing keys temporarily unavailable")
				return
			}
			log.Warn("platform assertion rejected", "reason", err.Error())
			writeProblem(w, http.StatusUnauthorized, "missing or invalid platform assertion")
			return
		}
		next.ServeHTTP(w, r2.WithContext(ContextWithClaims(r.Context(), claims)))
	})
}

func writeProblem(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{
		Type:   "about:blank",
		Title:  http.StatusText(status),
		Status: status,
		Detail: detail,
	})
}
