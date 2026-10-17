// Package webui serves the SPA embedded in the binary. Every HTML response carries a Content-Security-Policy
// computed once at start-up from the inline scripts of the embedded HTML, so the policy never needs
// 'unsafe-inline' and always matches the build it ships with.
package webui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/Wysted/atrium/platform/api/internal/httpx"
)

// dist holds the output of `nuxt generate` (copied by Dockerfile.portal). Only .gitkeep is tracked, so a plain
// `go build` embeds an empty tree and New answers ErrNotBuilt.
//
//go:embed all:dist
var dist embed.FS

const (
	distDir      = "dist"
	fallbackFile = "200.html"
	assetPrefix  = "_nuxt/"

	headerCSP = "Content-Security-Policy"
	// cacheImmutable is for /_nuxt/ assets, whose names carry a content hash.
	cacheImmutable  = "public, max-age=31536000, immutable"
	cacheRevalidate = "no-cache"
	allowMethods    = "GET, HEAD"
	etagBytes       = 16 // bytes of the SHA-256 kept in an ETag
)

// Embedded returns the embedded SPA files rooted at the build output.
func Embedded() (fs.FS, error) {
	sub, err := fs.Sub(dist, distDir)
	if err != nil {
		return nil, fmt.Errorf("webui: %w", err)
	}
	return sub, nil
}

// page is an HTML document held in memory with its validator.
type page struct {
	body []byte
	etag string
}

// Options configures New.
type Options struct {
	// AuthOrigin is the origin of the auth host (the portal's PORTAL_ISSUER), added to form-action.
	AuthOrigin string
}

// Handler serves the SPA: exact files, and the 200.html shell for client-side routes.
type Handler struct {
	fsys  fs.FS
	csp   string
	pages map[string]page   // every *.html by file name
	etags map[string]string // every regular file by name, computed at start-up
}

// New builds the handler over fsys. It fails with ErrNotBuilt when 200.html is missing, and when the HTML files
// have no executable inline script or one the policy cannot express safely.
func New(fsys fs.FS, opts Options) (*Handler, error) {
	origin, err := ParseAuthOrigin(opts.AuthOrigin)
	if err != nil {
		return nil, err
	}
	if _, err := fs.Stat(fsys, fallbackFile); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotBuilt, err)
	}
	hashes, err := scriptHashes(fsys)
	if err != nil {
		return nil, err
	}
	pages := map[string]page{}
	etags := map[string]string{}
	err = fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		etag := `"` + hex.EncodeToString(sum[:etagBytes]) + `"`
		etags[name] = etag
		if path.Ext(name) == ".html" {
			pages[name] = page{body: body, etag: etag}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("webui: %w", err)
	}
	return &Handler{fsys: fsys, csp: buildCSP(hashes, origin), pages: pages, etags: etags}, nil
}

// CSP returns the policy sent with every HTML response.
func (h *Handler) CSP() string { return h.csp }

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", allowMethods)
		httpx.WriteProblem(w, httpx.Problem{Status: http.StatusMethodNotAllowed})
		return
	}
	name, ok := fileName(r.URL.Path)
	if !ok {
		httpx.WriteProblem(w, httpx.Problem{Status: http.StatusNotFound})
		return
	}
	if name != "" {
		if p, isPage := h.pages[name]; isPage {
			h.servePage(w, r, p)
			return
		}
		if h.serveAsset(w, r, name) {
			return
		}
		if !isRoute(name) {
			httpx.WriteProblem(w, httpx.Problem{Status: http.StatusNotFound})
			return
		}
	}
	h.servePage(w, r, h.pages[fallbackFile])
}

// fileName maps a request path to a slash-separated name relative to the root of the file system, "" meaning the
// root. It reports false for a path that tries to escape, names a hidden file or is not a valid file name; the
// ".." check runs on the decoded path, so %2e%2e is covered.
func fileName(urlPath string) (string, bool) {
	if !strings.HasPrefix(urlPath, "/") {
		return "", false
	}
	for seg := range strings.SplitSeq(urlPath, "/") {
		if seg == ".." || (strings.HasPrefix(seg, ".") && seg != ".") {
			return "", false
		}
	}
	name := strings.TrimPrefix(path.Clean(urlPath), "/")
	if name != "" && !fs.ValidPath(name) {
		return "", false
	}
	return name, true
}

// isRoute reports whether a path that matched no file may be answered with the SPA shell: a client-side route
// has no file extension (or .html), and nothing under /_nuxt/ ever is one. A last segment with a dot counts as a
// file name, so a missing one answers 404 instead of the shell: SPA routes must not end in a dotted segment
// (identifiers are UUID-like, per the contract, and never contain a dot).
func isRoute(name string) bool {
	if name == strings.TrimSuffix(assetPrefix, "/") || strings.HasPrefix(name, assetPrefix) {
		return false
	}
	ext := path.Ext(name)
	return ext == "" || ext == ".html"
}

func (h *Handler) servePage(w http.ResponseWriter, r *http.Request, p page) {
	hd := w.Header()
	hd.Set(httpx.HeaderContentType, contentType(".html"))
	hd.Set(httpx.HeaderCacheControl, cacheRevalidate)
	hd.Set(headerCSP, h.csp)
	hd.Set("ETag", p.etag)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(p.body))
}

// serveAsset serves a regular file; it reports false when name is not one (missing or a directory).
func (h *Handler) serveAsset(w http.ResponseWriter, r *http.Request, name string) bool {
	f, err := h.fsys.Open(name)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	hd := w.Header()
	hd.Set(httpx.HeaderContentType, contentType(path.Ext(name)))
	if etag, ok := h.etags[name]; ok {
		hd.Set("ETag", etag)
	}
	if strings.HasPrefix(name, assetPrefix) {
		hd.Set(httpx.HeaderCacheControl, cacheImmutable)
	} else {
		hd.Set(httpx.HeaderCacheControl, cacheRevalidate)
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok { // embed and fstest files seek; an exotic fs.FS gets its content buffered
		body, err := io.ReadAll(f)
		if err != nil {
			httpx.WriteProblem(w, httpx.Problem{Status: http.StatusInternalServerError})
			return true
		}
		rs = bytes.NewReader(body)
	}
	http.ServeContent(w, r, "", time.Time{}, rs)
	return true
}

// contentTypes maps file extensions to media types; the files are never sniffed.
var contentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".json":        "application/json",
	".map":         "application/json",
	".webmanifest": "application/manifest+json",
	".txt":         "text/plain; charset=utf-8",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".jpg":         "image/jpeg",
	".jpeg":        "image/jpeg",
	".gif":         "image/gif",
	".webp":        "image/webp",
	".avif":        "image/avif",
	".ico":         "image/x-icon",
	".woff":        "font/woff",
	".woff2":       "font/woff2",
	".ttf":         "font/ttf",
	".otf":         "font/otf",
}

const contentTypeUnknown = "application/octet-stream"

func contentType(ext string) string {
	if ct, ok := contentTypes[strings.ToLower(ext)]; ok {
		return ct
	}
	return contentTypeUnknown
}
