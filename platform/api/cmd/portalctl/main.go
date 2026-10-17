// Command portalctl grants and revokes initiative access through the portal API. It is a plain HTTP client:
// it authenticates with a personal or robot API token and talks only to the configured portal host.
//
// Usage:
//
//	portalctl grant  <subject> <initiative-key> <role-key>
//	portalctl revoke <subject> <initiative-key> <role-key>
//
// The subject is an email, or robot:<name>. The environment holds PORTAL_URL (https, no path) and
// PORTAL_API_TOKEN; the secret is never a flag.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"
)

// Exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

const (
	envURL   = "PORTAL_URL"
	envToken = "PORTAL_API_TOKEN" //nolint:gosec // G101: an environment variable name, not a credential

	requestTimeout   = 30 * time.Second
	maxResponseBytes = 1 << 20
	robotPrefix      = "robot:"

	usageText = "usage: portalctl grant|revoke <email|robot:name> <initiative-key> <role-key>\n" +
		"environment: " + envURL + " (https://host), " + envToken + "\n"
)

const (
	modeGrant  = "grant"
	modeRevoke = "revoke"

	robotDisabled          = "disabled"
	codeNoUniqueActiveUser = "no_unique_active_user"
)

// Request and response shapes of the portal API (only the fields the CLI uses).
type (
	initiative struct{ ID, Key string }
	role       struct{ ID, Key string }
	robot      struct{ ID, Name, Status string }
	grant      struct {
		ID   string `json:"id"`
		Role struct {
			ID string `json:"id"`
		} `json:"role"`
		User *struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"user"`
		Group     *struct{}  `json:"group"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	grantRequest struct {
		Email   string `json:"email,omitempty"`
		RobotID string `json:"robot_id,omitempty"`
		RoleID  string `json:"role_id"`
	}
)

// subject is who the grant is for: exactly one of email or robotName is set.
type subject struct {
	email     string
	robotName string
}

func (s subject) String() string {
	if s.robotName != "" {
		return robotPrefix + s.robotName
	}
	return s.email
}

// apiError is a non-2xx answer: the status plus the problem+json fields.
type apiError struct {
	Status int `json:"-"` // always the HTTP status: decisions never depend on the body
	Title  string
	Detail string
	Errors []struct{ Field, Code string }
}

func (e *apiError) hasField(field, code string) bool {
	for _, f := range e.Errors {
		if f.Field == field && f.Code == code {
			return true
		}
	}
	return false
}

func (e *apiError) Error() string {
	msg := fmt.Sprintf("%d %s", e.Status, http.StatusText(e.Status))
	if e.Title != "" && e.Title != http.StatusText(e.Status) {
		msg += ": " + e.Title
	}
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	for _, f := range e.Errors {
		msg += fmt.Sprintf(" (%s: %s)", f.Field, f.Code)
	}
	return msg
}

// client talks to one base URL, with one token, and never follows a redirect.
type client struct {
	base  string // scheme://host, no trailing slash
	token string
	http  *http.Client
}

func newClient(base *url.URL, token string, transport http.RoundTripper) *client {
	return &client{
		base:  "https://" + base.Host,
		token: token,
		http: &http.Client{
			Timeout:   requestTimeout,
			Transport: transport,
			// A redirect would carry the token elsewhere: the 3xx is returned as an error instead.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// do sends one request. A 2xx body is decoded into out (when not nil); anything else is an *apiError.
func (c *client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if method != http.MethodGet {
		// The portal answers 415 to any unsafe method without it, a bodiless DELETE included.
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// url.Error repeats the URL (no secret in it); the token only ever travels in the header.
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("%s %s: read response: %w", method, path, err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		e := &apiError{Status: resp.StatusCode}
		// A redirect or a non-JSON body leaves only the status: nothing from the answer is echoed blindly.
		if resp.StatusCode >= http.StatusBadRequest {
			_ = json.Unmarshal(data, e)
		}
		if resp.StatusCode >= http.StatusMultipleChoices && resp.StatusCode < http.StatusBadRequest {
			e.Detail = "unexpected redirect, refusing to follow"
		}
		return e
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s %s: decode response: %w", method, path, err)
	}
	return nil
}

// listAll pages through a collection: the response holds the items under key and an optional next_cursor.
func listAll[T any](ctx context.Context, c *client, path, key string) ([]T, error) {
	var all []T
	var cursor string
	for {
		q := url.Values{}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page map[string]json.RawMessage
		if err := c.do(ctx, http.MethodGet, path, q, nil, &page); err != nil {
			return nil, err
		}
		var items []T
		if raw, ok := page[key]; ok {
			if err := json.Unmarshal(raw, &items); err != nil {
				return nil, fmt.Errorf("GET %s: decode %s: %w", path, key, err)
			}
		}
		all = append(all, items...)
		cursor = ""
		if raw, ok := page["next_cursor"]; ok {
			if err := json.Unmarshal(raw, &cursor); err != nil {
				return nil, fmt.Errorf("GET %s: decode next_cursor: %w", path, err)
			}
		}
		if cursor == "" {
			return all, nil
		}
	}
}

func initiativePath(id string) string { return "/api/v1/initiatives/" + url.PathEscape(id) }

// target is what the keys and the subject resolve to.
type target struct {
	initiativeKey, roleKey string
	initiativeID, roleID   string
	robotID                string
	robotDisabled          bool
}

// resolve turns the keys and the subject into ids through the API; unknown ones are errors.
func resolve(ctx context.Context, c *client, sub subject, initiativeKey, roleKey string) (target, error) {
	t := target{initiativeKey: initiativeKey, roleKey: roleKey}
	// shortcut: the API has no filter by key, so this pages through every initiative the caller manages;
	// upgrade to a ?key= filter if the catalog ever grows past a few pages.
	inits, err := listAll[initiative](ctx, c, "/api/v1/initiatives", "initiatives")
	if err != nil {
		return target{}, fmt.Errorf("list initiatives: %w", err)
	}
	for _, i := range inits {
		if i.Key == initiativeKey {
			t.initiativeID = i.ID
			break
		}
	}
	if t.initiativeID == "" {
		return target{}, fmt.Errorf("unknown initiative %q (or you do not manage it)", initiativeKey)
	}
	roles, err := listAll[role](ctx, c, initiativePath(t.initiativeID)+"/roles", "roles")
	if err != nil {
		return target{}, fmt.Errorf("list roles: %w", err)
	}
	for _, r := range roles {
		if r.Key == roleKey {
			t.roleID = r.ID
			break
		}
	}
	if t.roleID == "" {
		return target{}, fmt.Errorf("unknown role %q in initiative %q", roleKey, initiativeKey)
	}
	if sub.robotName == "" {
		return t, nil
	}
	robots, err := listAll[robot](ctx, c, "/api/v1/robots", "robots")
	if err != nil {
		return target{}, fmt.Errorf("list robots: %w", err)
	}
	for _, r := range robots {
		if strings.EqualFold(r.Name, sub.robotName) { // names are unique case-insensitively
			t.robotID, t.robotDisabled = r.ID, r.Status == robotDisabled
			return t, nil
		}
	}
	return target{}, fmt.Errorf("unknown robot %q", sub.robotName)
}

// directGrants lists the initiative's direct grants of the subject with the role. Group grants never match.
// Several users may share an email (one disabled, one active), so there can be more than one.
func directGrants(ctx context.Context, c *client, sub subject, t target) ([]grant, error) {
	grants, err := listAll[grant](ctx, c, initiativePath(t.initiativeID)+"/grants", "grants")
	if err != nil {
		return nil, fmt.Errorf("list grants: %w", err)
	}
	var out []grant
	for _, g := range grants {
		if g.Group != nil || g.User == nil || g.Role.ID != t.roleID {
			continue
		}
		if sub.email != "" && strings.EqualFold(g.User.Email, sub.email) ||
			sub.email == "" && t.robotID != "" && g.User.ID == t.robotID {
			out = append(out, g)
		}
	}
	return out, nil
}

// doGrant returns whether something changed. The API answers 409 when the grant already exists, but the
// unique key ignores expires_at and the listing returns expired grants, so a 409 may hide a grant with no effect.
func doGrant(ctx context.Context, c *client, sub subject, t target, stderr io.Writer) (bool, error) {
	req := grantRequest{Email: sub.email, RobotID: t.robotID, RoleID: t.roleID}
	err := c.do(ctx, http.MethodPost, initiativePath(t.initiativeID)+"/grants", nil, req, nil)
	ae, ok := errors.AsType[*apiError](err)
	if !ok {
		return err == nil, err
	}
	switch {
	case ae.Status == http.StatusUnprocessableEntity && ae.hasField("email", codeNoUniqueActiveUser):
		return false, fmt.Errorf("no single active user with email %s: the user must sign in once first (%w)", sub.email, ae)
	case ae.Status != http.StatusConflict: // POST /grants has a single conflict: the grant already exists
		return false, err
	}
	existing, lerr := directGrants(ctx, c, sub, t)
	if lerr != nil {
		return false, lerr
	}
	var latest *time.Time // the latest expiry among the matches; nil means a grant that never expires
	effective := false
	for _, g := range existing {
		switch {
		case g.ExpiresAt == nil:
			return false, nil
		case g.ExpiresAt.After(time.Now()):
			effective = true
			if latest == nil || g.ExpiresAt.After(*latest) {
				latest = g.ExpiresAt
			}
		}
	}
	switch {
	case effective:
		_, _ = fmt.Fprintf(stderr, "warning: existing grant expires at %s\n", latest.Format(time.RFC3339))
		return false, nil
	case len(existing) > 0:
		return false, fmt.Errorf("an expired grant exists for %s -> %s/%s; revoke it first", sub, t.initiativeKey, t.roleKey)
	}
	return false, err // the conflict is not a visible direct grant: report it as it came
}

// doRevoke deletes every direct grant of the subject with the role.
func doRevoke(ctx context.Context, c *client, sub subject, t target) (bool, error) {
	existing, err := directGrants(ctx, c, sub, t)
	if err != nil {
		return false, err
	}
	changed := false
	for _, g := range existing {
		err := c.do(ctx, http.MethodDelete, "/api/v1/grants/"+url.PathEscape(g.ID), nil, nil, nil)
		if ae, ok := errors.AsType[*apiError](err); ok && ae.Status == http.StatusNotFound {
			continue // revoked concurrently
		}
		if err != nil {
			return changed, err
		}
		changed = true
	}
	return changed, nil
}

// parseSubject accepts an email (lowercased) or robot:<name>.
func parseSubject(s string) (subject, error) {
	if name, ok := strings.CutPrefix(s, robotPrefix); ok {
		if name == "" {
			return subject{}, errors.New("empty robot name")
		}
		return subject{robotName: name}, nil
	}
	if !strings.Contains(s, "@") {
		return subject{}, fmt.Errorf("subject must be an email or %s<name>", robotPrefix)
	}
	return subject{email: strings.ToLower(s)}, nil
}

// parseBaseURL accepts only https://host[:port][/] with nothing else.
func parseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("not a valid URL")
	}
	switch {
	case u.Scheme != "https":
		return nil, errors.New("must use https")
	case u.Host == "" || u.Hostname() == "":
		return nil, errors.New("missing host")
	case u.User != nil:
		return nil, errors.New("must not contain credentials")
	case u.Path != "" && u.Path != "/", u.RawQuery != "", u.ForceQuery, u.Fragment != "", u.Opaque != "":
		return nil, errors.New("must be only scheme and host")
	}
	return u, nil
}

// run is the testable core. transport nil means the default one (system roots, SSL_CERT_FILE).
func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer, transport http.RoundTripper) int {
	usage := func(msg string) int {
		_, _ = fmt.Fprintf(stderr, "error: %s\n%s", msg, usageText)
		return exitUsage
	}
	if len(args) != 4 || (args[0] != modeGrant && args[0] != modeRevoke) {
		return usage("expected: grant|revoke <subject> <initiative-key> <role-key>")
	}
	mode, initiativeKey, roleKey := args[0], args[2], args[3]
	sub, err := parseSubject(args[1])
	if err != nil {
		return usage(err.Error())
	}
	if initiativeKey == "" || roleKey == "" {
		return usage("initiative and role keys must not be empty")
	}
	base, err := parseBaseURL(getenv(envURL))
	if err != nil {
		return usage(envURL + ": " + err.Error())
	}
	token := getenv(envToken)
	if token == "" {
		return usage(envToken + " is not set")
	}

	c := newClient(base, token, transport)
	t, err := resolve(ctx, c, sub, initiativeKey, roleKey)
	var changed bool
	if err == nil {
		switch {
		case mode == modeGrant && t.robotDisabled:
			err = fmt.Errorf("robot %s is disabled", sub.robotName)
		case mode == modeGrant:
			changed, err = doGrant(ctx, c, sub, t, stderr)
		default:
			changed, err = doRevoke(ctx, c, sub, t)
		}
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return exitError
	}
	result := "unchanged"
	if changed {
		result = "changed"
	}
	_, _ = fmt.Fprintln(stdout, result)
	_, _ = fmt.Fprintf(stdout, "%s done: %s -> %s/%s\n", mode, sub, initiativeKey, roleKey)
	return exitOK
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr, nil)
	stop()
	os.Exit(code)
}
