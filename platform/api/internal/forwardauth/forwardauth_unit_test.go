package forwardauth

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTTLCache(t *testing.T) {
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	c := newTTLCache[string, int](30*time.Second, func() time.Time { return now })

	_, ok, gen := c.get("k")
	if ok {
		t.Fatal("empty cache hit")
	}
	c.put("k", 1, gen)
	if v, ok, _ := c.get("k"); !ok || v != 1 {
		t.Fatalf("get = %v %v", v, ok)
	}
	now = now.Add(30 * time.Second)
	if _, ok, _ := c.get("k"); ok {
		t.Fatal("entry outlived its TTL")
	}

	t.Run("a value loaded before a clear is never stored after it", func(t *testing.T) {
		_, _, gen := c.get("x") // loader starts
		c.clear()               // NOTIFY arrives while it runs
		c.put("x", 2, gen)      // loader finishes with the pre-change answer
		if _, ok, _ := c.get("x"); ok {
			t.Fatal("stale value stored after invalidation")
		}
	})
}

func TestIsNavigation(t *testing.T) {
	for name, tc := range map[string]struct {
		method  string
		headers map[string]string
		want    bool
	}{
		"navigate + html":          {http.MethodGet, map[string]string{"Accept": "text/html,application/xhtml+xml", "Sec-Fetch-Mode": "navigate"}, true},
		"navigate without accept":  {http.MethodGet, map[string]string{"Sec-Fetch-Mode": "navigate"}, true},
		"head navigate":            {http.MethodHead, map[string]string{"Sec-Fetch-Mode": "navigate"}, true},
		"cors + html":              {http.MethodGet, map[string]string{"Accept": "text/html", "Sec-Fetch-Mode": "cors"}, false},
		"same-origin + json":       {http.MethodGet, map[string]string{"Accept": "application/json", "Sec-Fetch-Mode": "same-origin"}, false},
		"no-cors + html":           {http.MethodGet, map[string]string{"Accept": "text/html", "Sec-Fetch-Mode": "no-cors"}, false},
		"navigate + json":          {http.MethodGet, map[string]string{"Accept": "application/json", "Sec-Fetch-Mode": "navigate"}, true},
		"no mode + html":           {http.MethodGet, map[string]string{"Accept": "text/html,application/xhtml+xml"}, true},
		"no mode + html uppercase": {http.MethodGet, map[string]string{"Accept": "TEXT/HTML"}, true},
		"head no mode + html":      {http.MethodHead, map[string]string{"Accept": "text/html"}, true},
		"no mode + json":           {http.MethodGet, map[string]string{"Accept": "application/json"}, false},
		"no mode + wildcard":       {http.MethodGet, map[string]string{"Accept": "*/*"}, false},
		"no hints":                 {http.MethodGet, nil, false},
		"post navigate form":       {http.MethodPost, map[string]string{"Accept": "text/html", "Sec-Fetch-Mode": "navigate"}, false},
		"post no mode + html":      {http.MethodPost, map[string]string{"Accept": "text/html"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tc.headers {
				h.Set(k, v)
			}
			if got := isNavigation(h, tc.method); got != tc.want {
				t.Fatalf("isNavigation = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCallbackCode(t *testing.T) {
	for name, tc := range map[string]struct {
		method, uri, code string
		ok                bool
	}{
		"ok":           {http.MethodGet, "/_platform/callback?code=abc", "abc", true},
		"extra params": {http.MethodGet, "/_platform/callback?x=1&code=abc", "abc", true},
		"no code":      {http.MethodGet, "/_platform/callback", "", false},
		"empty code":   {http.MethodGet, "/_platform/callback?code=", "", false},
		"post":         {http.MethodPost, "/_platform/callback?code=abc", "", false},
		"other path":   {http.MethodGet, "/other?code=abc", "", false},
		"prefix only":  {http.MethodGet, "/_platform/callback/x?code=abc", "", false},
		"unparsable":   {http.MethodGet, "::", "", false},
	} {
		t.Run(name, func(t *testing.T) {
			code, ok := callbackCode(tc.method, tc.uri)
			if code != tc.code || ok != tc.ok {
				t.Fatalf("got %q %v", code, ok)
			}
		})
	}
}

func TestOriginalURL(t *testing.T) {
	const host = "a.apps.example.test"
	for name, tc := range map[string]struct{ uri, want string }{
		"path and query":    {"/x?y=1", "https://" + host + "/x?y=1"},
		"root":              {"/", "https://" + host + "/"},
		"empty":             {"", "https://" + host + "/"},
		"no leading slash":  {"evil.example/x", "https://" + host + "/"},
		"protocol relative": {"//evil.example/x", "https://" + host + "/"},
		"too long":          {"/" + strings.Repeat("a", 3000), "https://" + host + "/"},
		"double slash":      {"/a//b", "https://" + host + "/"},
		"encoded double":    {"/a%2F%2Fb", "https://" + host + "/"},
		"backslash":         {"/a%5Cb", "https://" + host + "/"},
		"control char":      {"/a%0Ab", "https://" + host + "/"},
		"single encoded /":  {"/a%2Fb", "https://" + host + "/a%2Fb"},
		"url in query":      {"/p?next=https://x//y", "https://" + host + "/p?next=https://x//y"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := originalURL(host, tc.uri); got != tc.want {
				t.Fatalf("got %q", got)
			}
		})
	}
}
