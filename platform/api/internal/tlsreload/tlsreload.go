// Package tlsreload serves a TLS certificate pair read from two PEM files and picks up replacements without a
// restart. It is deliberately neutral about who writes the files (cert-manager, Vault agent, a manual Secret
// update, a sidecar): rotation is "the files change".
package tlsreload

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"platform/api/internal/secretfile"
)

const (
	// DefaultInterval is how often the files are compared with the loaded pair. A kubelet refreshes mounted
	// Secrets about once a minute, so a faster poll buys nothing.
	DefaultInterval = 30 * time.Second
	// MinVersion is the lowest TLS version served. Every in-cluster client (the Go SDK, Grafana) speaks 1.3.
	MinVersion = tls.VersionTLS13

	// ExpiryWarning is how long before NotAfter the served leaf starts being reported (once per pair).
	ExpiryWarning = 7 * 24 * time.Hour

	// maxPEMBytes bounds each file (a long chain is a few KiB).
	maxPEMBytes = 256 << 10
)

// ValidityError reports a leaf that is expired or not yet valid.
type ValidityError struct {
	NotBefore, NotAfter time.Time
	Expired             bool // true: NotAfter has passed; false: NotBefore is still ahead
}

func (e *ValidityError) Error() string {
	state := "not yet valid"
	if e.Expired {
		state = "expired"
	}
	return fmt.Sprintf("certificate %s (not before %s, not after %s)",
		state, e.NotBefore.UTC().Format(time.RFC3339), e.NotAfter.UTC().Format(time.RFC3339))
}

// Option customizes a Reloader.
type Option func(*Reloader)

// WithClock replaces time.Now (tests).
func WithClock(now func() time.Time) Option {
	return func(r *Reloader) {
		if now != nil {
			r.now = now
		}
	}
}

// Reloader holds the current certificate pair and refreshes it when the files change.
type Reloader struct {
	certFile, keyFile string
	interval          time.Duration
	log               *slog.Logger
	now               func() time.Time

	current atomic.Pointer[loaded]

	mu      sync.Mutex // guards lastErr
	lastErr string     // text of the last reported reload failure, to log each distinct failure once
}

// loaded is one parsed pair together with the raw bytes it came from (the change detector).
type loaded struct {
	cert     tls.Certificate
	rawCert  []byte
	rawKey   []byte
	leaf     *x509.Certificate
	notAfter time.Time
	warned   atomic.Bool // the expiry warning was logged for this pair
}

// New loads the initial pair; an invalid pair is an error so the process fails at start-up, not at the first
// handshake. A leaf that is expired or not yet valid counts as invalid. interval <= 0 means DefaultInterval.
func New(certFile, keyFile string, interval time.Duration, log *slog.Logger, opts ...Option) (*Reloader, error) {
	if certFile == "" || keyFile == "" {
		return nil, errors.New("tlsreload: certificate and key files are required")
	}
	if interval <= 0 {
		interval = DefaultInterval
	}
	if log == nil {
		log = slog.Default()
	}
	r := &Reloader{certFile: certFile, keyFile: keyFile, interval: interval, log: log, now: time.Now}
	for _, opt := range opts {
		opt(r)
	}
	l, err := r.read()
	if err != nil {
		return nil, err
	}
	if err := r.checkValidity(l); err != nil {
		return nil, err
	}
	r.current.Store(l)
	r.warnExpiry()
	return r, nil
}

// read parses the pair currently on disk.
func (r *Reloader) read() (*loaded, error) {
	rawCert, err := readPublic(r.certFile, maxPEMBytes)
	if err != nil {
		return nil, fmt.Errorf("certificate file: %w", err)
	}
	rawKey, err := secretfile.Read(r.keyFile, maxPEMBytes)
	if err != nil {
		return nil, fmt.Errorf("key file: %w", err)
	}
	pair, err := tls.X509KeyPair(rawCert, rawKey)
	if err != nil {
		return nil, fmt.Errorf("load key pair: %w", err)
	}
	if len(pair.Certificate) == 0 {
		return nil, errors.New("load key pair: no certificate")
	}
	leaf := pair.Leaf
	if leaf == nil {
		if leaf, err = x509.ParseCertificate(pair.Certificate[0]); err != nil {
			return nil, fmt.Errorf("parse leaf certificate: %w", err)
		}
	}
	return &loaded{cert: pair, rawCert: rawCert, rawKey: rawKey, leaf: leaf, notAfter: leaf.NotAfter}, nil
}

// checkValidity rejects a leaf outside its validity window at the reloader's clock.
func (r *Reloader) checkValidity(l *loaded) error {
	now := r.now()
	switch {
	case now.After(l.leaf.NotAfter):
		return &ValidityError{NotBefore: l.leaf.NotBefore, NotAfter: l.leaf.NotAfter, Expired: true}
	case now.Before(l.leaf.NotBefore):
		return &ValidityError{NotBefore: l.leaf.NotBefore, NotAfter: l.leaf.NotAfter}
	}
	return nil
}

// warnExpiry logs once per pair when the served leaf expires within ExpiryWarning (or already has).
func (r *Reloader) warnExpiry() {
	cur := r.current.Load()
	if cur.leaf.NotAfter.Sub(r.now()) > ExpiryWarning || !cur.warned.CompareAndSwap(false, true) {
		return
	}
	r.log.Warn("JWKS TLS certificate expires soon; renew it",
		"not_before", cur.leaf.NotBefore, "not_after", cur.leaf.NotAfter)
}

// readPublic reads a file of public material (the certificate chain): it must be a regular file of at most
// maxBytes, but no permission policy applies. Only the private key goes through secretfile.
func readPublic(path string, maxBytes int) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // G304: path is the operator-configured certificate location, never request input
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer func() { _ = f.Close() }() // read-only handle: a close error cannot lose data
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	if len(raw) > maxBytes {
		return nil, errors.New("file too large")
	}
	return raw, nil
}

// GetCertificate implements tls.Config.GetCertificate with the last good pair.
func (r *Reloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return &r.current.Load().cert, nil
}

// TLSConfig returns the server configuration backed by the reloader.
func (r *Reloader) TLSConfig() *tls.Config {
	return &tls.Config{MinVersion: MinVersion, GetCertificate: r.GetCertificate}
}

// Reload re-reads the files and swaps the pair in when it differs from the loaded one. On failure the last
// good pair keeps being served and the error is returned.
func (r *Reloader) Reload() error {
	l, err := r.read()
	if err != nil {
		return err
	}
	cur := r.current.Load()
	if bytes.Equal(cur.rawCert, l.rawCert) && bytes.Equal(cur.rawKey, l.rawKey) {
		return nil
	}
	if err := r.checkValidity(l); err != nil {
		return err
	}
	r.current.Store(l)
	r.log.Info("JWKS TLS certificate reloaded", "not_after", l.notAfter)
	return nil
}

// Run polls the files until ctx ends. A failure is logged once per distinct message (not every tick), and
// the recovery is logged too.
func (r *Reloader) Run(ctx context.Context) {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tick()
		}
	}
}

func (r *Reloader) tick() {
	err := r.Reload()
	r.warnExpiry()
	r.mu.Lock()
	defer r.mu.Unlock()
	var verr *ValidityError
	switch {
	case err == nil && r.lastErr != "":
		r.log.Info("JWKS TLS certificate files readable again")
		r.lastErr = ""
	case errors.As(err, &verr) && err.Error() != r.lastErr:
		r.lastErr = err.Error()
		r.log.Warn("JWKS TLS certificate replacement rejected; keeping the previous pair",
			"reason", err.Error(), "not_before", verr.NotBefore, "not_after", verr.NotAfter)
	case err != nil && err.Error() != r.lastErr:
		r.lastErr = err.Error()
		r.log.Error("JWKS TLS certificate reload failed; keeping the previous pair", "error", err.Error())
	}
}
