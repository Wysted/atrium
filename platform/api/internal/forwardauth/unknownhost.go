package forwardauth

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"platform/api/internal/access"
)

// Recording of unknown_host denials (contract block 2). The host comes straight from the network, so the
// recorder treats it as hostile input: it is validated, rate limited and written by a background goroutine, and
// the response to ForwardAuth never waits for any of it.
const (
	// UnknownHostRecordTTL is how long a host stays marked after it was queued: at most one row per host every
	// 10 minutes per replica.
	UnknownHostRecordTTL = 10 * time.Minute
	// UnknownHostMarkerEntries bounds the marker cache.
	UnknownHostMarkerEntries = 10_000
	// UnknownHostBudgetPerMinute and UnknownHostBurst are the token bucket of the whole replica: whatever the
	// number of distinct hosts, at most this many rows per minute (and a burst of this many) are queued.
	UnknownHostBudgetPerMinute = 30
	UnknownHostBurst           = 30
	// UnknownHostQueueSize is the capacity of the queue between the handlers and the writer.
	UnknownHostQueueSize = 128
	// unknownHostReportInterval is how often the writer logs the drops of the interval, if any.
	unknownHostReportInterval = time.Minute
)

// UnknownHostDrops counts the unknown_host denials that were not recorded since start-up, by reason.
type UnknownHostDrops struct {
	Invalid int64 // not a valid DNS name (hostname.Valid)
	Budget  int64 // over the per-replica token bucket
	Marker  int64 // the marker cache was full
	Queue   int64 // the writer fell behind and the queue was full
}

// tokenBucket is a small token bucket on the handler's clock.
type tokenBucket struct {
	rate  float64 // tokens per second
	burst float64

	mu     sync.Mutex
	tokens float64
	last   time.Time
}

func newTokenBucket(perMinute, burst int, now time.Time) *tokenBucket {
	return &tokenBucket{rate: float64(perMinute) / time.Minute.Seconds(), burst: float64(burst), tokens: float64(burst), last: now}
}

// refund gives back a token taken for something that did not happen.
func (b *tokenBucket) refund() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tokens = min(b.burst, b.tokens+1)
}

func (b *tokenBucket) take(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if d := now.Sub(b.last); d > 0 {
		b.tokens = min(b.burst, b.tokens+d.Seconds()*b.rate)
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

type unknownHostRecorder struct {
	now     func() time.Time
	markers *ttlCache[string, struct{}]
	budget  *tokenBucket
	queue   chan string

	invalid, overBudget, markerFull, queueFull atomic.Int64
}

func newUnknownHostRecorder(now func() time.Time) *unknownHostRecorder {
	return &unknownHostRecorder{
		now:     now,
		markers: newBoundedTTLCache[string, struct{}](UnknownHostRecordTTL, UnknownHostMarkerEntries, now),
		budget:  newTokenBucket(UnknownHostBudgetPerMinute, UnknownHostBurst, now()),
		queue:   make(chan string, UnknownHostQueueSize),
	}
}

// offer queues host (already validated by the handler) for recording, or drops it and counts why. It never
// blocks and never touches the database. A token and a marker are only kept for a host that was really queued:
// when the marker cache is full the token is given back, and when the queue is full the marker is removed, so the
// host is not silenced for 10 minutes without a row.
func (r *unknownHostRecorder) offer(host string) {
	_, marked, gen := r.markers.get(host)
	if marked {
		return
	}
	if !r.budget.take(r.now()) {
		r.overBudget.Add(1)
		return
	}
	if !r.markers.put(host, struct{}{}, gen) {
		r.budget.refund()
		r.markerFull.Add(1)
		return
	}
	select {
	case r.queue <- host:
	default:
		r.markers.delete(host)
		r.budget.refund()
		r.queueFull.Add(1)
	}
}

func (r *unknownHostRecorder) drops() UnknownHostDrops {
	return UnknownHostDrops{Invalid: r.invalid.Load(), Budget: r.overBudget.Load(), Marker: r.markerFull.Load(), Queue: r.queueFull.Load()}
}

// UnknownHostDrops returns the counters of unrecorded unknown_host denials.
func (h *Handler) UnknownHostDrops() UnknownHostDrops { return h.unknown.drops() }

// Run writes the queued unknown_host denials until ctx ends (what is still queued then is dropped: it is
// telemetry). A failed insert is logged and not retried. The caller runs it in the background.
func (h *Handler) Run(ctx context.Context) {
	ticker := time.NewTicker(unknownHostReportInterval)
	defer ticker.Stop()
	var reported UnknownHostDrops
	for {
		select {
		case <-ctx.Done():
			return
		case host := <-h.unknown.queue:
			h.writeUnknownHost(ctx, host)
		case <-ticker.C:
			if d := h.unknown.drops(); d != reported {
				h.log.Warn("unknown_host denials were not recorded", "invalid", d.Invalid-reported.Invalid,
					"over_budget", d.Budget-reported.Budget, "marker_cache_full", d.Marker-reported.Marker, "queue_full", d.Queue-reported.Queue)
				reported = d
			}
		}
	}
}

func (h *Handler) writeUnknownHost(ctx context.Context, host string) {
	ctx, cancel := context.WithTimeout(ctx, DenialRecordTimeout)
	defer cancel()
	if err := h.cfg.Denials.RecordDenial(ctx, access.Denial{Host: host, Reason: access.ReasonUnknownHost}); err != nil && ctx.Err() == nil {
		h.log.Error("cannot record unknown host", "error", err.Error(), "host", host)
	}
}
