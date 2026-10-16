package probe

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	controlHost = "portal.example.test"
	controlApp  = "app_portal"
)

// fakeProber answers from a map and counts concurrency.
type fakeProber struct {
	status map[string]Status
	// controlSeq, when set, answers the successive probes of controlHost (the last one repeats).
	controlSeq []Status
	ctlCalls   atomic.Int32
	codes      map[string]int
	block      chan struct{} // when non-nil every Check waits for it
	current    atomic.Int32
	peak       atomic.Int32
	calls      atomic.Int32
}

func (f *fakeProber) Check(ctx context.Context, t Target) Result {
	f.calls.Add(1)
	n := f.current.Add(1)
	defer f.current.Add(-1)
	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			break
		}
	}
	if f.block != nil && t.Host != controlHost {
		select {
		case <-f.block:
		case <-ctx.Done():
		}
	}
	st, ok := f.status[t.Host]
	if t.Host == controlHost && len(f.controlSeq) > 0 {
		i := int(f.ctlCalls.Add(1)) - 1
		st, ok = f.controlSeq[min(i, len(f.controlSeq)-1)], true
	}
	if !ok {
		st = StatusOK
	}
	return Result{Target: t, Status: st, Code: f.codes[t.Host]}
}

// fakeStore hands out fixed targets and keeps what the job stored.
type fakeStore struct {
	mu        sync.Mutex
	targets   []Target
	control   []Target
	noControl bool // the control app has no deployment
	skip      bool
	rounds    int
	dueAfter  []time.Duration
	stored    []Result
}

func (s *fakeStore) Round(ctx context.Context, dueAfter time.Duration, app string, run func(context.Context, []Target, []Target) []Result) (Report, error) {
	s.mu.Lock()
	s.rounds++
	s.dueAfter = append(s.dueAfter, dueAfter)
	skip, targets := s.skip, s.targets
	s.mu.Unlock()
	if skip {
		return Report{Skipped: true}, nil
	}
	control := s.control
	if control == nil && !s.noControl {
		control = []Target{{Host: controlHost, AppID: app}}
	}
	results := run(ctx, targets, control)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stored = append(s.stored, results...)
	return Report{Stored: int64(len(results))}, nil
}

func newJob(t *testing.T, store Store, p Prober, mutate func(*Config)) (*Job, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	cfg := Config{
		Store: store, Checker: p, Interval: 10 * time.Minute, ControlAppID: controlApp,
		Jitter: func(time.Duration) time.Duration { return time.Millisecond },
		Logger: slog.New(slog.NewJSONHandler(logs, nil)),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	j, err := NewJob(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return j, logs
}

func TestNewJobValidation(t *testing.T) {
	t.Parallel()
	if _, err := NewJob(Config{Store: &fakeStore{}, Checker: &fakeProber{}, Interval: time.Minute}); err == nil {
		t.Fatal("accepted a missing control app")
	}
	if _, err := NewJob(Config{Checker: &fakeProber{}, Interval: time.Minute, ControlAppID: controlApp}); err == nil {
		t.Fatal("accepted a missing store")
	}
	if _, err := NewJob(Config{Store: &fakeStore{}, Interval: time.Minute, ControlAppID: controlApp}); err == nil {
		t.Fatal("accepted a missing checker")
	}
	// Interval 0 disables the probe: the binary does not build a job at all, and the job refuses to exist
	// rather than spin.
	if _, err := NewJob(Config{Store: &fakeStore{}, Checker: &fakeProber{}, ControlAppID: controlApp}); err == nil {
		t.Fatal("accepted a zero interval")
	}
}

func TestRoundStoresEveryResultAndWarnsOnlyForDangerousOnes(t *testing.T) {
	t.Parallel()
	store := &fakeStore{targets: []Target{
		{Host: "ok.example.test", AppID: "app_ok"},
		{Host: "open.example.test", AppID: "app_open"},
		{Host: "odd.example.test", AppID: "app_odd"},
		{Host: "down.example.test", AppID: "app_down"},
	}}
	p := &fakeProber{
		status: map[string]Status{"open.example.test": StatusUnprotected, "odd.example.test": StatusUnexpected, "down.example.test": StatusUnreachable},
		codes:  map[string]int{"open.example.test": 200, "odd.example.test": 301},
	}
	j, logs := newJob(t, store, p, nil)

	rep, err := j.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Probed != 4 || rep.Stored != 4 || len(store.stored) != 4 {
		t.Fatalf("report %+v, stored %d", rep, len(store.stored))
	}
	if want := 5 * time.Minute; store.dueAfter[0] != want {
		t.Fatalf("dueAfter = %s, want half the interval (%s)", store.dueAfter[0], want)
	}

	var warns []string
	for line := range strings.SplitSeq(strings.TrimSpace(logs.String()), "\n") {
		if strings.Contains(line, `"level":"WARN"`) {
			warns = append(warns, line)
		}
	}
	if len(warns) != 2 {
		t.Fatalf("%d warnings, want one for the unprotected and one for the unexpected host:\n%s", len(warns), logs.String())
	}
	for _, w := range warns {
		if !strings.Contains(w, `"host"`) || !strings.Contains(w, `"app_id"`) || !strings.Contains(w, `"http_status"`) {
			t.Fatalf("warning without host, app id and status: %s", w)
		}
	}
	if !strings.Contains(warns[0]+warns[1], "app_open") || !strings.Contains(warns[0]+warns[1], "app_odd") {
		t.Fatalf("warnings = %v", warns)
	}
	for _, quiet := range []string{"ok.example.test", "down.example.test"} {
		if strings.Contains(strings.Join(warns, ""), quiet) {
			t.Fatalf("%s was reported as dangerous", quiet)
		}
	}
}

func TestSkippedRoundProbesNothing(t *testing.T) {
	t.Parallel()
	store := &fakeStore{skip: true, targets: []Target{{Host: "a.example.test"}}}
	p := &fakeProber{}
	j, _ := newJob(t, store, p, nil)
	rep, err := j.RunOnce(context.Background())
	if err != nil || !rep.Skipped || p.calls.Load() != 0 {
		t.Fatalf("report %+v, err %v, probes %d", rep, err, p.calls.Load())
	}
}

func TestConcurrencyIsBounded(t *testing.T) {
	t.Parallel()
	var targets []Target
	for i := range 30 {
		targets = append(targets, Target{Host: string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".example.test"})
	}
	block := make(chan struct{})
	p := &fakeProber{block: block}
	j, _ := newJob(t, &fakeStore{targets: targets}, p, func(c *Config) { c.Concurrency = 3 })

	done := make(chan Report)
	go func() {
		rep, _ := j.RunOnce(context.Background())
		done <- rep
	}()
	deadline := time.After(5 * time.Second)
	for p.current.Load() < 3 {
		select {
		case <-deadline:
			t.Fatal("probes never reached the concurrency limit")
		case <-time.After(time.Millisecond):
		}
	}
	time.Sleep(50 * time.Millisecond) // give an over-limit probe the chance to start
	close(block)
	rep := <-done
	if p.peak.Load() != 3 || rep.Probed != 30 {
		t.Fatalf("peak concurrency %d, probed %d", p.peak.Load(), rep.Probed)
	}
}

func TestResultsCutShortByShutdownAreDropped(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	p := &fakeProber{block: block}
	store := &fakeStore{targets: []Target{{Host: "a.example.test"}, {Host: "b.example.test"}}}
	j, _ := newJob(t, store, p, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _, _ = j.RunOnce(ctx); close(done) }()
	for p.current.Load() < 2 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if len(store.stored) != 0 {
		t.Fatalf("stored %v for probes that were interrupted", store.stored)
	}
}

func TestRunRepeatsUntilCanceled(t *testing.T) {
	t.Parallel()
	store := &fakeStore{targets: []Target{{Host: "a.example.test"}}}
	j, _ := newJob(t, store, &fakeProber{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { j.Run(ctx); close(done) }()
	deadline := time.After(5 * time.Second)
	for {
		store.mu.Lock()
		n := store.rounds
		store.mu.Unlock()
		if n >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("only %d rounds", n)
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestRandomJitterStaysWithinTenPercent(t *testing.T) {
	t.Parallel()
	const interval = 10 * time.Minute
	lo, hi := time.Duration(float64(interval)*0.9), time.Duration(float64(interval)*1.1)
	distinct := map[time.Duration]bool{}
	for range 200 {
		d := randomJitter(interval)
		if d < lo || d > hi {
			t.Fatalf("jitter %s outside [%s, %s]", d, lo, hi)
		}
		distinct[d] = true
	}
	if len(distinct) < 10 {
		t.Fatalf("only %d distinct waits: no jitter", len(distinct))
	}
	if got := randomJitter(0); got != 0 {
		t.Fatalf("zero interval jittered to %s", got)
	}
}

func TestControlSample(t *testing.T) {
	t.Parallel()
	due := []Target{{Host: "a.example.test", AppID: "app_a"}, {Host: "b.example.test", AppID: "app_b"}}

	t.Run("control ok: the round is written", func(t *testing.T) {
		t.Parallel()
		store := &fakeStore{targets: due}
		j, _ := newJob(t, store, &fakeProber{}, nil)
		rep, err := j.RunOnce(context.Background())
		if err != nil || rep.Inconclusive || rep.Probed != 2 || len(store.stored) != 2 {
			t.Fatalf("report %+v, err %v, stored %v", rep, err, store.stored)
		}
	})

	for _, st := range []Status{StatusUnprotected, StatusUnreachable, StatusUnexpected} {
		t.Run("control "+string(st)+": nothing is written", func(t *testing.T) {
			t.Parallel()
			store := &fakeStore{targets: due}
			p := &fakeProber{status: map[string]Status{controlHost: st, "a.example.test": StatusUnprotected}, codes: map[string]int{controlHost: 503}}
			j, logs := newJob(t, store, p, nil)
			rep, err := j.RunOnce(context.Background())
			if err != nil || !rep.Inconclusive || rep.Probed != 0 || len(store.stored) != 0 {
				t.Fatalf("report %+v, err %v, stored %v", rep, err, store.stored)
			}
			if p.calls.Load() != 1 {
				t.Fatalf("%d probes: the due hosts were probed after a failed control", p.calls.Load())
			}
			out := logs.String()
			if !strings.Contains(out, `"level":"WARN"`) || !strings.Contains(out, `"control_status":"`+string(st)+`"`) || strings.Contains(out, "not protected by ForwardAuth") {
				t.Fatalf("log = %s", out)
			}
		})
	}

	t.Run("control lost during the round: the whole round is discarded", func(t *testing.T) {
		t.Parallel()
		for _, end := range []Status{StatusUnprotected, StatusUnreachable, StatusUnexpected} {
			store := &fakeStore{targets: due}
			p := &fakeProber{controlSeq: []Status{StatusOK, end}, status: map[string]Status{"a.example.test": StatusUnprotected}}
			j, logs := newJob(t, store, p, nil)
			rep, err := j.RunOnce(context.Background())
			if err != nil || !rep.Inconclusive || rep.Probed != 0 || len(store.stored) != 0 {
				t.Fatalf("%s: report %+v, err %v, stored %v", end, rep, err, store.stored)
			}
			if p.calls.Load() != 4 { // control, two hosts, control again
				t.Fatalf("%s: %d probes", end, p.calls.Load())
			}
			if out := logs.String(); !strings.Contains(out, "discarded") || !strings.Contains(out, `"control_status":"`+string(end)+`"`) || strings.Contains(out, "not protected by ForwardAuth") {
				t.Fatalf("%s: log = %s", end, out)
			}
		}
	})

	t.Run("no portal deployment: skipped with a warning", func(t *testing.T) {
		t.Parallel()
		store := &fakeStore{targets: due, noControl: true}
		p := &fakeProber{}
		j, logs := newJob(t, store, p, nil)
		rep, err := j.RunOnce(context.Background())
		if err != nil || !rep.Inconclusive || len(store.stored) != 0 || p.calls.Load() != 0 {
			t.Fatalf("report %+v, err %v, stored %v, probes %d", rep, err, store.stored, p.calls.Load())
		}
		if !strings.Contains(logs.String(), `"level":"WARN"`) || !strings.Contains(logs.String(), controlApp) {
			t.Fatalf("log = %s", logs)
		}
	})

	t.Run("a due control host is stored (probed at the start and re-checked at the end)", func(t *testing.T) {
		t.Parallel()
		both := append([]Target{{Host: controlHost, AppID: controlApp}}, due...)
		store := &fakeStore{targets: both}
		p := &fakeProber{}
		j, _ := newJob(t, store, p, nil)
		rep, err := j.RunOnce(context.Background())
		if err != nil || rep.Probed != 3 || len(store.stored) != 3 || p.calls.Load() != 4 {
			t.Fatalf("report %+v, err %v, stored %d, probes %d", rep, err, len(store.stored), p.calls.Load())
		}
	})
}
