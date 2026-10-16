package portalhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"platform/api/internal/portal"
)

const (
	grantID      = "01a14296-1449-7be0-957b-4b1ad8d7a001"
	initiativeID = "01a14296-1449-7be0-957b-4b1ad8d7a002"
	groupID      = "01a14296-1449-7be0-957b-4b1ad8d7a003"
	targetUserID = "01a14296-1449-7be0-957b-4b1ad8d7a004"
)

// fakeAdmin records what the handlers pass to the use cases and returns canned results.
type fakeAdmin struct {
	err error // returned by every method

	caller, id string
	req        portal.PageRequest
	newGrant   portal.NewGrant
	newGroup   portal.NewGroup
	email      string
	status     string
	auditQuery portal.AuditQuery
	calls      int

	grants  portal.Page[portal.Grant]
	groups  portal.Page[portal.Group]
	members portal.Page[portal.GroupMember]
	events  portal.Page[portal.AuditEvent]
	users   []portal.User

	health  portal.AppHealth
	orphans []portal.OrphanHost

	catalogCalls
}

func (f *fakeAdmin) note(caller, id string) error {
	f.calls++
	f.caller, f.id = caller, id
	return f.err
}

func (f *fakeAdmin) ListGrants(_ context.Context, caller, id string, req portal.PageRequest) (portal.Page[portal.Grant], error) {
	f.req = req
	return f.grants, f.note(caller, id)
}

func (f *fakeAdmin) CreateGrant(_ context.Context, caller, id string, in portal.NewGrant) (portal.Grant, error) {
	f.newGrant = in
	return portal.Grant{
		ID: grantID, InitiativeID: id, Role: portal.RoleRef{ID: "r", Key: "editor"},
		Group: &portal.GroupRef{ID: groupID, Key: "ops", Name: "Ops"},
	}, f.note(caller, id)
}

func (f *fakeAdmin) DeleteGrant(_ context.Context, caller, id string) error {
	return f.note(caller, id)
}

func (f *fakeAdmin) ListGroups(_ context.Context, caller string, req portal.PageRequest) (portal.Page[portal.Group], error) {
	f.req = req
	return f.groups, f.note(caller, "")
}

func (f *fakeAdmin) CreateGroup(_ context.Context, caller string, in portal.NewGroup) (portal.Group, error) {
	f.newGroup = in
	return portal.Group{ID: groupID, Key: in.Key, Name: in.Name}, f.note(caller, "")
}

func (f *fakeAdmin) DeleteGroup(_ context.Context, caller, id string) error {
	return f.note(caller, id)
}

func (f *fakeAdmin) ListGroupMembers(_ context.Context, caller, id string, req portal.PageRequest) (portal.Page[portal.GroupMember], error) {
	f.req = req
	return f.members, f.note(caller, id)
}

func (f *fakeAdmin) AddGroupMember(_ context.Context, caller, id, email string) (portal.GroupMember, error) {
	f.email = email
	return portal.GroupMember{UserID: targetUserID, Email: email, Status: "active"}, f.note(caller, id)
}

func (f *fakeAdmin) RemoveGroupMember(_ context.Context, caller, id, userID string) error {
	f.email = userID
	return f.note(caller, id)
}

func (f *fakeAdmin) FindUsers(_ context.Context, caller, email string) ([]portal.User, error) {
	f.email = email
	return f.users, f.note(caller, "")
}

func (f *fakeAdmin) SetUserStatus(_ context.Context, caller, id, status string) (portal.User, error) {
	f.status = status
	return portal.User{ID: id, Email: "a@example.com", Status: status}, f.note(caller, id)
}

func (f *fakeAdmin) RevokeUserSessions(_ context.Context, caller, id string) (int64, error) {
	return 3, f.note(caller, id)
}

func (f *fakeAdmin) ListAudit(_ context.Context, caller string, q portal.AuditQuery) (portal.Page[portal.AuditEvent], error) {
	f.auditQuery = q
	return f.events, f.note(caller, "")
}

// do sends a same-origin JSON request as the default caller.
func do(h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), method, "https://"+testHost+path, strings.NewReader(body))
	r.Host = testHost
	r.Header.Set("X-Test-Sub", callerID)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if method != http.MethodGet {
		r.Header.Set("Content-Type", contentJSON)
	}
	for k, v := range headers {
		if v == "" {
			r.Header.Del(k)
			continue
		}
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAdminRoutes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		status int
		want   string // substring of the body
	}{
		{"list grants", http.MethodGet, "/api/v1/initiatives/" + initiativeID + "/grants", "", http.StatusOK, `{"grants":[]}`},
		{"create grant for a group", http.MethodPost, "/api/v1/initiatives/" + initiativeID + "/grants", `{"group_id":"` + groupID + `","role_id":"r"}`, http.StatusCreated, `"user":null,"group":{"id":"` + groupID + `"`},
		{"delete grant", http.MethodDelete, "/api/v1/grants/" + grantID, "{}", http.StatusNoContent, ""},
		{"list groups", http.MethodGet, "/api/v1/groups", "", http.StatusOK, `{"groups":[]}`},
		{"create group", http.MethodPost, "/api/v1/groups", `{"key":"ops","name":"Ops"}`, http.StatusCreated, `"key":"ops"`},
		{"delete group", http.MethodDelete, "/api/v1/groups/" + groupID, "{}", http.StatusNoContent, ""},
		{"list members", http.MethodGet, "/api/v1/groups/" + groupID + "/members", "", http.StatusOK, `{"members":[]}`},
		{"add member", http.MethodPost, "/api/v1/groups/" + groupID + "/members", `{"email":"b@example.com"}`, http.StatusCreated, `"email":"b@example.com"`},
		{"remove member", http.MethodDelete, "/api/v1/groups/" + groupID + "/members/" + targetUserID, "", http.StatusNoContent, ""},
		{"find users", http.MethodGet, "/api/v1/users?email=a@example.com", "", http.StatusOK, `{"users":[]}`},
		{"patch user", http.MethodPatch, "/api/v1/users/" + targetUserID, `{"status":"disabled"}`, http.StatusOK, `"status":"disabled"`},
		{"revoke sessions", http.MethodDelete, "/api/v1/users/" + targetUserID + "/sessions", "{}", http.StatusOK, `{"revoked":3}`},
		{"list audit", http.MethodGet, "/api/v1/audit", "", http.StatusOK, `{"events":[]}`},
		{"unknown method on a collection", http.MethodPut, "/api/v1/groups", "{}", http.StatusNotFound, ""},
		{"patch on grants", http.MethodPatch, "/api/v1/grants/" + grantID, "{}", http.StatusNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeAdmin{}
			w := do(newTestHandler(t, fakeService{}, fake, nil), tt.method, tt.path, tt.body, nil)
			if w.Code != tt.status || !strings.Contains(w.Body.String(), tt.want) {
				t.Fatalf("status %d body %s, want %d containing %q", w.Code, w.Body.String(), tt.status, tt.want)
			}
			if tt.status >= http.StatusInternalServerError || tt.status == http.StatusNotFound {
				return
			}
			if fake.caller != callerID {
				t.Fatalf("use case called as %q, want the assertion subject", fake.caller)
			}
		})
	}
}

func TestAdminRoutePassesIDsAndBodies(t *testing.T) {
	t.Parallel()
	exp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	fake := &fakeAdmin{}
	h := newTestHandler(t, fakeService{}, fake, nil)

	do(h, http.MethodPost, "/api/v1/initiatives/"+initiativeID+"/grants",
		`{"email":"b@example.com","role_id":"r1","expires_at":"2030-01-02T03:04:05Z"}`, nil)
	want := portal.NewGrant{Email: "b@example.com", RoleID: "r1", ExpiresAt: &exp}
	if fake.id != initiativeID || fake.newGrant.Email != want.Email || fake.newGrant.RoleID != want.RoleID ||
		fake.newGrant.ExpiresAt == nil || !fake.newGrant.ExpiresAt.Equal(exp) || fake.newGrant.GroupID != "" {
		t.Fatalf("grant call = %q %+v", fake.id, fake.newGrant)
	}

	do(h, http.MethodPatch, "/api/v1/users/"+targetUserID, `{"status":"active"}`, nil)
	if fake.id != targetUserID || fake.status != "active" {
		t.Fatalf("patch call = %q %q", fake.id, fake.status)
	}

	do(h, http.MethodGet, "/api/v1/users?email=Ada%40Example.com", "", nil)
	if fake.email != "Ada@Example.com" {
		t.Fatalf("email = %q", fake.email)
	}

	do(h, http.MethodGet, "/api/v1/audit?initiative_id="+initiativeID+"&cursor=abc&limit=7", "", nil)
	if fake.auditQuery.InitiativeID != initiativeID || fake.auditQuery.Page.Cursor != "abc" || fake.auditQuery.Page.Limit != 7 {
		t.Fatalf("audit query = %+v", fake.auditQuery)
	}

	do(h, http.MethodDelete, "/api/v1/groups/"+groupID+"/members/"+targetUserID, "", nil)
	if fake.id != groupID || fake.email != targetUserID {
		t.Fatalf("remove member call = %q %q", fake.id, fake.email)
	}
}

func TestAdminErrorMapping(t *testing.T) {
	t.Parallel()
	secret := errors.New("pq: password authentication failed for user platform_portal")
	tests := []struct {
		name   string
		err    error
		status int
		detail string
		retry  bool
	}{
		{"forbidden", portal.ErrForbidden, http.StatusForbidden, "", false},
		{"inactive caller", portal.ErrUserInactive, http.StatusForbidden, "", false},
		{"not found", portal.ErrNotFound, http.StatusNotFound, "", false},
		{"wrapped not found", fmt.Errorf("get grant: %w", portal.ErrNotFound), http.StatusNotFound, "", false},
		{"duplicate grant", portal.ErrGrantExists, http.StatusConflict, portal.ErrGrantExists.Detail, false},
		{"last manager", fmt.Errorf("revoke: %w", portal.ErrLastManager), http.StatusConflict, portal.ErrLastManager.Detail, false},
		{"group in use", portal.ErrGroupInUse, http.StatusConflict, portal.ErrGroupInUse.Detail, false},
		{"self disable", portal.ErrSelfDisable, http.StatusConflict, portal.ErrSelfDisable.Detail, false},
		{"bad cursor", portal.ErrInvalidCursor, http.StatusBadRequest, detailCursorInvalid, false},
		{"contention", fmt.Errorf("x: %w: %w", portal.ErrContention, errors.New("deadlock")), http.StatusServiceUnavailable, "", true},
		{"deadline", fmt.Errorf("x: %w", context.DeadlineExceeded), http.StatusServiceUnavailable, "", false},
		{"internal", secret, http.StatusInternalServerError, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			h := newTestHandler(t, fakeService{}, &fakeAdmin{err: tt.err}, &logs)
			w := do(h, http.MethodDelete, "/api/v1/grants/"+grantID, "{}", nil)
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d (%s)", w.Code, tt.status, w.Body.String())
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Fatalf("content type = %q", ct)
			}
			var p struct {
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || p.Detail != tt.detail {
				t.Fatalf("detail = %q (%v), want %q", p.Detail, err, tt.detail)
			}
			if got := w.Header().Get("Retry-After") != ""; got != tt.retry {
				t.Fatalf("Retry-After present = %v, want %v", got, tt.retry)
			}
			if strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), "platform_portal") {
				t.Fatalf("internals leaked: %s", w.Body.String())
			}
		})
	}
}

func TestValidationProblemListsFieldsAndNeverEchoesInput(t *testing.T) {
	t.Parallel()
	err := &portal.ValidationError{Problems: []portal.FieldProblem{
		{Field: "email", Code: portal.CodeNoUniqueActiveUser}, {Field: "expires_at", Code: portal.CodeNotInTheFuture},
	}}
	h := newTestHandler(t, fakeService{}, &fakeAdmin{err: err}, nil)
	w := do(h, http.MethodPost, "/api/v1/initiatives/"+initiativeID+"/grants",
		`{"email":"s3cr3t@example.com","role_id":"s3cr3t-role"}`, nil)
	if w.Code != http.StatusUnprocessableEntity || w.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("status %d content type %q", w.Code, w.Header().Get("Content-Type"))
	}
	var p struct {
		Errors []struct{ Field, Code string } `json:"errors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || len(p.Errors) != 2 ||
		p.Errors[0].Field != "email" || p.Errors[0].Code != portal.CodeNoUniqueActiveUser ||
		p.Errors[1].Field != "expires_at" {
		t.Fatalf("errors = %+v (%v)", p.Errors, err)
	}
	if strings.Contains(w.Body.String(), "s3cr3t") {
		t.Fatalf("input echoed: %s", w.Body.String())
	}
}

func TestBadBodiesNeverReachTheUseCase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		method  string
		path    string
		body    string
		headers map[string]string
		status  int
	}{
		{"unknown field", http.MethodPost, "/api/v1/groups", `{"key":"a","admin":true}`, nil, http.StatusBadRequest},
		{"empty body", http.MethodPost, "/api/v1/groups", ``, nil, http.StatusBadRequest},
		{"wrong type", http.MethodPatch, "/api/v1/users/" + targetUserID, `{"status":1}`, nil, http.StatusBadRequest},
		{"expires_at is not a time", http.MethodPost, "/api/v1/initiatives/" + initiativeID + "/grants", `{"expires_at":"tomorrow"}`, nil, http.StatusBadRequest},
		{"not json content type", http.MethodPost, "/api/v1/groups", `{"key":"a"}`, map[string]string{"Content-Type": "text/plain"}, http.StatusUnsupportedMediaType},
		{"form content type", http.MethodPatch, "/api/v1/users/" + targetUserID, `status=disabled`, map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, http.StatusUnsupportedMediaType},
		{"delete without json", http.MethodDelete, "/api/v1/grants/" + grantID, ``, map[string]string{"Content-Type": ""}, http.StatusUnsupportedMediaType},
		{"cross-site post", http.MethodPost, "/api/v1/groups", `{"key":"a"}`, map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"the old body-based removal is gone", http.MethodDelete, "/api/v1/groups/" + groupID + "/members", `{"user_id":"` + targetUserID + `"}`, nil, http.StatusNotFound},
		{"no assertion", http.MethodPost, "/api/v1/groups", `{"key":"a"}`, map[string]string{"X-Test-Sub": ""}, http.StatusUnauthorized},
		{"subject is not a uuid", http.MethodDelete, "/api/v1/grants/" + grantID, `{}`, map[string]string{"X-Test-Sub": "nope"}, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeAdmin{}
			w := do(newTestHandler(t, fakeService{}, fake, nil), tt.method, tt.path, tt.body, tt.headers)
			if w.Code != tt.status || fake.calls != 0 {
				t.Fatalf("status %d after %d calls, want %d and none (%s)", w.Code, fake.calls, tt.status, w.Body.String())
			}
		})
	}
}

func TestLimitParameter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		query     string
		status    int
		wantLimit int
	}{
		{"", http.StatusOK, 0},
		{"?limit=25", http.StatusOK, 25},
		{"?limit=100000", http.StatusOK, 100000}, // the use case caps it
		{"?limit=0", http.StatusUnprocessableEntity, 0},
		{"?limit=-3", http.StatusUnprocessableEntity, 0},
		{"?limit=abc", http.StatusUnprocessableEntity, 0},
		{"?limit=1.5", http.StatusUnprocessableEntity, 0},
		{"?limit=99999999999999999999", http.StatusUnprocessableEntity, 0},
	}
	for _, path := range []string{"/api/v1/groups", "/api/v1/audit", "/api/v1/initiatives/" + initiativeID + "/grants", "/api/v1/groups/" + groupID + "/members"} {
		for _, tt := range tests {
			t.Run(path+tt.query, func(t *testing.T) {
				t.Parallel()
				fake := &fakeAdmin{}
				w := do(newTestHandler(t, fakeService{}, fake, nil), http.MethodGet, path+tt.query, "", nil)
				if w.Code != tt.status {
					t.Fatalf("status = %d, want %d (%s)", w.Code, tt.status, w.Body.String())
				}
				if tt.status == http.StatusOK && fake.req.Limit != tt.wantLimit && fake.auditQuery.Page.Limit != tt.wantLimit {
					t.Fatalf("limit passed = %d / %d, want %d", fake.req.Limit, fake.auditQuery.Page.Limit, tt.wantLimit)
				}
				if tt.status == http.StatusUnprocessableEntity && (!strings.Contains(w.Body.String(), `"field":"limit"`) || fake.calls != 0) {
					t.Fatalf("422 without the field name or a call was made: %s", w.Body.String())
				}
			})
		}
	}
}

func TestListResponsesCarryTheNextCursor(t *testing.T) {
	t.Parallel()
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	fake := &fakeAdmin{
		groups: portal.Page[portal.Group]{Items: []portal.Group{{ID: groupID, Key: "ops", Name: "Ops", CreatedAt: at}}, NextCursor: "next"},
		events: portal.Page[portal.AuditEvent]{Items: []portal.AuditEvent{{
			ID: grantID, OccurredAt: at, ActorID: callerID, Action: "grant.create", TargetType: "grant", TargetID: grantID,
			After: json.RawMessage(`{"k":"v"}`),
		}}},
	}
	h := newTestHandler(t, fakeService{}, fake, nil)
	w := do(h, http.MethodGet, "/api/v1/groups", "", nil)
	const wantGroups = `{"groups":[{"id":"` + groupID + `","key":"ops","name":"Ops","created_at":"2030-01-02T03:04:05Z"}],"next_cursor":"next"}`
	if strings.TrimSpace(w.Body.String()) != wantGroups {
		t.Fatalf("groups = %s\nwant    %s", w.Body.String(), wantGroups)
	}
	w = do(h, http.MethodGet, "/api/v1/audit", "", nil)
	const wantAudit = `{"events":[{"id":"` + grantID + `","occurred_at":"2030-01-02T03:04:05Z","actor_id":"` + callerID +
		`","actor_email":null,"initiative_id":null,"action":"grant.create","target_type":"grant","target_id":"` + grantID +
		`","before":null,"after":{"k":"v"}}]}`
	if strings.TrimSpace(w.Body.String()) != wantAudit {
		t.Fatalf("audit = %s\nwant    %s", w.Body.String(), wantAudit)
	}
}

func TestNewRequiresTheAdminService(t *testing.T) {
	t.Parallel()
	_, err := New(Config{Service: fakeService{}, Auth: func(h http.Handler) http.Handler { return h }})
	if err == nil {
		t.Fatal("New accepted a config without the admin service")
	}
}

// The assertion subject is lowercased before it reaches the use cases, which compare ids as stored.
func TestCallerIDIsLowercased(t *testing.T) {
	t.Parallel()
	fake := &fakeAdmin{}
	h := newTestHandler(t, fakeService{}, fake, nil)
	do(h, http.MethodGet, "/api/v1/groups", "", map[string]string{"X-Test-Sub": strings.ToUpper(callerID)})
	if fake.caller != callerID {
		t.Fatalf("caller = %q, want %q", fake.caller, callerID)
	}
}
