package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testToken = "pt_super-secret-token-value"
	iid       = "11111111-1111-4111-8111-111111111111"
	otherIID  = "22222222-2222-4222-8222-222222222222"
	roleOps   = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	roleView  = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	robotID   = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

// fakePortal is the slice of the portal API the CLI uses, with one page boundary in the initiatives listing.
type fakePortal struct {
	mu      sync.Mutex
	grants  []map[string]any
	robots  []map[string]any            // nil means the default single robot
	post    func(w http.ResponseWriter) // overrides the answer of POST /grants
	del     func(w http.ResponseWriter) // overrides the answer of DELETE /grants/{id}
	posts   []map[string]any
	deleted []string
	badAuth int
}

func grantJSON(id, roleID string, user, group map[string]any) map[string]any {
	return map[string]any{"id": id, "role": map[string]any{"id": roleID}, "user": user, "group": group}
}

// pageOne answers a list one item per page, the cursor being the index of the next item.
func pageOne(key string, items []map[string]any, cursor string) map[string]any {
	i, _ := strconv.Atoi(cursor)
	out := map[string]any{key: []map[string]any{}}
	if i < len(items) {
		out[key] = items[i : i+1]
		if i+1 < len(items) {
			out["next_cursor"] = strconv.Itoa(i + 1)
		}
	}
	return out
}

func problem(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func (f *fakePortal) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		f.badAuth++
	}
	write := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	// Same rule as portalhttp.RequireJSON: every unsafe method, DELETE included, must say application/json.
	if r.Method != http.MethodGet && r.Header.Get("Content-Type") != "application/json" {
		write(http.StatusUnsupportedMediaType, map[string]any{"status": http.StatusUnsupportedMediaType, "detail": "content type must be application/json"})
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /api/v1/initiatives":
		if r.URL.Query().Get("cursor") == "" {
			write(http.StatusOK, map[string]any{"initiatives": []map[string]any{{"id": otherIID, "key": "other"}}, "next_cursor": "p2"})
			return
		}
		write(http.StatusOK, map[string]any{"initiatives": []map[string]any{{"id": iid, "key": "demo"}}})
	case "GET /api/v1/initiatives/" + iid + "/roles":
		write(http.StatusOK, map[string]any{"roles": []map[string]any{{"id": roleOps, "key": "ops"}, {"id": roleView, "key": "viewer"}}})
	case "GET /api/v1/robots":
		robots := f.robots
		if robots == nil {
			robots = []map[string]any{{"id": robotID, "name": "Deploy-Bot", "status": "active"}}
		}
		write(http.StatusOK, pageOne("robots", robots, r.URL.Query().Get("cursor")))
	case "GET /api/v1/initiatives/" + iid + "/grants":
		write(http.StatusOK, pageOne("grants", f.grants, r.URL.Query().Get("cursor")))
	case "POST /api/v1/initiatives/" + iid + "/grants":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.posts = append(f.posts, in)
		if f.post != nil {
			f.post(w)
			return
		}
		write(http.StatusCreated, map[string]any{"id": "new"})
	default:
		if id, ok := strings.CutPrefix(r.URL.Path, "/api/v1/grants/"); ok && r.Method == http.MethodDelete {
			f.deleted = append(f.deleted, id)
			if f.del != nil {
				f.del(w)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"title":"Not Found","status":404}`))
	}
}

func conflict(w http.ResponseWriter) {
	problem(w, http.StatusConflict, `{"title":"Conflict","status":409,"detail":"a grant for this subject and role already exists"}`)
}

func invoke(t *testing.T, srv *httptest.Server, url string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	env := map[string]string{envURL: url, envToken: testToken}
	var out, errb bytes.Buffer
	code = run(context.Background(), args, func(k string) string { return env[k] }, &out, &errb, srv.Client().Transport)
	return code, out.String(), errb.String()
}

func TestGrant(t *testing.T) {
	tests := []struct {
		name      string
		existing  bool
		subject   string
		wantOut   string
		wantEmail string
		wantRobot string
	}{
		{name: "created", subject: "Ana@Example.com", wantOut: "changed\ngrant done: ana@example.com -> demo/ops\n", wantEmail: "ana@example.com"},
		{name: "already exists", existing: true, subject: "ana@example.com", wantOut: "unchanged\ngrant done: ana@example.com -> demo/ops\n", wantEmail: "ana@example.com"},
		{name: "robot resolved case-insensitively", subject: "robot:deploy-bot", wantOut: "changed\ngrant done: robot:deploy-bot -> demo/ops\n", wantRobot: robotID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakePortal{}
			if tt.existing {
				f.grants = []map[string]any{grantJSON("g1", roleOps, map[string]any{"id": "u1", "email": "ana@example.com"}, nil)}
				f.post = conflict
			}
			srv := httptest.NewTLSServer(f)
			defer srv.Close()

			code, out, errOut := invoke(t, srv, srv.URL, "grant", tt.subject, "demo", "ops")
			if code != exitOK || out != tt.wantOut {
				t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
			}
			if len(f.posts) != 1 {
				t.Fatalf("posts = %v", f.posts)
			}
			p := f.posts[0]
			if p["email"] != nilIfEmpty(tt.wantEmail) || p["robot_id"] != nilIfEmpty(tt.wantRobot) || p["role_id"] != roleOps {
				t.Errorf("body = %v", p)
			}
			if f.badAuth != 0 {
				t.Errorf("%d requests without the bearer token", f.badAuth)
			}
		})
	}
}

// nilIfEmpty maps "" to the untyped nil a missing JSON key decodes to.
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func TestRevoke(t *testing.T) {
	user := func(id, email string) map[string]any { return map[string]any{"id": id, "email": email} }
	tests := []struct {
		name        string
		grants      []map[string]any
		subject     string
		wantOut     string
		wantDeleted []string
	}{
		{
			name:        "direct grant is deleted",
			grants:      []map[string]any{grantJSON("g1", roleView, user("u1", "ana@example.com"), nil), grantJSON("g2", roleOps, user("u1", "ana@example.com"), nil)},
			subject:     "ANA@example.com",
			wantOut:     "changed\nrevoke done: ana@example.com -> demo/ops\n",
			wantDeleted: []string{"g2"},
		},
		{
			name:    "none left is unchanged",
			grants:  []map[string]any{grantJSON("g1", roleView, user("u1", "ana@example.com"), nil)},
			subject: "ana@example.com",
			wantOut: "unchanged\nrevoke done: ana@example.com -> demo/ops\n",
		},
		{
			name:    "group grant is never matched",
			grants:  []map[string]any{grantJSON("g1", roleOps, nil, map[string]any{"id": "grp", "key": "ana@example.com"})},
			subject: "ana@example.com",
			wantOut: "unchanged\nrevoke done: ana@example.com -> demo/ops\n",
		},
		{
			name:        "robot grant matches by id",
			grants:      []map[string]any{grantJSON("g1", roleOps, user(robotID, ""), nil)},
			subject:     "robot:Deploy-Bot",
			wantOut:     "changed\nrevoke done: robot:Deploy-Bot -> demo/ops\n",
			wantDeleted: []string{"g1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakePortal{grants: tt.grants}
			srv := httptest.NewTLSServer(f)
			defer srv.Close()

			code, out, errOut := invoke(t, srv, srv.URL, "revoke", tt.subject, "demo", "ops")
			if code != exitOK || out != tt.wantOut {
				t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
			}
			if strings.Join(f.deleted, ",") != strings.Join(tt.wantDeleted, ",") {
				t.Errorf("deleted = %v, want %v", f.deleted, tt.wantDeleted)
			}
		})
	}
}

func TestUnknownNames(t *testing.T) {
	srv := httptest.NewTLSServer(&fakePortal{})
	defer srv.Close()
	for name, args := range map[string][]string{
		"initiative": {"grant", "a@b.c", "nope", "ops"},
		"role":       {"grant", "a@b.c", "demo", "nope"},
		"robot":      {"grant", "robot:nope", "demo", "ops"},
	} {
		t.Run(name, func(t *testing.T) {
			code, _, errOut := invoke(t, srv, srv.URL, args...)
			if code != exitError || !strings.Contains(errOut, "unknown "+name) {
				t.Errorf("code=%d err=%q", code, errOut)
			}
		})
	}
}

func TestRedirectIsAnErrorAndNeverFollowed(t *testing.T) {
	var leaked bool
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked = true }))
	defer target.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/steal", http.StatusFound)
	}))
	defer srv.Close()

	code, out, errOut := invoke(t, srv, srv.URL, "grant", "a@b.c", "demo", "ops")
	if code != exitError || out != "" || !strings.Contains(errOut, "302") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	if leaked {
		t.Error("the redirect target was contacted")
	}
	if strings.Contains(errOut, testToken) || strings.Contains(errOut, target.URL) {
		t.Errorf("error output leaks: %q", errOut)
	}
}

func TestAPIErrorShowsProblemWithoutToken(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"title":"Unprocessable Entity","status":422,"detail":"the request has invalid fields","errors":[{"field":"email","code":"unknown"}]}`))
	}))
	defer srv.Close()

	code, _, errOut := invoke(t, srv, srv.URL, "grant", "a@b.c", "demo", "ops")
	if code != exitError {
		t.Fatalf("code = %d", code)
	}
	for _, want := range []string{"422", "the request has invalid fields", "email: unknown"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("missing %q in %q", want, errOut)
		}
	}
	if strings.Contains(errOut, testToken) {
		t.Errorf("error output leaks the token: %q", errOut)
	}
}

func TestUsageErrors(t *testing.T) {
	srv := httptest.NewTLSServer(&fakePortal{})
	defer srv.Close()
	tests := []struct {
		name string
		url  string
		args []string
	}{
		{"http scheme", "http://portal.example", []string{"grant", "a@b.c", "demo", "ops"}},
		{"empty url", "", []string{"grant", "a@b.c", "demo", "ops"}},
		{"userinfo", "https://u:p@portal.example", []string{"grant", "a@b.c", "demo", "ops"}},
		{"path", "https://portal.example/app", []string{"grant", "a@b.c", "demo", "ops"}},
		{"query", "https://portal.example/?x=1", []string{"grant", "a@b.c", "demo", "ops"}},
		{"empty query mark", "https://portal.example?", []string{"grant", "a@b.c", "demo", "ops"}},
		{"fragment", "https://portal.example/#f", []string{"grant", "a@b.c", "demo", "ops"}},
		{"bad subject", srv.URL, []string{"grant", "not-an-email", "demo", "ops"}},
		{"empty robot", srv.URL, []string{"grant", "robot:", "demo", "ops"}},
		{"unknown command", srv.URL, []string{"list", "a@b.c", "demo", "ops"}},
		{"missing args", srv.URL, []string{"grant", "a@b.c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out, errOut := invoke(t, srv, tt.url, tt.args...)
			if code != exitUsage || out != "" || !strings.Contains(errOut, "usage:") {
				t.Errorf("code=%d out=%q err=%q", code, out, errOut)
			}
		})
	}

	t.Run("missing token", func(t *testing.T) {
		var out, errb bytes.Buffer
		code := run(context.Background(), []string{"grant", "a@b.c", "demo", "ops"}, func(k string) string {
			if k == envURL {
				return srv.URL
			}
			return ""
		}, &out, &errb, nil)
		if code != exitUsage {
			t.Errorf("code = %d", code)
		}
	})
}

func TestGrantConflictChecksExpiry(t *testing.T) {
	ana := map[string]any{"id": "u1", "email": "ana@example.com"}
	withExpiry := func(id string, at time.Time) map[string]any {
		g := grantJSON(id, roleOps, ana, nil)
		g["expires_at"] = at.UTC().Format(time.RFC3339)
		return g
	}
	future := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	past := time.Now().Add(-48 * time.Hour)
	tests := []struct {
		name       string
		grants     []map[string]any
		wantCode   int
		wantErr    string
		wantWarn   string
		wantStdout string
	}{
		{name: "expired grant hides the conflict", grants: []map[string]any{withExpiry("g1", past)}, wantCode: exitError, wantErr: "an expired grant exists for ana@example.com -> demo/ops; revoke it first"},
		{name: "future expiry warns", grants: []map[string]any{withExpiry("g1", future)}, wantCode: exitOK, wantWarn: "warning: existing grant expires at " + future.Format(time.RFC3339), wantStdout: "unchanged\n"},
		{name: "expired and active duplicates are unchanged", grants: []map[string]any{withExpiry("g1", past), grantJSON("g2", roleOps, ana, nil)}, wantCode: exitOK, wantStdout: "unchanged\n"},
		{name: "no visible direct grant reports the conflict", grants: nil, wantCode: exitError, wantErr: "409"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(&fakePortal{grants: tt.grants, post: conflict})
			defer srv.Close()
			code, out, errOut := invoke(t, srv, srv.URL, "grant", "ana@example.com", "demo", "ops")
			if code != tt.wantCode || !strings.HasPrefix(out, tt.wantStdout) || !strings.Contains(errOut, tt.wantErr) || !strings.Contains(errOut, tt.wantWarn) {
				t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
			}
			if tt.wantWarn == "" && strings.Contains(errOut, "warning") {
				t.Errorf("unexpected warning: %q", errOut)
			}
			if tt.wantCode == exitError && out != "" {
				t.Errorf("stdout on failure: %q", out)
			}
		})
	}
}

func TestAPIFailures(t *testing.T) {
	ana := map[string]any{"id": "u1", "email": "ana@example.com"}
	tests := []struct {
		name    string
		portal  *fakePortal
		args    []string
		want    []string
		notWant string
	}{
		{
			name: "read scope token is forbidden",
			portal: &fakePortal{post: func(w http.ResponseWriter) {
				problem(w, http.StatusForbidden, `{"title":"Forbidden","status":403,"detail":"token scope read cannot write"}`)
			}},
			args: []string{"grant", "ana@example.com", "demo", "ops"},
			want: []string{"403", "token scope read cannot write"},
		},
		{
			name: "last manager on delete",
			portal: &fakePortal{grants: []map[string]any{grantJSON("g1", roleOps, ana, nil)}, del: func(w http.ResponseWriter) {
				problem(w, http.StatusConflict, `{"title":"Conflict","status":409,"detail":"the initiative must keep at least one effective manages_access grant"}`)
			}},
			args: []string{"revoke", "ana@example.com", "demo", "ops"},
			want: []string{"409", "must keep at least one effective manages_access grant"},
		},
		{
			name: "500 claiming status 409 is not unchanged",
			portal: &fakePortal{grants: []map[string]any{grantJSON("g1", roleOps, ana, nil)}, post: func(w http.ResponseWriter) {
				problem(w, http.StatusInternalServerError, `{"title":"Internal Server Error","status":409,"detail":"boom"}`)
			}},
			args:    []string{"grant", "ana@example.com", "demo", "ops"},
			want:    []string{"500", "boom"},
			notWant: "unchanged",
		},
		{
			name: "user must sign in first",
			portal: &fakePortal{post: func(w http.ResponseWriter) {
				problem(w, http.StatusUnprocessableEntity, `{"title":"Unprocessable Entity","status":422,"detail":"the request has invalid fields","errors":[{"field":"email","code":"no_unique_active_user"}]}`)
			}},
			args: []string{"grant", "ana@example.com", "demo", "ops"},
			want: []string{"must sign in once first"},
		},
		{
			name:    "disabled robot fails before the POST",
			portal:  &fakePortal{robots: []map[string]any{{"id": robotID, "name": "Deploy-Bot", "status": "disabled"}}},
			args:    []string{"grant", "robot:deploy-bot", "demo", "ops"},
			want:    []string{"robot deploy-bot is disabled"},
			notWant: "unchanged",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(tt.portal)
			defer srv.Close()
			code, out, errOut := invoke(t, srv, srv.URL, tt.args...)
			if code != exitError || out != "" {
				t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
			}
			for _, w := range tt.want {
				if !strings.Contains(errOut, w) {
					t.Errorf("missing %q in %q", w, errOut)
				}
			}
			if tt.notWant != "" && strings.Contains(errOut, tt.notWant) {
				t.Errorf("unexpected %q in %q", tt.notWant, errOut)
			}
			if strings.Contains(errOut, testToken) {
				t.Errorf("error output leaks the token: %q", errOut)
			}
			if tt.name == "disabled robot fails before the POST" && len(tt.portal.posts) != 0 {
				t.Errorf("POST sent for a disabled robot")
			}
		})
	}
}

func TestPaginationOfRobotsAndGrants(t *testing.T) {
	user := func(id, email string) map[string]any { return map[string]any{"id": id, "email": email} }
	f := &fakePortal{
		robots: []map[string]any{{"id": "r0", "name": "first", "status": "active"}, {"id": robotID, "name": "Deploy-Bot", "status": "active"}},
		grants: []map[string]any{
			grantJSON("g0", roleOps, user("u0", "x@example.com"), nil),
			grantJSON("g1", roleOps, user("u1", "y@example.com"), nil),
			grantJSON("g2", roleOps, user(robotID, ""), nil),
		},
	}
	srv := httptest.NewTLSServer(f)
	defer srv.Close()
	code, out, errOut := invoke(t, srv, srv.URL, "revoke", "robot:deploy-bot", "demo", "ops")
	if code != exitOK || !strings.HasPrefix(out, "changed\n") || strings.Join(f.deleted, ",") != "g2" {
		t.Fatalf("code=%d out=%q err=%q deleted=%v", code, out, errOut, f.deleted)
	}
}

func TestRevokeDeletesEveryDirectMatch(t *testing.T) {
	f := &fakePortal{grants: []map[string]any{
		grantJSON("g1", roleOps, map[string]any{"id": "u-disabled", "email": "ana@example.com"}, nil),
		grantJSON("g2", roleView, map[string]any{"id": "u-active", "email": "ana@example.com"}, nil),
		grantJSON("g3", roleOps, map[string]any{"id": "u-active", "email": "Ana@example.com"}, nil),
	}}
	srv := httptest.NewTLSServer(f)
	defer srv.Close()
	code, out, errOut := invoke(t, srv, srv.URL, "revoke", "ana@example.com", "demo", "ops")
	if code != exitOK || !strings.HasPrefix(out, "changed\n") || strings.Join(f.deleted, ",") != "g1,g3" {
		t.Fatalf("code=%d out=%q err=%q deleted=%v", code, out, errOut, f.deleted)
	}
}
