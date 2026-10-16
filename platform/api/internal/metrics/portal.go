package metrics

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"platform/api/internal/portalhttp"
	"platform/api/internal/probe"
)

// probeBuckets (seconds) cover a round from one host up to a full round of slow hosts (10 s each, 8 at a time).
var probeBuckets = []float64{0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300}

// Closed sets of the portal labels.
var (
	probeResults  = newSet(probe.RoundWritten, probe.RoundInconclusive, probe.RoundSkipped, probe.RoundIdle, probe.RoundError)
	probeStatuses = []probe.Status{
		probe.StatusOK, probe.StatusUnprotected, probe.StatusUnexpected, probe.StatusUnreachable, probe.StatusNever,
	}
)

// Portal holds the event metrics of the portal process. It implements portalhttp.Observer and probe.Observer.
// Create it with NewPortal and register the database pool with RegisterPortalState.
type Portal struct {
	requests      *prometheus.CounterVec
	duration      *prometheus.HistogramVec
	contention    prometheus.Counter
	probeRounds   *prometheus.CounterVec
	probeDuration *prometheus.HistogramVec
	probeWritten  prometheus.Gauge
	now           func() time.Time
}

// NewPortal creates the event metrics and registers them in reg. probeInterval is the configured probe interval
// (PORTAL_PROBE_INTERVAL, zero when the probe is disabled), exposed once as portal_probe_interval_seconds.
func NewPortal(reg prometheus.Registerer, probeInterval time.Duration) (*Portal, error) {
	p := &Portal{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "portal_http_requests_total",
			Help: "Requests to the public listener by method, route pattern and status class. A request rejected before routing has the route \"" + portalhttp.RouteUnmatched + "\".",
		}, []string{"method", "route", "status_class"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "portal_http_request_duration_seconds",
			Help:    "Time taken to answer a request to the public listener, by route pattern.",
			Buckets: latencyBuckets,
		}, []string{"route"}),
		contention: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "portal_contention_responses_total",
			Help: "Requests answered 503 because their transaction kept losing against concurrent ones.",
		}),
		probeRounds: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "portal_probe_rounds_total",
			Help: "Integration probe rounds by result: written, inconclusive (control sample failed), skipped (another replica runs it), idle (nothing due), error.",
		}, []string{"result"}),
		probeDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "portal_probe_round_duration_seconds",
			Help:    "Duration of an integration probe round, by result.",
			Buckets: probeBuckets,
		}, []string{"result"}),
		probeWritten: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "portal_probe_last_write_timestamp_seconds",
			Help: "Unix time of the last probe round that wrote results; the process start time until the first one (also when the probe is disabled).",
		}),
		now: time.Now,
	}
	p.probeWritten.SetToCurrentTime()
	for r := range probeResults {
		p.probeRounds.WithLabelValues(r)
	}
	interval := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "portal_probe_interval_seconds",
		Help: "Configured integration probe interval in seconds; 0 when the probe is disabled. Used to gate the probe alerts.",
	})
	interval.Set(max(probeInterval, 0).Seconds())
	if err := register(reg, p.requests, p.duration, p.contention, p.probeRounds, p.probeDuration, p.probeWritten, interval); err != nil {
		return nil, err
	}
	return p, nil
}

// Request implements portalhttp.Observer. route is a ServeMux pattern (or portalhttp.RouteUnmatched), so the
// number of routes bounds it; the method is client input and is clamped.
func (p *Portal) Request(method, route string, status int, elapsed time.Duration) {
	if route == "" {
		route = portalhttp.RouteUnmatched
	}
	p.requests.WithLabelValues(methods.clamp(method), route, statusClass(status)).Inc()
	p.duration.WithLabelValues(route).Observe(elapsed.Seconds())
}

// Contention implements portalhttp.Observer.
func (p *Portal) Contention() { p.contention.Inc() }

// Round implements probe.Observer.
func (p *Portal) Round(result string, elapsed time.Duration) {
	result = probeResults.clamp(result)
	p.probeRounds.WithLabelValues(result).Inc()
	p.probeDuration.WithLabelValues(result).Observe(elapsed.Seconds())
	if result == probe.RoundWritten {
		p.probeWritten.Set(float64(p.now().UnixNano()) / float64(time.Second))
	}
}

// Settings of the probe status collector.
const (
	// ProbeStatusTTL is how long a count of deployments by probe status is reused: Prometheus scrapes every replica
	// every few seconds and the value only changes once per probe round.
	ProbeStatusTTL = 30 * time.Second
	// ProbeStatusTimeout bounds the query made by a scrape.
	ProbeStatusTimeout = 2 * time.Second
)

// PortalSources are the places the portal state metrics are read from, at scrape time.
type PortalSources struct {
	Pool *pgxpool.Pool
	// ProbeStatus counts deployments by last probe status. It is read whether or not this replica runs the probe,
	// so every replica reports the state of the database.
	ProbeStatus probe.StatusCounter
	// TTL, Timeout and Now default to ProbeStatusTTL, ProbeStatusTimeout and time.Now.
	TTL     time.Duration
	Timeout time.Duration
	Now     func() time.Time
}

// RegisterPortalState registers the database pool statistics and the deployments by probe status, read when
// Prometheus scrapes.
func RegisterPortalState(reg prometheus.Registerer, src PortalSources) error {
	c := &probeStatusCollector{
		src: src.ProbeStatus, ttl: src.TTL, timeout: src.Timeout, now: src.Now,
		desc: prometheus.NewDesc("portal_deployments_by_probe_status",
			"Deployments by last probe status (never: not probed yet), read from the database at scrape time (cached for 30 s). Absent when the count failed.",
			[]string{"status"}, nil),
		errors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "portal_probe_status_count_errors_total",
			Help: "Failed counts of deployments by probe status, at most one per cache TTL (the gauge is absent meanwhile).",
		}),
	}
	if c.ttl <= 0 {
		c.ttl = ProbeStatusTTL
	}
	if c.timeout <= 0 {
		c.timeout = ProbeStatusTimeout
	}
	if c.now == nil {
		c.now = time.Now
	}
	return errors.Join(poolMetrics(reg, "portal", src.Pool), register(reg, c, c.errors))
}

// probeStatusCollector counts deployments by probe status when scraped, with a short cache. On a failed count it
// exposes nothing (a stale value would hide the problem) and counts the error; the failure is cached for the TTL
// too, so a database outage is not queried on every scrape.
type probeStatusCollector struct {
	src     probe.StatusCounter
	ttl     time.Duration
	timeout time.Duration
	now     func() time.Time
	desc    *prometheus.Desc
	errors  prometheus.Counter

	mu     sync.Mutex
	at     time.Time
	loaded bool
	counts map[probe.Status]int // nil after a failed count
}

func (c *probeStatusCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *probeStatusCollector) Collect(ch chan<- prometheus.Metric) {
	counts := c.read()
	if counts == nil {
		return
	}
	for _, s := range probeStatuses {
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(counts[s]), string(s))
	}
}

// read returns the counts, from the cache while it is fresh, or nil when the last count failed.
func (c *probeStatusCollector) read() map[probe.Status]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded && c.now().Sub(c.at) < c.ttl {
		return c.counts
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	counts, err := c.src.StatusCounts(ctx)
	c.at, c.loaded = c.now(), true
	if err != nil {
		c.errors.Inc()
		c.counts = nil
		return nil
	}
	c.counts = counts
	return counts
}
