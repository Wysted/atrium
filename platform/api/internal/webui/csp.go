package webui

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Fixed directives of the SPA policy. The script-src directive is built at start-up because it carries the hashes
// of the inline scripts of the embedded HTML.
const (
	cspDefaultSrc     = "default-src 'none'"
	cspScriptSrcSelf  = "script-src 'self'"
	cspStyleSrc       = "style-src 'self'"
	cspImgSrc         = "img-src 'self'"
	cspFontSrc        = "font-src 'self'"
	cspConnectSrc     = "connect-src 'self'"
	cspBaseURI        = "base-uri 'none'"
	cspFormActionSelf = "form-action 'self'"
	cspFrameAncestors = "frame-ancestors 'none'"
)

// Script types. An empty type or a JavaScript MIME type is executable; application/json is data; importmap and
// anything unknown are refused.
const (
	typeJSON      = "application/json"
	typeModule    = "module"
	typeImportMap = "importmap"
)

// javascriptTypes are the JavaScript MIME types of the HTML Standard ("JavaScript MIME type essence match").
var javascriptTypes = []string{
	"application/ecmascript", "application/javascript", "application/x-ecmascript", "application/x-javascript",
	"text/ecmascript", "text/javascript", "text/javascript1.0", "text/javascript1.1", "text/javascript1.2",
	"text/javascript1.3", "text/javascript1.4", "text/javascript1.5", "text/jscript", "text/livescript",
	"text/x-ecmascript", "text/x-javascript",
}

// ErrNotBuilt means the file system has no 200.html: the SPA was not generated into the binary.
var ErrNotBuilt = errors.New("webui: 200.html not found, the SPA is not built into this binary")

// ErrNoInlineScript means no HTML file has an executable inline script. The Nuxt runtime config is always one, so
// its absence means the build is not what the policy was designed for.
var ErrNoInlineScript = errors.New("webui: no executable inline script found in the HTML files")

// scriptKind is how a script element counts for the policy.
type scriptKind int

const (
	scriptExecutable scriptKind = iota
	scriptData
)

// classifyScript decides what a script type attribute means. Browsers trim ASCII whitespace and compare the
// essence case-insensitively.
func classifyScript(typeAttr string) (scriptKind, error) {
	t := strings.ToLower(strings.Trim(typeAttr, " \t\n\f\r"))
	switch {
	case t == "", t == typeModule, slices.Contains(javascriptTypes, t):
		return scriptExecutable, nil
	case t == typeJSON:
		return scriptData, nil
	case t == typeImportMap:
		return 0, errors.New("importmap scripts are not allowed")
	default:
		return 0, fmt.Errorf("script type %q is not allowed", typeAttr)
	}
}

// scriptHashes parses every *.html of fsys and returns the sorted, deduplicated CSP hash sources
// ('sha256-<base64>') of their executable inline scripts.
func scriptHashes(fsys fs.FS) ([]string, error) {
	seen := map[string]struct{}{}
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || path.Ext(name) != ".html" {
			return nil
		}
		doc, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		hashes, err := documentHashes(doc)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		for _, h := range hashes {
			seen[h] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("webui: %w", err)
	}
	if len(seen) == 0 {
		return nil, ErrNoInlineScript
	}
	out := make([]string, 0, len(seen))
	for h := range seen {
		out = append(out, h)
	}
	slices.Sort(out)
	return out, nil
}

// documentHashes hashes the executable inline scripts of one HTML document. The parser runs with scripting
// disabled, like check-csp.mjs, so <noscript> content is parsed into elements and inspected too. The text of a
// script element is raw (no entity decoding) and the tokenizer normalizes CRLF and CR to LF, which is what the
// browser hashes.
func documentHashes(doc []byte) ([]string, error) {
	root, err := html.ParseWithOptions(bytes.NewReader(doc), html.ParseOptionEnableScripting(false))
	if err != nil {
		return nil, fmt.Errorf("parse HTML: %w", err)
	}
	var hashes []string
	var walk func(n *html.Node) error
	walk = func(n *html.Node) error {
		if n.Type == html.ElementNode && n.DataAtom == atom.Script {
			h, ok, err := scriptHash(n)
			if err != nil {
				return err
			}
			if ok {
				hashes = append(hashes, h)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if err := walk(c); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root); err != nil {
		return nil, err
	}
	return hashes, nil
}

// scriptHash returns the hash source of an inline executable script; ok is false for external and data scripts.
func scriptHash(n *html.Node) (hash string, ok bool, err error) {
	// Browsers ignore repeated attributes after the first; x/net/html keeps them all, in order, so take the first.
	var typeAttr string
	var seenType bool
	for _, a := range n.Attr {
		switch a.Key {
		case "src":
			return "", false, nil // external: governed by 'self'
		case "type":
			if !seenType {
				typeAttr, seenType = a.Val, true
			}
		}
	}
	kind, err := classifyScript(typeAttr)
	if err != nil {
		return "", false, err
	}
	if kind == scriptData {
		return "", false, nil
	}
	var text strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			text.WriteString(c.Data)
		}
	}
	sum := sha256.Sum256([]byte(text.String()))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'", true, nil
}

// buildCSP assembles the policy of the HTML responses, with the directives and the order of the contract.
// authOrigin is the validated origin of the auth host: Chrome applies form-action to the 303 that follows the
// logout POST.
func buildCSP(hashes []string, authOrigin string) string {
	script := cspScriptSrcSelf + " " + strings.Join(hashes, " ")
	return strings.Join([]string{
		cspDefaultSrc, script, cspStyleSrc, cspImgSrc, cspFontSrc, cspConnectSrc,
		cspFormActionSelf + " " + authOrigin, cspFrameAncestors, cspBaseURI,
	}, "; ")
}

// ErrInvalidAuthOrigin means Options.AuthOrigin is not a bare https origin.
var ErrInvalidAuthOrigin = errors.New("webui: auth origin must be https://host[:port] with no path, query, fragment or credentials")

// authOriginHostChars are the only characters accepted in the host[:port] of the auth origin, so nothing in it can
// act as a CSP separator or add a source.
const authOriginHostChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.-:"

// maxPort is the highest TCP port.
const maxPort = 65535

// ParseAuthOrigin validates raw (PORTAL_ISSUER) and returns scheme://host[:port]. It is the single source of truth
// for the auth origin: the config check of the binary and New both go through it. A port, when present, must be
// decimal digits in 1-65535, because browsers ignore a CSP source with any other.
func ParseAuthOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawPath != "" || u.RawQuery != "" ||
		u.ForceQuery || u.Fragment != "" || u.User != nil || u.Opaque != "" || strings.Trim(u.Host, authOriginHostChars) != "" ||
		(strings.Contains(u.Host, ":") && !validPort(u.Port())) {
		return "", fmt.Errorf("%w: %q", ErrInvalidAuthOrigin, raw)
	}
	return u.Scheme + "://" + u.Host, nil
}

// validPort reports whether p is a decimal port in 1-maxPort, with no sign or other characters.
func validPort(p string) bool {
	if p == "" || strings.Trim(p, "0123456789") != "" {
		return false
	}
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= maxPort
}
