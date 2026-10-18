package googlelogin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

type noStore struct{}

func (noStore) InsertFlow(context.Context, NewFlow) error { return nil }
func (noStore) ConsumeFlow(context.Context, []byte, string) (Flow, error) {
	return Flow{}, ErrInvalidFlow
}

func newLogin(t *testing.T, hd ...string) *Login {
	t.Helper()
	l, err := New(Config{ClientID: "id", ClientSecret: "secret", RedirectURL: "https://auth.example.test/oauth2/callback", AllowedHD: hd, Flows: noStore{}})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestNewValidates(t *testing.T) {
	base := Config{ClientID: "id", ClientSecret: "s", RedirectURL: "https://a/cb", AllowedHD: []string{"a.test"}, Flows: noStore{}}
	for name, mutate := range map[string]func(*Config){
		"no client id": func(c *Config) { c.ClientID = "" },
		"no secret":    func(c *Config) { c.ClientSecret = "" },
		"no redirect":  func(c *Config) { c.RedirectURL = "" },
		"no store":     func(c *Config) { c.Flows = nil },
		"no hd":        func(c *Config) { c.AllowedHD = nil },
		"empty hd":     func(c *Config) { c.AllowedHD = []string{""} },
		"uppercase hd": func(c *Config) { c.AllowedHD = []string{"A.test"} },
	} {
		t.Run(name, func(t *testing.T) {
			c := base
			mutate(&c)
			if _, err := New(c); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	if l, err := New(base); err != nil || l.cfg.Issuer != GoogleIssuer || l.Ready() {
		t.Fatalf("defaults: issuer %q ready %v err %v", l.cfg.Issuer, l.Ready(), err)
	}
}

func TestNotReadyBeforeDiscovery(t *testing.T) {
	l := newLogin(t, "a.test")
	if _, _, err := l.Begin(context.Background(), BeginInput{}); !errors.Is(err, ErrNotReady) {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := l.Complete(context.Background(), CompleteInput{}); !errors.Is(err, ErrNotReady) {
		t.Fatalf("Complete: %v", err)
	}
}

func TestHostedDomainHint(t *testing.T) {
	for _, tc := range []struct {
		allowed []string
		want    string
	}{
		{[]string{"a.test"}, "a.test"},
		{[]string{"a.test", "b.test"}, "*"}, // the first domain alone would lock b.test users out of Google's page
	} {
		if got := hostedDomainHint(tc.allowed); got != tc.want {
			t.Errorf("hostedDomainHint(%v) = %q, want %q", tc.allowed, got, tc.want)
		}
	}
}

func TestCheckIdentity(t *testing.T) {
	l := newLogin(t, "example.test", "other.test")
	tests := []struct {
		name       string
		sub, email string
		nameClaim  string
		hd         string
		verified   bool
		wantReason string
		wantEmail  string
		wantName   string
	}{
		{name: "ok", sub: "1", email: "A@Example.Test", nameClaim: "Ana", hd: "example.test", verified: true, wantEmail: "a@example.test", wantName: "Ana"},
		{name: "hd case-insensitive", sub: "1", email: "a@x", hd: " Other.TEST ", verified: true, wantEmail: "a@x"},
		{name: "unverified", sub: "1", email: "a@x", hd: "example.test", wantReason: ReasonEmailNotVerified},
		{name: "no hd", sub: "1", email: "a@x", verified: true, wantReason: ReasonHDMissing},
		{name: "foreign hd", sub: "1", email: "a@x", hd: "evil.test", verified: true, wantReason: ReasonHDNotAllowed},
		{name: "subdomain is not the domain", sub: "1", email: "a@x", hd: "sub.example.test", verified: true, wantReason: ReasonHDNotAllowed},
		{name: "fake prefix", sub: FakeSubjectPrefix + "a@x", email: "a@x", hd: "example.test", verified: true, wantReason: ReasonIncompleteClaims},
		{name: "fake prefix alone", sub: FakeSubjectPrefix, email: "a@x", hd: "example.test", verified: true, wantReason: ReasonIncompleteClaims},
		{name: "contains but does not start with fake prefix", sub: "1fake:2", email: "a@x", hd: "example.test", verified: true, wantEmail: "a@x"},
		{name: "no sub", email: "a@x", hd: "example.test", verified: true, wantReason: ReasonIncompleteClaims},
		{name: "no email", sub: "1", hd: "example.test", verified: true, wantReason: ReasonIncompleteClaims},
		{name: "NUL in email", sub: "1", email: "a\x00@x", hd: "example.test", verified: true, wantReason: ReasonIncompleteClaims},
		{name: "long name is cut", sub: "1", email: "a@x", nameClaim: strings.Repeat("é", maxNameRunes+10), hd: "example.test", verified: true, wantEmail: "a@x", wantName: strings.Repeat("é", maxNameRunes)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := l.checkIdentity(tt.sub, tt.email, tt.nameClaim, tt.hd, tt.verified)
			if tt.wantReason != "" {
				var rej *RejectedError
				if !errors.As(err, &rej) || rej.Reason != tt.wantReason {
					t.Fatalf("err = %v, want reason %q", err, tt.wantReason)
				}
				return
			}
			if err != nil || id.Email != tt.wantEmail || id.Name != tt.wantName || id.Subject != tt.sub {
				t.Fatalf("id = %+v, err %v", id, err)
			}
		})
	}
}

func TestProviderUnavailableClassification(t *testing.T) {
	retrieve := func(status int) error {
		return &oauth2.RetrieveError{Response: &http.Response{StatusCode: status}, ErrorCode: "invalid_grant"}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{"invalid_grant 400", context.Background(), retrieve(http.StatusBadRequest), false},
		{"invalid_client 401", context.Background(), retrieve(http.StatusUnauthorized), false},
		{"429", context.Background(), retrieve(http.StatusTooManyRequests), true},
		{"500", context.Background(), retrieve(http.StatusInternalServerError), true},
		{"502", context.Background(), retrieve(http.StatusBadGateway), true},
		{"307 (redirects are not followed)", context.Background(), retrieve(http.StatusTemporaryRedirect), true},
		{"transport error", context.Background(), errors.New("dial tcp: connection refused"), true},
		{"deadline over a refusal", canceled, retrieve(http.StatusBadRequest), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := providerUnavailable(tt.ctx, tt.err); got != tt.want {
				t.Fatalf("providerUnavailable = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHTTPClientNeverFollowsRedirects(t *testing.T) {
	provided := &http.Client{}
	l, err := New(Config{ClientID: "id", ClientSecret: "s", RedirectURL: "https://a/cb", AllowedHD: []string{"a.test"}, Flows: noStore{}, HTTPClient: provided})
	if err != nil {
		t.Fatal(err)
	}
	if l.http.CheckRedirect == nil || !errors.Is(l.http.CheckRedirect(nil, nil), http.ErrUseLastResponse) {
		t.Fatal("redirects are followed")
	}
	if provided.CheckRedirect != nil {
		t.Fatal("the caller's client was modified")
	}
	if d, _ := New(Config{ClientID: "id", ClientSecret: "s", RedirectURL: "https://a/cb", AllowedHD: []string{"a.test"}, Flows: noStore{}}); d.http.CheckRedirect == nil || d.http.Timeout != DefaultHTTPTimeout {
		t.Fatal("default client lost its redirect policy or timeout")
	}
}
