package access_test

import (
	"slices"
	"testing"

	"platform/api/internal/access"
)

func TestDecide(t *testing.T) {
	t.Parallel()
	base := access.Facts{
		HostKnown: true, AppID: "app_X", InitiativeID: "i1", InitiativeStatus: "active",
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
		{"unsupported action", "delete", base, false, access.ReasonUnsupportedAction},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := access.Decide(access.Request{Action: access.Action{Name: tt.action}}, tt.facts)
			if got.Allow != tt.allow || got.Reason != tt.reason {
				t.Fatalf("got allow=%v reason=%q, want allow=%v reason=%q", got.Allow, got.Reason, tt.allow, tt.reason)
			}
			if got.Allow && !slices.Equal(got.Roles, tt.facts.Roles) {
				t.Fatalf("roles = %v", got.Roles)
			}
		})
	}
}
