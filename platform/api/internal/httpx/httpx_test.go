package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSameOrigin(t *testing.T) {
	for name, tc := range map[string]struct {
		headers map[string]string
		want    bool
	}{
		"matching origin":   {map[string]string{"Origin": "https://app.example.test"}, true},
		"same-origin fetch": {map[string]string{"Sec-Fetch-Site": "same-origin"}, true},
		"foreign origin":    {map[string]string{"Origin": "https://evil.example"}, false},
		"http origin":       {map[string]string{"Origin": "http://app.example.test"}, false},
		"null origin":       {map[string]string{"Origin": "null"}, false},
		"cross-site":        {map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
		"same-site only":    {map[string]string{"Sec-Fetch-Site": "same-site"}, false},
		"none":              {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tc.headers {
				h.Set(k, v)
			}
			if got := SameOrigin(h, "app.example.test"); got != tc.want {
				t.Fatalf("got %v", got)
			}
		})
	}
}

func TestClientIP(t *testing.T) {
	for name, tc := range map[string]struct {
		xff, remote, want string
	}{
		"proxy appended last": {"6.6.6.6, 203.0.113.9", "10.0.0.1:1234", "203.0.113.9"},
		"single":              {"203.0.113.9", "10.0.0.1:1234", "203.0.113.9"},
		"garbage falls back":  {"not-an-ip", "10.0.0.1:1234", "10.0.0.1"},
		"no header":           {"", "10.0.0.1:1234", "10.0.0.1"},
		"ipv4 mapped":         {"::ffff:203.0.113.9", "10.0.0.1:1", "203.0.113.9"},
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tc.remote
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := ClientIP(r).String(); got != tc.want {
				t.Fatalf("got %s", got)
			}
		})
	}
}

func TestCookies(t *testing.T) {
	rec := httptest.NewRecorder()
	SetCookie(rec, "__Host-Http-x", "v", 0)
	ClearCookie(rec, "__Host-Http-y")
	cookies := rec.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("cookies = %v", cookies)
	}
	for _, c := range cookies {
		if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" {
			t.Errorf("%s attributes: %+v", c.Name, c)
		}
	}
	if cookies[1].MaxAge >= 0 {
		t.Errorf("cleared cookie MaxAge = %d", cookies[1].MaxAge)
	}
}

func TestRecoverFailsClosed(t *testing.T) {
	h := Recover(nilLogger(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d", rec.Code)
	}
}
