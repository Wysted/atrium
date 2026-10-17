package main

import (
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	platformauth "github.com/Wysted/atrium/platform/sdk/go"
)

const (
	logoutPath = "/_platform/logout" // reserved platform path on every app host
	whoamiPath = "/api/whoami"

	contentSecurityPolicy = "default-src 'none'; frame-ancestors 'none'; base-uri 'none'"
)

// identity is the view of the verified claims exposed by the page and the JSON endpoint.
type identity struct {
	AppID     string    `json:"app_id"`
	Subject   string    `json:"sub"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Roles     []string  `json:"roles"`
	SessionID string    `json:"sid"`
	ExpiresAt time.Time `json:"exp"`
}

func identityFrom(c *platformauth.Claims) identity {
	roles := c.Roles
	if roles == nil {
		roles = []string{} // JSON [] instead of null
	}
	return identity{
		AppID:     c.Audience,
		Subject:   c.Subject,
		Email:     c.Email,
		Name:      c.Name,
		Roles:     roles,
		SessionID: c.SessionID,
		ExpiresAt: c.ExpiresAt,
	}
}

// html/template escapes every value by context.
var pageTemplate = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Hello</title>
</head>
<body>
<h1>Hello, {{.Name}}</h1>
<dl>
<dt>App id</dt><dd>{{.AppID}}</dd>
<dt>Subject</dt><dd>{{.Subject}}</dd>
<dt>Email</dt><dd>{{.Email}}</dd>
<dt>Name</dt><dd>{{.Name}}</dd>
<dt>Roles</dt><dd>{{range $i, $r := .Roles}}{{if $i}}, {{end}}{{$r}}{{else}}none{{end}}</dd>
<dt>Session id</dt><dd>{{.SessionID}}</dd>
</dl>
<form method="post" action="{{.LogoutPath}}">
<button type="submit">Sign out</button>
</form>
</body>
</html>
`))

type pageData struct {
	identity
	LogoutPath string
}

// newHandler routes the app. The caller wraps it with the SDK middleware, which puts the claims in the context.
func newHandler(log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", page(log))
	mux.HandleFunc("GET "+whoamiPath, whoami(log))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK) // normally answered without validation; the SDK exempts this path
	})
	return mux
}

func page(log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := platformauth.ClaimsFrom(r.Context())
		if !ok {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		if err := pageTemplate.Execute(w, pageData{identity: identityFrom(claims), LogoutPath: logoutPath}); err != nil {
			log.Error("render page", "error", err.Error())
		}
	}
}

func whoami(log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := platformauth.ClaimsFrom(r.Context())
		if !ok {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "application/json")
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		if err := json.NewEncoder(w).Encode(identityFrom(claims)); err != nil {
			log.Error("encode whoami", "error", err.Error())
		}
	}
}
