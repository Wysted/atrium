package postgres_test

import (
	"context"
	"slices"
	"testing"

	"platform/api/internal/access"
	"platform/api/internal/postgres"
)

func TestEvaluate(t *testing.T) {
	t.Parallel()
	pool := newTestDB(t)
	w := world{pool: pool}

	// Initiative "alpha": two roles, host alpha.example.test.
	alpha := w.initiative(t, "alpha", "active")
	alphaEditor := w.role(t, alpha, "editor")
	alphaViewer := w.role(t, alpha, "viewer")
	alphaApp := w.app(t, alpha, "alpha.example.test")
	// Initiative "beta": its own roles (a "viewer" key too, must not mix), host beta.example.test.
	beta := w.initiative(t, "beta", "active")
	betaAdmin := w.role(t, beta, "admin")
	w.role(t, beta, "viewer")
	w.app(t, beta, "beta.example.test")
	// Inactive initiative.
	dead := w.initiative(t, "dead", "inactive")
	deadRole := w.role(t, dead, "editor")
	w.app(t, dead, "dead.example.test")

	// Disabled app in an active initiative, and the portal app.
	offApp := w.app(t, alpha, "off.example.test")
	mustExec(t, pool, `UPDATE apps SET status = 'disabled' WHERE id = $1`, offApp)
	portalHost := "portal.example.test"
	platform := w.initiative(t, access.PlatformInitiativeKey, "active")
	portalApp := w.app(t, platform, portalHost)
	// An app configured as portal but owned by another initiative: the exception must not apply.
	strayPortal := w.app(t, alpha, "stray-portal.example.test")
	ev := access.NewEvaluator(postgres.NewAccessRepository(pool), access.WithPortalApp(portalApp))
	evStray := access.NewEvaluator(postgres.NewAccessRepository(pool), access.WithPortalApp(strayPortal))

	ops := w.group(t, "ops")

	direct := w.user(t, "active")
	w.grantUser(t, alpha, alphaEditor, direct, "")

	viaGroup := w.user(t, "active")
	w.member(t, ops, viaGroup)
	w.grantGroup(t, alpha, alphaViewer, ops)

	both := w.user(t, "active")
	w.member(t, ops, both)
	w.grantUser(t, alpha, alphaEditor, both, "")
	w.grantUser(t, alpha, alphaViewer, both, "") // overlaps with the group grant: must be deduplicated

	disabled := w.user(t, "disabled")
	w.grantUser(t, alpha, alphaEditor, disabled, "")

	inDead := w.user(t, "active")
	w.grantUser(t, dead, deadRole, inDead, "")

	expired := w.user(t, "active")
	w.grantUser(t, alpha, alphaEditor, expired, "2000-01-01T00:00:00Z")

	future := w.user(t, "active")
	w.grantUser(t, alpha, alphaEditor, future, "2999-01-01T00:00:00Z")

	noGrants := w.user(t, "active")

	onlyBeta := w.user(t, "active")
	w.grantUser(t, beta, betaAdmin, onlyBeta, "")

	admin := w.user(t, "active")
	mustExec(t, pool, `INSERT INTO platform_admins (user_id) VALUES ($1)`, admin)

	const missingUser = "00000000-0000-7000-8000-000000000000"

	tests := []struct {
		name   string
		user   string
		host   string
		allow  bool
		reason string
		roles  []string
		appID  string
	}{
		{"direct grant", direct, "alpha.example.test", true, access.ReasonGranted, []string{"editor"}, alphaApp},
		{"grant via group", viaGroup, "alpha.example.test", true, access.ReasonGranted, []string{"viewer"}, alphaApp},
		{"union of direct and group roles", both, "alpha.example.test", true, access.ReasonGranted, []string{"editor", "viewer"}, alphaApp},
		{"unexpired future grant", future, "alpha.example.test", true, access.ReasonGranted, []string{"editor"}, alphaApp},
		{"host in upper case resolves", direct, "ALPHA.Example.Test", true, access.ReasonGranted, []string{"editor"}, alphaApp},
		{"unknown host", direct, "nope.example.test", false, access.ReasonUnknownHost, nil, ""},
		{"disabled user", disabled, "alpha.example.test", false, access.ReasonUserDisabled, nil, alphaApp},
		{"unknown user", missingUser, "alpha.example.test", false, access.ReasonUnknownUser, nil, alphaApp},
		{"inactive initiative", inDead, "dead.example.test", false, access.ReasonInitiativeInactive, nil, ""},
		{"expired grant", expired, "alpha.example.test", false, access.ReasonNoGrant, nil, alphaApp},
		{"no grants", noGrants, "alpha.example.test", false, access.ReasonNoGrant, nil, alphaApp},
		{"grant on another initiative gives no access", onlyBeta, "alpha.example.test", false, access.ReasonNoGrant, nil, alphaApp},
		{"roles are not mixed across initiatives", onlyBeta, "beta.example.test", true, access.ReasonGranted, []string{"admin"}, ""},
		{"disabled app", direct, "off.example.test", false, access.ReasonAppDisabled, nil, offApp},
		{"disabled app, disabled user keeps user_disabled", disabled, "off.example.test", false, access.ReasonUserDisabled, nil, offApp},
		{"other app of the initiative stays reachable", direct, "alpha.example.test", true, access.ReasonGranted, []string{"editor"}, alphaApp},
		{"portal app, active user without grants", noGrants, portalHost, true, access.ReasonGranted, []string{}, portalApp},
		{"portal app, disabled user", disabled, portalHost, false, access.ReasonUserDisabled, nil, portalApp},
		{"portal app, unknown user", missingUser, portalHost, false, access.ReasonUnknownUser, nil, portalApp},
		{"platform admin without grant is denied", admin, "alpha.example.test", false, access.ReasonNoGrant, nil, alphaApp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ev.Evaluate(context.Background(), access.Request{
				Subject:  access.Subject{ID: tt.user},
				Resource: access.Resource{Type: access.ResourceTypeApp, ID: tt.host},
				Action:   access.Action{Name: access.ActionAccess},
			})
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if got.Allow != tt.allow || got.Reason != tt.reason {
				t.Fatalf("allow=%v reason=%q, want allow=%v reason=%q", got.Allow, got.Reason, tt.allow, tt.reason)
			}
			if !slices.Equal(got.Roles, tt.roles) || (tt.allow && got.Roles == nil) {
				t.Fatalf("roles = %v, want %v", got.Roles, tt.roles)
			}
			if tt.appID != "" && got.AppID != tt.appID {
				t.Fatalf("app id = %q, want %q", got.AppID, tt.appID)
			}
		})
	}
	t.Run("portal app id pointing to a non-platform initiative follows the grant rules", func(t *testing.T) {
		t.Parallel()
		got, err := evStray.Evaluate(context.Background(), access.Request{
			Subject:  access.Subject{ID: noGrants},
			Resource: access.Resource{Type: access.ResourceTypeApp, ID: "stray-portal.example.test"},
			Action:   access.Action{Name: access.ActionAccess},
		})
		if err != nil || got.Allow || got.Reason != access.ReasonNoGrant {
			t.Fatalf("got %+v, err %v; want no_grant", got, err)
		}
	})
}

func TestEvaluatePortalAppStates(t *testing.T) {
	t.Parallel()
	pool := newTestDB(t)
	w := world{pool: pool}
	init := w.initiative(t, "platform", "active")
	portalApp := w.app(t, init, "portal.example.test")
	ev := access.NewEvaluator(postgres.NewAccessRepository(pool), access.WithPortalApp(portalApp))
	user := w.user(t, "active")

	evaluate := func() access.Decision {
		t.Helper()
		got, err := ev.Evaluate(context.Background(), access.Request{
			Subject:  access.Subject{ID: user},
			Resource: access.Resource{Type: access.ResourceTypeApp, ID: "portal.example.test"},
			Action:   access.Action{Name: access.ActionAccess},
		})
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		return got
	}
	if got := evaluate(); !got.Allow {
		t.Fatalf("active portal app: %+v", got)
	}
	mustExec(t, pool, `UPDATE apps SET status = 'disabled' WHERE id = $1`, portalApp)
	if got := evaluate(); got.Allow || got.Reason != access.ReasonAppDisabled {
		t.Fatalf("disabled portal app: %+v", got)
	}
	mustExec(t, pool, `UPDATE apps SET status = 'active' WHERE id = $1`, portalApp)
	mustExec(t, pool, `UPDATE initiatives SET status = 'inactive' WHERE id = $1`, init)
	if got := evaluate(); got.Allow || got.Reason != access.ReasonInitiativeInactive {
		t.Fatalf("inactive initiative: %+v", got)
	}
}
