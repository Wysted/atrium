package platformauth_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	platformauth "platform/sdk/go"
)

// testCA is an ephemeral CA that can issue a loopback server certificate.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t testing.TB) *testCA {
	t.Helper()
	key := newKey(t)
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
	return &testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// leaf issues a 127.0.0.1 server certificate signed by the CA.
func (ca *testCA) leaf(t testing.TB) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "jwks"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func (ca *testCA) writeFile(t testing.TB) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(p, ca.pem, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// tlsJWKS serves the JWKS of srv over HTTPS with a certificate issued by ca.
func tlsJWKS(t testing.TB, srv *jwksServer, ca *testCA) string {
	t.Helper()
	ts := httptest.NewUnstartedServer(srv.Config.Handler)
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{ca.leaf(t)}, MinVersion: tls.VersionTLS13}
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return ts.URL + "/.well-known/jwks.json"
}

func TestVerifierRootCAs(t *testing.T) {
	signer := newKey(t)
	srv := newJWKSServer(t, map[string]*ecdsa.PrivateKey{testKID: signer})
	ca := newTestCA(t)
	url := tlsJWKS(t, srv, ca)
	clock := newClock()
	token := mint(t, signer, nil, baseClaims(clock.Now()))

	verify := func(t *testing.T, roots *x509.CertPool) error {
		t.Helper()
		v, err := platformauth.NewVerifier(platformauth.Config{
			Issuer: testIssuer, Audience: testAudience, JWKSURL: url, RootCAs: roots, Now: clock.Now,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = v.Verify(context.Background(), token)
		return err
	}

	t.Run("trusted CA", func(t *testing.T) {
		pool, err := platformauth.LoadRootCAs(ca.writeFile(t))
		if err != nil {
			t.Fatal(err)
		}
		if err := verify(t, pool); err != nil {
			t.Fatalf("verify: %v", err)
		}
	})
	t.Run("another CA", func(t *testing.T) {
		pool, err := platformauth.LoadRootCAs(newTestCA(t).writeFile(t))
		if err != nil {
			t.Fatal(err)
		}
		if err := verify(t, pool); !errors.Is(err, platformauth.ErrKeysUnavailable) {
			t.Fatalf("err = %v, want ErrKeysUnavailable", err)
		}
	})
	t.Run("system roots do not trust a private CA", func(t *testing.T) {
		if err := verify(t, nil); !errors.Is(err, platformauth.ErrKeysUnavailable) {
			t.Fatalf("err = %v, want ErrKeysUnavailable", err)
		}
	})
}

func TestLoadRootCAs(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty.pem")
	if err := os.WriteFile(empty, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"missing": filepath.Join(t.TempDir(), "nope.pem"), "no certificate": empty} {
		if _, err := platformauth.LoadRootCAs(path); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

type fakeTransport struct{}

func (fakeTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("unused")
}

func TestRootCAsRejectsForeignTransport(t *testing.T) {
	_, err := platformauth.NewVerifier(platformauth.Config{
		Issuer: testIssuer, Audience: testAudience, JWKSURL: "https://jwks.example.test/jwks",
		RootCAs: x509.NewCertPool(), HTTPClient: &http.Client{Transport: fakeTransport{}},
	})
	if err == nil {
		t.Fatal("expected an error for a non-*http.Transport")
	}
}

// wrapTransport is a RoundTripper that is not an *http.Transport.
type wrapTransport struct{ http.RoundTripper }

func TestRootCAsDoesNotDependOnDefaultTransport(t *testing.T) {
	signer := newKey(t)
	srv := newJWKSServer(t, map[string]*ecdsa.PrivateKey{testKID: signer})
	ca := newTestCA(t)
	url := tlsJWKS(t, srv, ca)
	clock := newClock()
	pool, err := platformauth.LoadRootCAs(ca.writeFile(t))
	if err != nil {
		t.Fatal(err)
	}

	orig := http.DefaultTransport
	http.DefaultTransport = wrapTransport{orig}
	t.Cleanup(func() { http.DefaultTransport = orig })

	v, err := platformauth.NewVerifier(platformauth.Config{
		Issuer: testIssuer, Audience: testAudience, JWKSURL: url, RootCAs: pool, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), mint(t, signer, nil, baseClaims(clock.Now()))); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestRootCAsRejectsCustomTLSDialers(t *testing.T) {
	dial := func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("unused") }
	for name, tr := range map[string]*http.Transport{
		"DialTLSContext": {DialTLSContext: dial},
		"DialTLS":        {DialTLS: func(string, string) (net.Conn, error) { return nil, errors.New("unused") }}, //nolint:staticcheck // SA1019: the deprecated field is what is being rejected
	} {
		t.Run(name, func(t *testing.T) {
			_, err := platformauth.NewVerifier(platformauth.Config{
				Issuer: testIssuer, Audience: testAudience, JWKSURL: "https://jwks.example.test/jwks",
				RootCAs: x509.NewCertPool(), HTTPClient: &http.Client{Transport: tr},
			})
			if err == nil {
				t.Fatal("expected an error: RootCAs would be ignored")
			}
		})
	}
}

func TestRootCAsMinVersion(t *testing.T) {
	for name, tc := range map[string]struct {
		transport *http.Transport
	}{
		"nil transport":         {nil},
		"transport without TLS": {&http.Transport{}},
		"TLS 1.2 is raised":     {&http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}},
		"unset is raised":       {&http.Transport{TLSClientConfig: &tls.Config{ServerName: "x"}}},
		"TLS 1.3 kept":          {&http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13}}},
	} {
		t.Run(name, func(t *testing.T) {
			client := &http.Client{}
			if tc.transport != nil {
				client.Transport = tc.transport
			}
			v, err := platformauth.NewVerifier(platformauth.Config{
				Issuer: testIssuer, Audience: testAudience, JWKSURL: "https://jwks.example.test/jwks",
				RootCAs: x509.NewCertPool(), HTTPClient: client,
			})
			if err != nil {
				t.Fatal(err)
			}
			tr, ok := platformauth.VerifierClient(v).Transport.(*http.Transport)
			if !ok || tr.TLSClientConfig == nil || tr.TLSClientConfig.MinVersion != tls.VersionTLS13 || tr.TLSClientConfig.RootCAs == nil {
				t.Fatalf("transport %#v: want TLS 1.3 minimum and the RootCAs", tr)
			}
			if tc.transport != nil && tc.transport.TLSClientConfig != nil && tc.transport.TLSClientConfig.RootCAs != nil {
				t.Fatal("the caller's transport must not be modified")
			}
		})
	}
}

func TestRootCAsKeepsRedirectAndTimeoutPolicy(t *testing.T) {
	signer := newKey(t)
	ca := newTestCA(t)
	pool, err := platformauth.LoadRootCAs(ca.writeFile(t))
	if err != nil {
		t.Fatal(err)
	}
	clock := newClock()
	token := mint(t, signer, nil, baseClaims(clock.Now()))
	startTLS := func(t *testing.T, h http.Handler) string {
		t.Helper()
		ts := httptest.NewUnstartedServer(h)
		ts.TLS = &tls.Config{Certificates: []tls.Certificate{ca.leaf(t)}, MinVersion: tls.VersionTLS13}
		ts.StartTLS()
		t.Cleanup(ts.Close)
		return ts.URL
	}

	t.Run("redirects are not followed", func(t *testing.T) {
		real := newJWKSServer(t, map[string]*ecdsa.PrivateKey{testKID: signer})
		var redirected atomic.Int64
		target := startTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			redirected.Add(1)
			http.Redirect(w, r, real.URLPath(), http.StatusFound)
		}))
		v, err := platformauth.NewVerifier(platformauth.Config{
			Issuer: testIssuer, Audience: testAudience, JWKSURL: target, RootCAs: pool, Now: clock.Now,
			HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return nil }},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := v.Verify(context.Background(), token); !errors.Is(err, platformauth.ErrKeysUnavailable) {
			t.Fatalf("err = %v, want ErrKeysUnavailable", err)
		}
		if redirected.Load() == 0 || real.hits.Load() != 0 {
			t.Fatalf("redirector hits = %d, redirect target hits = %d (must be 1+ and 0)", redirected.Load(), real.hits.Load())
		}
	})

	t.Run("client timeout is preserved", func(t *testing.T) {
		const clientTimeout = 100 * time.Millisecond
		release := make(chan struct{})
		target := startTLS(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
		t.Cleanup(func() { close(release) })
		v, err := platformauth.NewVerifier(platformauth.Config{
			Issuer: testIssuer, Audience: testAudience, JWKSURL: target, RootCAs: pool, Now: clock.Now,
			HTTPClient: &http.Client{Timeout: clientTimeout},
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := platformauth.VerifierClient(v).Timeout; got != clientTimeout {
			t.Fatalf("Timeout = %s, want %s", got, clientTimeout)
		}
		start := time.Now()
		if _, err := v.Verify(context.Background(), token); !errors.Is(err, platformauth.ErrKeysUnavailable) {
			t.Fatalf("err = %v, want ErrKeysUnavailable", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("fetch took %s, the client timeout was not applied", elapsed)
		}
	})
}
