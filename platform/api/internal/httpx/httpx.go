// Package httpx holds the HTTP helpers shared by the internal and public listeners: RFC 9457 problems,
// host-only cookies, page security headers, the same-origin (CSRF) rule, client IP and panic recovery.
package httpx

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// Header names and values used across handlers.
const (
	HeaderCacheControl   = "Cache-Control"
	HeaderContentType    = "Content-Type"
	HeaderLocation       = "Location"
	HeaderOrigin         = "Origin"
	HeaderReferrerPolicy = "Referrer-Policy"
	HeaderSecFetchSite   = "Sec-Fetch-Site"
	HeaderForwardedFor   = "X-Forwarded-For"

	CacheNoStore     = "no-store"
	ReferrerNone     = "no-referrer"
	SecFetchSameSite = "same-origin"

	contentTypeProblem = "application/problem+json"
	contentTypeHTML    = "text/html; charset=utf-8"

	// pageCSP is restrictive on purpose: the pages carry no script, style or subresource. form-action is
	// left out on purpose: Chrome also checks the redirect that answers a form POST against it, and both the
	// login form (to an app host) and the app-host Sign out form (303 to the auth host) end in a cross-origin
	// redirect, so 'self' would block them.
	pageCSP = "default-src 'none'; base-uri 'none'; frame-ancestors 'none'"
)

// Problem is an RFC 9457 problem document with the extra login_url member of the ForwardAuth contract.
type Problem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	LoginURL string `json:"login_url,omitempty"`
	// Errors lists the invalid request fields of a validation problem (422). Codes are fixed texts; the input
	// itself is never echoed.
	Errors []FieldError `json:"errors,omitempty"`
}

// FieldError is one invalid request field of a validation problem.
type FieldError struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}

// WriteProblem writes p as application/problem+json with p.Status.
func WriteProblem(w http.ResponseWriter, p Problem) {
	if p.Type == "" {
		p.Type = "about:blank"
	}
	if p.Title == "" {
		p.Title = http.StatusText(p.Status)
	}
	body, err := json.Marshal(p)
	if err != nil { // cannot happen for this fixed shape
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set(HeaderContentType, contentTypeProblem)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(p.Status)
	_, _ = w.Write(body)
}

// SetPageHeaders sets the security headers every HTML page and auth-host redirect carries.
func SetPageHeaders(h http.Header) {
	h.Set(HeaderCacheControl, CacheNoStore)
	h.Set("Content-Security-Policy", pageCSP)
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set(HeaderReferrerPolicy, ReferrerNone)
}

// WithPageHeaders applies SetPageHeaders before next runs.
func WithPageHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		SetPageHeaders(w.Header())
		next.ServeHTTP(w, r)
	})
}

const errorPage = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>%s</title></head><body><h1>%s</h1><p>%s</p></body></html>`

// WriteHTML writes a complete HTML document that the caller guarantees to be safe.
func WriteHTML(w http.ResponseWriter, status int, doc string) {
	w.Header().Set(HeaderContentType, contentTypeHTML)
	w.WriteHeader(status)
	_, _ = w.Write([]byte(doc))
}

// WritePage writes a minimal HTML page; title and message are escaped.
func WritePage(w http.ResponseWriter, status int, title, message string) {
	t, m := html.EscapeString(title), html.EscapeString(message)
	WriteHTML(w, status, fmt.Sprintf(errorPage, t, t, m))
}

// SetCookie sets a platform cookie: Secure, HttpOnly, SameSite=Lax, Path=/, no Domain (contract block 5).
// A ttl of zero or less makes it a session cookie.
func SetCookie(w http.ResponseWriter, name, value string, ttl time.Duration) {
	c := &http.Cookie{Name: name, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode}
	if ttl > 0 {
		c.MaxAge = int(math.Ceil(ttl.Seconds()))
	}
	http.SetCookie(w, c)
}

// ClearCookie deletes a platform cookie.
func ClearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: -1, Expires: time.Unix(0, 0),
	})
}

// CookieValue returns the named cookie of the request, or "" if absent.
func CookieValue(h http.Header, name string) string {
	c, err := (&http.Request{Header: h}).Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

// SameOrigin is the CSRF rule for state-changing requests: Origin equals https://<host>, or the browser
// states Sec-Fetch-Site: same-origin. Requests carrying neither are refused.
func SameOrigin(h http.Header, host string) bool {
	if origin := h.Get(HeaderOrigin); origin != "" && origin == "https://"+host {
		return true
	}
	return h.Get(HeaderSecFetchSite) == SecFetchSameSite
}

// ClientIP returns the client address. Behind Traefik the last X-Forwarded-For entry is the one the proxy
// itself appended (client-sent prefixes sit before it); without the header, the peer address is used.
// This is only meaningful while the listener is reachable solely through the proxy (NetworkPolicy).
func ClientIP(r *http.Request) netip.Addr {
	if xff := strings.Join(r.Header.Values(HeaderForwardedFor), ","); xff != "" {
		parts := strings.Split(xff, ",")
		if a, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1])); err == nil {
			return a.Unmap()
		}
	}
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr().Unmap()
	}
	return netip.Addr{}
}

// Recover turns a handler panic into a 503 (fail closed) and logs it without request data.
func Recover(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler { //nolint:errorlint // net/http only silences the exact sentinel value
				panic(rec)
			}
			log.Error("handler panic", "panic", fmt.Sprint(rec), "path", r.URL.Path)
			w.Header().Set(HeaderCacheControl, CacheNoStore)
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		}()
		next.ServeHTTP(w, r)
	})
}

// HTTP server limits shared by every process. Requests are tiny (headers, cookies and small JSON bodies), so
// these are tight.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 15 * time.Second
	idleTimeout       = 60 * time.Second
	maxHeaderBytes    = 32 << 10 // Traefik forwards the whole Cookie header
)

// NewServer returns an http.Server for addr with the platform's timeouts and header limit.
func NewServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
}

// RequestTimeout is the per-request deadline of the ForwardAuth and auth-host handlers. It stays well under
// the server WriteTimeout so a hung dependency ends in an explicit 503 instead of a dropped connection.
const RequestTimeout = 5 * time.Second

// WithDeadline gives every request a context that expires after d (RequestTimeout when d <= 0), so database
// calls made with r.Context() fail fast and the handler can answer 503.
func WithDeadline(d time.Duration, next http.Handler) http.Handler {
	if d <= 0 {
		d = RequestTimeout
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
