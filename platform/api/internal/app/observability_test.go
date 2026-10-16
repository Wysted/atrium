package app_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// last_seen_at follows authorized decisions only, is written by the flusher (never by a request) and does not
// flush the ForwardAuth caches.
func TestLastSeenFollowsAuthorizedDecisions(t *testing.T) {
	e := newEnv(t, envOptions{})
	b := e.newBrowser()
	b.login(hostA, "/", userEmail)
	e.grant(userEmail)
	e.app.InvalidateAccess()

	ctx, cancel := context.WithTimeout(context.Background(), testTO)
	defer cancel()
	seen := func(host string) string {
		return e.scalar(`SELECT coalesce(last_seen_at::text, '') FROM deployments WHERE host = $1`, host)
	}

	// Denials, redirects and the auth host leave nothing to flush.
	e.newBrowser().forward(fwd{host: hostA, headers: navHeaders})
	e.newBrowser().forward(fwd{host: authHost, uri: "/login"})
	e.newBrowser().forward(fwd{host: "unknown.apps.example.test", headers: navHeaders})
	if n := e.app.LastSeen.Pending(); n != 0 {
		t.Fatalf("%d hosts pending after requests that were not authorized", n)
	}

	for range 5 {
		if r := b.forward(fwd{host: hostA, uri: "/api", headers: apiHeaders}); r.status != http.StatusOK {
			t.Fatalf("authorized request = %d", r.status)
		}
	}
	if n := e.app.LastSeen.Pending(); n != 1 {
		t.Fatalf("pending = %d, want the one host that served requests", n)
	}
	if got := seen(hostA); got != "" {
		t.Fatalf("a request wrote last_seen_at (%s)", got)
	}

	// Listen for access_changed on a connection of the owner pool: the flush must not emit anything.
	listener, err := e.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Release()
	if _, err := listener.Exec(ctx, "LISTEN access_changed"); err != nil {
		t.Fatal(err)
	}

	if err := e.app.LastSeen.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := seen(hostA); got == "" {
		t.Fatal("the flush did not set last_seen_at")
	}
	if got := seen(hostB); got != "" {
		t.Fatalf("a host that served nothing has last_seen_at = %s", got)
	}
	if n := e.app.LastSeen.Pending(); n != 0 {
		t.Fatalf("%d hosts still pending after the flush", n)
	}

	// A sentinel on apps after the flush: the first notification must be the sentinel's.
	e.exec(`UPDATE apps SET name = name WHERE id = $1`, appIDB)
	wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
	defer wcancel()
	n, err := listener.Conn().WaitForNotification(wctx)
	if err != nil {
		t.Fatalf("no notification: %v", err)
	}
	if n.Payload != "apps" {
		t.Fatalf("first notification payload = %q, want apps: the flush notified", n.Payload)
	}

	// The next flush moves the time forward and never backwards.
	first := e.scalar(`SELECT last_seen_at::text FROM deployments WHERE host = $1`, hostA)
	if r := b.forward(fwd{host: hostA, uri: "/api", headers: apiHeaders}); r.status != http.StatusOK {
		t.Fatalf("authorized request = %d", r.status)
	}
	if err := e.app.LastSeen.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if later := e.scalar(`SELECT (last_seen_at > $1::timestamptz)::text FROM deployments WHERE host = $2`, first, hostA); later != "true" {
		t.Fatal("the second flush did not move last_seen_at forward")
	}
}

// A host Traefik routed here without a deployment is recorded as an unknown_host denial by the background
// writer, at most once per window, and never when the host cannot be a DNS name.
func TestUnknownHostIsRecordedOncePerWindow(t *testing.T) {
	e := newEnv(t, envOptions{})
	const orphan = "orphan.apps.example.test"
	count := func(host string) int {
		return e.count(`SELECT count(*) FROM access_denials WHERE reason = 'unknown_host' AND host = $1 AND user_id IS NULL`, host)
	}

	for _, headers := range []map[string]string{navHeaders, apiHeaders, navHeaders} {
		if r := e.newBrowser().forward(fwd{host: orphan, headers: headers}); r.status != http.StatusForbidden {
			t.Fatalf("status %d", r.status)
		}
	}
	e.newBrowser().forward(fwd{host: "other-orphan.apps.example.test", headers: navHeaders})
	// A flush of the caches (NOTIFY) does not make the same host write again.
	e.app.InvalidateAccess()
	e.newBrowser().forward(fwd{host: orphan, headers: navHeaders})

	label := strings.Repeat("a", 63)
	long := strings.Join([]string{label, label, label, label, "test"}, ".") // 260 bytes
	for _, bad := range []string{long, "bad_host.apps.example.test", "10.0.0.1"} {
		if r := e.newBrowser().forward(fwd{host: bad, headers: navHeaders}); r.status != http.StatusForbidden {
			t.Fatalf("%q status %d", bad, r.status)
		}
	}
	// The queue is in order: once the last valid host is written, the earlier ones were too.
	e.newBrowser().forward(fwd{host: "last.apps.example.test", headers: navHeaders})
	waitFor(t, "the unknown hosts to be recorded", func() bool { return count("last.apps.example.test") == 1 })

	for _, host := range []string{orphan, "other-orphan.apps.example.test"} {
		if got := count(host); got != 1 {
			t.Fatalf("%s: %d denials, want 1", host, got)
		}
	}
	if n := e.count(`SELECT count(*) FROM access_denials WHERE reason = 'unknown_host'`); n != 3 {
		t.Fatalf("%d unknown_host rows, want 3: invalid hosts must never be recorded", n)
	}
}
