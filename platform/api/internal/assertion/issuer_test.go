package assertion_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"platform/api/internal/assertion"
	"platform/api/internal/keys"
)

const testIssuer = "https://auth.apps.example.test"

var now = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

type staticSigner struct {
	signer keys.Signer
	err    error
}

func (s staticSigner) Signer() (keys.Signer, error) { return s.signer, s.err }

func newSigner(t *testing.T) keys.Signer {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	kid, err := keys.Thumbprint(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return keys.Signer{KID: kid, Key: k}
}

func newIssuer(t *testing.T, src assertion.SignerSource) *assertion.Issuer {
	t.Helper()
	i, err := assertion.NewIssuer(assertion.Config{Issuer: testIssuer, Signers: src, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func validRequest() assertion.Request {
	return assertion.Request{
		Audience: "app_01J0000000000000000000000", Subject: "0198a1e2-0000-7000-8000-000000000001",
		Email: "ana@example.test", Name: "Ana", SessionID: "0198a1e2-0000-7000-8000-0000000000aa",
		Roles: []string{"editor", "viewer"},
	}
}

func TestIssueMatchesContract(t *testing.T) {
	signer := newSigner(t)
	issued, err := newIssuer(t, staticSigner{signer: signer}).Issue(validRequest())
	if err != nil {
		t.Fatal(err)
	}

	var claims jwt.MapClaims
	tok, err := jwt.NewParser(jwt.WithValidMethods([]string{"ES256"}), jwt.WithTimeFunc(func() time.Time { return now })).
		ParseWithClaims(issued.Token, &claims, func(*jwt.Token) (any, error) { return &signer.Key.PublicKey, nil })
	if err != nil {
		t.Fatalf("signature/claims do not validate: %v", err)
	}

	wantHeader := map[string]any{"alg": "ES256", "kid": signer.KID, "typ": "platform-assertion+jwt"}
	if fmt.Sprint(tok.Header) != fmt.Sprint(wantHeader) {
		t.Errorf("header = %v, want %v", tok.Header, wantHeader)
	}

	iat := float64(now.Unix())
	want := map[string]any{
		"iss": testIssuer, "aud": "app_01J0000000000000000000000", "sub": "0198a1e2-0000-7000-8000-000000000001",
		"email": "ana@example.test", "name": "Ana", "iat": iat, "nbf": iat, "exp": iat + 90,
		"sid": "0198a1e2-0000-7000-8000-0000000000aa",
	}
	for k, v := range want {
		if claims[k] != v {
			t.Errorf("claim %s = %v (%T), want %v", k, claims[k], claims[k], v)
		}
	}
	if roles, _ := claims["roles"].([]any); len(roles) != 2 || roles[0] != "editor" || roles[1] != "viewer" {
		t.Errorf("roles = %v", claims["roles"])
	}
	if len(claims) != len(want)+2 { // + jti + roles
		t.Errorf("unexpected claim set: %v", claims)
	}
	if issued.JTI != claims["jti"] || !issued.ExpiresAt.Equal(now.Add(assertion.TTL)) {
		t.Errorf("Issued metadata = %+v", issued)
	}
}

func TestJTIIs128BitRandom(t *testing.T) {
	issuer := newIssuer(t, staticSigner{signer: newSigner(t)})
	seen := map[string]bool{}
	for range 200 {
		issued, err := issuer.Issue(validRequest())
		if err != nil {
			t.Fatal(err)
		}
		raw, err := base64.RawURLEncoding.DecodeString(issued.JTI)
		if err != nil || len(raw) != 16 {
			t.Fatalf("jti %q: %d bytes, err %v", issued.JTI, len(raw), err)
		}
		if seen[issued.JTI] {
			t.Fatalf("duplicate jti %q", issued.JTI)
		}
		seen[issued.JTI] = true
	}
}

func TestIssueRoles(t *testing.T) {
	roles := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("role-%02d-%s", i, strings.Repeat("x", 40))
		}
		return out
	}
	tests := []struct {
		name      string
		roles     []string
		wantErr   error
		wantEmpty bool
	}{
		{"nil roles encode as an empty array", nil, nil, true},
		{"at the cap", roles(assertion.MaxRoles), nil, false},
		{"over the cap is an error, not truncated", roles(assertion.MaxRoles + 1), assertion.ErrTooManyRoles, false},
	}
	issuer := newIssuer(t, staticSigner{signer: newSigner(t)})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validRequest()
			req.Roles = tt.roles
			issued, err := issuer.Issue(req)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			payload := strings.Split(issued.Token, ".")[1]
			raw, _ := base64.RawURLEncoding.DecodeString(payload)
			if tt.wantEmpty && !strings.Contains(string(raw), `"roles":[]`) {
				t.Errorf("payload = %s", raw)
			}
		})
	}
}

// worstCaseRequest fills every field up to the schema caps (migration 0005, which only bounds LENGTH,
// in characters): 32 roles of 32 bytes, a 254-character email and a 200-character name built from
// email and name characters.
func worstCaseRequest(email, name string) assertion.Request {
	req := validRequest()
	req.Email = email
	req.Name = name
	req.Roles = make([]string, assertion.MaxRoles)
	for i := range req.Roles {
		req.Roles[i] = fmt.Sprintf("%02d-%s", i, strings.Repeat("r", 29)) // 32-byte role keys
	}
	return req
}

// The worst case with ordinary text (4-byte runes in the name) fits MaxSize. This is NOT a bound for every
// value the schema accepts: see TestEscapedCharactersCanExceedMaxSizeAndFailClosed.
func TestWorstCaseAssertionFitsMaxSize(t *testing.T) {
	req := worstCaseRequest(
		strings.Repeat("e", 254-len("@example.test"))+"@example.test",
		strings.Repeat("\U0001F600", 200),
	)
	issued, err := newIssuer(t, staticSigner{signer: newSigner(t)}).Issue(req)
	if err != nil {
		t.Fatalf("worst case of plain text within the schema caps was rejected: %v", err)
	}
	if len(issued.Token) > assertion.MaxSize {
		t.Fatalf("assertion is %d bytes, want <= %d", len(issued.Token), assertion.MaxSize)
	}
	t.Logf("worst-case assertion: %d bytes (max %d)", len(issued.Token), assertion.MaxSize)
}

// json.Marshal escapes <, > and & as 6-byte \u00XX sequences, so email and name values that pass the
// length CHECKs can serialize well past MaxSize. The result must be ErrTooLarge (fail closed, never a
// truncated or oversized token), and the error must not leak the values.
func TestEscapedCharactersCanExceedMaxSizeAndFailClosed(t *testing.T) {
	req := worstCaseRequest(strings.Repeat("<", 254), strings.Repeat("&>", 100))
	issued, err := newIssuer(t, staticSigner{signer: newSigner(t)}).Issue(req)
	if !errors.Is(err, assertion.ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge for an escape-heavy worst case", err)
	}
	if issued.Token != "" {
		t.Fatalf("a token was returned together with ErrTooLarge (%d bytes)", len(issued.Token))
	}
	if strings.Contains(err.Error(), "<<<") {
		t.Fatal("error leaks the email")
	}
}

func TestIssueRejectsOversizedAssertion(t *testing.T) {
	req := validRequest()
	req.Name = strings.Repeat("n", assertion.MaxSize) // far beyond the 200-character DB cap
	_, err := newIssuer(t, staticSigner{signer: newSigner(t)}).Issue(req)
	if !errors.Is(err, assertion.ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestIssueErrors(t *testing.T) {
	noKey := errors.New("no key")
	tests := []struct {
		name string
		src  assertion.SignerSource
		edit func(*assertion.Request)
		is   error
	}{
		{"no active signer", staticSigner{err: noKey}, func(*assertion.Request) {}, noKey},
		{"missing audience", staticSigner{}, func(r *assertion.Request) { r.Audience = "" }, nil},
		{"missing subject", staticSigner{}, func(r *assertion.Request) { r.Subject = "" }, nil},
		{"missing session id", staticSigner{}, func(r *assertion.Request) { r.SessionID = "" }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validRequest()
			tt.edit(&req)
			_, err := newIssuer(t, tt.src).Issue(req)
			if err == nil || (tt.is != nil && !errors.Is(err, tt.is)) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestNewIssuerValidatesConfig(t *testing.T) {
	if _, err := assertion.NewIssuer(assertion.Config{Signers: staticSigner{}}); err == nil {
		t.Error("missing issuer accepted")
	}
	if _, err := assertion.NewIssuer(assertion.Config{Issuer: testIssuer}); err == nil {
		t.Error("missing signers accepted")
	}
}
