package forwardauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"platform/api/internal/access"
	"platform/api/internal/apitoken"
	"platform/api/internal/assertion"
	"platform/api/internal/audit"
)

// stubs: nothing but the panicking directory is ever called.
type panickingApps struct{}

func (panickingApps) AppForHost(context.Context, string) (string, bool, error) { panic("boom") }

type (
	noopSessions  struct{ Sessions }
	noopEvaluator struct{}
	noopProfiles  struct{}
	noopDenials   struct{}
	noopIssuer    struct{}
	noopAudit     struct{ audit.Recorder }
	noopTokens    struct{}
)

func (noopTokens) TokenByHash(context.Context, []byte) (apitoken.Record, bool, error) {
	return apitoken.Record{}, false, nil
}

func (noopEvaluator) Evaluate(context.Context, access.Request) (access.Decision, error) {
	return access.Decision{}, nil
}
func (noopProfiles) Profile(context.Context, string) (access.Profile, error) {
	return access.Profile{}, nil
}
func (noopDenials) RecordDenial(context.Context, access.Denial) error { return nil }
func (noopIssuer) Issue(assertion.Request) (assertion.Issued, error)  { return assertion.Issued{}, nil }

type recordedDecision struct{ decision, reason string }

type decisionObserver struct{ got []recordedDecision }

func (o *decisionObserver) Decision(decision, reason string, _ time.Duration) {
	o.got = append(o.got, recordedDecision{decision, reason})
}
func (*decisionObserver) CacheLookup(string, bool) {}
func (*decisionObserver) AppLogout()               {}

// A panic in the decision is still counted, as a backend error: the recovery middleware answers 503 outside.
func TestPanicInTheDecisionIsCountedAsABackendError(t *testing.T) {
	t.Parallel()
	obs := &decisionObserver{}
	h, err := New(Config{
		AuthHost: "auth.example.test", Apps: panickingApps{}, Sessions: noopSessions{}, Evaluator: noopEvaluator{},
		Profiles: noopProfiles{}, Denials: noopDenials{}, Issuer: noopIssuer{}, Audit: noopAudit{}, Tokens: noopTokens{}, Observer: obs,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/forwardauth", nil)
	r.Header.Set(headerForwardedHost, "app.example.test")
	func() {
		defer func() { _ = recover() }()
		h.ServeHTTP(httptest.NewRecorder(), r)
	}()
	if len(obs.got) != 1 || obs.got[0] != (recordedDecision{DecisionError, ReasonBackend}) {
		t.Fatalf("decisions = %v, want one error/backend_failure", obs.got)
	}
}
