package portalapp_test

import (
	"fmt"
	"net/http"
	"regexp"
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

// freshRole inserts a role without grants into the initiative and returns its id.
func (s *stack) freshRole(initiativeID string) string {
	return scalar[string](s, `INSERT INTO initiative_roles (initiative_id, key) VALUES ($1, $2) RETURNING id::text`,
		initiativeID, fmt.Sprintf("role-%d", seq.Add(1)))
}

// freshApp inserts an app with one deployment into the initiative and returns the app id, the deployment id and
// the host.
func (s *stack) freshApp(initiativeID string) (appID, deploymentID, host string) {
	n := seq.Add(1)
	appID = fmt.Sprintf("app_%026d", n)
	host = fmt.Sprintf("fresh-%d.apps.example.test", n)
	s.exec(`INSERT INTO apps (id, initiative_id, name) VALUES ($1, $2, 'Fresh')`, appID, initiativeID)
	deploymentID = scalar[string](s, `INSERT INTO deployments (host, app_id) VALUES ($1, $2) RETURNING id::text`, host, appID)
	return appID, deploymentID, host
}

// catalogState is a digest of every column of every catalog row: a refused request must leave it as it was.
func (s *stack) catalogState() string {
	return scalar[string](s, `SELECT md5(
  coalesce((SELECT string_agg(i::text, ',' ORDER BY id) FROM initiatives i), '') ||
  coalesce((SELECT string_agg(r::text, ',' ORDER BY id) FROM initiative_roles r), '') ||
  coalesce((SELECT string_agg(a::text, ',' ORDER BY id) FROM apps a), '') ||
  coalesce((SELECT string_agg(d::text, ',' ORDER BY id) FROM deployments d), ''))`)
}

// snapshot summarizes every table an administration endpoint can change.
func (s *stack) snapshot() string {
	return fmt.Sprintf("catalog=%s grants=%d groups=%d members=%d users=%d active=%d sessions=%d audit=%d", s.catalogState(),
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
		{"list initiatives", func() req { return req{http.MethodGet, "/api/v1/initiatives", ""} }, [5]int{ok, ok, ok, fb, fb}},
		{"create initiative", func() req {
			return req{http.MethodPost, "/api/v1/initiatives", fmt.Sprintf(`{"key":"new-%d","name":"New"}`, seq.Add(1))}
		}, [5]int{cr, fb, fb, fb, fb}},
		{"get initiative", func() req { return req{http.MethodGet, "/api/v1/initiatives/" + c.alpha, ""} }, [5]int{ok, ok, nf, nf, fb}},
		{"patch initiative", func() req {
			return req{http.MethodPatch, "/api/v1/initiatives/" + c.alpha, `{"name":"Alpha 2"}`}
		}, [5]int{ok, fb, nf, nf, fb}},
		{"list roles", func() req { return req{http.MethodGet, "/api/v1/initiatives/" + c.alpha + "/roles", ""} }, [5]int{ok, ok, nf, nf, fb}},
		{"create role", func() req {
			return req{http.MethodPost, "/api/v1/initiatives/" + c.alpha + "/roles", fmt.Sprintf(`{"key":"r-%d"}`, seq.Add(1))}
		}, [5]int{cr, fb, nf, nf, fb}},
		{"patch role", func() req {
			return req{http.MethodPatch, "/api/v1/roles/" + s.freshRole(c.alpha), `{"manages_access":true}`}
		}, [5]int{ok, fb, nf, nf, fb}},
		{"delete role", func() req { return req{http.MethodDelete, "/api/v1/roles/" + s.freshRole(c.alpha), "{}"} }, [5]int{nc, fb, nf, nf, fb}},
		{"list apps", func() req { return req{http.MethodGet, "/api/v1/initiatives/" + c.alpha + "/apps", ""} }, [5]int{ok, ok, nf, nf, fb}},
		{"create app", func() req {
			return req{http.MethodPost, "/api/v1/initiatives/" + c.alpha + "/apps", `{"name":"New"}`}
		}, [5]int{cr, fb, nf, nf, fb}},
		{"patch app", func() req {
			id, _, _ := s.freshApp(c.alpha)
			return req{http.MethodPatch, "/api/v1/apps/" + id, `{"status":"disabled"}`}
		}, [5]int{ok, fb, nf, nf, fb}},
		{"create deployment", func() req {
			id, _, _ := s.freshApp(c.alpha)
			return req{http.MethodPost, "/api/v1/apps/" + id + "/deployments", fmt.Sprintf(`{"host":"extra-%d.apps.example.test"}`, seq.Add(1))}
		}, [5]int{cr, fb, nf, nf, fb}},
		{"delete deployment", func() req {
			_, id, _ := s.freshApp(c.alpha)
			return req{http.MethodDelete, "/api/v1/deployments/" + id, "{}"}
		}, [5]int{nc, fb, nf, nf, fb}},
		{"app health", func() req {
			id, _, _ := s.freshApp(c.alpha)
			return req{http.MethodGet, "/api/v1/apps/" + id + "/health", ""}
		}, [5]int{ok, ok, nf, nf, fb}},
		{"orphan hosts", func() req { return req{http.MethodGet, "/api/v1/health/orphan-hosts", ""} }, [5]int{ok, fb, fb, fb, fb}},
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

// The catalog over HTTP, from an empty initiative to a deployed app, with the audit trail and the guards that
// keep the portal reachable.
func TestCatalogOverHTTP(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	s.seed() // the platform initiative with the portal app on portalHost
	c := s.seedCast()
	appIDPattern := regexp.MustCompile(`^app_[0-9A-HJKMNP-TV-Z]{26}$`)
	type idBody struct{ ID string }
	audit := func(action, targetID, initiativeID string) int {
		return scalar[int](s, `SELECT count(*) FROM audit_events WHERE action = $1 AND target_id = $2 AND initiative_id = $3 AND actor_id = $4`,
			action, targetID, initiativeID, c.admin)
	}
	mustStatus := func(w *recorder, want int) {
		t.Helper()
		if w.Code != want {
			t.Fatalf("status = %d, want %d: %s", w.Code, want, w.Body.String())
		}
	}

	// Initiatives.
	w := s.api(c.admin, http.MethodPost, "/api/v1/initiatives", `{"key":"gamma","name":"Gamma"}`)
	mustStatus(w, http.StatusCreated)
	gamma := decode[idBody](t, w).ID
	if audit("initiative.create", gamma, gamma) != 1 {
		t.Fatal("no audit row for the creation")
	}
	mustStatus(s.api(c.admin, http.MethodPost, "/api/v1/initiatives", `{"key":"gamma"}`), http.StatusConflict)
	w = s.api(c.admin, http.MethodPost, "/api/v1/initiatives", `{"key":"Not A Key"}`)
	mustStatus(w, http.StatusUnprocessableEntity)
	if !strings.Contains(w.Body.String(), `{"field":"key","code":"invalid"}`) || strings.Contains(w.Body.String(), "Not A Key") {
		t.Fatalf("validation = %s", w.Body.String())
	}
	mustStatus(s.api(c.admin, http.MethodPatch, "/api/v1/initiatives/"+gamma, `{"key":"other"}`), http.StatusBadRequest)
	mustStatus(s.api(c.admin, http.MethodPatch, "/api/v1/initiatives/"+gamma, `{}`), http.StatusUnprocessableEntity)
	w = s.api(c.admin, http.MethodPatch, "/api/v1/initiatives/"+gamma, `{"status":"inactive"}`)
	mustStatus(w, http.StatusOK)
	if got := decode[struct{ Key, Status string }](t, w); got.Key != "gamma" || got.Status != "inactive" || audit("initiative.deactivate", gamma, gamma) != 1 {
		t.Fatalf("initiative = %+v", got)
	}
	mustStatus(s.api(c.admin, http.MethodPatch, "/api/v1/initiatives/"+gamma, `{"status":"active"}`), http.StatusOK)
	if audit("initiative.activate", gamma, gamma) != 1 {
		t.Fatal("no audit row for the activation")
	}

	// Roles and the last-manager rule.
	rolesPath := "/api/v1/initiatives/" + gamma + "/roles"
	w = s.api(c.admin, http.MethodPost, rolesPath, `{"key":"lead","manages_access":true}`)
	mustStatus(w, http.StatusCreated)
	lead := decode[idBody](t, w).ID
	mustStatus(s.api(c.admin, http.MethodPost, rolesPath, `{"key":"lead"}`), http.StatusConflict)
	w = s.api(c.admin, http.MethodPost, rolesPath, `{"key":"editor"}`)
	mustStatus(w, http.StatusCreated)
	editor := decode[idBody](t, w).ID
	if audit("role.create", lead, gamma) != 1 || audit("role.create", editor, gamma) != 1 {
		t.Fatal("no audit rows for the roles")
	}
	mustStatus(s.api(c.admin, http.MethodPatch, "/api/v1/roles/"+lead, `{"key":"x","manages_access":false}`), http.StatusBadRequest)
	mustStatus(s.api(c.admin, http.MethodPatch, "/api/v1/roles/"+lead, `{}`), http.StatusUnprocessableEntity)
	holder, _ := s.freshUser()
	s.exec(`INSERT INTO initiative_access (initiative_id, role_id, user_id) VALUES ($1, $2, $3)`, gamma, lead, holder)
	w = s.api(c.admin, http.MethodPatch, "/api/v1/roles/"+lead, `{"manages_access":false}`)
	mustStatus(w, http.StatusConflict)
	if scalar[bool](s, `SELECT manages_access FROM initiative_roles WHERE id = $1`, lead) == false {
		t.Fatal("the refused demotion was applied")
	}
	mustStatus(s.api(c.admin, http.MethodPatch, "/api/v1/roles/"+editor, `{"manages_access":true}`), http.StatusOK)
	if audit("role.update", editor, gamma) != 1 {
		t.Fatal("no audit row for the promotion")
	}
	mustStatus(s.api(c.admin, http.MethodDelete, "/api/v1/roles/"+lead, "{}"), http.StatusConflict) // it has a grant
	mustStatus(s.api(c.admin, http.MethodDelete, "/api/v1/roles/"+editor, "{}"), http.StatusNoContent)
	mustStatus(s.api(c.admin, http.MethodDelete, "/api/v1/roles/"+editor, "{}"), http.StatusNotFound)
	if audit("role.delete", editor, gamma) != 1 {
		t.Fatal("no audit row for the deletion")
	}

	// Apps and deployments.
	appsPath := "/api/v1/initiatives/" + gamma + "/apps"
	w = s.api(c.admin, http.MethodPost, appsPath, `{"name":"Hello"}`)
	mustStatus(w, http.StatusCreated)
	app := decode[struct {
		ID          string
		Deployments []struct{ Host string }
	}](t, w)
	if !appIDPattern.MatchString(app.ID) || app.Deployments == nil || len(app.Deployments) != 0 || !strings.Contains(w.Body.String(), `"deployments":[]`) {
		t.Fatalf("app = %+v: %s", app, w.Body.String())
	}
	mustStatus(s.api(c.admin, http.MethodPost, appsPath, `{"id":"app_00000000000000000000000000"}`), http.StatusBadRequest)
	if audit("app.create", app.ID, gamma) != 1 {
		t.Fatal("no audit row for the app")
	}
	deploymentsPath := "/api/v1/apps/" + app.ID + "/deployments"
	w = s.api(c.admin, http.MethodPost, deploymentsPath, `{"host":"hello-gamma.apps.example.test"}`)
	mustStatus(w, http.StatusCreated)
	dep := decode[idBody](t, w).ID
	if audit("deployment.create", dep, gamma) != 1 {
		t.Fatal("no audit row for the deployment")
	}
	mustStatus(s.api(c.admin, http.MethodPost, deploymentsPath, `{"host":"hello-gamma.apps.example.test"}`), http.StatusConflict)
	mustStatus(s.api(c.admin, http.MethodPost, deploymentsPath, `{"host":"`+portalHost+`"}`), http.StatusConflict)
	w = s.api(c.admin, http.MethodPost, deploymentsPath, `{"host":"auth.apps.example.test"}`)
	mustStatus(w, http.StatusUnprocessableEntity)
	if !strings.Contains(w.Body.String(), `{"field":"host","code":"reserved"}`) {
		t.Fatalf("auth host = %s", w.Body.String())
	}
	mustStatus(s.api(c.admin, http.MethodPost, deploymentsPath, `{"host":"Upper.apps.example.test"}`), http.StatusUnprocessableEntity)
	mustStatus(s.api(c.admin, http.MethodPost, deploymentsPath, `{"host":"*.apps.example.test"}`), http.StatusUnprocessableEntity)

	w = s.api(c.admin, http.MethodGet, appsPath, "")
	listed := decode[struct {
		Apps []struct {
			ID          string
			Deployments []struct{ Host string }
		}
	}](t, w)
	if w.Code != http.StatusOK || len(listed.Apps) != 1 || len(listed.Apps[0].Deployments) != 1 || listed.Apps[0].Deployments[0].Host != "hello-gamma.apps.example.test" {
		t.Fatalf("apps = %s", w.Body.String())
	}
	mustStatus(s.api(c.admin, http.MethodPatch, "/api/v1/apps/"+app.ID, `{"status":"disabled"}`), http.StatusOK)
	if audit("app.disable", app.ID, gamma) != 1 {
		t.Fatal("no audit row for the disable")
	}
	mustStatus(s.api(c.admin, http.MethodPatch, "/api/v1/apps/"+app.ID, `{"initiative_id":"`+c.alpha+`"}`), http.StatusBadRequest)
	mustStatus(s.api(c.admin, http.MethodPatch, "/api/v1/apps/"+strings.ToLower(app.ID), `{"name":"x"}`), http.StatusNotFound)
	mustStatus(s.api(c.admin, http.MethodDelete, "/api/v1/deployments/"+dep, "{}"), http.StatusNoContent)
	mustStatus(s.api(c.admin, http.MethodDelete, "/api/v1/deployments/"+dep, "{}"), http.StatusNotFound)
	if n := scalar[int](s, `SELECT count(*) FROM audit_events WHERE action = 'deployment.delete' AND target_id = $1 AND before->>'host' = 'hello-gamma.apps.example.test'`, dep); n != 1 {
		t.Fatalf("%d audit rows keep the removed host", n)
	}

	// The guards that keep the portal reachable.
	platform := scalar[string](s, `SELECT id::text FROM initiatives WHERE key = 'platform'`)
	mustStatus(s.api(c.admin, http.MethodPatch, "/api/v1/initiatives/"+platform, `{"status":"inactive"}`), http.StatusConflict)
	mustStatus(s.api(c.admin, http.MethodPatch, "/api/v1/apps/"+portalAppID, `{"status":"disabled"}`), http.StatusConflict)
	portalDeployment := scalar[string](s, `SELECT id::text FROM deployments WHERE host = $1`, portalHost)
	mustStatus(s.api(c.admin, http.MethodDelete, "/api/v1/deployments/"+portalDeployment, "{}"), http.StatusConflict)
	mustStatus(s.api(c.admin, http.MethodPost, "/api/v1/apps/"+portalAppID+"/deployments", `{"host":"portal-2.apps.example.test"}`), http.StatusCreated)
	mustStatus(s.api(c.admin, http.MethodDelete, "/api/v1/deployments/"+portalDeployment, "{}"), http.StatusNoContent)

	// A manager reads what they need to grant roles and check integrations, in their initiative only.
	w = s.api(c.mgrA, http.MethodGet, "/api/v1/initiatives/"+c.alpha+"/roles", "")
	if roles := decode[struct{ Roles []struct{ Key string } }](t, w); w.Code != http.StatusOK || len(roles.Roles) != 2 {
		t.Fatalf("manager's roles = %d %s", w.Code, w.Body.String())
	}
	mustStatus(s.api(c.mgrA, http.MethodGet, "/api/v1/initiatives/"+gamma+"/apps", ""), http.StatusNotFound)
}

// The health endpoints over the real stack: telemetry reaches the JSON, problems are derived from it, and
// reading changes nothing.
func TestHealthOverHTTP(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	s.seed()
	c := s.seedCast()
	app, _, host := s.freshApp(c.alpha)
	s.exec(`UPDATE deployments SET last_seen_at = now() - interval '2 days', last_probe_at = now(), last_probe_status = 'unprotected' WHERE host = $1`, host)
	s.exec(`SELECT create_access_denials_partition(now()::date)`)
	s.exec(`INSERT INTO access_denials (host, reason) VALUES ('orphan.apps.example.test', 'unknown_host'), ('orphan.apps.example.test', 'unknown_host')`)

	type deployment struct {
		Host            string  `json:"host"`
		LastSeenAt      *string `json:"last_seen_at"`
		LastProbeAt     *string `json:"last_probe_at"`
		LastProbeStatus *string `json:"last_probe_status"`
	}
	type health struct {
		App struct {
			ID          string       `json:"id"`
			Deployments []deployment `json:"deployments"`
		} `json:"app"`
		InitiativeStatus       string `json:"initiative_status"`
		GrantCount             int    `json:"grant_count"`
		EffectiveManagerGrants int    `json:"effective_manager_grants"`
		Problems               []struct {
			Code string  `json:"code"`
			Host *string `json:"host"`
		} `json:"problems"`
	}

	before := s.snapshot()
	for name, caller := range map[string]string{"admin": c.admin, "manager": c.mgrA} {
		w := s.api(caller, http.MethodGet, "/api/v1/apps/"+app+"/health", "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", name, w.Code, w.Body.String())
		}
		h := decode[health](t, w)
		if h.App.ID != app || h.InitiativeStatus != "active" || h.GrantCount != 1 || h.EffectiveManagerGrants != 1 || len(h.App.Deployments) != 1 {
			t.Fatalf("%s: health = %s", name, w.Body.String())
		}
		d := h.App.Deployments[0]
		if d.Host != host || d.LastSeenAt == nil || d.LastProbeAt == nil || d.LastProbeStatus == nil || *d.LastProbeStatus != "unprotected" {
			t.Fatalf("%s: deployment = %+v", name, d)
		}
		var codes []string
		for _, p := range h.Problems {
			codes = append(codes, p.Code)
			if p.Code == "unprotected" && (p.Host == nil || *p.Host != host) {
				t.Fatalf("%s: unprotected without its host: %s", name, w.Body.String())
			}
		}
		if len(codes) != 2 || codes[0] != "unprotected" || codes[1] != "not_seen_recently" {
			t.Fatalf("%s: problems = %v", name, codes)
		}
	}

	w := s.api(c.admin, http.MethodGet, "/api/v1/health/orphan-hosts", "")
	orphans := decode[struct {
		Hosts []struct {
			Host         string `json:"host"`
			Count        int    `json:"count"`
			LastDeniedAt string `json:"last_denied_at"`
		} `json:"hosts"`
	}](t, w)
	if w.Code != http.StatusOK || len(orphans.Hosts) != 1 || orphans.Hosts[0].Host != "orphan.apps.example.test" ||
		orphans.Hosts[0].Count != 2 || orphans.Hosts[0].LastDeniedAt == "" {
		t.Fatalf("orphan hosts = %d %s", w.Code, w.Body.String())
	}
	if after := s.snapshot(); after != before {
		t.Fatalf("reading the health changed state:\n before %s\n after  %s", before, after)
	}

	// Health is read only: no verb but GET exists on these paths.
	for _, m := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete} {
		if w := s.api(c.admin, m, "/api/v1/apps/"+app+"/health", "{}"); w.Code != http.StatusNotFound {
			t.Fatalf("%s health = %d, want 404", m, w.Code)
		}
	}
}
