// Package probe checks, from inside the platform, that every deployment host is protected by ForwardAuth
// (contract block 6, "Observability"). A host that answers a cookie-less navigation with anything but a
// redirect to the auth host login is reachable without ForwardAuth, the most dangerous integration failure and
// one platform-api logs can never show.
package probe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Status is the outcome of probing a host. The values mirror the deployments.last_probe_status CHECK.
type Status string

const (
	// StatusOK means a redirect to the auth host login: ForwardAuth protects the host.
	StatusOK Status = "ok"
	// StatusUnprotected means the host answered without a redirect: it is served without ForwardAuth.
	StatusUnprotected Status = "unprotected"
	// StatusUnexpected means a redirect somewhere other than the auth host login.
	StatusUnexpected Status = "unexpected"
	// StatusUnreachable means a DNS, TCP, TLS or timeout failure: nothing could be said about the host.
	StatusUnreachable Status = "unreachable"
)

const (
	// DefaultTimeout bounds one probe, connection and TLS handshake included.
	DefaultTimeout = 10 * time.Second
	// loginPath is the path of the auth host login (the redirect target of every unauthenticated navigation).
	loginPath = "/login"
	// userAgent identifies the probe in access logs.
	userAgent = "portal-probe/1"
)

// Target is a deployment to probe.
type Target struct {
	Host  string
	AppID string
}

// Result is the outcome of probing one target. Code is the HTTP status, zero when there was no response.
type Result struct {
	Target
	Status Status
	Code   int
}

// CheckerConfig describes how the probe reaches the public entrypoint.
type CheckerConfig struct {
	// AuthHost is the host (with port, if any) of the auth server, from the issuer: a redirect is only "ok"
	// when it points to https://<AuthHost>/login.
	AuthHost string
	// Addr is the host:port the connection is made to, whatever the deployment host is (for example the Traefik
	// service). Empty means the deployment host on port 443 through DNS. The TLS server name and the Host
	// header are always the deployment host.
	Addr string
	// RootCAs verifies the entrypoint certificate; nil means the system roots.
	RootCAs *x509.CertPool
	// InsecureSkipVerify disables the certificate verification (development only: the local entrypoint uses a
	// self-signed certificate).
	InsecureSkipVerify bool
	// Timeout bounds one probe; zero means DefaultTimeout.
	Timeout time.Duration
}

// Checker probes hosts through the public entrypoint without cookies and classifies the answer.
type Checker struct {
	authHost string
	client   *http.Client
}

// NewChecker validates cfg and builds the HTTP client: no redirects followed, no cookies, no proxy, no
// connection reuse between hosts.
func NewChecker(cfg CheckerConfig) (*Checker, error) {
	if cfg.AuthHost == "" {
		return nil, errors.New("probe: the auth host is required")
	}
	if cfg.Addr != "" {
		if _, port, err := net.SplitHostPort(cfg.Addr); err != nil || port == "" {
			return nil, fmt.Errorf("probe: the dial address must be host:port: %q", cfg.Addr)
		}
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		// Every request dials the configured entrypoint; the request URL keeps the deployment host, which
		// becomes the TLS server name and the Host header.
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if cfg.Addr != "" {
				addr = cfg.Addr
			}
			return dialer.DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			RootCAs:            cfg.RootCAs,
			InsecureSkipVerify: cfg.InsecureSkipVerify, //nolint:gosec // G402: development-only flag, logged at Error on start-up
		},
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		DisableKeepAlives:     true,
		Proxy:                 nil,
	}
	return &Checker{
		authHost: strings.ToLower(cfg.AuthHost),
		client: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			// The redirect itself is the answer.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// Check requests https://<host>/ the way a browser navigation does, with no cookies, and classifies the answer.
// The body is never read.
func (c *Checker) Check(ctx context.Context, t Target) Result {
	res := Result{Target: t, Status: StatusUnreachable}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+t.Host+"/", nil)
	if err != nil {
		return res
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		return res
	}
	defer func() { _ = resp.Body.Close() }()
	res.Code = resp.StatusCode
	res.Status = classify(resp, c.authHost)
	return res
}

// classify applies the rules of the contract to a response:
//
//   - a redirect (301, 302, 303, 307, 308) to https://<auth host>/login... is ok, but only the temporary
//     redirects ForwardAuth uses (302, 303, 307); a permanent redirect to the login is unexpected
//   - any other redirect, one without a usable Location, or a relative one (it stays on the deployment host)
//     is unexpected
//   - any other response is unprotected: the host was served without ForwardAuth
func classify(resp *http.Response, authHost string) Status {
	if !isRedirect(resp.StatusCode) {
		return StatusUnprotected
	}
	switch resp.StatusCode {
	case http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect:
	default:
		return StatusUnexpected
	}
	loc, err := resp.Location() // a relative Location resolves against the deployment host
	if err != nil {
		return StatusUnexpected
	}
	if !isLoginURL(loc, authHost) {
		return StatusUnexpected
	}
	return StatusOK
}

func isRedirect(code int) bool {
	switch code {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

func isLoginURL(u *url.URL, authHost string) bool {
	return u.Scheme == "https" && strings.EqualFold(u.Host, authHost) && u.User == nil && u.Path == loginPath
}
