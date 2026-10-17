package portalhttp

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"platform/api/internal/portal"
)

const areaID = "01a14296-1449-7be0-957b-4b1ad8d7a0aa"

// areaCalls is the area part of fakeAdmin.
type areaCalls struct {
	newArea portal.NewArea
	areas   portal.Page[portal.Area]
	people  portal.Page[portal.AreaPerson]
	userID  string
}

func (f *fakeAdmin) ListAreas(_ context.Context, caller string, req portal.PageRequest) (portal.Page[portal.Area], error) {
	f.req = req
	return f.areas, f.note(caller, "")
}

func (f *fakeAdmin) GetArea(_ context.Context, caller, id string) (portal.Area, error) {
	return portal.Area{ID: id, Key: "ops", Name: "Ops", CreatedAt: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}, f.note(caller, id)
}

func (f *fakeAdmin) CreateArea(_ context.Context, caller string, in portal.NewArea) (portal.Area, error) {
	f.newArea = in
	return portal.Area{ID: areaID, Key: in.Key, Name: in.Name}, f.note(caller, "")
}

func (f *fakeAdmin) DeleteArea(_ context.Context, caller, id string) error { return f.note(caller, id) }

func (f *fakeAdmin) person(email string) portal.AreaPerson {
	return portal.AreaPerson{UserID: targetUserID, Email: email, Name: "N", Status: "active", AddedAt: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}
}

func (f *fakeAdmin) ListAreaMembers(_ context.Context, caller, id string, req portal.PageRequest) (portal.Page[portal.AreaPerson], error) {
	f.req = req
	return f.people, f.note(caller, id)
}

func (f *fakeAdmin) AddAreaMember(_ context.Context, caller, id, email string) (portal.AreaPerson, error) {
	f.email = email
	return f.person(email), f.note(caller, id)
}

func (f *fakeAdmin) RemoveAreaMember(_ context.Context, caller, id, userID string) error {
	f.userID = userID
	return f.note(caller, id)
}

func (f *fakeAdmin) ListAreaSupervisors(_ context.Context, caller, id string, req portal.PageRequest) (portal.Page[portal.AreaPerson], error) {
	f.req = req
	return f.people, f.note(caller, id)
}

func (f *fakeAdmin) AddAreaSupervisor(_ context.Context, caller, id, email string) (portal.AreaPerson, error) {
	f.email = email
	return f.person(email), f.note(caller, id)
}

func (f *fakeAdmin) RemoveAreaSupervisor(_ context.Context, caller, id, userID string) error {
	f.userID = userID
	return f.note(caller, id)
}

func TestAreaRoutes(t *testing.T) {
	t.Parallel()
	const person = `{"user_id":"` + targetUserID + `","email":"b@example.com","name":"N","status":"active","added_at":"2030-01-02T03:04:05Z"}`
	base := "/api/v1/areas/" + areaID
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		status int
		want   string // exact body, trimmed; empty means no body
	}{
		{"list", http.MethodGet, PathAreas, "", http.StatusOK, `{"areas":[]}`},
		{"get", http.MethodGet, base, "", http.StatusOK, `{"id":"` + areaID + `","key":"ops","name":"Ops","created_at":"2030-01-02T03:04:05Z"}`},
		{"create", http.MethodPost, PathAreas, `{"key":"ops","name":"Ops"}`, http.StatusCreated, `{"id":"` + areaID + `","key":"ops","name":"Ops","created_at":"0001-01-01T00:00:00Z"}`},
		{"delete", http.MethodDelete, base, "{}", http.StatusNoContent, ""},
		{"list members", http.MethodGet, base + "/members", "", http.StatusOK, `{"members":[]}`},
		{"add member", http.MethodPost, base + "/members", `{"email":"b@example.com"}`, http.StatusCreated, person},
		{"remove member", http.MethodDelete, base + "/members/" + targetUserID, "", http.StatusNoContent, ""},
		{"list supervisors", http.MethodGet, base + "/supervisors", "", http.StatusOK, `{"supervisors":[]}`},
		{"add supervisor", http.MethodPost, base + "/supervisors", `{"email":"b@example.com"}`, http.StatusCreated, person},
		{"remove supervisor", http.MethodDelete, base + "/supervisors/" + targetUserID, "", http.StatusNoContent, ""},
		{"no PATCH of an area", http.MethodPatch, base, "{}", http.StatusNotFound, ""},
		{"no PUT of the collection", http.MethodPut, PathAreas, "{}", http.StatusNotFound, ""},
		{"no body-based removal", http.MethodDelete, base + "/members", `{"user_id":"` + targetUserID + `"}`, http.StatusNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeAdmin{}
			w := do(newTestHandler(t, fakeService{}, fake, nil), tt.method, tt.path, tt.body, nil)
			if w.Code != tt.status {
				t.Fatalf("status %d body %s, want %d", w.Code, w.Body.String(), tt.status)
			}
			if tt.status == http.StatusNotFound {
				return
			}
			if got := strings.TrimSpace(w.Body.String()); got != tt.want {
				t.Fatalf("body = %s\nwant   %s", got, tt.want)
			}
			if fake.caller != callerID {
				t.Fatalf("use case called as %q, want the assertion subject", fake.caller)
			}
		})
	}
}

func TestAreaRequestsPassIDsAndBodies(t *testing.T) {
	t.Parallel()
	fake := &fakeAdmin{}
	h := newTestHandler(t, fakeService{}, fake, nil)

	do(h, http.MethodPost, PathAreas, `{"key":"ops","name":"Ops"}`, nil)
	if fake.newArea != (portal.NewArea{Key: "ops", Name: "Ops"}) {
		t.Fatalf("new area = %+v", fake.newArea)
	}
	do(h, http.MethodGet, PathAreas+"?limit=7&cursor=abc", "", nil)
	if fake.req.Limit != 7 || fake.req.Cursor != "abc" {
		t.Fatalf("page request = %+v", fake.req)
	}
	do(h, http.MethodDelete, "/api/v1/areas/"+areaID+"/supervisors/"+targetUserID, "", nil)
	if fake.id != areaID || fake.userID != targetUserID {
		t.Fatalf("remove supervisor call = %q %q", fake.id, fake.userID)
	}
	do(h, http.MethodPost, "/api/v1/areas/"+areaID+"/members", `{"email":"B@example.com"}`, nil)
	if fake.id != areaID || fake.email != "B@example.com" {
		t.Fatalf("add member call = %q %q", fake.id, fake.email)
	}
}

func TestAreaErrorsAndBadBodies(t *testing.T) {
	t.Parallel()
	for name, tt := range map[string]struct {
		err    error
		status int
	}{
		"area in use":       {portal.ErrAreaInUse, http.StatusConflict},
		"key taken":         {portal.ErrAreaKeyTaken, http.StatusConflict},
		"already in a area": {portal.ErrAreaMemberExists, http.StatusConflict},
		"not found":         {portal.ErrNotFound, http.StatusNotFound},
		"forbidden":         {portal.ErrForbidden, http.StatusForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := do(newTestHandler(t, fakeService{}, &fakeAdmin{err: tt.err}, nil), http.MethodDelete, "/api/v1/areas/"+areaID, "{}", nil)
			if w.Code != tt.status {
				t.Fatalf("status %d, want %d (%s)", w.Code, tt.status, w.Body.String())
			}
		})
	}
	fake := &fakeAdmin{}
	h := newTestHandler(t, fakeService{}, fake, nil)
	for _, body := range []string{`{"key":"a","admin":true}`, ``} {
		if w := do(h, http.MethodPost, PathAreas, body, nil); w.Code != http.StatusBadRequest {
			t.Errorf("body %q: status %d", body, w.Code)
		}
	}
	if fake.calls != 0 {
		t.Fatal("a bad body reached the use case")
	}
}
