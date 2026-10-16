// Package metrics defines the Prometheus metrics of platform-api and the portal (contract block 6,
// "Observability") and serves them at /metrics.
//
// Every process builds its own registry (NewRegistry): the global default registry is never used, so a
// test can build several apps in one process and nothing registers itself by import side effect.
//
// Cardinality rule. No label may take a value supplied by a client: not the Host, the path, the user agent, the
// query, an email or a user id. A label takes a value from a closed set (decision, reason, outcome), a ServeMux
// route pattern, or a fixed text. The methods that receive a string clamp it to its closed set and report
// anything else as OtherValue, so a caller bug cannot grow the number of series.
package metrics

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	// Path is where each process serves its metrics, on its dedicated metrics listener only.
	Path = "/metrics"
	// Route is the ServeMux pattern of Path.
	Route = http.MethodGet + " " + Path

	// OtherValue is the label value of anything outside a closed set.
	OtherValue = "other"

	// maxScrapesInFlight bounds concurrent scrapes: a handful of Prometheus replicas never need more.
	maxScrapesInFlight = 4
)

// NewRegistry returns a registry holding the Go runtime and process collectors.
func NewRegistry() (*prometheus.Registry, error) {
	reg := prometheus.NewRegistry()
	if err := register(reg, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})); err != nil {
		return nil, err
	}
	return reg, nil
}

// Handler serves reg in the Prometheus text format. It has no authentication: it must only be mounted on the
// dedicated metrics listener, whose port a NetworkPolicy opens to the monitoring namespace only.
func Handler(reg *prometheus.Registry) http.Handler {
	return promhttp.InstrumentMetricHandler(reg, promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		Registry:            reg,
		MaxRequestsInFlight: maxScrapesInFlight,
	}))
}

// register registers every collector and reports all failures together.
func register(reg prometheus.Registerer, cs ...prometheus.Collector) error {
	var errs []error
	for _, c := range cs {
		if err := reg.Register(c); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// set is a closed set of label values.
type set map[string]struct{}

func newSet(values ...string) set {
	s := make(set, len(values))
	for _, v := range values {
		s[v] = struct{}{}
	}
	return s
}

// clamp returns v when it belongs to the set, OtherValue otherwise.
func (s set) clamp(v string) string {
	if _, ok := s[v]; ok {
		return v
	}
	return OtherValue
}

// methods are the HTTP methods reported as such; the method of a request is client input.
var methods = newSet(
	http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
	http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace,
)

// statusClass returns "2xx" for 200..299 and so on; a code outside 100..599 is OtherValue.
func statusClass(code int) string {
	switch {
	case code >= 100 && code < 200:
		return "1xx"
	case code >= 200 && code < 300:
		return "2xx"
	case code >= 300 && code < 400:
		return "3xx"
	case code >= 400 && code < 500:
		return "4xx"
	case code >= 500 && code < 600:
		return "5xx"
	default:
		return OtherValue
	}
}

func resultLabel(hit bool) string {
	if hit {
		return "hit"
	}
	return "miss"
}

// collector is a Collector made of a fixed list of descriptors and a function that emits their metrics at scrape
// time. Unlike prometheus.CollectorFunc it declares its descriptors up front, so a metric that is absent when the
// collector is registered (for example the age of a key that is not loaded yet) can appear later.
type collector struct {
	descs   []*prometheus.Desc
	collect func(emit func(*prometheus.Desc, prometheus.ValueType, float64, ...string))
}

func (c collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range c.descs {
		ch <- d
	}
}

func (c collector) Collect(ch chan<- prometheus.Metric) {
	c.collect(func(d *prometheus.Desc, t prometheus.ValueType, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, t, v, labels...)
	})
}

// poolMetrics registers the statistics of pool under namespace (platform or portal), read at scrape time.
func poolMetrics(reg prometheus.Registerer, namespace string, pool *pgxpool.Pool) error {
	desc := func(name, help string) *prometheus.Desc {
		return prometheus.NewDesc(namespace+"_db_pool_"+name, help, nil, nil)
	}
	var (
		acquired    = desc("acquired_connections", "Connections currently checked out of the pool.")
		idle        = desc("idle_connections", "Idle connections in the pool.")
		total       = desc("total_connections", "Connections in the pool (acquired, idle and being opened).")
		maxConns    = desc("max_connections", "Configured maximum size of the pool.")
		acquires    = desc("acquires_total", "Successful acquires of a connection.")
		waited      = desc("acquires_waited_total", "Successful acquires that found no idle connection and had to wait.")
		waitSeconds = desc("acquire_wait_seconds_total", "Total time spent acquiring connections (successful acquires).")
		canceled    = desc("acquires_canceled_total", "Acquires abandoned because their context ended.")
	)
	return register(reg, collector{
		descs: []*prometheus.Desc{acquired, idle, total, maxConns, acquires, waited, waitSeconds, canceled},
		collect: func(emit func(*prometheus.Desc, prometheus.ValueType, float64, ...string)) {
			st := pool.Stat()
			emit(acquired, prometheus.GaugeValue, float64(st.AcquiredConns()))
			emit(idle, prometheus.GaugeValue, float64(st.IdleConns()))
			emit(total, prometheus.GaugeValue, float64(st.TotalConns()))
			emit(maxConns, prometheus.GaugeValue, float64(st.MaxConns()))
			emit(acquires, prometheus.CounterValue, float64(st.AcquireCount()))
			emit(waited, prometheus.CounterValue, float64(st.EmptyAcquireCount()))
			emit(waitSeconds, prometheus.CounterValue, st.AcquireDuration().Seconds())
			emit(canceled, prometheus.CounterValue, float64(st.CanceledAcquireCount()))
		},
	})
}

// latencyBuckets (seconds) cover a cached decision (microseconds) up to a request that waits on the database.
var latencyBuckets = []float64{0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
