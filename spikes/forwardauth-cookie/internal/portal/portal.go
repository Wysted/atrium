// Package portal implements the authz service: ForwardAuth decisions, fake login,
// per-app __Host- cookies issued through one-time codes, and ES256 token minting.
package portal

import (
	"crypto/ecdsa"
	"io"
	"time"
)

const (
	// SessionCookie is the central session cookie, scoped to the auth host only.
	SessionCookie = "__Host-portal_session"
	// AppCookie is the per-app opaque session cookie (one per app host).
	AppCookie = "__Host-portal_app"
	// CallbackPath is served on each app host (through ForwardAuth) to plant the app cookie.
	CallbackPath = "/_portal/callback"

	TokenTTL      = 90 * time.Second
	CodeTTL       = 30 * time.Second
	SessionTTL    = 8 * time.Hour
	AppSessionTTL = 8 * time.Hour

	secretBytes = 32
	apiPrefix   = "/api/"
)

// AppID is the stable identifier of an app (the JWT "aud").
type AppID string

// User is the authorization record of a person. ID is the internal UUID (JWT "sub").
type User struct {
	ID    string
	Roles []string
	Apps  map[AppID]bool
}

// Config holds everything the server needs; Clock and Rand are injectable for tests.
type Config struct {
	AuthHost string
	Apps     map[string]AppID // app host -> app id
	Users    map[string]User  // login name -> user
	Key      *ecdsa.PrivateKey
	Clock    func() time.Time
	Rand     io.Reader
}

// AccessRequest has the AuthZEN shape: subject, resource, action, context.
type AccessRequest struct {
	Subject  User
	Resource AppID
	Action   string
	Context  map[string]string
}

// Decide is a pure access decision. Spike policy: user must be granted the app.
func Decide(r AccessRequest) bool {
	return r.Subject.Apps[r.Resource]
}
