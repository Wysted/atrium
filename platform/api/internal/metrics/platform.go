package metrics

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Wysted/atrium/platform/api/internal/access"
	"github.com/Wysted/atrium/platform/api/internal/authhost"
	"github.com/Wysted/atrium/platform/api/internal/forwardauth"
	"github.com/Wysted/atrium/platform/api/internal/httpx"
)

// Listeners of platform-api, the label of the JWKS counter.
const (
	ListenerJWKS   = "jwks"
	ListenerPublic = "public"
)

// Closed sets of the platform-api labels. The constants are the ones the producing packages use.
var (
	decisions = newSet(
		forwardauth.DecisionBypass, forwardauth.DecisionAllow, forwardauth.DecisionDeny, forwardauth.DecisionLoginRedirect,
		forwardauth.DecisionUnauthenticated, forwardauth.DecisionCallback, forwardauth.DecisionLogout, forwardauth.DecisionError,
	)
	reasons = newSet(
		access.ReasonGranted, access.ReasonUnknownHost, access.ReasonUnknownUser, access.ReasonUserDisabled,
		access.ReasonInitiativeInactive, access.ReasonAppDisabled, access.ReasonNoGrant, access.ReasonUnsupportedAction,
		forwardauth.ReasonAuthHost, forwardauth.ReasonPreflight, forwardauth.ReasonCallbackOK, forwardauth.ReasonCallbackInvalid,
		forwardauth.ReasonLogoutOK, forwardauth.ReasonLogoutMethod, forwardauth.ReasonLogoutCSRF, forwardauth.ReasonNoCookie,
		forwardauth.ReasonInvalidSession, forwardauth.ReasonBackend, forwardauth.ReasonNoSigner, forwardauth.ReasonAssertion,
		forwardauth.ReasonTokenNoBearer, forwardauth.ReasonTokenMalformed, forwardauth.ReasonTokenUnknown, forwardauth.ReasonTokenRevoked,
		forwardauth.ReasonTokenExpired, forwardauth.ReasonTokenOwnerInactive, forwardauth.ReasonTokenScope,
	)
	caches = newSet(forwardauth.CacheDecision, forwardauth.CacheHost, forwardauth.CacheNegativeHost)
	logins = []string{
		authhost.LoginSuccess, authhost.LoginProviderRefused, authhost.LoginRejected, authhost.LoginInvalidFlow,
		authhost.LoginFailed, authhost.LoginProviderUnavailable, authhost.LoginUserDisabled, authhost.LoginRateLimited,
		authhost.LoginInvalidRequest, authhost.LoginError,
	}
	loginOutcomes = newSet(logins...)
	listeners     = newSet(ListenerJWKS, ListenerPublic)
)

// Values of the logout "via" label.
const (
	viaApp      = "app"
	viaAuthHost = "auth_host"
)

// Platform holds the event metrics of platform-api. It implements forwardauth.Observer and authhost.Observer.
// Create it with NewPlatform and register the state read at scrape time with
// RegisterPlatformState.
type Platform struct {
	decisions        *prometheus.CounterVec
	decisionDuration *prometheus.HistogramVec
	cacheLookups     *prometheus.CounterVec
	logins           *prometheus.CounterVec
	logouts          *prometheus.CounterVec
	jwks             *prometheus.CounterVec
}

// NewPlatform creates the event metrics and registers them in reg.
func NewPlatform(reg prometheus.Registerer) (*Platform, error) {
	p := &Platform{
		decisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "platform_forwardauth_decisions_total",
			Help: "ForwardAuth decisions by decision and reason (the values of the decision log line).",
		}, []string{"decision", "reason"}),
		decisionDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "platform_forwardauth_decision_duration_seconds",
			Help:    "Time taken to decide a ForwardAuth request, database calls included.",
			Buckets: latencyBuckets,
		}, []string{"decision"}),
		cacheLookups: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "platform_forwardauth_cache_lookups_total",
			Help: "Lookups in the ForwardAuth caches (decision, host, negative_host) by result.",
		}, []string{"cache", "result"}),
		logins: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "platform_logins_total",
			Help: "Login attempts finished at the callback of the auth host, by outcome.",
		}, []string{"outcome"}),
		logouts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "platform_logouts_total",
			Help: "Sessions revoked by a logout, by where the user logged out (app or auth_host).",
		}, []string{"via"}),
		jwks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "platform_jwks_requests_total",
			Help: "Requests to the JWKS endpoint by listener and status class.",
		}, []string{"listener", "status_class"}),
	}
	// The small closed sets start at zero so rate() works from the first event.
	for _, o := range logins {
		p.logins.WithLabelValues(o)
	}
	for _, v := range []string{viaApp, viaAuthHost} {
		p.logouts.WithLabelValues(v)
	}
	for c := range caches {
		p.cacheLookups.WithLabelValues(c, resultLabel(true))
		p.cacheLookups.WithLabelValues(c, resultLabel(false))
	}
	if err := register(reg, p.decisions, p.decisionDuration, p.cacheLookups, p.logins, p.logouts, p.jwks); err != nil {
		return nil, err
	}
	return p, nil
}

// Decision implements forwardauth.Observer. Neither the host nor the app id is a label: see Cardinality in the
// package documentation.
func (p *Platform) Decision(decision, reason string, elapsed time.Duration) {
	decision = decisions.clamp(decision)
	p.decisions.WithLabelValues(decision, reasons.clamp(reason)).Inc()
	p.decisionDuration.WithLabelValues(decision).Observe(elapsed.Seconds())
}

// CacheLookup implements forwardauth.Observer.
func (p *Platform) CacheLookup(cache string, hit bool) {
	p.cacheLookups.WithLabelValues(caches.clamp(cache), resultLabel(hit)).Inc()
}

// AppLogout implements forwardauth.Observer.
func (p *Platform) AppLogout() { p.logouts.WithLabelValues(viaApp).Inc() }

// Login implements authhost.Observer.
func (p *Platform) Login(outcome string) {
	p.logins.WithLabelValues(loginOutcomes.clamp(outcome)).Inc()
}

// AuthHostLogout implements authhost.Observer.
func (p *Platform) AuthHostLogout() { p.logouts.WithLabelValues(viaAuthHost).Inc() }

// CountJWKS wraps the JWKS handler of one listener and counts its requests by status class.
func (p *Platform) CountJWKS(listener string, next http.Handler) http.Handler {
	listener = listeners.clamp(listener)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &httpx.StatusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		p.jwks.WithLabelValues(listener, statusClass(rec.StatusOrOK())).Inc()
	})
}

// KeyState is what the keyring tells about its signing key (keys.Keyring).
type KeyState interface {
	// ActiveSince returns when the loaded signer became active, and false while none is loaded.
	ActiveSince() (time.Time, bool)
	// KEKMismatch reports a KEK that does not match the active or next keys.
	KEKMismatch() bool
}

// PlatformSources are the places the state metrics are read from, at scrape time. Pool and Keys are required;
// Pending and UnknownDrops are skipped when nil.
type PlatformSources struct {
	Pool         *pgxpool.Pool
	Keys         KeyState
	Pending      func() int                          // hosts waiting for the next last_seen_at flush
	SeenDrops    func() uint64                       // host observations discarded because the queue was full
	SeenErrors   func() uint64                       // failed last_seen_at flushes
	UnknownDrops func() forwardauth.UnknownHostDrops // unknown_host denials that were not recorded
	TokenPending func() int                          // tokens waiting for the next last_used_at flush
	TokenDrops   func() uint64                       // token observations discarded because the queue was full
	TokenErrors  func() uint64                       // failed last_used_at flushes
	Now          func() time.Time                    // default time.Now
}

// RegisterPlatformState registers the metrics that are read from live state when Prometheus scrapes: signing key
// state, pending last_seen_at hosts, unknown_host drops and the database pool.
func RegisterPlatformState(reg prometheus.Registerer, src PlatformSources) error {
	now := src.Now
	if now == nil {
		now = time.Now
	}
	var (
		loaded = prometheus.NewDesc("platform_signing_key_loaded",
			"1 when this replica has an active signing key loaded and can sign, 0 otherwise.", nil, nil)
		age = prometheus.NewDesc("platform_signing_key_active_age_seconds",
			"Seconds since the active signing key was activated; absent while no signer is loaded.", nil, nil)
		mismatch = prometheus.NewDesc("platform_signing_key_kek_mismatch",
			"1 when this replica's KEK does not match the active or next keys (it neither signs nor rotates), 0 otherwise.", nil, nil)
		pending = prometheus.NewDesc("platform_lastseen_pending_hosts",
			"Hosts waiting in memory for the next last_seen_at flush.", nil, nil)
		seenDrops = prometheus.NewDesc("platform_lastseen_dropped_total",
			"last_seen_at observations discarded because the in-memory queue was full.", nil, nil)
		seenErrors = prometheus.NewDesc("platform_lastseen_flush_errors_total",
			"Failed last_seen_at flushes (the batch is kept and retried).", nil, nil)
		tokenErrors = prometheus.NewDesc("platform_token_use_flush_errors_total",
			"Failed last_used_at flushes (the batch is kept and retried).", nil, nil)
		tokenPending = prometheus.NewDesc("platform_token_use_pending_tokens",
			"API tokens waiting in memory for the next last_used_at flush.", nil, nil)
		tokenDrops = prometheus.NewDesc("platform_token_use_dropped_total",
			"last_used_at observations discarded because the in-memory queue was full.", nil, nil)
		drops = prometheus.NewDesc("platform_unknown_host_drops_total",
			"unknown_host denials that were not recorded, by reason (invalid, budget, marker, queue).", []string{"reason"}, nil)
	)
	errPool := poolMetrics(reg, "platform", src.Pool)
	errState := register(reg, collector{
		descs: []*prometheus.Desc{loaded, age, mismatch, pending, seenDrops, seenErrors, tokenPending, tokenDrops, tokenErrors, drops},
		collect: func(emit func(*prometheus.Desc, prometheus.ValueType, float64, ...string)) {
			since, ok := src.Keys.ActiveSince()
			emit(loaded, prometheus.GaugeValue, boolValue(ok))
			if ok {
				emit(age, prometheus.GaugeValue, max(0, now().Sub(since).Seconds()))
			}
			emit(mismatch, prometheus.GaugeValue, boolValue(src.Keys.KEKMismatch()))
			if src.Pending != nil {
				emit(pending, prometheus.GaugeValue, float64(src.Pending()))
			}
			if src.SeenDrops != nil {
				emit(seenDrops, prometheus.CounterValue, float64(src.SeenDrops()))
			}
			if src.SeenErrors != nil {
				emit(seenErrors, prometheus.CounterValue, float64(src.SeenErrors()))
			}
			if src.TokenErrors != nil {
				emit(tokenErrors, prometheus.CounterValue, float64(src.TokenErrors()))
			}
			if src.TokenPending != nil {
				emit(tokenPending, prometheus.GaugeValue, float64(src.TokenPending()))
			}
			if src.TokenDrops != nil {
				emit(tokenDrops, prometheus.CounterValue, float64(src.TokenDrops()))
			}
			if src.UnknownDrops != nil {
				d := src.UnknownDrops()
				emit(drops, prometheus.CounterValue, float64(d.Invalid), "invalid")
				emit(drops, prometheus.CounterValue, float64(d.Budget), "budget")
				emit(drops, prometheus.CounterValue, float64(d.Marker), "marker")
				emit(drops, prometheus.CounterValue, float64(d.Queue), "queue")
			}
		},
	})
	return errors.Join(errPool, errState)
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
