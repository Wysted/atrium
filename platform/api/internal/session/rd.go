package session

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// MaxRDLength bounds the redirect target.
const MaxRDLength = 2048

// RD is a validated post-login redirect target.
type RD struct {
	URL  string // normalized: https, lowercase host, non-empty path
	Host string
}

// parseRD applies every redirect rule that needs no registry: absolute https URL, no userinfo, port,
// backslash, control character, or "//" in the path (raw or percent-decoded). Rejections wrap ErrInvalidRD.
func parseRD(raw string) (*url.URL, error) {
	reject := func(reason string) (*url.URL, error) { return nil, fmt.Errorf("%w: %s", ErrInvalidRD, reason) }

	if raw == "" || len(raw) > MaxRDLength {
		return reject("empty or too long")
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f || r == ' ' || r == '\\' {
			return reject("forbidden character")
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return reject("unparsable")
	}
	switch {
	case u.Scheme != "https":
		return reject("scheme must be https")
	case u.Opaque != "":
		return reject("opaque URL")
	case u.User != nil:
		return reject("userinfo not allowed")
	case u.Host == "" || strings.Contains(u.Host, ":"):
		return reject("host required, ports not allowed")
	case strings.Contains(u.Path, "//") || strings.Contains(u.EscapedPath(), "//"):
		return reject("empty path segment")
	case strings.Contains(u.Path, `\`):
		return reject("backslash in path")
	case strings.ContainsFunc(u.Path, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return reject("control character in path")
	}
	return u, nil
}

// WellFormedRD reports whether raw passes every ValidateRD rule except the registered-host check. The
// ForwardAuth handler uses it so it never builds a login URL that the auth host would answer with a 400.
func WellFormedRD(raw string) bool {
	_, err := parseRD(raw)
	return err == nil
}

// ValidateRD accepts only absolute https URLs whose host is registered in deployments (see parseRD for the
// shape rules) and returns the normalized URL. Every rejection wraps ErrInvalidRD; a failing registry lookup
// is returned as is.
func (s *Service) ValidateRD(ctx context.Context, raw string) (RD, error) {
	u, err := parseRD(raw)
	if err != nil {
		return RD{}, err
	}
	host := strings.ToLower(u.Hostname())
	registered, err := s.hosts.HostRegistered(ctx, host)
	if err != nil {
		return RD{}, fmt.Errorf("check redirect host: %w", err)
	}
	if !registered {
		return RD{}, fmt.Errorf("%w: host not registered", ErrInvalidRD)
	}
	u.Host = host
	if u.Path == "" {
		u.Path = "/"
	}
	return RD{URL: u.String(), Host: host}, nil
}
