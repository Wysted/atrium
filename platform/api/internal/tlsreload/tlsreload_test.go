package tlsreload_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wysted/atrium/platform/api/internal/httpx"
	"github.com/Wysted/atrium/platform/api/internal/tlsreload"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testCA{cert: cert, key: key}
}

// issue returns a PEM certificate and key for 127.0.0.1 with the given serial.
func (ca *testCA) issue(t *testing.T, serial int64) (certPEM, keyPEM []byte) {
	t.Helper()
	return ca.issueWindow(t, serial, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
}

// issueWindow is issue with an explicit validity window.
func (ca *testCA) issueWindow(t *testing.T, serial int64, notBefore, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "jwks"},
		NotBefore: notBefore, NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func writePair(t *testing.T, dir string, certPEM, keyPEM []byte) (certFile, keyFile string) {
	t.Helper()
	certFile, keyFile = filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	writeAtomic(t, certFile, certPEM)
	writeAtomic(t, keyFile, keyPEM)
	return certFile, keyFile
}

// writeAtomic replaces path through a temp file and a rename, so a concurrent reader never sees a partial file.
func writeAtomic(t *testing.T, path string, content []byte) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

// syncBuffer is a log sink safe for the reloader goroutine and the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// serial returns the serial number of the certificate presented now.
func serial(t *testing.T, r *tlsreload.Reloader) int64 {
	t.Helper()
	c, err := r.TLSConfig().GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf.SerialNumber.Int64()
}

func TestNewValidation(t *testing.T) {
	ca := newCA(t)
	certPEM, keyPEM := ca.issue(t, 10)
	otherKey := func() []byte { _, k := ca.issue(t, 11); return k }()

	tests := []struct {
		name  string
		setup func(dir string) (cert, key string)
		want  string
	}{
		{"missing cert path", func(string) (string, string) { return "", "x" }, "required"},
		{"missing key path", func(string) (string, string) { return "x", "" }, "required"},
		{"unreadable cert", func(dir string) (string, string) {
			_, k := writePair(t, dir, certPEM, keyPEM)
			return filepath.Join(dir, "absent.crt"), k
		}, "certificate file"},
		{"unreadable key", func(dir string) (string, string) {
			c, _ := writePair(t, dir, certPEM, keyPEM)
			return c, filepath.Join(dir, "absent.key")
		}, "key file"},
		{"garbage", func(dir string) (string, string) { return writePair(t, dir, []byte("x"), []byte("y")) }, "key pair"},
		{"key does not match", func(dir string) (string, string) { return writePair(t, dir, certPEM, otherKey) }, "key pair"},
		{"key too open", func(dir string) (string, string) {
			c, k := writePair(t, dir, certPEM, keyPEM)
			if err := os.Chmod(k, 0o644); err != nil { //nolint:gosec // deliberately too open: the policy must reject it
				t.Fatal(err)
			}
			return c, k
		}, "too open"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, k := tt.setup(t.TempDir())
			_, err := tlsreload.New(c, k, 0, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
	t.Run("world-readable cert is public material", func(t *testing.T) {
		c, k := writePair(t, t.TempDir(), certPEM, keyPEM)
		if err := os.Chmod(c, 0o644); err != nil { //nolint:gosec // a certificate is public: the permission policy must not apply
			t.Fatal(err)
		}
		if _, err := tlsreload.New(c, k, 0, nil); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("valid", func(t *testing.T) {
		c, k := writePair(t, t.TempDir(), certPEM, keyPEM)
		r, err := tlsreload.New(c, k, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := r.TLSConfig().MinVersion; got != tls.VersionTLS13 {
			t.Fatalf("MinVersion = %#x, want TLS 1.3", got)
		}
	})
}

func TestReload(t *testing.T) {
	ca := newCA(t)
	dir := t.TempDir()
	c1, k1 := ca.issue(t, 1)
	certFile, keyFile := writePair(t, dir, c1, k1)
	logs := &syncBuffer{}
	r, err := tlsreload.New(certFile, keyFile, time.Hour, slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if got := serial(t, r); got != 1 {
		t.Fatalf("initial serial = %d", got)
	}

	// Unchanged files: nothing happens.
	if err := r.Reload(); err != nil || serial(t, r) != 1 {
		t.Fatalf("no-op reload: err %v", err)
	}

	// Rotation: new handshakes present the new pair.
	c2, k2 := ca.issue(t, 2)
	writePair(t, dir, c2, k2)
	if err := r.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := serial(t, r); got != 2 {
		t.Fatalf("serial after rotation = %d, want 2", got)
	}

	// Invalid replacement (cert and key out of step): the previous pair keeps being served.
	c3, _ := ca.issue(t, 3)
	writePair(t, dir, c3, k2)
	if err := r.Reload(); err == nil {
		t.Fatal("expected an error for a mismatched pair")
	}
	if got := serial(t, r); got != 2 {
		t.Fatalf("serial after invalid replacement = %d, want 2", got)
	}

	// Recovery with a valid pair.
	c4, k4 := ca.issue(t, 4)
	writePair(t, dir, c4, k4)
	if err := r.Reload(); err != nil || serial(t, r) != 4 {
		t.Fatalf("recovery: err %v", err)
	}
}

func TestRunReloadsAndLogsFailuresOnce(t *testing.T) {
	ca := newCA(t)
	dir := t.TempDir()
	c1, k1 := ca.issue(t, 1)
	certFile, keyFile := writePair(t, dir, c1, k1)
	logs := &syncBuffer{}
	const interval = 10 * time.Millisecond
	r, err := tlsreload.New(certFile, keyFile, interval, slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { defer close(done); r.Run(stopCtx(stop)) }()
	defer func() { close(stop); <-done }()

	writePair(t, dir, []byte("broken"), []byte("broken"))
	waitFor(t, func() bool { return strings.Contains(logs.String(), "reload failed") })
	time.Sleep(10 * interval) // many more ticks with the same failure
	if n := strings.Count(logs.String(), "reload failed"); n != 1 {
		t.Fatalf("failure logged %d times, want once", n)
	}
	if got := serial(t, r); got != 1 {
		t.Fatalf("serial = %d, want the old pair", got)
	}

	c2, k2 := ca.issue(t, 2)
	writePair(t, dir, c2, k2)
	waitFor(t, func() bool { return serial(t, r) == 2 })
}

// TestServeOverTLS performs real handshakes: a client trusting the CA connects, and after a rotation a new
// connection sees the new certificate while the server keeps running.
func TestServeOverTLS(t *testing.T) {
	ca := newCA(t)
	dir := t.TempDir()
	c1, k1 := ca.issue(t, 1)
	certFile, keyFile := writePair(t, dir, c1, k1)
	r, err := tlsreload.New(certFile, keyFile, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", r.TLSConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = conn.(*tls.Conn).Read(make([]byte, 1)); _ = conn.Close() }()
		}
	}()
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	peerSerial := func(roots *x509.CertPool) (int64, error) {
		d := tls.Dialer{Config: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}
		raw, err := d.DialContext(t.Context(), "tcp", ln.Addr().String())
		if err != nil {
			return 0, err
		}
		conn := raw.(*tls.Conn) //nolint:forcetypeassert // tls.Dialer returns *tls.Conn
		defer func() { _ = conn.Close() }()
		return conn.ConnectionState().PeerCertificates[0].SerialNumber.Int64(), nil
	}

	if got, err := peerSerial(pool); err != nil || got != 1 {
		t.Fatalf("first handshake: serial %d, err %v", got, err)
	}
	if _, err := peerSerial(newCA(t).poolOf()); err == nil {
		t.Fatal("handshake with an untrusted CA must fail")
	}
	c2, k2 := ca.issue(t, 2)
	writePair(t, dir, c2, k2)
	if err := r.Reload(); err != nil {
		t.Fatal(err)
	}
	if got, err := peerSerial(pool); err != nil || got != 2 {
		t.Fatalf("handshake after rotation: serial %d, err %v", got, err)
	}
}

func (ca *testCA) poolOf() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.cert)
	return p
}

// fakeClock is a settable clock shared by the test and the reloader goroutine.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func TestNewRejectsLeafOutsideValidity(t *testing.T) {
	ca := newCA(t)
	now := time.Now()
	for _, tt := range []struct {
		name string
		at   time.Time
		want string
	}{
		{"expired", now.Add(2 * time.Hour), "expired"},
		{"not yet valid", now.Add(-2 * time.Hour), "not yet valid"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			certPEM, keyPEM := ca.issue(t, 1)
			c, k := writePair(t, t.TempDir(), certPEM, keyPEM)
			_, err := tlsreload.New(c, k, 0, nil, tlsreload.WithClock((&fakeClock{t: tt.at}).Now))
			var verr *tlsreload.ValidityError
			if !errors.As(err, &verr) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want a ValidityError containing %q", err, tt.want)
			}
		})
	}
}

func TestReloadRejectsLeafOutsideValidity(t *testing.T) {
	ca := newCA(t)
	dir := t.TempDir()
	now := time.Now()
	c1, k1 := ca.issue(t, 1)
	certFile, keyFile := writePair(t, dir, c1, k1)
	logs := &syncBuffer{}
	const interval = 10 * time.Millisecond
	r, err := tlsreload.New(certFile, keyFile, interval, slog.New(slog.NewTextHandler(logs, nil)),
		tlsreload.WithClock((&fakeClock{t: now}).Now))
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { defer close(done); r.Run(stopCtx(stop)) }()
	defer func() { close(stop); <-done }()

	notAfter := now.Add(-time.Hour)
	expired, ek := ca.issueWindow(t, 2, now.Add(-3*time.Hour), notAfter)
	writePair(t, dir, expired, ek)
	waitFor(t, func() bool { return strings.Contains(logs.String(), "rejected") })
	time.Sleep(10 * interval)
	out := logs.String()
	if n := strings.Count(out, "rejected"); n != 1 {
		t.Fatalf("rejection logged %d times, want once:\n%s", n, out)
	}
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "not_before=") || !strings.Contains(out, "not_after=") {
		t.Fatalf("want a WARN with not_before and not_after:\n%s", out)
	}
	if got := serial(t, r); got != 1 {
		t.Fatalf("serial = %d, want the previous pair", got)
	}

	future, fk := ca.issueWindow(t, 3, now.Add(time.Hour), now.Add(2*time.Hour))
	writePair(t, dir, future, fk)
	waitFor(t, func() bool { return strings.Count(logs.String(), "rejected") == 2 })
	if got := serial(t, r); got != 1 {
		t.Fatalf("serial = %d, want the previous pair", got)
	}

	c4, k4 := ca.issue(t, 4)
	writePair(t, dir, c4, k4)
	waitFor(t, func() bool { return serial(t, r) == 4 })
}

func TestExpiryWarningOncePerPair(t *testing.T) {
	ca := newCA(t)
	dir := t.TempDir()
	now := time.Now()
	soon, sk := ca.issueWindow(t, 1, now.Add(-time.Hour), now.Add(3*24*time.Hour))
	certFile, keyFile := writePair(t, dir, soon, sk)
	logs := &syncBuffer{}
	const interval = 10 * time.Millisecond
	r, err := tlsreload.New(certFile, keyFile, interval, slog.New(slog.NewTextHandler(logs, nil)),
		tlsreload.WithClock((&fakeClock{t: now}).Now))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(logs.String(), "expires soon"); n != 1 || !strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("start-up warnings = %d, want 1 WARN:\n%s", n, logs.String())
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { defer close(done); r.Run(stopCtx(stop)) }()
	defer func() { close(stop); <-done }()

	time.Sleep(10 * interval)
	if n := strings.Count(logs.String(), "expires soon"); n != 1 {
		t.Fatalf("warned %d times for the same pair, want once", n)
	}

	long, lk := ca.issueWindow(t, 2, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	writePair(t, dir, long, lk)
	waitFor(t, func() bool { return serial(t, r) == 2 })
	time.Sleep(5 * interval)
	if n := strings.Count(logs.String(), "expires soon"); n != 1 {
		t.Fatalf("a long-lived pair must not warn; warnings = %d", n)
	}

	again, ak := ca.issueWindow(t, 3, now.Add(-time.Hour), now.Add(24*time.Hour))
	writePair(t, dir, again, ak)
	waitFor(t, func() bool { return strings.Count(logs.String(), "expires soon") == 2 })
}

// TestKubernetesSecretSymlinkFlip mimics a kubelet-refreshed Secret volume: the files are symlinks through a
// "..data" symlink to a versioned directory, and a refresh swaps "..data" with an atomic rename.
func TestKubernetesSecretSymlinkFlip(t *testing.T) {
	ca := newCA(t)
	dir := t.TempDir()
	writeVersion := func(name string, serialNo int64) {
		t.Helper()
		v := filepath.Join(dir, name)
		if err := os.Mkdir(v, 0o750); err != nil {
			t.Fatal(err)
		}
		c, k := ca.issue(t, serialNo)
		writeAtomic(t, filepath.Join(v, "tls.crt"), c)
		writeAtomic(t, filepath.Join(v, "tls.key"), k)
	}
	flip := func(name string) {
		t.Helper()
		tmp := filepath.Join(dir, "..data_tmp")
		if err := os.Symlink(name, tmp); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, filepath.Join(dir, "..data")); err != nil {
			t.Fatal(err)
		}
	}
	writeVersion("..v1", 1)
	writeVersion("..v2", 2)
	flip("..v1")
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	for _, f := range []string{"tls.crt", "tls.key"} {
		if err := os.Symlink(filepath.Join("..data", f), filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
	}

	const interval = 10 * time.Millisecond
	r, err := tlsreload.New(certFile, keyFile, interval, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := serial(t, r); got != 1 {
		t.Fatalf("initial serial = %d", got)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { defer close(done); r.Run(stopCtx(stop)) }()
	defer func() { close(stop); <-done }()

	flip("..v2")
	waitFor(t, func() bool { return serial(t, r) == 2 })
}

// TestServeJWKSOverTLS runs the JWKS listener the way serve does (httpx.NewServer + TLSConfig backed by the
// reloader, certificates only from GetCertificate): HTTPS works and a plain-HTTP request is refused with 400.
func TestServeJWKSOverTLS(t *testing.T) {
	ca := newCA(t)
	c1, k1 := ca.issue(t, 1)
	certFile, keyFile := writePair(t, t.TempDir(), c1, k1)
	r, err := tlsreload.New(certFile, keyFile, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httpx.NewServer("127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("jwks"))
	}))
	srv.TLSConfig = r.TLSConfig()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- srv.ServeTLS(ln, "", "") }() // same certificate source as ListenAndServeTLS("", "")
	t.Cleanup(func() {
		_ = srv.Close()
		if err := <-served; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serve: %v", err)
		}
	})

	get := func(client *http.Client, scheme string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, scheme+"://"+ln.Addr().String()+"/", nil)
		if err != nil {
			return nil, err
		}
		return client.Do(req)
	}
	secure := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: ca.poolOf(), MinVersion: tls.VersionTLS13}}}
	resp, err := get(secure, "https")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("https status = %d", resp.StatusCode)
	}
	resp, err = get(http.DefaultClient, "http")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("plain http status = %d, want 400", resp.StatusCode)
	}
}
