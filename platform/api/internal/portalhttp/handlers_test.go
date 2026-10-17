package portalhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"platform/api/internal/portal"
	platformauth "platform/sdk/go"
)

const callerID = "01a14296-1449-7be0-957b-4b1ad8d7a267"

type fakeService struct {
	me      portal.Permissions
	meErr   error
	apps    []portal.HomeApp
	appsErr error
}

func (f fakeService) Me(context.Context, string) (portal.Permissions, error) { return f.me, f.meErr }

func (f fakeService) HomeApps(context.Context, string) ([]portal.HomeApp, error) {
	return f.apps, f.appsErr
}

// testHandler builds the public handler with an auth step that trusts the X-Test-Sub header.
func testHandler(t *testing.T, svc Service, logs *bytes.Buffer) http.Handler {
	t.Helper()
	return newTestHandler(t, svc, &fakeAdmin{}, logs)
}

// newTestHandler is testHandler with a chosen administration service.
func newTestHandler(t *testing.T, svc Service, admin AdminService, logs *bytes.Buffer) http.Handler {
	t.Helper()
	log := discardLog
	if logs != nil {
		log = slog.New(slog.NewTextHandler(logs, nil))
	}
	h, err := New(Config{
		Service: svc,
		Admin:   admin,
		Auth: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sub := r.Header.Get("X-Test-Sub")
				if sub == "" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				claims := &platformauth.Claims{Subject: sub, APITokenID: r.Header.Get("X-Test-Tid"), Scope: r.Header.Get("X-Test-Scope"), SubjectType: r.Header.Get("X-Test-SubType")}
				next.ServeHTTP(w, r.WithContext(platformauth.ContextWithClaims(r.Context(), claims)))
			})
		},
		Logger: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func get(h http.Handler, path string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "https://"+testHost+path, nil)
	r.Host = testHost
	r.Header.Set("X-Test-Sub", callerID)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
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

func TestNewRequiresServiceAndAuth(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{}); err == nil {
		t.Fatal("New accepted an empty config")
	}
}

func TestMeResponse(t *testing.T) {
	t.Parallel()
	svc := fakeService{me: portal.Permissions{
		UserID: callerID, Email: "ada@example.com", Name: "Ada", Status: "active", PlatformAdmin: true,
		Managed:    []portal.InitiativeRef{{ID: "i-1", Key: "alpha", Name: "Alpha"}},
		Supervised: []portal.AreaRef{{ID: "a-1", Key: "ops", Name: "Ops"}},
	}}
	w := get(testHandler(t, svc, nil), PathMe, nil)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, content type %q", w.Code, w.Header().Get("Content-Type"))
	}
	const want = `{"user":{"id":"` + callerID + `","email":"ada@example.com","name":"Ada"},` +
		`"permissions":{"platform_admin":true,"managed_initiatives":[{"id":"i-1","key":"alpha","name":"Alpha"}],` +
		`"supervised_areas":[{"id":"a-1","key":"ops","name":"Ops"}]}}`
	if got := strings.TrimSpace(w.Body.String()); got != want {
		t.Fatalf("body = %s\nwant   %s", got, want)
	}

	// Lists are never null.
	w = get(testHandler(t, fakeService{me: portal.Permissions{UserID: callerID}}, nil), PathMe, nil)
	if !strings.Contains(w.Body.String(), `"managed_initiatives":[],"supervised_areas":[]`) {
		t.Fatalf("empty list body = %s", w.Body.String())
	}
}

func TestHomeAppsResponse(t *testing.T) {
	t.Parallel()
	svc := fakeService{apps: []portal.HomeApp{{
		ID: "app_X", Name: "Web", Hosts: []string{"web.example.test"}, Roles: []string{"editor"},
		Initiative: portal.InitiativeRef{ID: "i-1", Key: "alpha", Name: "Alpha"},
	}}}
	w := get(testHandler(t, svc, nil), PathMeApps, nil)
	const want = `{"apps":[{"id":"app_X","name":"Web","hosts":["web.example.test"],` +
		`"initiative":{"id":"i-1","key":"alpha","name":"Alpha"},"roles":["editor"]}]}`
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != want {
		t.Fatalf("status %d body = %s", w.Code, w.Body.String())
	}

	w = get(testHandler(t, fakeService{}, nil), PathMeApps, nil)
	if strings.TrimSpace(w.Body.String()) != `{"apps":[]}` {
		t.Fatalf("empty home body = %s", w.Body.String())
	}
}

func TestHandlerErrors(t *testing.T) {
	t.Parallel()
	secret := errors.New("pq: password authentication failed for user platform_portal")
	tests := []struct {
		name    string
		svc     fakeService
		headers map[string]string
		path    string
		status  int
	}{
		{"me: inactive user", fakeService{meErr: portal.ErrUserInactive}, nil, PathMe, http.StatusForbidden},
		{"me: internal error", fakeService{meErr: secret}, nil, PathMe, http.StatusInternalServerError},
		{"me: contention", fakeService{meErr: fmt.Errorf("grant: %w: %w", portal.ErrContention, errors.New("deadlock"))}, nil, PathMe, http.StatusServiceUnavailable},
		{"me: deadline", fakeService{meErr: fmt.Errorf("load: %w", context.DeadlineExceeded)}, nil, PathMe, http.StatusServiceUnavailable},
		{"me: sub is not a uuid", fakeService{}, map[string]string{"X-Test-Sub": "not-a-uuid"}, PathMe, http.StatusUnauthorized},
		{"me: sub is SQL", fakeService{}, map[string]string{"X-Test-Sub": "x'; DROP TABLE users;--"}, PathMe, http.StatusUnauthorized},
		{"apps: internal error", fakeService{appsErr: secret}, nil, PathMeApps, http.StatusInternalServerError},
		{"apps: sub is not a uuid", fakeService{}, map[string]string{"X-Test-Sub": "nope"}, PathMeApps, http.StatusUnauthorized},
		{"no assertion", fakeService{}, map[string]string{"X-Test-Sub": ""}, PathMe, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			w := get(testHandler(t, tt.svc, &logs), tt.path, tt.headers)
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d (%s)", w.Code, tt.status, w.Body.String())
			}
			if retry := w.Header().Get("Retry-After"); (retry != "") != strings.Contains(tt.name, "contention") {
				t.Fatalf("Retry-After = %q", retry)
			}
			if strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), "platform_portal") {
				t.Fatalf("internals leaked: %s", w.Body.String())
			}
			if ct := w.Header().Get("Content-Type"); w.Body.Len() > 0 && ct != "application/problem+json" {
				t.Fatalf("content type = %q, want a problem document", ct)
			}
		})
	}
}

func TestRoutes(t *testing.T) {
	t.Parallel()
	h := testHandler(t, fakeService{}, nil)
	tests := []struct {
		name   string
		method string
		path   string
		status int
	}{
		{"unknown api route", http.MethodGet, "/api/v1/nope", http.StatusNotFound},
		{"api root", http.MethodGet, "/api/", http.StatusNotFound},
		{"web without a handler", http.MethodGet, "/", http.StatusNotFound},
		{"head is a read", http.MethodHead, PathMe, http.StatusOK},
		{"options on the api is not served", http.MethodOptions, PathMe, http.StatusNotFound},
		{"post on a read route", http.MethodPost, PathMe, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequestWithContext(context.Background(), tt.method, "https://"+testHost+tt.path, strings.NewReader("{}"))
			r.Host = testHost
			r.Header.Set("X-Test-Sub", callerID)
			r.Header.Set("Content-Type", contentJSON)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d", w.Code, tt.status)
			}
			for name := range w.Header() {
				if strings.HasPrefix(strings.ToLower(name), "access-control-") {
					t.Errorf("response carries %s", name)
				}
			}
		})
	}
}

func TestRequestLogDoesNotLeakRequestData(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	h := testHandler(t, fakeService{me: portal.Permissions{UserID: callerID}}, &logs)
	get(h, PathMe+"?token=s3cr3t", map[string]string{"Cookie": "session=s3cr3t", "Platform-Assertion": "s3cr3t"})
	out := logs.String()
	if !strings.Contains(out, "path=/api/v1/me") || !strings.Contains(out, "status=200") {
		t.Fatalf("log line missing: %q", out)
	}
	if strings.Contains(out, "s3cr3t") {
		t.Fatalf("request data leaked into the log: %q", out)
	}
}

type payload struct {
	Email  string `json:"email"`
	RoleID string `json:"role_id"`
	Nested struct {
		N int `json:"n"`
	} `json:"nested"`
}

func TestDecodeJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		body       string
		wantOK     bool
		wantStatus int
		wantDetail string
	}{
		{"valid", `{"email":"a@example.com","role_id":"r1"}`, true, 0, ""},
		{"valid with whitespace around", " \n {\"email\":\"a@example.com\"} \n", true, 0, ""},
		{"valid empty object", `{}`, true, 0, ""},
		{"unknown field", `{"email":"a@example.com","admin":true}`, false, http.StatusBadRequest, detailBodyInvalid},
		{"unknown nested field", `{"nested":{"n":1,"x":2}}`, false, http.StatusBadRequest, detailBodyInvalid},
		{"trailing second object", `{"email":"a"} {"email":"b"}`, false, http.StatusBadRequest, detailBodyInvalid},
		{"trailing garbage", `{"email":"a"} x`, false, http.StatusBadRequest, detailBodyInvalid},
		{"trailing comma-separated value", `{"email":"a"},{}`, false, http.StatusBadRequest, detailBodyInvalid},
		{"empty body", ``, false, http.StatusBadRequest, detailBodyEmpty},
		{"only whitespace", "  \n", false, http.StatusBadRequest, detailBodyEmpty},
		{"truncated", `{"email":"a`, false, http.StatusBadRequest, detailBodyInvalid},
		{"not json", `email=a`, false, http.StatusBadRequest, detailBodyInvalid},
		{"wrong type", `{"email":42}`, false, http.StatusBadRequest, detailBodyInvalid},
		{"null", `null`, false, http.StatusBadRequest, detailBodyInvalid},
		{"array", `[{"email":"a"}]`, false, http.StatusBadRequest, detailBodyInvalid},
		{"string", `"email"`, false, http.StatusBadRequest, detailBodyInvalid},
		{"too large", `{"email":"` + strings.Repeat("a", MaxBodyBytes) + `"}`, false, http.StatusRequestEntityTooLarge, detailBodyTooLarge},
		{"just under the limit", `{"email":"` + strings.Repeat("a", MaxBodyBytes-len(`{"email":""}`)) + `"}`, true, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(tt.body))
			w := httptest.NewRecorder()
			v, ok := DecodeJSON[payload](w, r)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (status %d, body %s)", ok, tt.wantOK, w.Code, w.Body.String())
			}
			if ok {
				if w.Body.Len() != 0 {
					t.Fatalf("a successful decode wrote a response: %q", w.Body.String())
				}
				return
			}
			if w.Code != tt.wantStatus || w.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("status %d content type %q, want %d problem", w.Code, w.Header().Get("Content-Type"), tt.wantStatus)
			}
			var p struct {
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || p.Detail != tt.wantDetail {
				t.Fatalf("detail = %q (%v), want %q", p.Detail, err, tt.wantDetail)
			}
			if v.Email != "" {
				t.Fatalf("a failed decode returned data: %+v", v)
			}
		})
	}
}

// The decoder's messages quote field names and values; none of that may reach the client.
func TestDecodeJSONDoesNotEchoInput(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(`{"s3cr3t_field": "s3cr3t_value"}`))
	w := httptest.NewRecorder()
	if _, ok := DecodeJSON[payload](w, r); ok {
		t.Fatal("unknown field accepted")
	}
	if strings.Contains(w.Body.String(), "s3cr3t") {
		t.Fatalf("input echoed: %s", w.Body.String())
	}
}

func TestWriteJSONFailureIsAClean500(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	WriteJSON(w, http.StatusOK, map[string]any{"bad": make(chan int)})
	if w.Code != http.StatusInternalServerError || w.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("status %d content type %q", w.Code, w.Header().Get("Content-Type"))
	}
}

// With a web handler set, only paths outside /api/ reach it: an unknown API path stays a JSON 404.
func TestUnknownAPIPathNeverReachesWebHandler(t *testing.T) {
	t.Parallel()
	web := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>spa</html>"))
	})
	h, err := New(Config{
		Service: fakeService{}, Admin: &fakeAdmin{}, Web: web, Logger: discardLog,
		Auth: func(next http.Handler) http.Handler { return next },
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/unknown", "/api/", "/api/v1/nope"} {
		w := get(h, path, nil)
		if w.Code != http.StatusNotFound || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
			t.Errorf("%s: status = %d, Content-Type = %q", path, w.Code, w.Header().Get("Content-Type"))
		}
		if strings.Contains(w.Body.String(), "spa") {
			t.Errorf("%s: served the web handler", path)
		}
	}
	if w := get(h, "/some/route", nil); w.Body.String() != "<html>spa</html>" {
		t.Errorf("web route body = %q", w.Body.String())
	}
}
