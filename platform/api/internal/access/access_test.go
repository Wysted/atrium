package access_test

import (
	"context"
	"slices"
	"testing"

	"github.com/Wysted/atrium/platform/api/internal/access"
)

func TestDecide(t *testing.T) {
	t.Parallel()
	base := access.Facts{
		HostKnown: true, AppID: "app_X", InitiativeID: "i1", InitiativeStatus: "active", AppStatus: "active",
		UserFound: true, UserStatus: "active", Roles: []string{"editor"},
	}
	with := func(mod func(*access.Facts)) access.Facts { f := base; mod(&f); return f }
	tests := []struct {
		name   string
		action string
		facts  access.Facts
		allow  bool
		reason string
	}{
		{"granted", access.ActionAccess, base, true, access.ReasonGranted},
		{"unknown host", access.ActionAccess, access.Facts{}, false, access.ReasonUnknownHost},
		{"unknown user", access.ActionAccess, with(func(f *access.Facts) { f.UserFound = false }), false, access.ReasonUnknownUser},
		{"disabled user", access.ActionAccess, with(func(f *access.Facts) { f.UserStatus = "disabled" }), false, access.ReasonUserDisabled},
		{"inactive initiative", access.ActionAccess, with(func(f *access.Facts) { f.InitiativeStatus = "inactive" }), false, access.ReasonInitiativeInactive},
		{"no roles", access.ActionAccess, with(func(f *access.Facts) { f.Roles = nil }), false, access.ReasonNoGrant},
		{"app disabled", access.ActionAccess, with(func(f *access.Facts) { f.AppStatus = "disabled" }), false, access.ReasonAppDisabled},
		{"app disabled with no grant reports app_disabled", access.ActionAccess, with(func(f *access.Facts) { f.AppStatus = "disabled"; f.Roles = nil }), false, access.ReasonAppDisabled},
		{"disabled user wins over disabled app", access.ActionAccess, with(func(f *access.Facts) { f.AppStatus = "disabled"; f.UserStatus = "disabled" }), false, access.ReasonUserDisabled},
		{"inactive initiative wins over disabled app", access.ActionAccess, with(func(f *access.Facts) { f.AppStatus = "disabled"; f.InitiativeStatus = "inactive" }), false, access.ReasonInitiativeInactive},
		{"portal app allows an active user without grants", access.ActionAccess, with(func(f *access.Facts) { f.PortalApp = true; f.Roles = nil }), true, access.ReasonGranted},
		{"portal app with unknown user", access.ActionAccess, with(func(f *access.Facts) { f.PortalApp = true; f.UserFound = false }), false, access.ReasonUnknownUser},
		{"portal app with disabled user", access.ActionAccess, with(func(f *access.Facts) { f.PortalApp = true; f.UserStatus = "disabled" }), false, access.ReasonUserDisabled},
		{"portal app disabled", access.ActionAccess, with(func(f *access.Facts) { f.PortalApp = true; f.AppStatus = "disabled" }), false, access.ReasonAppDisabled},
		{"portal app with inactive initiative", access.ActionAccess, with(func(f *access.Facts) { f.PortalApp = true; f.InitiativeStatus = "inactive" }), false, access.ReasonInitiativeInactive},
		{"unsupported action", "delete", base, false, access.ReasonUnsupportedAction},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := access.Decide(access.Request{Action: access.Action{Name: tt.action}}, tt.facts)
			if got.Allow != tt.allow || got.Reason != tt.reason {
				t.Fatalf("got allow=%v reason=%q, want allow=%v reason=%q", got.Allow, got.Reason, tt.allow, tt.reason)
			}
			wantRoles := tt.facts.Roles
			if tt.facts.PortalApp {
				wantRoles = []string{} // the portal app never carries roles
			}
			if got.Allow && (got.Roles == nil || !slices.Equal(got.Roles, wantRoles)) {
				t.Fatalf("roles = %#v, want %#v", got.Roles, wantRoles)
			}
		})
	}
}

type stubFacts access.Facts

func (s stubFacts) AccessFacts(context.Context, string, string) (access.Facts, error) {
	return access.Facts(s), nil
}

func TestEvaluatorPortalApp(t *testing.T) {
	t.Parallel()
	const portal = "app_00000000000000000000000001"
	facts := stubFacts{
		HostKnown: true, AppID: portal, InitiativeKey: access.PlatformInitiativeKey, InitiativeStatus: "active", AppStatus: "active",
		UserFound: true, UserStatus: "active",
	}
	req := access.Request{Action: access.Action{Name: access.ActionAccess}, Resource: access.Resource{ID: "portal.example.test"}}
	otherInitiative := facts
	otherInitiative.InitiativeKey = "alpha"
	tests := []struct {
		name  string
		opts  []access.EvaluatorOption
		facts stubFacts
		allow bool
	}{
		{"configured portal app", []access.EvaluatorOption{access.WithPortalApp(portal)}, facts, true},
		{"no portal app configured", nil, facts, false},
		{"portal app id of another initiative is not the portal", []access.EvaluatorOption{access.WithPortalApp(portal)}, otherInitiative, false},
		{"empty portal app id never matches", []access.EvaluatorOption{access.WithPortalApp("")}, facts, false},
		{"other app is not the portal", []access.EvaluatorOption{access.WithPortalApp("app_00000000000000000000000002")}, facts, false},
		{"unknown host with empty app id", []access.EvaluatorOption{access.WithPortalApp("")}, stubFacts{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := access.NewEvaluator(tt.facts, tt.opts...).Evaluate(context.Background(), req)
			if err != nil || got.Allow != tt.allow || got.PortalApp != tt.allow {
				t.Fatalf("allow=%v portal=%v err=%v, want %v", got.Allow, got.PortalApp, err, tt.allow)
			}
		})
	}
}
