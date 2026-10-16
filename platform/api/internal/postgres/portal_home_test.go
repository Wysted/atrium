package postgres_test

import (
	"context"
	"slices"
	"testing"

	"platform/api/internal/access"
	"platform/api/internal/postgres"
	"platform/api/internal/testdb"
)

// The home invariant (contract block 6): a deployment appears in /me/apps for a user if and only if
// access.Evaluator would allow that user on that host, with the same roles. The single exception is the portal
// app, which every active user may open but which the home leaves out.
func TestHomeMatchesAccessDecisions(t *testing.T) {
	t.Parallel()
	var portalAppOfPlatform string // the portal app id of the scenario under check ("" in the misconfigured one)
	owner := newTestDB(t)
	w := world{pool: owner}

	// Initiatives: two active, one inactive, and the reserved "platform" one that owns the portal app.
	alpha := w.initiative(t, "alpha", "active")
	beta := w.initiative(t, "beta", "active")
	dead := w.initiative(t, "dead", "inactive")
	platform := w.initiative(t, access.PlatformInitiativeKey, "active")

	alphaViewer, alphaEditor := w.role(t, alpha, "viewer"), w.role(t, alpha, "editor")
	betaViewer := w.role(t, beta, "viewer")
	deadViewer := w.role(t, dead, "viewer")
	platformRole := w.role(t, platform, "operator")

	// Apps and deployments.
	alphaWeb := w.app(t, alpha, "web.alpha.test")
	mustExec(t, owner, `INSERT INTO deployments (host, app_id) VALUES ('web-alt.alpha.test', $1)`, alphaWeb) // second host
	alphaOff := w.app(t, alpha, "off.alpha.test")
	mustExec(t, owner, `UPDATE apps SET status = 'disabled' WHERE id = $1`, alphaOff)
	w.app(t, beta, "app.beta.test")
	w.app(t, beta, "second.beta.test") // second active app of beta: beta grants cover every app of the initiative
	w.app(t, dead, "app.dead.test")
	portalApp := w.app(t, platform, "portal.test")
	mustExec(t, owner, `INSERT INTO apps (id, initiative_id) VALUES ($1, $2)`, appID(fixtureSeq.Add(1)), alpha) // no deployment
	misconfApp := w.app(t, alpha, "misconf.alpha.test")                                                         // what a mistyped PLATFORM_PORTAL_APP_ID would point to

	// Users and grants.
	direct := w.user(t, "active")
	w.grantUser(t, alpha, alphaViewer, direct, "")
	w.grantUser(t, beta, betaViewer, direct, "")

	viaGroup := w.user(t, "active")
	grp := w.group(t, "alpha-team")
	w.member(t, grp, viaGroup)
	w.grantGroup(t, alpha, alphaEditor, grp)

	both := w.user(t, "active") // the same role directly and through a group, plus a second role: roles are distinct
	w.member(t, grp, both)
	w.grantUser(t, alpha, alphaEditor, both, "")
	w.grantUser(t, alpha, alphaViewer, both, "")

	expired := w.user(t, "active")
	w.grantUser(t, alpha, alphaViewer, expired, "2000-01-01T00:00:00Z")
	w.grantUser(t, beta, betaViewer, expired, "2999-01-01T00:00:00Z") // a future expiry still counts

	// Expiry boundary: a grant that expires at the instant it was created is already over when it is evaluated,
	// one that expires in an hour is not.
	boundary := w.user(t, "active")
	mustExec(t, owner, `INSERT INTO initiative_access (initiative_id, role_id, user_id, expires_at) VALUES ($1, $2, $3, now())`, alpha, alphaViewer, boundary)
	justValid := w.user(t, "active")
	mustExec(t, owner, `INSERT INTO initiative_access (initiative_id, role_id, user_id, expires_at) VALUES ($1, $2, $3, now() + interval '1 hour')`, alpha, alphaViewer, justValid)

	expiredGroup := w.user(t, "active") // member of a group whose only grant has expired
	gExp := w.group(t, "expired-team")
	w.member(t, gExp, expiredGroup)
	mustExec(t, owner, `INSERT INTO initiative_access (initiative_id, role_id, group_id, expires_at) VALUES ($1, $2, $3, now() - interval '1 second')`, alpha, alphaViewer, gExp)

	otherGroup := w.user(t, "active") // member of a group whose grant is in another initiative: sees only that one
	gBeta := w.group(t, "beta-team")
	w.member(t, gBeta, otherGroup)
	w.grantGroup(t, beta, betaViewer, gBeta)

	disabled := w.user(t, "disabled")
	w.grantUser(t, alpha, alphaViewer, disabled, "")

	deadOnly := w.user(t, "active")
	w.grantUser(t, dead, deadViewer, deadOnly, "")

	portalOperator := w.user(t, "active") // a grant on the platform initiative must not list the portal app either
	w.grantUser(t, platform, platformRole, portalOperator, "")

	noGrants := w.user(t, "active")
	unknown := "00000000-0000-0000-0000-00000000dead"

	users := map[string]string{
		"direct grants": direct, "group grant": viaGroup, "direct and group, roles distinct": both,
		"expired and future grants": expired, "expiry at creation instant": boundary, "expiry in an hour": justValid,
		"expired group grant": expiredGroup, "group grant in another initiative": otherGroup,
		"disabled user": disabled, "inactive initiative only": deadOnly, "platform grant only": portalOperator,
		"no grants": noGrants, "unknown user": unknown,
	}

	pp := testdb.Portal(t, owner)
	home := postgres.NewHomeRepo(pp)
	hosts := scalar[[]string](t, owner, `SELECT array_agg(host ORDER BY host) FROM deployments`)
	if len(hosts) != 8 {
		t.Fatalf("seed has %d hosts, want 8", len(hosts))
	}
	userActive := func(uid string) bool {
		var st string
		return owner.QueryRow(context.Background(), `SELECT status FROM users WHERE id = $1`, uid).Scan(&st) == nil && st == "active"
	}

	// checkInvariant compares the home with the evaluator configured with portalApp as the portal app (the home
	// excludes the same id).
	checkInvariant := func(t *testing.T, portalApp string) {
		rt := testdb.Runtime(t, owner)
		evaluator := access.NewEvaluator(postgres.NewAccessRepository(rt), access.WithPortalApp(portalApp))
		for name, uid := range users {
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
				defer cancel()
				apps, err := home.HomeApps(ctx, uid, portalApp)
				if err != nil {
					t.Fatal(err)
				}
				listed := map[string][]string{} // host -> roles
				for _, a := range apps {
					if len(a.Roles) == 0 || len(a.Hosts) == 0 {
						t.Errorf("app %s listed without roles or hosts: %+v", a.ID, a)
					}
					if !slices.IsSorted(a.Hosts) { // byte order, COLLATE "C": stable whatever the database locale
						t.Errorf("app %s hosts not sorted: %+v", a.ID, a)
					}
					for _, h := range a.Hosts {
						listed[h] = a.Roles
					}
				}
				for _, host := range hosts {
					d, err := evaluator.Evaluate(ctx, access.Request{
						Subject: access.Subject{ID: uid}, Action: access.Action{Name: access.ActionAccess},
						Resource: access.Resource{Type: access.ResourceTypeApp, ID: host},
					})
					if err != nil {
						t.Fatal(err)
					}
					roles, inHome := listed[host]
					if d.AppID == portalApp && portalApp == portalAppOfPlatform {
						// The documented exception: every active user may open the portal, the home never lists it.
						if inHome {
							t.Errorf("portal host %s is listed in the home", host)
						}
						if d.Allow != userActive(uid) {
							t.Errorf("portal host %s: evaluator allow=%v (%s) for active=%v", host, d.Allow, d.Reason, userActive(uid))
						}
						if d.Allow && len(d.Roles) != 0 {
							t.Errorf("portal host %s: roles %v, want none", host, d.Roles)
						}
						continue
					}
					if d.Allow != inHome {
						t.Errorf("host %s: evaluator allow=%v (%s), home listed=%v", host, d.Allow, d.Reason, inHome)
					}
					if d.Allow && !slices.Equal(d.Roles, roles) {
						t.Errorf("host %s: evaluator roles %v, home roles %v", host, d.Roles, roles)
					}
				}
			})
		}
	}
	t.Run("portal app of the platform initiative", func(t *testing.T) {
		portalAppOfPlatform = portalApp
		checkInvariant(t, portalApp)
	})
	// A portal app id that points to an app of another initiative gets no exception from the evaluator, so the
	// home must not hide it either.
	t.Run("misconfigured portal app id", func(t *testing.T) {
		portalAppOfPlatform = ""
		checkInvariant(t, misconfApp)
	})

	// Spot checks, so the invariant is not satisfied by an always-empty home.
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	apps, err := home.HomeApps(ctx, both, portalApp)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 2 || apps[0].ID != alphaWeb && apps[1].ID != alphaWeb || apps[0].Initiative.Key != "alpha" {
		t.Fatalf("home of the user with both grants = %+v", apps)
	}
	for _, a := range apps {
		if a.ID == alphaWeb && (!slices.Equal(a.Hosts, []string{"web-alt.alpha.test", "web.alpha.test"}) || !slices.Equal(a.Roles, []string{"editor", "viewer"})) {
			t.Fatalf("web app of the user with both grants = %+v", a)
		}
	}
	if apps, err = home.HomeApps(ctx, direct, portalApp); err != nil || len(apps) != 4 {
		t.Fatalf("home of the user with direct grants = %+v, %v", apps, err)
	}
	// Without excluding the portal app, a platform grant does list it: the exclusion is the parameter.
	if apps, err = home.HomeApps(ctx, portalOperator, ""); err != nil || len(apps) != 1 || apps[0].ID != portalApp {
		t.Fatalf("home without exclusion = %+v, %v", apps, err)
	}
	// Excluding an app outside the platform initiative hides nothing.
	if apps, err = home.HomeApps(ctx, both, misconfApp); err != nil || len(apps) != 2 {
		t.Fatalf("home excluding a non-platform app = %+v, %v", apps, err)
	}
}
