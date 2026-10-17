package portalhttp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"platform/api/internal/portal"
)

func (f *fakeAdmin) AppHealth(_ context.Context, caller, id string) (portal.AppHealth, error) {
	return f.health, f.note(caller, id)
}

func (f *fakeAdmin) ListOrphanHosts(_ context.Context, caller string) ([]portal.OrphanHost, error) {
	return f.orphans, f.note(caller, "")
}

func TestHealthRoutes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		status int
		want   string
	}{
		{"app health", http.MethodGet, "/api/v1/apps/" + appID + "/health", "", http.StatusOK, `"problems":[]`},
		{"orphan hosts", http.MethodGet, "/api/v1/health/orphan-hosts", "", http.StatusOK, `{"hosts":[]}`},
		{"health is read only", http.MethodPost, "/api/v1/apps/" + appID + "/health", "{}", http.StatusNotFound, ""},
		{"health cannot be deleted", http.MethodDelete, "/api/v1/apps/" + appID + "/health", "{}", http.StatusNotFound, ""},
		{"orphan hosts are read only", http.MethodDelete, "/api/v1/health/orphan-hosts", "{}", http.StatusNotFound, ""},
		{"orphan hosts take no id", http.MethodGet, "/api/v1/health/orphan-hosts/x", "", http.StatusNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeAdmin{}
			w := do(newTestHandler(t, fakeService{}, fake, nil), tt.method, tt.path, tt.body, nil)
			if w.Code != tt.status || !strings.Contains(w.Body.String(), tt.want) {
				t.Fatalf("status %d body %s, want %d containing %q", w.Code, w.Body.String(), tt.status, tt.want)
			}
			if tt.status == http.StatusNotFound {
				if fake.calls != 0 {
					t.Fatalf("a refused route reached the use case %d times", fake.calls)
				}
				return
			}
			if fake.caller != callerID {
				t.Fatalf("use case called as %q, want the assertion subject", fake.caller)
			}
		})
	}
}

func TestHealthPassesTheAppID(t *testing.T) {
	t.Parallel()
	fake := &fakeAdmin{}
	do(newTestHandler(t, fakeService{}, fake, nil), http.MethodGet, "/api/v1/apps/"+appID+"/health", "", nil)
	if fake.id != appID {
		t.Fatalf("app id = %q", fake.id)
	}
}

func TestHealthResponseShapes(t *testing.T) {
	t.Parallel()
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	seen, probed := at.Add(-time.Minute), at.Add(-2*time.Minute)
	fake := &fakeAdmin{}
	fake.health = portal.AppHealth{
		App: portal.App{ID: appID, InitiativeID: initiativeID, Name: "Hello", Status: "active", CreatedAt: at, Deployments: []portal.Deployment{
			{ID: deploymentID, AppID: appID, Host: "a.example.test", CreatedAt: at, LastSeenAt: &seen, LastProbeAt: &probed, LastProbeStatus: "ok"},
			{ID: deploymentID, AppID: appID, Host: "b.example.test", CreatedAt: at},
		}},
		InitiativeStatus: "active",
		Grants:           portal.GrantCounts{Grants: 3, EffectiveManagers: 1},
		Problems:         []portal.HealthProblem{{Code: portal.ProblemNoGrants}, {Code: portal.ProblemNeverSeen, Host: "b.example.test"}},
		CheckedAt:        at,
	}
	fake.orphans = []portal.OrphanHost{{Host: "orphan.example.test", Count: 4, LastDeniedAt: at}}
	h := newTestHandler(t, fakeService{}, fake, nil)

	w := do(h, http.MethodGet, "/api/v1/apps/"+appID+"/health", "", nil)
	const wantHealth = `{"app":{"id":"` + appID + `","initiative_id":"` + initiativeID + `","name":"Hello","status":"active","created_at":"2030-01-02T03:04:05Z","portal_app":false,"deployments":[` +
		`{"id":"` + deploymentID + `","app_id":"` + appID + `","host":"a.example.test","created_at":"2030-01-02T03:04:05Z",` +
		`"last_seen_at":"2030-01-02T03:03:05Z","last_probe_at":"2030-01-02T03:02:05Z","last_probe_status":"ok"},` +
		`{"id":"` + deploymentID + `","app_id":"` + appID + `","host":"b.example.test","created_at":"2030-01-02T03:04:05Z",` +
		`"last_seen_at":null,"last_probe_at":null,"last_probe_status":null}]},` +
		`"initiative_status":"active","grant_count":3,"effective_manager_grants":1,` +
		`"problems":[{"code":"no_grants","host":null},{"code":"never_seen","host":"b.example.test"}],"checked_at":"2030-01-02T03:04:05Z"}`
	if strings.TrimSpace(w.Body.String()) != wantHealth {
		t.Fatalf("health = %s\nwant    %s", w.Body.String(), wantHealth)
	}

	w = do(h, http.MethodGet, "/api/v1/health/orphan-hosts", "", nil)
	const wantOrphans = `{"hosts":[{"host":"orphan.example.test","count":4,"last_denied_at":"2030-01-02T03:04:05Z"}]}`
	if strings.TrimSpace(w.Body.String()) != wantOrphans {
		t.Fatalf("orphans = %s\nwant    %s", w.Body.String(), wantOrphans)
	}
}

func TestHealthErrorMapping(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/api/v1/apps/" + appID + "/health", "/api/v1/health/orphan-hosts"} {
		for name, tc := range map[string]struct {
			err    error
			status int
		}{
			"forbidden":     {portal.ErrForbidden, http.StatusForbidden},
			"not found":     {portal.ErrNotFound, http.StatusNotFound},
			"inactive user": {portal.ErrUserInactive, http.StatusForbidden},
			"contention":    {portal.ErrContention, http.StatusServiceUnavailable},
			"unexpected":    {errors.New("secret internal detail"), http.StatusInternalServerError},
		} {
			t.Run(path+" "+name, func(t *testing.T) {
				t.Parallel()
				w := do(newTestHandler(t, fakeService{}, &fakeAdmin{err: tc.err}, nil), http.MethodGet, path, "", nil)
				if w.Code != tc.status || strings.Contains(w.Body.String(), "secret internal detail") || strings.Contains(w.Body.String(), "app_") {
					t.Fatalf("status %d body %s, want %d without details", w.Code, w.Body.String(), tc.status)
				}
			})
		}
	}
}

// Reads are subject to the read isolation of /api/*: a cross-site GET never reaches the use case.
func TestHealthReadIsolation(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/api/v1/apps/" + appID + "/health", "/api/v1/health/orphan-hosts"} {
		fake := &fakeAdmin{}
		h := newTestHandler(t, fakeService{}, fake, nil)
		for _, site := range []string{"same-site", "cross-site"} {
			if w := do(h, http.MethodGet, path, "", map[string]string{"Sec-Fetch-Site": site}); w.Code != http.StatusForbidden || fake.calls != 0 {
				t.Fatalf("%s with Sec-Fetch-Site %s: status %d, calls %d", path, site, w.Code, fake.calls)
			}
		}
	}
}
