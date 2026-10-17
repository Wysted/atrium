package portal_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Wysted/atrium/platform/api/internal/adminz"
	"github.com/Wysted/atrium/platform/api/internal/portal"
)

type fakePermissions struct {
	p   portal.Permissions
	err error
}

func (f fakePermissions) LoadPermissions(context.Context, string) (portal.Permissions, error) {
	return f.p, f.err
}

type fakeHome struct {
	apps    []portal.HomeApp
	err     error
	exclude *string // records the excluded app id
}

func (f fakeHome) HomeApps(_ context.Context, _, excludeAppID string) ([]portal.HomeApp, error) {
	if f.exclude != nil {
		*f.exclude = excludeAppID
	}
	return f.apps, f.err
}

// fakeStore satisfies portal.Store for tests that never open a unit of work.
type fakeStore struct{}

func (fakeStore) WithTx(context.Context, func(portal.Tx) error) error {
	return errors.New("unexpected unit of work")
}

func TestNewServiceRequiresReadersAndStore(t *testing.T) {
	t.Parallel()
	if _, err := portal.NewService(portal.Config{Permissions: fakePermissions{}, Store: fakeStore{}}); err == nil {
		t.Fatal("accepted a missing home reader")
	}
	if _, err := portal.NewService(portal.Config{Home: fakeHome{}, Store: fakeStore{}}); err == nil {
		t.Fatal("accepted a missing permissions reader")
	}
	if _, err := portal.NewService(portal.Config{Permissions: fakePermissions{}, Home: fakeHome{}}); err == nil {
		t.Fatal("accepted a missing store")
	}
}

func TestMe(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	tests := []struct {
		name    string
		reader  fakePermissions
		wantErr error
	}{
		{"active user", fakePermissions{p: portal.Permissions{UserID: "u1", Status: "active"}}, nil},
		{"disabled user", fakePermissions{p: portal.Permissions{UserID: "u1", Status: "disabled"}}, portal.ErrUserInactive},
		{"unknown user", fakePermissions{err: portal.ErrNotFound}, portal.ErrUserInactive},
		{"storage error", fakePermissions{err: boom}, boom},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc, err := portal.NewService(portal.Config{Permissions: tt.reader, Home: fakeHome{}, Store: fakeStore{}})
			if err != nil {
				t.Fatal(err)
			}
			p, err := svc.Me(context.Background(), "u1")
			if !errors.Is(err, tt.wantErr) || (err == nil && p.UserID != "u1") {
				t.Fatalf("Me = %+v, %v; want error %v", p, err, tt.wantErr)
			}
			if errors.Is(tt.wantErr, boom) && errors.Is(err, portal.ErrUserInactive) {
				t.Fatal("a storage error must not look like an inactive user")
			}
		})
	}
}

func TestHomeAppsExcludesThePortalApp(t *testing.T) {
	t.Parallel()
	var excluded string
	want := []portal.HomeApp{{ID: "app_1"}}
	svc, err := portal.NewService(portal.Config{
		Permissions: fakePermissions{}, Home: fakeHome{apps: want, exclude: &excluded}, Store: fakeStore{}, PortalAppID: "app_portal",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.HomeApps(context.Background(), "u1")
	if err != nil || excluded != "app_portal" || len(got) != 1 || got[0].ID != "app_1" {
		t.Fatalf("HomeApps = %+v, %v (excluded %q)", got, err, excluded)
	}

	boom := errors.New("boom")
	svc, _ = portal.NewService(portal.Config{Permissions: fakePermissions{}, Home: fakeHome{err: boom}, Store: fakeStore{}})
	if _, err := svc.HomeApps(context.Background(), "u1"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestManagedIDs(t *testing.T) {
	t.Parallel()
	p := portal.Permissions{Managed: []portal.InitiativeRef{{ID: "a"}, {ID: "b"}}}
	if got := p.ManagedIDs(); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("ManagedIDs = %v", got)
	}
	if got := (portal.Permissions{}).ManagedIDs(); got == nil || len(got) != 0 {
		t.Fatalf("ManagedIDs of none = %#v, want an empty slice", got)
	}
}

func TestPermissionsSubject(t *testing.T) {
	t.Parallel()
	active := portal.Permissions{UserID: "u1", Status: "active", PlatformAdmin: true, Managed: []portal.InitiativeRef{{ID: "a"}}}
	sub, err := active.Subject()
	if err != nil || sub.ID != "u1" || !sub.PlatformAdmin || !sub.Manages("a") {
		t.Fatalf("Subject = %+v, %v", sub, err)
	}
	if d := adminz.Decide(sub, adminz.InitiativeCreate, adminz.Resource{}); d.Outcome != adminz.Allow {
		t.Fatalf("an active admin must be allowed: %+v", d)
	}

	// The areas a user supervises reach the subject (portal.subject drops them under a token).
	withAreas := portal.Permissions{UserID: "u2", Status: "active", Supervised: []portal.AreaRef{{ID: "x"}}}
	if sub, err := withAreas.Subject(); err != nil || !sub.Supervises("x") || sub.Supervises("y") {
		t.Fatalf("Subject = %+v, %v", sub, err)
	}

	// A disabled platform admin gets no subject at all.
	disabled := active
	disabled.Status = "disabled"
	sub, err = disabled.Subject()
	if !errors.Is(err, portal.ErrUserInactive) {
		t.Fatalf("err = %v, want ErrUserInactive", err)
	}
	// And the subject it returns alongside the error, if misused, is still denied everything.
	if d := adminz.Decide(sub, adminz.InitiativeCreate, adminz.Resource{}); d.Outcome != adminz.Forbidden {
		t.Fatalf("a disabled admin was allowed: %+v", d)
	}
}
