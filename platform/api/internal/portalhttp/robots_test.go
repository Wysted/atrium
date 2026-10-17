package portalhttp

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"platform/api/internal/portal"
)

const robotID = "01a14296-1449-7be0-957b-4b1ad8d7a0bb"

// robotCalls is the robot part of fakeAdmin.
type robotCalls struct {
	newRobot   portal.NewRobot
	robotPatch portal.RobotPatch
	robots     portal.Page[portal.Robot]
}

func (f *fakeAdmin) ListRobots(_ context.Context, caller string, req portal.PageRequest, status string) (portal.Page[portal.Robot], error) {
	f.req, f.status = req, status
	return f.robots, f.note(caller, "")
}

func (f *fakeAdmin) GetRobot(_ context.Context, caller, id string) (portal.Robot, error) {
	return portal.Robot{
		ID: id, Name: "ci", Status: "active", Detail: true, OwnerUserID: callerID, OwnerEmail: "o@example.com", OwnerName: "Owner",
		CreatedAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
	}, f.note(caller, id)
}

func (f *fakeAdmin) CreateRobot(_ context.Context, caller string, in portal.NewRobot) (portal.Robot, error) {
	f.newRobot = in
	return portal.Robot{ID: robotID, Name: in.Name, Status: "active", Detail: true, OwnerUserID: in.OwnerUserID}, f.note(caller, "")
}

func (f *fakeAdmin) UpdateRobot(_ context.Context, caller, id string, p portal.RobotPatch) (portal.Robot, error) {
	f.robotPatch = p
	return portal.Robot{ID: id, Name: "ci", Status: "disabled", Detail: true}, f.note(caller, id)
}

func (f *fakeAdmin) CreateRobotToken(ctx context.Context, caller, id string, in portal.NewToken) (portal.CreatedToken, error) {
	f.newToken, f.ctxToken = in, portal.TokenFrom(ctx)
	exp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	return portal.CreatedToken{
		APIToken: portal.APIToken{ID: tokenID, UserID: id, Name: in.Name, Scope: in.Scope, CreatedAt: exp.Add(-time.Hour), ExpiresAt: exp},
		Secret:   "ptk_secret",
	}, f.note(caller, id)
}

func (f *fakeAdmin) ListRobotTokens(_ context.Context, caller, id string, req portal.PageRequest) (portal.Page[portal.APIToken], error) {
	f.req = req
	return f.tokens, f.note(caller, id)
}

func TestRobotRoutes(t *testing.T) {
	t.Parallel()
	rp := robotPath()
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		status int
	}{
		{"list", http.MethodGet, PathRobots, "", http.StatusOK},
		{"create", http.MethodPost, PathRobots, `{"name":"ci"}`, http.StatusCreated},
		{"patch", http.MethodPatch, rp, `{"status":"disabled"}`, http.StatusOK},
		{"list tokens", http.MethodGet, rp + "/tokens", "", http.StatusOK},
		{"create token", http.MethodPost, rp + "/tokens", `{"name":"n","scope":"read"}`, http.StatusCreated},
		{"get one", http.MethodGet, rp, "", http.StatusOK},
		{"no DELETE of a robot", http.MethodDelete, rp, "{}", http.StatusNotFound},
		{"no PUT of the collection", http.MethodPut, PathRobots, "{}", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := do(newTestHandler(t, fakeService{}, &fakeAdmin{}, nil), tt.method, tt.path, tt.body, nil)
			if w.Code != tt.status {
				t.Fatalf("status %d (want %d), body %s", w.Code, tt.status, w.Body.String())
			}
		})
	}
}

func TestRobotRequestsAndResponses(t *testing.T) {
	t.Parallel()
	made := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := &fakeAdmin{robots: portal.Page[portal.Robot]{Items: []portal.Robot{
		{ID: robotID, Name: "ci", Status: "active", Detail: true, OwnerUserID: callerID, CreatedAt: made},
		{ID: "01a14296-1449-7be0-957b-4b1ad8d7a0cc", Name: "bot", Status: "disabled"}, // what a manager gets
	}}}
	h := newTestHandler(t, fakeService{}, fake, nil)

	w := do(h, http.MethodGet, PathRobots+"?limit=7&cursor=abc", "", nil)
	const want = `{"robots":[{"id":"` + robotID + `","name":"ci","status":"active","owner_user_id":"` + callerID +
		`","owner_email":null,"owner_name":null,"created_at":"2030-01-01T00:00:00Z","last_used_at":null},` +
		`{"id":"01a14296-1449-7be0-957b-4b1ad8d7a0cc","name":"bot","status":"disabled"}]}`
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != want || fake.req.Limit != 7 || fake.req.Cursor != "abc" {
		t.Fatalf("status %d body %s req %+v", w.Code, w.Body.String(), fake.req)
	}

	do(h, http.MethodGet, PathRobots+"?status=disabled", "", nil)
	if fake.status != "disabled" {
		t.Fatalf("status filter = %q", fake.status)
	}
	do(h, http.MethodGet, PathRobots, "", nil)
	if fake.status != "" {
		t.Fatalf("status without the parameter = %q", fake.status)
	}

	w = do(h, http.MethodGet, robotPath(), "", nil)
	const wantOne = `{"id":"` + robotID + `","name":"ci","status":"active","owner_user_id":"` + callerID +
		`","owner_email":"o@example.com","owner_name":"Owner","created_at":"2030-01-01T00:00:00Z","last_used_at":null}`
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != wantOne {
		t.Fatalf("get = %d %s\nwant    %s", w.Code, w.Body.String(), wantOne)
	}

	do(h, http.MethodPost, PathRobots, `{"name":"ci","owner_user_id":"`+callerID+`"}`, nil)
	if fake.newRobot.Name != "ci" || fake.newRobot.OwnerUserID != callerID {
		t.Fatalf("create request = %+v", fake.newRobot)
	}

	do(h, http.MethodPatch, robotPath(), `{"owner_user_id":""}`, nil)
	if p := fake.robotPatch; p.Name != nil || p.Status != nil || p.OwnerUserID == nil || *p.OwnerUserID != "" {
		t.Fatalf("an empty owner must reach the use case as a clear: %+v", p)
	}

	for name, req := range map[string]struct{ method, path, body string }{
		"create unknown field": {http.MethodPost, PathRobots, `{"name":"ci","email":"x"}`},
		"patch unknown field":  {http.MethodPatch, robotPath(), `{"kind":"human"}`},
		"patch not an object":  {http.MethodPatch, robotPath(), `[]`},
	} {
		before := fake.calls
		if w := do(h, req.method, req.path, req.body, nil); w.Code != http.StatusBadRequest || fake.calls != before {
			t.Errorf("%s: status %d, reached the use case: %v", name, w.Code, fake.calls != before)
		}
	}
}

func robotPath() string { return "/api/v1/robots/" + robotID }

func TestCreateRobotTokenRequestAndResponse(t *testing.T) {
	t.Parallel()
	fake := &fakeAdmin{}
	h := newTestHandler(t, fakeService{}, fake, nil)
	w := do(h, http.MethodPost, robotPath()+"/tokens", `{"name":"deploy","scope":"admin","expires_in":3600}`, nil)
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"secret":"ptk_secret"`) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d, Cache-Control %q, body %s", w.Code, w.Header().Get("Cache-Control"), w.Body.String())
	}
	if d := fake.newToken.ExpiresIn; fake.id != robotID || fake.newToken.Scope != "admin" || d == nil || *d != time.Hour {
		t.Fatalf("request = %+v for %s", fake.newToken, fake.id)
	}
	// A request authenticated by a token reaches the use case with the token in the context, which refuses it.
	do(h, http.MethodPost, robotPath()+"/tokens", `{"name":"x","scope":"read"}`, map[string]string{"X-Test-Tid": tokenID, "X-Test-Scope": "admin"})
	if fake.ctxToken != tokenID {
		t.Fatalf("context token = %q", fake.ctxToken)
	}
}

func TestRobotErrorMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{"name taken", portal.ErrRobotNameTaken, http.StatusConflict},
		{"robot disabled", portal.ErrRobotDisabled, http.StatusConflict},
		{"token limit", portal.ErrTokenLimit, http.StatusConflict},
		{"not found", portal.ErrNotFound, http.StatusNotFound},
		{"forbidden", portal.ErrForbidden, http.StatusForbidden},
		{"contention", portal.ErrContention, http.StatusServiceUnavailable},
		{"validation", &portal.ValidationError{Problems: []portal.FieldProblem{{Field: "owner_user_id", Code: portal.CodeUnknown}}}, http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newTestHandler(t, fakeService{}, &fakeAdmin{err: tt.err}, nil)
			for _, req := range []struct{ method, path, body string }{
				{http.MethodGet, PathRobots, ""},
				{http.MethodGet, robotPath(), ""},
				{http.MethodPost, PathRobots, `{"name":"ci"}`},
				{http.MethodPatch, robotPath(), `{"status":"active"}`},
				{http.MethodGet, robotPath() + "/tokens", ""},
				{http.MethodPost, robotPath() + "/tokens", `{"name":"n","scope":"read"}`},
			} {
				w := do(h, req.method, req.path, req.body, nil)
				if w.Code != tt.status {
					t.Errorf("%s %s: status %d, want %d", req.method, req.path, w.Code, tt.status)
				}
				if tt.status == http.StatusServiceUnavailable && w.Header().Get("Retry-After") == "" {
					t.Errorf("%s: 503 without Retry-After", req.path)
				}
			}
		})
	}
}

// A read token cannot mutate robots, whatever the use case would say.
func TestReadTokenCannotChangeRobots(t *testing.T) {
	t.Parallel()
	fake := &fakeAdmin{}
	h := newTestHandler(t, fakeService{}, fake, nil)
	read := map[string]string{"X-Test-Tid": tokenID, "X-Test-Scope": "read"}
	for _, req := range []struct{ method, path, body string }{
		{http.MethodPost, PathRobots, `{"name":"ci"}`},
		{http.MethodPatch, robotPath(), `{"status":"disabled"}`},
		{http.MethodPost, robotPath() + "/tokens", `{"name":"n","scope":"read"}`},
	} {
		if w := do(h, req.method, req.path, req.body, read); w.Code != http.StatusForbidden || w.Header().Get("WWW-Authenticate") != insufficient {
			t.Errorf("%s %s: status %d", req.method, req.path, w.Code)
		}
	}
	if fake.calls != 0 {
		t.Fatal("a refused request reached the use case")
	}
}
