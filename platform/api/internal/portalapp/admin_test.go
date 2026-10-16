package portalapp_test

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// cast is the set of callers of the permission matrix.
type cast struct {
	admin, mgrA, mgrB, plain, disabledAdmin string
	alpha, beta                             string // initiative ids
	editorA, managerA                       string // role ids of alpha
	ops                                     string // a group id
}

// order of the columns of every matrix row.
var castNames = []string{"platform admin", "manager of the initiative", "manager of another initiative", "user without grants", "disabled admin"}

func (c cast) subjects() []string {
	return []string{c.admin, c.mgrA, c.mgrB, c.plain, c.disabledAdmin}
}

var seq atomic.Int64

func (s *stack) seedCast() cast {
	s.t.Helper()
	user := func(status string) string {
		n := seq.Add(1)
		return scalar[string](s, `INSERT INTO users (google_sub, email, name, status) VALUES ($1, $2, 'n', $3) RETURNING id::text`,
			fmt.Sprintf("sub-%d", n), fmt.Sprintf("u%d@example.com", n), status)
	}
	c := cast{
		alpha: scalar[string](s, `INSERT INTO initiatives (key) VALUES ('alpha') RETURNING id::text`),
		beta:  scalar[string](s, `INSERT INTO initiatives (key) VALUES ('beta') RETURNING id::text`),
		ops:   scalar[string](s, `INSERT INTO groups (key) VALUES ('ops') RETURNING id::text`),
	}
	c.managerA = scalar[string](s, `INSERT INTO initiative_roles (initiative_id, key, manages_access) VALUES ($1, 'owner', true) RETURNING id::text`, c.alpha)
	c.editorA = scalar[string](s, `INSERT INTO initiative_roles (initiative_id, key) VALUES ($1, 'editor') RETURNING id::text`, c.alpha)
	managerB := scalar[string](s, `INSERT INTO initiative_roles (initiative_id, key, manages_access) VALUES ($1, 'owner', true) RETURNING id::text`, c.beta)

	c.admin, c.mgrA, c.mgrB, c.plain, c.disabledAdmin = user("active"), user("active"), user("active"), user("active"), user("disabled")
	s.exec(`INSERT INTO platform_admins (user_id) VALUES ($1), ($2)`, c.admin, c.disabledAdmin)
	s.exec(`INSERT INTO initiative_access (initiative_id, role_id, user_id) VALUES ($1, $2, $3), ($4, $5, $6)`,
		c.alpha, c.managerA, c.mgrA, c.beta, managerB, c.mgrB)
	return c
}

func (s *stack) freshUser() (id, email string) {
	n := seq.Add(1)
	email = fmt.Sprintf("fresh%d@example.com", n)
	id = scalar[string](s, `INSERT INTO users (google_sub, email) VALUES ($1, $2) RETURNING id::text`, fmt.Sprintf("fresh-%d", n), email)
	return id, email
}

func (s *stack) freshGroup() string {
	return scalar[string](s, `INSERT INTO groups (key) VALUES ($1) RETURNING id::text`, fmt.Sprintf("fresh-%d", seq.Add(1)))
}

// snapshot summarizes every table an administration endpoint can change.
func (s *stack) snapshot() string {
	return fmt.Sprintf("grants=%d groups=%d members=%d users=%d active=%d sessions=%d audit=%d",
		scalar[int](s, `SELECT count(*) FROM initiative_access`), scalar[int](s, `SELECT count(*) FROM groups`),
		scalar[int](s, `SELECT count(*) FROM group_members`), scalar[int](s, `SELECT count(*) FROM users`),
		scalar[int](s, `SELECT count(*) FROM users WHERE status = 'active'`), scalar[int](s, `SELECT count(*) FROM sessions`),
		scalar[int](s, `SELECT count(*) FROM audit_events`))
}

func (s *stack) api(sub, method, path, body string) *recorder {
	s.t.Helper()
	c := call{method: method, path: path, assertion: s.assertion(sub, portalAppID), body: body, headers: map[string]string{"Sec-Fetch-Site": "same-origin"}}
	if method != http.MethodGet {
		c.headers["Content-Type"] = "application/json"
	}
	return s.public(c)
}

// Every administration endpoint, for every kind of caller: the status each of them gets. A resource of an
// initiative the caller does not manage is 404, an action they may never perform is 403, and a disabled
// administrator is 403 everywhere even though the assertion is valid.
func TestPermissionMatrix(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	c := s.seedCast()

	const (
		ok = http.StatusOK
		cr = http.StatusCreated
		nc = http.StatusNoContent
		nf = http.StatusNotFound
		fb = http.StatusForbidden
	)
	type req struct{ method, path, body string }
	rows := []struct {
		name  string
		build func() req
		want  [5]int // admin, manager, other manager, plain user, disabled admin
	}{
		{"list grants", func() req { return req{http.MethodGet, "/api/v1/initiatives/" + c.alpha + "/grants", ""} }, [5]int{ok, ok, nf, nf, fb}},
		{"create grant", func() req {
			return req{http.MethodPost, "/api/v1/initiatives/" + c.alpha + "/grants", fmt.Sprintf(`{"group_id":%q,"role_id":%q}`, s.freshGroup(), c.editorA)}
		}, [5]int{cr, cr, nf, nf, fb}},
		{"delete grant", func() req {
			u, _ := s.freshUser()
			s.exec(`INSERT INTO initiative_access (initiative_id, role_id, user_id) VALUES ($1, $2, $3)`, c.alpha, c.editorA, u)
			id := scalar[string](s, `SELECT id::text FROM initiative_access WHERE user_id = $1`, u)
			return req{http.MethodDelete, "/api/v1/grants/" + id, "{}"}
		}, [5]int{nc, nc, nf, nf, fb}},
		{"list groups", func() req { return req{http.MethodGet, "/api/v1/groups", ""} }, [5]int{ok, ok, ok, fb, fb}},
		{"create group", func() req {
			return req{http.MethodPost, "/api/v1/groups", fmt.Sprintf(`{"key":"new-%d"}`, seq.Add(1))}
		}, [5]int{cr, fb, fb, fb, fb}},
		{"delete group", func() req { return req{http.MethodDelete, "/api/v1/groups/" + s.freshGroup(), "{}"} }, [5]int{nc, fb, fb, fb, fb}},
		{"list members", func() req { return req{http.MethodGet, "/api/v1/groups/" + c.ops + "/members", ""} }, [5]int{ok, fb, fb, fb, fb}},
		{"add member", func() req {
			_, email := s.freshUser()
			return req{http.MethodPost, "/api/v1/groups/" + c.ops + "/members", fmt.Sprintf(`{"email":%q}`, email)}
		}, [5]int{cr, fb, fb, fb, fb}},
		{"remove member", func() req {
			u, _ := s.freshUser()
			s.exec(`INSERT INTO group_members (group_id, user_id) VALUES ($1, $2)`, c.ops, u)
			return req{http.MethodDelete, "/api/v1/groups/" + c.ops + "/members/" + u, ""}
		}, [5]int{nc, fb, fb, fb, fb}},
		{"find users", func() req { return req{http.MethodGet, "/api/v1/users?email=" + "u1@example.com", ""} }, [5]int{ok, fb, fb, fb, fb}},
		{"disable user", func() req {
			u, _ := s.freshUser()
			return req{http.MethodPatch, "/api/v1/users/" + u, `{"status":"disabled"}`}
		}, [5]int{ok, fb, fb, fb, fb}},
		{"revoke sessions", func() req {
			u, _ := s.freshUser()
			return req{http.MethodDelete, "/api/v1/users/" + u + "/sessions", "{}"}
		}, [5]int{ok, fb, fb, fb, fb}},
		{"audit log", func() req { return req{http.MethodGet, "/api/v1/audit", ""} }, [5]int{ok, ok, ok, fb, fb}},
		{"audit of the initiative", func() req { return req{http.MethodGet, "/api/v1/audit?initiative_id=" + c.alpha, ""} }, [5]int{ok, ok, nf, nf, fb}},
	}
	for _, row := range rows {
		for i, sub := range c.subjects() {
			t.Run(row.name+"/"+castNames[i], func(t *testing.T) {
				r := row.build()
				before := s.snapshot()
				w := s.api(sub, r.method, r.path, r.body)
				if w.Code != row.want[i] {
					t.Fatalf("status = %d, want %d: %s", w.Code, row.want[i], w.Body.String())
				}
				if w.Code >= http.StatusBadRequest && w.Header().Get("Content-Type") != "application/problem+json" {
					t.Fatalf("error without a problem document: %q", w.Header().Get("Content-Type"))
				}
				if w.Code == http.StatusForbidden || w.Code == http.StatusNotFound {
					// 403 and 404 carry no detail about the resource.
					if strings.Contains(w.Body.String(), c.alpha) || strings.Contains(w.Body.String(), "alpha") {
						t.Fatalf("the refusal leaks the resource: %s", w.Body.String())
					}
					// And a refusal changes nothing: no row touched, no audit event.
					if after := s.snapshot(); after != before {
						t.Fatalf("a %d left side effects:\n before %s\n after  %s", w.Code, before, after)
					}
				}
			})
		}
	}
}

// The error contract over HTTP: conflicts, validation, idempotency and the audit row of a real request.
func TestAdministrationOverHTTP(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	c := s.seedCast()
	_, email := s.freshUser()
	grantPath := "/api/v1/initiatives/" + c.alpha + "/grants"
	body := fmt.Sprintf(`{"email":%q,"role_id":%q}`, email, c.editorA)

	w := s.api(c.mgrA, http.MethodPost, grantPath, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", w.Code, w.Body.String())
	}
	grant := decode[struct {
		ID   string
		Role struct{ Key string }
		User struct{ Email string }
	}](t, w)
	if grant.ID == "" || grant.Role.Key != "editor" || grant.User.Email != email {
		t.Fatalf("grant = %+v", grant)
	}
	if n := scalar[int](s, `SELECT count(*) FROM audit_events WHERE action = 'grant.create' AND target_id = $1 AND actor_id = $2 AND initiative_id = $3`,
		grant.ID, c.mgrA, c.alpha); n != 1 {
		t.Fatalf("%d audit rows for the request", n)
	}

	if w := s.api(c.mgrA, http.MethodPost, grantPath, body); w.Code != http.StatusConflict {
		t.Fatalf("repeated create = %d, want 409", w.Code)
	}
	if n := scalar[int](s, `SELECT count(*) FROM initiative_access WHERE user_id IN (SELECT id FROM users WHERE email = $1)`, email); n != 1 {
		t.Fatalf("%d grants after a repeated create", n)
	}

	w = s.api(c.mgrA, http.MethodPost, grantPath, `{"email":"nobody@example.com","role_id":"`+c.editorA+`"}`)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `{"field":"email","code":"no_unique_active_user"}`) {
		t.Fatalf("unknown email = %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "nobody@example.com") {
		t.Fatalf("the input was echoed: %s", w.Body.String())
	}
	w = s.api(c.mgrA, http.MethodPost, grantPath, `{"email":"`+email+`","role_id":"`+c.editorA+`","expires_at":"2000-01-01T00:00:00Z"}`)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `{"field":"expires_at","code":"must_be_in_the_future"}`) {
		t.Fatalf("past expiry = %d: %s", w.Code, w.Body.String())
	}

	if w := s.api(c.mgrA, http.MethodDelete, "/api/v1/grants/"+grant.ID, "{}"); w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", w.Code)
	}
	if w := s.api(c.mgrA, http.MethodDelete, "/api/v1/grants/"+grant.ID, "{}"); w.Code != http.StatusNotFound {
		t.Fatalf("repeated delete = %d, want 404", w.Code)
	}
	if w := s.api(c.mgrA, http.MethodDelete, "/api/v1/grants/not-a-uuid", "{}"); w.Code != http.StatusNotFound {
		t.Fatalf("malformed id = %d, want 404", w.Code)
	}

	// The last manager of an initiative cannot be revoked.
	last := scalar[string](s, `SELECT id::text FROM initiative_access WHERE user_id = $1 AND initiative_id = $2`, c.mgrA, c.alpha)
	if w := s.api(c.admin, http.MethodDelete, "/api/v1/grants/"+last, "{}"); w.Code != http.StatusConflict {
		t.Fatalf("last manager = %d, want 409: %s", w.Code, w.Body.String())
	}

	// Lockout guard.
	if w := s.api(c.admin, http.MethodPatch, "/api/v1/users/"+c.admin, `{"status":"disabled"}`); w.Code != http.StatusConflict {
		t.Fatalf("self disable = %d, want 409", w.Code)
	}

	// Pagination and a tampered cursor.
	w = s.api(c.admin, http.MethodGet, "/api/v1/audit?limit=1", "")
	page := decode[struct {
		Events     []struct{ ID string }
		NextCursor string `json:"next_cursor"`
	}](t, w)
	if w.Code != http.StatusOK || len(page.Events) != 1 || page.NextCursor == "" {
		t.Fatalf("audit page = %d %s", w.Code, w.Body.String())
	}
	if w := s.api(c.admin, http.MethodGet, "/api/v1/audit?cursor="+page.NextCursor, ""); w.Code != http.StatusOK {
		t.Fatalf("next page = %d", w.Code)
	}
	if w := s.api(c.admin, http.MethodGet, "/api/v1/audit?cursor="+page.NextCursor+"x", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("tampered cursor = %d, want 400", w.Code)
	}
	if w := s.api(c.admin, http.MethodGet, "/api/v1/audit?limit=0", ""); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("limit=0 = %d, want 422", w.Code)
	}
}
