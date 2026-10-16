package webui

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const (
	apiCSP     = "default-src 'none'; frame-ancestors 'none'"
	authOrigin = "https://auth.example.test"
	runtimeJS  = `window.__NUXT__={};window.__NUXT__.config={"public":{}}`
	appJS      = "console.log('app')"
)

func hashSource(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

func shell(scripts ...string) *fstest.MapFile {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><meta charset="utf-8"><link rel="stylesheet" href="/_nuxt/a.css"></head><body><div id="__nuxt"></div>`)
	for _, s := range scripts {
		b.WriteString("<script>" + s + "</script>")
	}
	b.WriteString(`<script type="module" src="/_nuxt/entry.js"></script></body></html>`)
	return &fstest.MapFile{Data: []byte(b.String())}
}

func fixture() fstest.MapFS {
	return fstest.MapFS{
		"200.html":            shell(runtimeJS),
		"index.html":          shell(runtimeJS),
		"_nuxt/app.abc.js":    {Data: []byte(appJS)},
		"_nuxt/a.css":         {Data: []byte("body{}")},
		"_nuxt/builds/x.json": {Data: []byte("{}")},
		"robots.txt":          {Data: []byte("User-agent: *")},
		".gitkeep":            {Data: nil},
	}
}

func newHandler(t *testing.T, fsys fstest.MapFS) *Handler {
	t.Helper()
	h, err := New(fsys, Options{AuthOrigin: authOrigin})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func do(h http.Handler, method, target string) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), method, target, nil)
	// Simulate the middleware that runs before the handler.
	w := httptest.NewRecorder()
	w.Header().Set("Content-Security-Policy", apiCSP)
	h.ServeHTTP(w, r)
	return w
}

func TestRoutesServeShell(t *testing.T) {
	t.Parallel()
	h := newHandler(t, fixture())
	for _, target := range []string{"/", "/admin/groups/123", "/admin", "/foo.html", "/some/route/"} {
		w := do(h, http.MethodGet, target)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", target, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Errorf("%s: Content-Type = %q", target, ct)
		}
		if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s: Cache-Control = %q", target, cc)
		}
		csp := w.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "script-src 'self' "+hashSource(runtimeJS)+";") {
			t.Errorf("%s: CSP = %q", target, csp)
		}
		if csp == apiCSP || csp != h.CSP() {
			t.Errorf("%s: CSP not replaced: %q", target, csp)
		}
		if !strings.Contains(w.Body.String(), `id="__nuxt"`) {
			t.Errorf("%s: body is not the shell", target)
		}
	}
}

func TestCSPShape(t *testing.T) {
	t.Parallel()
	want := "default-src 'none'; script-src 'self' " + hashSource(runtimeJS) +
		"; style-src 'self'; img-src 'self'; font-src 'self'; connect-src 'self'; " +
		"form-action 'self' https://auth.example.test; frame-ancestors 'none'; base-uri 'none'"
	if got := newHandler(t, fixture()).CSP(); got != want {
		t.Fatalf("CSP = %q\nwant  %q", got, want)
	}
}

func TestAssets(t *testing.T) {
	t.Parallel()
	h := newHandler(t, fixture())
	tests := []struct{ target, ct, cache string }{
		{"/_nuxt/app.abc.js", "text/javascript; charset=utf-8", "public, max-age=31536000, immutable"},
		{"/_nuxt/a.css", "text/css; charset=utf-8", "public, max-age=31536000, immutable"},
		{"/_nuxt/builds/x.json", "application/json", "public, max-age=31536000, immutable"},
		{"/robots.txt", "text/plain; charset=utf-8", "no-cache"},
	}
	for _, tt := range tests {
		w := do(h, http.MethodGet, tt.target)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", tt.target, w.Code)
		}
		if got := w.Header().Get("Content-Type"); got != tt.ct {
			t.Errorf("%s: Content-Type = %q", tt.target, got)
		}
		if got := w.Header().Get("Cache-Control"); got != tt.cache {
			t.Errorf("%s: Cache-Control = %q", tt.target, got)
		}
		if got := w.Header().Get("Content-Security-Policy"); got != apiCSP {
			t.Errorf("%s: asset CSP = %q, want the API one untouched", tt.target, got)
		}
	}
}

func TestAssetsHaveETagAnd304(t *testing.T) {
	t.Parallel()
	h := newHandler(t, fixture())
	for _, target := range []string{"/_nuxt/app.abc.js", "/_nuxt/a.css", "/robots.txt", "/200.html"} {
		etag := do(h, http.MethodGet, target).Header().Get("ETag")
		if etag == "" {
			t.Fatalf("%s: no ETag", target)
		}
		r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
		r.Header.Set("If-None-Match", etag)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNotModified || w.Body.Len() != 0 {
			t.Errorf("%s: status = %d, body = %d bytes, want 304 and empty", target, w.Code, w.Body.Len())
		}
	}
	if do(h, http.MethodGet, "/robots.txt").Header().Get("ETag") == do(h, http.MethodGet, "/_nuxt/a.css").Header().Get("ETag") {
		t.Error("different files share an ETag")
	}
	w := do(h, http.MethodGet, "/_nuxt/app.abc.js")
	if w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q, want immutable under /_nuxt/", w.Header().Get("Cache-Control"))
	}
}

func TestAuthOrigin(t *testing.T) {
	t.Parallel()
	valid := map[string]string{
		"https://auth.example.test":       "https://auth.example.test",
		"https://auth.example.test:8443":  "https://auth.example.test:8443",
		"https://auth.example.test:443":   "https://auth.example.test:443",
		"https://auth.example.test:1":     "https://auth.example.test:1",
		"https://auth.example.test:65535": "https://auth.example.test:65535",
	}
	for in, want := range valid {
		h, err := New(fixture(), Options{AuthOrigin: in})
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if !strings.Contains(h.CSP(), "form-action 'self' "+want+";") {
			t.Errorf("%q: CSP = %q", in, h.CSP())
		}
	}
	for _, in := range []string{
		"", "auth.example.test", "http://auth.example.test", "https://", "https://auth.example.test/",
		"https://auth.example.test/x", "https://auth.example.test?a=b", "https://auth.example.test?",
		"https://auth.example.test#f", "https://u:p@auth.example.test", "https://u@auth.example.test",
		"https://auth.example.test;script-src *", "https://auth.example.test, *", "https://a b.example.test",
		"https://*.example.test", "https://auth.example.test'", "ftp://auth.example.test", "mailto:a@b.test",
		"https://auth.example.test:", "https://auth.example.test:0", "https://auth.example.test:65536",
		"https://auth.example.test:99999", "https://auth.example.test:+1", "https://auth.example.test:-1",
		"https://auth.example.test:80a", "https://auth.example.test:1:2", "https://[::1]:8443",
	} {
		if _, err := New(fixture(), Options{AuthOrigin: in}); !errors.Is(err, ErrInvalidAuthOrigin) {
			t.Errorf("%q: err = %v, want ErrInvalidAuthOrigin", in, err)
		}
	}
}

func TestNotFoundIsNeverTheShell(t *testing.T) {
	t.Parallel()
	h := newHandler(t, fixture())
	for _, target := range []string{
		"/_nuxt/x.js", "/_nuxt/", "/_nuxt", "/_nuxt/deep/route", "/favicon.ico", "/a/b.png", "/.gitkeep", "/.env",
		"/_nuxt/.hidden",
	} {
		w := do(h, http.MethodGet, target)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", target, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
			t.Errorf("%s: Content-Type = %q", target, ct)
		}
		if w.Header().Get("Content-Security-Policy") != apiCSP {
			t.Errorf("%s: a 404 must keep the API CSP", target)
		}
	}
}

func TestMethods(t *testing.T) {
	t.Parallel()
	h := newHandler(t, fixture())
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		w := do(h, m, "/")
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d", m, w.Code)
		}
		if got := w.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("%s: Allow = %q", m, got)
		}
	}
	w := do(h, http.MethodHead, "/")
	if w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("HEAD: status = %d, body = %d bytes", w.Code, w.Body.Len())
	}
	if w.Header().Get("Content-Security-Policy") != h.CSP() {
		t.Error("HEAD must carry the web CSP")
	}
}

func TestConditionalRequest(t *testing.T) {
	t.Parallel()
	h := newHandler(t, fixture())
	etag := do(h, http.MethodGet, "/").Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	weak := "W/" + etag
	tests := []struct {
		name   string
		method string
		inm    string
		want   int
	}{
		{"exact etag", http.MethodGet, etag, http.StatusNotModified},
		{"star", http.MethodGet, "*", http.StatusNotModified},
		{"list with the etag", http.MethodGet, `"x", ` + etag, http.StatusNotModified},
		{"weak etag", http.MethodGet, weak, http.StatusNotModified},
		{"head", http.MethodHead, etag, http.StatusNotModified},
		{"other etag", http.MethodGet, `"x"`, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequestWithContext(context.Background(), tt.method, "/x", nil)
			r.Header.Set("If-None-Match", tt.inm)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d", w.Code, tt.want)
			}
			if w.Header().Get("Content-Security-Policy") != h.CSP() {
				t.Errorf("CSP = %q, want the web CSP", w.Header().Get("Content-Security-Policy"))
			}
			if got := w.Header().Get("Cache-Control"); got != "no-cache" {
				t.Errorf("Cache-Control = %q, want no-cache", got)
			}
			if got := w.Header().Get("ETag"); got != etag {
				t.Errorf("ETag = %q, want %q", got, etag)
			}
			if w.Code == http.StatusNotModified && w.Body.Len() != 0 {
				t.Errorf("304 with a %d-byte body", w.Body.Len())
			}
		})
	}
}

func TestTraversalIsSafe(t *testing.T) {
	t.Parallel()
	h := newHandler(t, fixture())
	// A secret that exists in the FS tree next to the served files must stay unreachable by any spelling.
	for _, target := range []string{"/../200.html", "/%2e%2e/etc/passwd", "/_nuxt/../../etc/passwd", "/a/%2E%2E/b", "/_nuxt/%2e%2e/%2e%2e/x", "/..%2f..%2fetc/passwd"} {
		w := do(h, http.MethodGet, target)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", target, w.Code)
		}
	}
	// A double slash cleans to the same in-tree path; it is served, never resolved outside the tree.
	for target, want := range map[string]int{"//": http.StatusOK, "//_nuxt//app.abc.js": http.StatusOK, "/_nuxt/./app.abc.js": http.StatusOK} {
		r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
		r.URL.Path = target
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("%s: status = %d, want %d", target, w.Code, want)
		}
	}
}

func TestNew(t *testing.T) {
	t.Parallel()
	html := func(body string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte("<!doctype html><html><head></head><body>" + body + "</body></html>")}
	}
	tests := []struct {
		name    string
		fsys    fstest.MapFS
		wantErr error
		hashes  []string
		anyErr  bool
	}{
		{name: "no 200.html", fsys: fstest.MapFS{".gitkeep": {}}, wantErr: ErrNotBuilt},
		{name: "empty fs", fsys: fstest.MapFS{}, wantErr: ErrNotBuilt},
		{name: "no inline script", fsys: fstest.MapFS{"200.html": html(`<script src="/_nuxt/e.js"></script>`)}, wantErr: ErrNoInlineScript},
		{name: "json data only", fsys: fstest.MapFS{"200.html": html(`<script type="application/json" id="__NUXT_DATA__">[1]</script>`)}, wantErr: ErrNoInlineScript},
		{
			name:   "two scripts across files",
			fsys:   fstest.MapFS{"200.html": html("<script>a()</script>"), "sub/page.html": html("<script>b()</script><script>a()</script>")},
			hashes: []string{hashSource("a()"), hashSource("b()")},
		},
		{
			name:   "json is not hashed",
			fsys:   fstest.MapFS{"200.html": html(`<script>a()</script><script type="application/json">{"x":1}</script>`)},
			hashes: []string{hashSource("a()")},
		},
		{
			name:   "module and explicit javascript types are executable",
			fsys:   fstest.MapFS{"200.html": html(`<script type="module">m()</script><script type=" Text/JavaScript ">j()</script>`)},
			hashes: []string{hashSource("j()"), hashSource("m()")},
		},
		{name: "importmap", fsys: fstest.MapFS{"200.html": html(`<script>a()</script><script type="importmap">{}</script>`)}, anyErr: true},
		{name: "unknown type", fsys: fstest.MapFS{"200.html": html(`<script>a()</script><script type="text/x-template">x</script>`)}, anyErr: true},
		{name: "json with parameters is unknown", fsys: fstest.MapFS{"200.html": html(`<script>a()</script><script type="application/json; charset=utf-8">{}</script>`)}, anyErr: true},
		{name: "script inside noscript is inspected", fsys: fstest.MapFS{"200.html": html(`<script>a()</script><noscript><script type="importmap">{}</script></noscript>`)}, anyErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, err := New(tt.fsys, Options{AuthOrigin: authOrigin})
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
			case tt.anyErr:
				if err == nil || errors.Is(err, ErrNotBuilt) {
					t.Fatalf("err = %v, want a refusal", err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				directive := strings.SplitN(strings.SplitN(h.CSP(), "script-src ", 2)[1], ";", 2)[0]
				if got := strings.Fields(directive); len(got) != len(tt.hashes)+1 {
					t.Fatalf("script-src = %q, want 'self' and %v", directive, tt.hashes)
				}
				for _, want := range tt.hashes {
					if !strings.Contains(directive, want) {
						t.Fatalf("script-src = %q, missing %s", directive, want)
					}
				}
			}
		})
	}
}

// The hash must be of the exact bytes the browser hashes: the raw script text, with CR and CRLF normalized to
// LF by the HTML parser, and no entity decoding.
func TestHashedBytesMatchTheBrowser(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		html string
		text string
	}{
		{"crlf becomes lf", "<script>a=1;\r\nb=2;\r\n</script>", "a=1;\nb=2;\n"},
		{"lone cr becomes lf", "<script>a=1;\rb=2;</script>", "a=1;\nb=2;"},
		{"entities stay raw", "<script>x='&amp;&lt;&#39;';</script>", "x='&amp;&lt;&#39;';"},
		{"leading newline is kept", "<script>\nx</script>", "\nx"},
		{"markup like text stays raw", `<script>x="<b>y</b>";</script>`, `x="<b>y</b>";`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := documentHashes([]byte("<!doctype html><html><body>" + tt.html + "</body></html>"))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0] != hashSource(tt.text) {
				t.Fatalf("hashes = %v, want [%s]", got, hashSource(tt.text))
			}
		})
	}
}

// Edge cases where the parser and the browser could disagree. Each case documents which side it lands on: "match"
// means the hash is of what the browser executes, "fail closed" means New refuses the document.
func TestHashEdgeCases(t *testing.T) {
	t.Parallel()
	const bom = "\ufeff"
	tests := []struct {
		name    string
		doc     string
		want    []string // match: exact hash sources
		wantErr bool     // fail closed
	}{
		{
			// match: an escaped closer does not end the element, the whole text is hashed.
			name: "escaped closing tag inside a JS string stays in the script",
			doc:  `<script>var s="<\/script>";</script>`,
			want: []string{hashSource(`var s="<\/script>";`)},
		},
		{
			// match: a literal closer ends the element there; the browser executes (and fails on) the truncated text,
			// and the tail is page text, not script.
			name: "literal closing tag inside a JS string truncates the script",
			doc:  `<script>var s="</script>";</script>`,
			want: []string{hashSource(`var s="`)},
		},
		{
			// match: the end tag is case-insensitive and may carry attributes or a slash.
			name: "closing tag spelling variants",
			doc:  `<script>a()</SCRIPT ><script>b()</script/>`,
			want: []string{hashSource("a()"), hashSource("b()")},
		},
		{
			// match: browsers strip a leading BOM when decoding, so it never reaches the document.
			name: "BOM at the start of the file is not part of any script",
			doc:  bom + `<script>a()</script>`,
			want: []string{hashSource("a()")},
		},
		{
			// match: a BOM inside the script text is a U+FEFF character the browser hashes.
			name: "BOM inside the script text is hashed",
			doc:  `<script>` + bom + `a()</script>`,
			want: []string{hashSource(bom + "a()")},
		},
		{
			// match: the HTML tokenizer replaces NUL with U+FFFD in script data, exactly like the browser, so the
			// hash is of the replaced text. This pins that behavior of x/net/html (a NUL never survives the parse).
			name: "NUL byte in the script becomes U+FFFD",
			doc:  "<script>a(\x00)</script>",
			want: []string{hashSource("a(\ufffd)")},
		},
		{
			// match: browsers ignore repeated attributes after the first (x/net/html keeps them all, so the code takes
			// the first one): the first type wins (executable here).
			name: "duplicate type, first is executable",
			doc:  `<script type="module" type="importmap">a()</script>`,
			want: []string{hashSource("a()")},
		},
		{
			// fail closed: the first type is the one in force, and importmap is refused.
			name:    "duplicate type, first is importmap",
			doc:     `<script type="importmap" type="text/javascript">{}</script>`,
			wantErr: true,
		},
		{
			// match: the first type is data, so there is nothing to hash.
			name: "duplicate type, first is json",
			doc:  `<script>a()</script><script type="application/json" type="module">{}</script>`,
			want: []string{hashSource("a()")},
		},
		{
			// match: an external script is governed by 'self', whatever follows.
			name: "duplicate src, first is external",
			doc:  `<script>a()</script><script src="/_nuxt/e.js" src="/x.js">ignored()</script>`,
			want: []string{hashSource("a()")},
		},
		{
			// fail closed: a type that is not a JavaScript MIME type is neither executed nor safe to assume data.
			name:    "unknown type",
			doc:     `<script>a()</script><script type="text/plain">x</script>`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			doc := "<!doctype html><html><body>" + tt.doc + "</body></html>"
			if strings.HasPrefix(tt.doc, bom) {
				doc = tt.doc // the BOM must be the first bytes of the file
			}
			got, err := documentHashes([]byte(doc))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("hashes = %v, want a refusal", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("hashes = %v, want %v", got, tt.want)
			}
		})
	}
}

// A script element with several text nodes (not producible by the tokenizer today, but the DOM allows it) hashes
// their concatenation, which is what the browser hashes ("child text content").
func TestScriptHashConcatenatesTextNodes(t *testing.T) {
	t.Parallel()
	n := &html.Node{Type: html.ElementNode, DataAtom: atom.Script, Data: "script"}
	n.AppendChild(&html.Node{Type: html.TextNode, Data: "a("})
	n.AppendChild(&html.Node{Type: html.CommentNode, Data: "ignored"})
	n.AppendChild(&html.Node{Type: html.TextNode, Data: ")"})
	got, ok, err := scriptHash(n)
	if err != nil || !ok || got != hashSource("a()") {
		t.Fatalf("hash = %q, ok = %v, err = %v", got, ok, err)
	}
}

// realBuildDir is the output of `npm run generate` in platform/web. The test is skipped when it is absent.
const realBuildDir = "../../../web/.output/public"

// inlineScript extracts the text of the first <script> with no attributes, by plain string search; it is
// deliberately independent of the HTML parser used by the code under test.
func inlineScript(t *testing.T, doc string) string {
	t.Helper()
	const open, closing = "<script>", "</script>"
	start := strings.Index(doc, open)
	if start < 0 {
		t.Fatal("no inline config script")
	}
	rest := doc[start+len(open):]
	end := strings.Index(rest, closing)
	if end < 0 {
		t.Fatal("unterminated inline config script")
	}
	return rest[:end]
}

func TestRealBuildHashes(t *testing.T) {
	if _, err := os.Stat(realBuildDir); err != nil {
		t.Skip("no nuxt generate output at " + realBuildDir)
	}
	h, err := New(os.DirFS(realBuildDir), Options{AuthOrigin: authOrigin})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("CSP: %s", h.CSP())
	htmlFiles, err := filepath.Glob(filepath.Join(realBuildDir, "*.html"))
	if err != nil || len(htmlFiles) == 0 {
		t.Fatalf("html files = %v, err = %v", htmlFiles, err)
	}
	for _, f := range htmlFiles {
		doc, err := fs.ReadFile(os.DirFS(realBuildDir), filepath.Base(f))
		if err != nil {
			t.Fatal(err)
		}
		want := hashSource(strings.ReplaceAll(inlineScript(t, string(doc)), "\r\n", "\n"))
		if !strings.Contains(h.CSP(), want) {
			t.Errorf("%s: CSP lacks %s", filepath.Base(f), want)
		}
		name := "/" + filepath.Base(f)
		if w := do(h, http.MethodGet, name); w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Security-Policy"), want) {
			t.Errorf("%s: status = %d, CSP = %q", name, w.Code, w.Header().Get("Content-Security-Policy"))
		}
	}
	if w := do(h, http.MethodGet, "/admin/groups/1"); w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
}
