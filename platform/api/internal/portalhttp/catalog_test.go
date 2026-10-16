package portalhttp

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"platform/api/internal/portal"
)

const (
	roleID       = "01a14296-1449-7be0-957b-4b1ad8d7a005"
	deploymentID = "01a14296-1449-7be0-957b-4b1ad8d7a006"
	appID        = "app_01ARZ3NDEKTSV4RRFFQ69G5FAV"
)

// catalogCalls records what the catalog handlers pass to the use cases.
type catalogCalls struct {
	newInitiative portal.NewInitiative
	initiative    portal.InitiativePatch
	newRole       portal.NewRole
	role          portal.RolePatch
	newApp        portal.NewApp
	app           portal.AppPatch
	newDeployment portal.NewDeployment

	initiatives portal.Page[portal.Initiative]
	roles       portal.Page[portal.Role]
	apps        portal.Page[portal.App]
}

func (f *fakeAdmin) ListInitiatives(_ context.Context, caller string, req portal.PageRequest) (portal.Page[portal.Initiative], error) {
	f.req = req
	return f.initiatives, f.note(caller, "")
}

func (f *fakeAdmin) GetInitiative(_ context.Context, caller, id string) (portal.Initiative, error) {
	return portal.Initiative{ID: id, Key: "alpha", Status: "active"}, f.note(caller, id)
}

func (f *fakeAdmin) CreateInitiative(_ context.Context, caller string, in portal.NewInitiative) (portal.Initiative, error) {
	f.newInitiative = in
	return portal.Initiative{ID: initiativeID, Key: in.Key, Name: in.Name, Status: "active"}, f.note(caller, "")
}

func (f *fakeAdmin) UpdateInitiative(_ context.Context, caller, id string, p portal.InitiativePatch) (portal.Initiative, error) {
	f.initiative = p
	return portal.Initiative{ID: id, Key: "alpha", Status: "inactive"}, f.note(caller, id)
}

func (f *fakeAdmin) ListRoles(_ context.Context, caller, id string, req portal.PageRequest) (portal.Page[portal.Role], error) {
	f.req = req
	return f.roles, f.note(caller, id)
}

func (f *fakeAdmin) CreateRole(_ context.Context, caller, id string, in portal.NewRole) (portal.Role, error) {
	f.newRole = in
	return portal.Role{ID: roleID, InitiativeID: id, Key: in.Key, ManagesAccess: in.ManagesAccess}, f.note(caller, id)
}

func (f *fakeAdmin) UpdateRole(_ context.Context, caller, id string, p portal.RolePatch) (portal.Role, error) {
	f.role = p
	return portal.Role{ID: id, InitiativeID: initiativeID, Key: "owner"}, f.note(caller, id)
}

func (f *fakeAdmin) DeleteRole(_ context.Context, caller, id string) error { return f.note(caller, id) }

func (f *fakeAdmin) ListApps(_ context.Context, caller, id string, req portal.PageRequest) (portal.Page[portal.App], error) {
	f.req = req
	return f.apps, f.note(caller, id)
}

func (f *fakeAdmin) CreateApp(_ context.Context, caller, id string, in portal.NewApp) (portal.App, error) {
	f.newApp = in
	return portal.App{ID: appID, InitiativeID: id, Name: in.Name, Status: "active"}, f.note(caller, id)
}

func (f *fakeAdmin) UpdateApp(_ context.Context, caller, id string, p portal.AppPatch) (portal.App, error) {
	f.app = p
	return portal.App{ID: id, InitiativeID: initiativeID, Status: "disabled"}, f.note(caller, id)
}

func (f *fakeAdmin) CreateDeployment(_ context.Context, caller, id string, in portal.NewDeployment) (portal.Deployment, error) {
	f.newDeployment = in
	return portal.Deployment{ID: deploymentID, AppID: id, Host: in.Host}, f.note(caller, id)
}

func (f *fakeAdmin) DeleteDeployment(_ context.Context, caller, id string) error {
	return f.note(caller, id)
}

func TestCatalogRoutes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		status int
		want   string // substring of the body
	}{
		{"list initiatives", http.MethodGet, "/api/v1/initiatives", "", http.StatusOK, `{"initiatives":[]}`},
		{"create initiative", http.MethodPost, "/api/v1/initiatives", `{"key":"alpha","name":"Alpha"}`, http.StatusCreated, `"key":"alpha"`},
		{"get initiative", http.MethodGet, "/api/v1/initiatives/" + initiativeID, "", http.StatusOK, `"status":"active"`},
		{"patch initiative", http.MethodPatch, "/api/v1/initiatives/" + initiativeID, `{"status":"inactive"}`, http.StatusOK, `"status":"inactive"`},
		{"list roles", http.MethodGet, "/api/v1/initiatives/" + initiativeID + "/roles", "", http.StatusOK, `{"roles":[]}`},
		{"create role", http.MethodPost, "/api/v1/initiatives/" + initiativeID + "/roles", `{"key":"owner","manages_access":true}`, http.StatusCreated, `"manages_access":true`},
		{"patch role", http.MethodPatch, "/api/v1/roles/" + roleID, `{"manages_access":false}`, http.StatusOK, `"manages_access":false`},
		{"delete role", http.MethodDelete, "/api/v1/roles/" + roleID, "{}", http.StatusNoContent, ""},
		{"list apps", http.MethodGet, "/api/v1/initiatives/" + initiativeID + "/apps", "", http.StatusOK, `{"apps":[]}`},
		{"create app", http.MethodPost, "/api/v1/initiatives/" + initiativeID + "/apps", `{"name":"Hello"}`, http.StatusCreated, `"id":"` + appID + `"`},
		{"patch app", http.MethodPatch, "/api/v1/apps/" + appID, `{"status":"disabled"}`, http.StatusOK, `"status":"disabled"`},
		{"create deployment", http.MethodPost, "/api/v1/apps/" + appID + "/deployments", `{"host":"a.example.test"}`, http.StatusCreated, `"host":"a.example.test"`},
		{"delete deployment", http.MethodDelete, "/api/v1/deployments/" + deploymentID, "{}", http.StatusNoContent, ""},
		{"initiatives cannot be deleted", http.MethodDelete, "/api/v1/initiatives/" + initiativeID, "{}", http.StatusNotFound, ""},
		{"apps cannot be deleted", http.MethodDelete, "/api/v1/apps/" + appID, "{}", http.StatusNotFound, ""},
		{"roles cannot be created at the top level", http.MethodPost, "/api/v1/roles", "{}", http.StatusNotFound, ""},
		{"deployments cannot be patched", http.MethodPatch, "/api/v1/deployments/" + deploymentID, "{}", http.StatusNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeAdmin{}
			w := do(newTestHandler(t, fakeService{}, fake, nil), tt.method, tt.path, tt.body, nil)
			if w.Code != tt.status || !strings.Contains(w.Body.String(), tt.want) {
				t.Fatalf("status %d body %s, want %d containing %q", w.Code, w.Body.String(), tt.status, tt.want)
			}
			if tt.status != http.StatusNotFound && fake.caller != callerID {
				t.Fatalf("use case called as %q, want the assertion subject", fake.caller)
			}
		})
	}
}

func TestCatalogRoutePassesIDsAndBodies(t *testing.T) {
	t.Parallel()
	fake := &fakeAdmin{}
	h := newTestHandler(t, fakeService{}, fake, nil)

	do(h, http.MethodPost, "/api/v1/initiatives", `{"key":"alpha","name":"Alpha"}`, nil)
	if fake.newInitiative != (portal.NewInitiative{Key: "alpha", Name: "Alpha"}) {
		t.Fatalf("new initiative = %+v", fake.newInitiative)
	}

	do(h, http.MethodPatch, "/api/v1/initiatives/"+initiativeID, `{"name":"Renamed"}`, nil)
	if fake.id != initiativeID || fake.initiative.Name == nil || *fake.initiative.Name != "Renamed" || fake.initiative.Status != nil {
		t.Fatalf("initiative patch = %q %+v", fake.id, fake.initiative)
	}
	do(h, http.MethodPatch, "/api/v1/initiatives/"+initiativeID, `{"name":null,"status":"active"}`, nil)
	if fake.initiative.Name != nil || fake.initiative.Status == nil || *fake.initiative.Status != "active" {
		t.Fatalf("a null field is absent: %+v", fake.initiative)
	}

	do(h, http.MethodPost, "/api/v1/initiatives/"+initiativeID+"/roles", `{"key":"owner"}`, nil)
	if fake.id != initiativeID || fake.newRole != (portal.NewRole{Key: "owner"}) {
		t.Fatalf("new role = %q %+v", fake.id, fake.newRole)
	}

	do(h, http.MethodPatch, "/api/v1/roles/"+roleID, `{"manages_access":false}`, nil)
	if fake.id != roleID || fake.role.ManagesAccess == nil || *fake.role.ManagesAccess {
		t.Fatalf("role patch = %q %+v", fake.id, fake.role)
	}
	do(h, http.MethodPatch, "/api/v1/roles/"+roleID, `{}`, nil)
	if fake.role.ManagesAccess != nil {
		t.Fatalf("an empty role patch must leave the field unset: %+v", fake.role)
	}

	do(h, http.MethodPost, "/api/v1/initiatives/"+initiativeID+"/apps", `{"name":"Hello"}`, nil)
	if fake.id != initiativeID || fake.newApp.Name != "Hello" {
		t.Fatalf("new app = %q %+v", fake.id, fake.newApp)
	}

	do(h, http.MethodPatch, "/api/v1/apps/"+appID, `{"name":"X","status":"disabled"}`, nil)
	if fake.id != appID || fake.app.Name == nil || *fake.app.Name != "X" || fake.app.Status == nil || *fake.app.Status != "disabled" {
		t.Fatalf("app patch = %q %+v", fake.id, fake.app)
	}

	do(h, http.MethodPost, "/api/v1/apps/"+appID+"/deployments", `{"host":"a.example.test"}`, nil)
	if fake.id != appID || fake.newDeployment.Host != "a.example.test" {
		t.Fatalf("new deployment = %q %+v", fake.id, fake.newDeployment)
	}
}

// Keys, the initiative of an app and any other identity are immutable: a patch that names them is rejected
// before it reaches the use case.
func TestImmutableFieldsAreRejected(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"initiative key", http.MethodPatch, "/api/v1/initiatives/" + initiativeID, `{"key":"other"}`},
		{"role key", http.MethodPatch, "/api/v1/roles/" + roleID, `{"key":"other","manages_access":true}`},
		{"app initiative", http.MethodPatch, "/api/v1/apps/" + appID, `{"initiative_id":"` + initiativeID + `"}`},
		{"app id", http.MethodPatch, "/api/v1/apps/" + appID, `{"id":"app_00000000000000000000000000"}`},
		{"app id on create", http.MethodPost, "/api/v1/initiatives/" + initiativeID + "/apps", `{"id":"app_00000000000000000000000000","name":"x"}`},
		{"deployment app on create", http.MethodPost, "/api/v1/apps/" + appID + "/deployments", `{"host":"a.example.test","app_id":"x"}`},
		{"manages_access not a boolean", http.MethodPatch, "/api/v1/roles/" + roleID, `{"manages_access":"yes"}`},
		{"status not a string", http.MethodPatch, "/api/v1/apps/" + appID, `{"status":true}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeAdmin{}
			w := do(newTestHandler(t, fakeService{}, fake, nil), tt.method, tt.path, tt.body, nil)
			if w.Code != http.StatusBadRequest || fake.calls != 0 {
				t.Fatalf("status %d after %d calls, want 400 and none (%s)", w.Code, fake.calls, w.Body.String())
			}
		})
	}
}

func TestCatalogConflictsAreFixedTexts(t *testing.T) {
	t.Parallel()
	for _, c := range []*portal.Conflict{
		portal.ErrInitiativeKeyTaken, portal.ErrRoleKeyTaken, portal.ErrRoleInUse, portal.ErrHostTaken,
		portal.ErrPlatformInitiative, portal.ErrPortalApp, portal.ErrLastPortalDeployment,
	} {
		t.Run(c.Detail, func(t *testing.T) {
			t.Parallel()
			h := newTestHandler(t, fakeService{}, &fakeAdmin{err: c}, nil)
			w := do(h, http.MethodDelete, "/api/v1/deployments/"+deploymentID, "{}", nil)
			if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), c.Detail) {
				t.Fatalf("status %d body %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestCatalogResponseShapes(t *testing.T) {
	t.Parallel()
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	fake := &fakeAdmin{}
	fake.apps = portal.Page[portal.App]{
		Items: []portal.App{
			{ID: appID, InitiativeID: initiativeID, Name: "Hello", Status: "active", CreatedAt: at, Deployments: []portal.Deployment{
				{ID: deploymentID, AppID: appID, InitiativeID: initiativeID, Host: "a.example.test", CreatedAt: at},
			}},
			{ID: appID, InitiativeID: initiativeID, CreatedAt: at}, // no deployments: an empty list, never null
		},
		NextCursor: "next",
	}
	fake.roles = portal.Page[portal.Role]{Items: []portal.Role{{ID: roleID, InitiativeID: initiativeID, Key: "owner", ManagesAccess: true}}}
	h := newTestHandler(t, fakeService{}, fake, nil)

	w := do(h, http.MethodGet, "/api/v1/initiatives/"+initiativeID+"/apps", "", nil)
	const wantApps = `{"apps":[` +
		`{"id":"` + appID + `","initiative_id":"` + initiativeID + `","name":"Hello","status":"active","created_at":"2030-01-02T03:04:05Z",` +
		`"deployments":[{"id":"` + deploymentID + `","app_id":"` + appID + `","host":"a.example.test","created_at":"2030-01-02T03:04:05Z",` +
		`"last_seen_at":null,"last_probe_at":null,"last_probe_status":null}]},` +
		`{"id":"` + appID + `","initiative_id":"` + initiativeID + `","name":"","status":"","created_at":"2030-01-02T03:04:05Z","deployments":[]}` +
		`],"next_cursor":"next"}`
	if strings.TrimSpace(w.Body.String()) != wantApps {
		t.Fatalf("apps = %s\nwant   %s", w.Body.String(), wantApps)
	}
	w = do(h, http.MethodGet, "/api/v1/initiatives/"+initiativeID+"/roles", "", nil)
	const wantRoles = `{"roles":[{"id":"` + roleID + `","initiative_id":"` + initiativeID + `","key":"owner","manages_access":true}]}`
	if strings.TrimSpace(w.Body.String()) != wantRoles {
		t.Fatalf("roles = %s\nwant    %s", w.Body.String(), wantRoles)
	}
}

func TestCatalogLimitParameter(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"/api/v1/initiatives", "/api/v1/initiatives/" + initiativeID + "/roles", "/api/v1/initiatives/" + initiativeID + "/apps",
	} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			fake := &fakeAdmin{}
			h := newTestHandler(t, fakeService{}, fake, nil)
			if w := do(h, http.MethodGet, path+"?limit=7&cursor=abc", "", nil); w.Code != http.StatusOK || fake.req.Limit != 7 || fake.req.Cursor != "abc" {
				t.Fatalf("status %d request %+v", w.Code, fake.req)
			}
			calls := fake.calls
			if w := do(h, http.MethodGet, path+"?limit=0", "", nil); w.Code != http.StatusUnprocessableEntity || fake.calls != calls {
				t.Fatalf("limit=0: status %d, calls %d -> %d", w.Code, calls, fake.calls)
			}
		})
	}
}
