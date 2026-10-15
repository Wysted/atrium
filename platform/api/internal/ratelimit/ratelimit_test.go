package ratelimit

import (
	"fmt"
	"net/netip"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func TestLimiter(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)}
	l := New(Config{Burst: 3, RefillEvery: time.Second, MaxKeys: 2, SweepEvery: 5 * time.Second, Now: clock.now})

	for i := range 3 {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("request %d refused inside the burst", i)
		}
	}
	ok, retry := l.Allow("a")
	if ok || retry <= 0 || retry > time.Second {
		t.Fatalf("over burst: ok=%v retry=%v", ok, retry)
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Fatal("keys must be independent")
	}
	clock.t = clock.t.Add(time.Second)
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("a token should have been refilled")
	}
	if ok, _ := l.Allow("a"); ok {
		t.Fatal("only one token was refilled")
	}

	t.Run("full table refuses new keys until idle ones age out", func(t *testing.T) {
		for range 3 {
			l.Allow("b") // drain b so neither tracked key has refilled
		}
		if ok, _ := l.Allow("c"); ok { // a and b are both tracked and not yet refilled
			t.Fatal("new key accepted beyond MaxKeys")
		}
		clock.t = clock.t.Add(10 * time.Second)
		if ok, _ := l.Allow("c"); !ok {
			t.Fatal("idle keys should have been swept")
		}
	})
}

func TestClientKey(t *testing.T) {
	for name, tc := range map[string]struct{ ip, want string }{
		"ipv4 is its own key":          {"203.0.113.7", "203.0.113.7"},
		"ipv4-mapped ipv6 is ipv4":     {"::ffff:203.0.113.7", "203.0.113.7"},
		"ipv6 collapses to its /64":    {"2001:db8:1:2:aaaa:bbbb:cccc:dddd", "2001:db8:1:2::/64"},
		"another host in the same /64": {"2001:db8:1:2::1", "2001:db8:1:2::/64"},
		"next /64 is another key":      {"2001:db8:1:3::1", "2001:db8:1:3::/64"},
		"zone is dropped":              {"fe80::1%eth0", "fe80::/64"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := ClientKey(netip.MustParseAddr(tc.ip)); got != tc.want {
				t.Fatalf("ClientKey(%s) = %q, want %q", tc.ip, got, tc.want)
			}
		})
	}
	if got := ClientKey(netip.Addr{}); got != "unknown" {
		t.Fatalf("invalid address key = %q", got)
	}
}

// Rotating addresses inside one /64 must hit one bucket.
func TestIPv6RotationSharesTheBucket(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)}
	l := New(Config{Burst: 3, RefillEvery: time.Hour, Now: clock.now})
	prefix := netip.MustParseAddr("2001:db8:aa:bb::").As16()
	allowed := 0
	for i := range 50 {
		a := prefix
		a[15], a[14] = byte(i), byte(i>>8)
		a[8] = byte(i + 1) // vary the interface identifier too
		if ok, _ := l.Allow(ClientKey(netip.AddrFrom16(a))); ok {
			allowed++
		}
	}
	if allowed != 3 {
		t.Fatalf("allowed %d requests from one /64, want the burst of 3", allowed)
	}
}

// With the table full, new keys cost O(1): the O(n) sweep runs on a timer, not per refused request.
func TestSweepIsAmortized(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)}
	const maxKeys = 100
	l := New(Config{Burst: 3, RefillEvery: time.Hour, MaxKeys: maxKeys, SweepEvery: time.Minute, Now: clock.now})
	for i := range maxKeys {
		l.Allow(fmt.Sprintf("k%d", i))
	}
	for i := range 10_000 {
		if ok, _ := l.Allow(fmt.Sprintf("new%d", i)); ok {
			t.Fatal("new key accepted beyond MaxKeys")
		}
	}
	if l.sweeps != 0 {
		t.Fatalf("%d sweeps ran for 10000 refused inserts inside one sweep interval, want 0", l.sweeps)
	}
	clock.t = clock.t.Add(time.Minute)
	for i := range 1000 {
		l.Allow(fmt.Sprintf("later%d", i))
	}
	if l.sweeps != 1 {
		t.Fatalf("sweeps = %d after one interval, want 1", l.sweeps)
	}
	if len(l.buckets) > maxKeys {
		t.Fatalf("buckets = %d, over the cap %d", len(l.buckets), maxKeys)
	}
}
