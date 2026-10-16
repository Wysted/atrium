package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
