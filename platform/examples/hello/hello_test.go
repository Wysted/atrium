package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	platformauth "platform/sdk/go"
)

const (
	testIssuer = "https://auth.apps.example.test"
	testAppID  = "app_01J0000000000000000000000"
	testKID    = "kid-1"
	testSub    = "0198a1e2-0000-7000-8000-000000000001"
)

// jwksServer serves one public key; status != 200 simulates an outage.
func jwksServer(t *testing.T, pub *ecdsa.PublicKey, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		raw, err := pub.Bytes()
		if err != nil {
			t.Errorf("public key bytes: %v", err)
		}
		doc := map[string]any{"keys": []map[string]string{{
			"kty": "EC", "crv": "P-256", "kid": testKID, "use": "sig", "alg": "ES256",
			"x": base64.RawURLEncoding.EncodeToString(raw[1:33]),
			"y": base64.RawURLEncoding.EncodeToString(raw[33:]),
		}}}
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("Content-Type", "application/jwk-set+json")
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func mint(t *testing.T, key *ecdsa.PrivateKey, name string) string {
	t.Helper()
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": testIssuer, "aud": testAppID, "sub": testSub,
		"email": "ana@example.test", "name": name, "sid": "sid-1", "jti": "jti-1",
		"roles": []string{"editor", "viewer"},
		"iat":   now.Unix(), "nbf": now.Unix(), "exp": now.Add(platformauth.AssertionTTL).Unix(),
	})
	tok.Header["typ"] = "platform-assertion+jwt"
	tok.Header["kid"] = testKID
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return s
}

func newApp(t *testing.T, jwksStatus int) (*httptest.Server, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey, jwksStatus)
	v, err := platformauth.NewVerifier(platformauth.Config{Issuer: testIssuer, Audience: testAppID, JWKSURL: jwks.URL})
	if err != nil {
		t.Fatalf("new verifier: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	app := httptest.NewServer(v.MiddlewareWithLogger(log, newHandler(log)))
	t.Cleanup(app.Close)
	return app, key
}

// reply is the part of a response the tests inspect, so the body is closed inside get.
type reply struct {
	StatusCode int
	Header     http.Header
}

func get(t *testing.T, url, assertion string) (reply, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if assertion != "" {
		req.Header.Set(platformauth.HeaderName, assertion)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return reply{StatusCode: resp.StatusCode, Header: resp.Header}, string(body)
}

func TestPageWithValidAssertion(t *testing.T) {
	app, key := newApp(t, http.StatusOK)
	resp, body := get(t, app.URL+"/", mint(t, key, "Ana <b>"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	for _, want := range []string{testAppID, testSub, "ana@example.test", "editor, viewer", "sid-1",
		"Ana &lt;b&gt;", `action="/_platform/logout"`, `method="post"`, "Sign out"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	if strings.Contains(body, "Ana <b>") {
		t.Error("name was not HTML-escaped")
	}
	for header, want := range map[string]string{
		"Content-Security-Policy": "frame-ancestors 'none'",
		"Referrer-Policy":         "no-referrer",
	} {
		if got := resp.Header.Get(header); !strings.Contains(got, want) {
			t.Errorf("%s = %q, want it to contain %q", header, got, want)
		}
	}
}

func TestWhoamiWithValidAssertion(t *testing.T) {
	app, key := newApp(t, http.StatusOK)
	resp, body := get(t, app.URL+whoamiPath, mint(t, key, "Ana"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got identity
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.AppID != testAppID || got.Subject != testSub || got.Email != "ana@example.test" ||
		got.Name != "Ana" || got.SessionID != "sid-1" || strings.Join(got.Roles, ",") != "editor,viewer" {
		t.Errorf("unexpected identity: %+v", got)
	}
}

func TestStatuses(t *testing.T) {
	tests := []struct {
		name       string
		jwksStatus int
		path       string
		assertion  bool
		want       int
	}{
		{"page without assertion", http.StatusOK, "/", false, http.StatusUnauthorized},
		{"api without assertion", http.StatusOK, whoamiPath, false, http.StatusUnauthorized},
		{"JWKS down with assertion", http.StatusServiceUnavailable, "/", true, http.StatusServiceUnavailable},
		{"healthz without assertion", http.StatusServiceUnavailable, "/healthz", false, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, key := newApp(t, tt.jwksStatus)
			var assertion string
			if tt.assertion {
				assertion = mint(t, key, "Ana")
			}
			resp, _ := get(t, app.URL+tt.path, assertion)
			if resp.StatusCode != tt.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.want)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	full := map[string]string{
		envListenAddr: ":8080", envAppID: testAppID, envIssuer: testIssuer, envJWKSURL: "http://platform-api/jwks",
	}
	tests := []struct {
		name    string
		drop    string
		wantErr bool
	}{
		{"complete", "", false},
		{"missing listen addr", envListenAddr, true},
		{"missing app id", envAppID, true},
		{"missing issuer", envIssuer, true},
		{"missing jwks url", envJWKSURL, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadConfig(func(k string) string {
				if k == tt.drop {
					return ""
				}
				return full[k]
			})
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadConfigOptionalCAFile(t *testing.T) {
	env := map[string]string{
		envListenAddr: ":8080", envAppID: testAppID, envIssuer: testIssuer, envJWKSURL: "https://platform-api/jwks",
		envJWKSCAFile: " /etc/ca/ca.pem ",
	}
	cfg, err := loadConfig(func(k string) string { return env[k] })
	if err != nil || cfg.JWKSCAFile != "/etc/ca/ca.pem" {
		t.Fatalf("cfg = %+v, err %v", cfg, err)
	}
}

func TestLoadConfigCAFileNeedsHTTPS(t *testing.T) {
	for _, tt := range []struct {
		name, url string
		wantErr   bool
	}{
		{"https", "https://platform-api/jwks", false},
		{"uppercase HTTPS", "HTTPS://platform-api/jwks", false},
		{"http", "http://platform-api/jwks", true},
		{"uppercase HTTP", "HTTP://platform-api/jwks", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{
				envListenAddr: ":8080", envAppID: testAppID, envIssuer: testIssuer, envJWKSURL: tt.url, envJWKSCAFile: "/etc/ca/ca.pem",
			}
			_, err := loadConfig(func(k string) string { return env[k] })
			if (err != nil) != tt.wantErr || (err != nil && !strings.Contains(err.Error(), envJWKSCAFile)) {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNewVerifierWarnsOnPlainHTTP(t *testing.T) {
	for _, tt := range []struct {
		name, url string
		want      bool
	}{
		{"http", "http://platform-api/jwks", true},
		{"uppercase HTTP", "HTTP://platform-api/jwks", true},
		{"uppercase HTTPS", "HTTPS://platform-api/jwks", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			cfg := config{AppID: testAppID, Issuer: testIssuer, JWKSURL: tt.url}
			if _, err := newVerifier(cfg, slog.New(slog.NewTextHandler(&logs, nil))); err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(logs.String(), "plain http"); got != tt.want {
				t.Fatalf("warned = %v, want %v", got, tt.want)
			}
		})
	}
}

// ephemeralCA writes a CA certificate PEM and returns its path and a TLS server certificate for 127.0.0.1.
func ephemeralCA(t *testing.T) (caFile string, leaf tls.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	caFile = filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600); err != nil {
		t.Fatal(err)
	}
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
	der, err := x509.CreateCertificate(rand.Reader, tpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return caFile, tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func TestNewVerifierWithCAFile(t *testing.T) {
	signer, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caFile, leaf := ephemeralCA(t)
	plain := jwksServer(t, &signer.PublicKey, http.StatusOK)
	jwks := httptest.NewUnstartedServer(plain.Config.Handler)
	jwks.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}, MinVersion: tls.VersionTLS13}
	jwks.StartTLS()
	t.Cleanup(jwks.Close)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := config{AppID: testAppID, Issuer: testIssuer, JWKSURL: jwks.URL}
	token := mint(t, signer, "Ana")

	tests := []struct {
		name    string
		caFile  string
		wantErr bool // Verify error
	}{
		{"private CA trusted", caFile, false},
		{"no CA file: system roots reject the private CA", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base
			cfg.JWKSCAFile = tt.caFile
			v, err := newVerifier(cfg, log)
			if err != nil {
				t.Fatal(err)
			}
			_, err = v.Verify(t.Context(), token)
			if (err != nil) != tt.wantErr {
				t.Fatalf("verify err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
	t.Run("unreadable CA file fails", func(t *testing.T) {
		cfg := base
		cfg.JWKSCAFile = filepath.Join(t.TempDir(), "absent.pem")
		if _, err := newVerifier(cfg, log); err == nil {
			t.Fatal("expected an error")
		}
	})
}
