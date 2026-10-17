package postgres_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/Wysted/atrium/platform/api/internal/portal"
)

// The grant numbers of the health report use the definition of the last-manager rule: a manages_access grant is
// effective when it is not expired and one of its users is active, directly or through a group.
func TestAppHealthCountsEffectiveManagers(t *testing.T) {
	t.Parallel()
	e := newCatalogEnv(t)
	ctx := e.ctx()
	alpha, gamma := e.initiative("alpha"), e.initiative("gamma")
	admin := e.admin()
	app := e.w.app(t, gamma.id, uniqueHost())
	e.w.app(t, alpha.id, uniqueHost())

	check := func(step string, grants, managers int, problems ...string) {
		t.Helper()
		h, err := e.svc.AppHealth(ctx, admin, app)
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		if h.Grants.Grants != grants || h.Grants.EffectiveManagers != managers {
			t.Fatalf("%s: grants %+v, want %d and %d effective managers", step, h.Grants, grants, managers)
		}
		var got []string
		for _, p := range h.Problems {
			if p.Host == "" {
				got = append(got, p.Code)
			}
		}
		if !slices.Equal(got, problems) {
			t.Fatalf("%s: app-level problems %v, want %v", step, got, problems)
		}
	}

	check("no grants", 0, 0, portal.ProblemNoGrants, portal.ProblemNoManager)

	disabled := e.w.user(t, "disabled")
	e.w.grantUser(t, gamma.id, gamma.manager, disabled, "")
	check("a manager grant held by a disabled user", 1, 0, portal.ProblemNoManager)

	editor := e.w.user(t, "active")
	e.w.grantUser(t, gamma.id, gamma.editor, editor, "")
	check("a plain role grant to an active user", 2, 0, portal.ProblemNoManager)

	active := e.w.user(t, "active")
	e.w.grantUser(t, gamma.id, gamma.manager, active, "")
	check("a manager grant held by an active user", 3, 1)

	expired := e.w.user(t, "active")
	e.w.grantUser(t, gamma.id, gamma.manager, expired, "2000-01-01T00:00:00Z")
	check("an expired manager grant counts nowhere", 3, 1)

	deadGroup, liveGroup := e.w.group(t, "dead"), e.w.group(t, "live")
	e.w.member(t, deadGroup, e.w.user(t, "disabled"))
	e.w.grantGroup(t, gamma.id, gamma.manager, deadGroup)
	check("a manager grant to a group of disabled users", 4, 1)

	e.w.member(t, liveGroup, e.w.user(t, "active"))
	e.w.grantGroup(t, gamma.id, gamma.manager, liveGroup)
	check("a manager grant to a group with an active member", 5, 2)

	// Another initiative's grants never leak in.
	other := e.w.user(t, "active")
	e.w.grantUser(t, alpha.id, alpha.manager, other, "")
	check("the grants of another initiative", 5, 2)
}

func TestAppHealthReport(t *testing.T) {
	t.Parallel()
	e := newCatalogEnv(t)
	ctx := e.ctx()
	alpha, beta := e.initiative("alpha"), e.initiative("beta")
	c := e.callers(alpha, beta)
	host := uniqueHost()
	app := e.w.app(t, alpha.id, host)
	second := uniqueHost()
	mustExec(t, e.owner, `INSERT INTO deployments (host, app_id) VALUES ($1, $2)`, second, app)
	mustExec(t, e.owner, `UPDATE deployments SET last_seen_at = now() - interval '2 hours', last_probe_at = now() - interval '3 minutes',
		last_probe_status = 'ok' WHERE host = $1`, host)
	mustExec(t, e.owner, `UPDATE deployments SET last_probe_at = now(), last_probe_status = 'unprotected' WHERE host = $1`, second)

	for name, caller := range map[string]string{"platform admin": c.admin, "manager of the initiative": c.mgrA} {
		t.Run(name, func(t *testing.T) {
			before := e.auditCount()
			h, err := e.svc.AppHealth(ctx, caller, app)
			if err != nil {
				t.Fatal(err)
			}
			if h.App.ID != app || h.InitiativeStatus != "active" || len(h.App.Deployments) != 2 || h.CheckedAt.IsZero() {
				t.Fatalf("health = %+v", h)
			}
			byHost := map[string]portal.Deployment{}
			for _, d := range h.App.Deployments {
				byHost[d.Host] = d
			}
			if d := byHost[host]; d.LastSeenAt == nil || d.LastProbeAt == nil || d.LastProbeStatus != portal.ProbeOK {
				t.Fatalf("telemetry of the first deployment = %+v", d)
			}
			if d := byHost[second]; d.LastSeenAt != nil || d.LastProbeStatus != portal.ProbeUnprotected {
				t.Fatalf("telemetry of the second deployment = %+v", d)
			}
			has := func(code, host string) bool {
				return slices.Contains(h.Problems, portal.HealthProblem{Code: code, Host: host})
			}
			if !has(portal.ProblemUnprotected, second) {
				t.Fatalf("the unprotected host is not reported: %v", h.Problems)
			}
			if !has(portal.ProblemNeverSeen, second) {
				t.Fatalf("the never seen host is not reported: %v", h.Problems)
			}
			if has(portal.ProblemNeverSeen, host) || has(portal.ProblemNotSeenRecently, host) {
				t.Fatalf("a recently seen host is reported: %v", h.Problems)
			}
			if e.auditCount() != before {
				t.Fatal("a read wrote an audit row")
			}
		})
	}

	t.Run("a manager of another initiative, a plain user and a malformed id see nothing", func(t *testing.T) {
		for name, caller := range map[string]string{"other manager": c.mgrB, "plain": c.plain} {
			if _, err := e.svc.AppHealth(ctx, caller, app); !errors.Is(err, portal.ErrNotFound) {
				t.Errorf("%s: err = %v, want not found", name, err)
			}
		}
		if _, err := e.svc.AppHealth(ctx, c.admin, "not-an-app"); !errors.Is(err, portal.ErrNotFound) {
			t.Errorf("malformed id: err = %v", err)
		}
		if _, err := e.svc.AppHealth(ctx, c.admin, "app_0000000000000000000000000Z"); !errors.Is(err, portal.ErrNotFound) {
			t.Errorf("unknown app: err = %v", err)
		}
		if _, err := e.svc.AppHealth(ctx, c.disabledAdmin, app); !errors.Is(err, portal.ErrUserInactive) {
			t.Errorf("disabled admin: err = %v", err)
		}
	})

	t.Run("disabled app and inactive initiative are reported", func(t *testing.T) {
		mustExec(t, e.owner, `UPDATE apps SET status = 'disabled' WHERE id = $1`, app)
		mustExec(t, e.owner, `UPDATE initiatives SET status = 'inactive' WHERE id = $1`, alpha.id)
		h, err := e.svc.AppHealth(ctx, c.mgrA, app)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, p := range h.Problems {
			if p.Host == "" {
				got = append(got, p.Code)
			}
		}
		if !slices.Contains(got, portal.ProblemAppDisabled) || !slices.Contains(got, portal.ProblemInitiativeInactive) || h.InitiativeStatus != "inactive" {
			t.Fatalf("app-level problems = %v, initiative %q", got, h.InitiativeStatus)
		}
		for _, p := range h.Problems {
			if p.Code == portal.ProblemNeverSeen || p.Code == portal.ProblemNotSeenRecently {
				t.Fatalf("%v reported for an app nobody can open", p)
			}
		}
	})
}

// The apps listing carries the telemetry of every deployment.
func TestAppListingCarriesDeploymentTelemetry(t *testing.T) {
	t.Parallel()
	e := newCatalogEnv(t)
	ctx := e.ctx()
	alpha := e.initiative("alpha")
	admin := e.admin()
	host := uniqueHost()
	e.w.app(t, alpha.id, host)
	other := uniqueHost()
	e.w.app(t, alpha.id, other)
	mustExec(t, e.owner, `UPDATE deployments SET last_seen_at = now() - interval '5 minutes', last_probe_at = now() - interval '1 minute',
		last_probe_status = 'unexpected' WHERE host = $1`, host)

	page, err := e.svc.ListApps(ctx, admin, alpha.id, portal.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	byHost := map[string]portal.Deployment{}
	for _, a := range page.Items {
		for _, d := range a.Deployments {
			byHost[d.Host] = d
		}
	}
	seen := scalar[time.Time](t, e.owner, `SELECT last_seen_at FROM deployments WHERE host = $1`, host)
	probed := scalar[time.Time](t, e.owner, `SELECT last_probe_at FROM deployments WHERE host = $1`, host)
	d := byHost[host]
	if d.LastSeenAt == nil || !d.LastSeenAt.Equal(seen) || d.LastProbeAt == nil || !d.LastProbeAt.Equal(probed) || d.LastProbeStatus != portal.ProbeUnexpected {
		t.Fatalf("telemetry = %+v, want seen %s probed %s unexpected", d, seen, probed)
	}
	if d := byHost[other]; d.LastSeenAt != nil || d.LastProbeAt != nil || d.LastProbeStatus != "" {
		t.Fatalf("a deployment never seen or probed carries %+v", d)
	}
}

func TestOrphanHosts(t *testing.T) {
	t.Parallel()
	e := newCatalogEnv(t)
	ctx := e.ctx()
	alpha, beta := e.initiative("alpha"), e.initiative("beta")
	c := e.callers(alpha, beta)
	// The window reaches back 7 days and the row 8 days old needs its monthly partition.
	mustExec(t, e.owner, `SELECT create_access_denials_partition((now() - interval '8 days')::date)`)
	deny := func(host, reason, age string) {
		mustExec(t, e.owner, `INSERT INTO access_denials (host, reason, occurred_at) VALUES ($1, $2, now() - $3::interval)`, host, reason, age)
	}

	deny("recent.example.test", "unknown_host", "30 minutes")
	for _, age := range []string{"1 hour", "2 hours", "3 hours"} {
		deny("busy.example.test", "unknown_host", age)
	}
	deny("old.example.test", "unknown_host", "8 days")
	deny("stopped.example.test", "unknown_host", "6 days")
	deny("other-reason.example.test", "no_grant", "1 hour")
	known := uniqueHost()
	e.w.app(t, alpha.id, known)
	deny(known, "unknown_host", "1 hour")

	hosts, err := e.svc.ListOrphanHosts(ctx, c.admin)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, h := range hosts {
		got = append(got, fmt.Sprintf("%s:%d", h.Host, h.Count))
	}
	want := []string{"recent.example.test:1", "busy.example.test:3", "stopped.example.test:1"}
	if !slices.Equal(got, want) {
		t.Fatalf("orphan hosts = %v, want %v (newest denial first; old, known and other-reason ones left out)", got, want)
	}
	busyLast := scalar[time.Time](t, e.owner, `SELECT max(occurred_at) FROM access_denials WHERE host = 'busy.example.test'`)
	if !hosts[1].LastDeniedAt.Equal(busyLast) {
		t.Fatalf("last denial = %s, want %s", hosts[1].LastDeniedAt, busyLast)
	}

	t.Run("a host that gets a deployment stops being an orphan", func(t *testing.T) {
		e.w.app(t, alpha.id, "recent.example.test")
		hosts, err := e.svc.ListOrphanHosts(ctx, c.admin)
		if err != nil || len(hosts) != 2 || hosts[0].Host != "busy.example.test" {
			t.Fatalf("hosts = %+v, %v", hosts, err)
		}
	})

	t.Run("only platform admins", func(t *testing.T) {
		before := e.auditCount()
		for name, caller := range map[string]string{"manager": c.mgrA, "other manager": c.mgrB, "plain": c.plain} {
			if _, err := e.svc.ListOrphanHosts(ctx, caller); !errors.Is(err, portal.ErrForbidden) {
				t.Errorf("%s: err = %v, want forbidden", name, err)
			}
		}
		if _, err := e.svc.ListOrphanHosts(ctx, c.disabledAdmin); !errors.Is(err, portal.ErrUserInactive) {
			t.Errorf("disabled admin: err = %v", err)
		}
		if e.auditCount() != before {
			t.Fatal("a read wrote an audit row")
		}
	})

	t.Run("the list is bounded", func(t *testing.T) {
		mustExec(t, e.owner, `INSERT INTO access_denials (host, reason, occurred_at)
			SELECT 'flood' || n || '.example.test', 'unknown_host', now() - make_interval(secs => n) FROM generate_series(1, 150) AS n`)
		hosts, err := e.svc.ListOrphanHosts(ctx, c.admin)
		if err != nil || len(hosts) != portal.OrphanHostLimit {
			t.Fatalf("%d hosts, %v, want %d", len(hosts), err, portal.OrphanHostLimit)
		}
		if !slices.IsSortedFunc(hosts, func(a, b portal.OrphanHost) int { return b.LastDeniedAt.Compare(a.LastDeniedAt) }) {
			t.Fatal("hosts are not ordered by their last denial, newest first")
		}
	})
}
