package portal

import (
	"context"
	"time"

	"platform/api/internal/adminz"
)

// Results of the last probe of a deployment (mirror the deployments.last_probe_status CHECK).
const (
	ProbeOK          = "ok"
	ProbeUnprotected = "unprotected"
	ProbeUnexpected  = "unexpected"
	ProbeUnreachable = "unreachable"
)

// NotSeenRecentlyAfter is how long a deployment may go without an authorized request before the health report
// calls it out (not_seen_recently).
const NotSeenRecentlyAfter = 24 * time.Hour

// ProbeStaleFactor is how many probe intervals may pass without a result before the health report calls the last
// probe stale (the probe is failing to complete rounds: for example the control sample keeps failing).
const ProbeStaleFactor = 3

// Orphan hosts: hosts Traefik routed to the platform that have no deployment.
const (
	// OrphanHostWindow is how far back unknown_host denials count.
	OrphanHostWindow = 7 * 24 * time.Hour
	// OrphanHostLimit bounds the list (most recent first).
	OrphanHostLimit = 100
)

// Problem codes of the integration health of an app. They are a stable vocabulary for the UI and alerts.
const (
	ProblemAppDisabled        = "app_disabled"        // the app is disabled: ForwardAuth denies everyone
	ProblemInitiativeInactive = "initiative_inactive" // the initiative is inactive: ForwardAuth denies everyone
	ProblemNoGrants           = "no_grants"           // nobody holds a non-expired grant in the initiative
	ProblemNoManager          = "no_manager"          // no effective manages_access grant (the last-manager rule's definition)
	ProblemUnprotected        = "unprotected"         // the last probe was served without ForwardAuth (host)
	ProblemUnexpectedRedirect = "unexpected_redirect" // the last probe was redirected somewhere else (host)
	ProblemUnreachable        = "unreachable"         // the last probe could not connect (host)
	ProblemNeverProbed        = "never_probed"        // no probe result yet (host)
	ProblemProbeStale         = "probe_stale"         // the last probe is older than ProbeStaleFactor intervals (host)
	ProblemProbeDisabled      = "probe_disabled"      // the portal runs without a probe (PORTAL_PROBE_INTERVAL=0)
	ProblemNeverSeen          = "never_seen"          // no authorized request ever reached the host
	ProblemNotSeenRecently    = "not_seen_recently"   // none for NotSeenRecentlyAfter (host)
)

// HealthProblem is one finding of the health report. Host is empty for findings about the app as a whole.
type HealthProblem struct {
	Code string
	Host string
}

// GrantCounts are the numbers the health report needs about an initiative's grants.
type GrantCounts struct {
	// Grants counts the non-expired grants of the initiative, whoever holds them.
	Grants int
	// EffectiveManagers counts the effective manages_access grants, by the definition of the last-manager rule
	// (Service.DeleteGrant): not expired, and held by at least one active user directly or through a group.
	EffectiveManagers int
}

// OrphanHost is a host that was refused with unknown_host and still has no deployment.
type OrphanHost struct {
	Host string
	// Count is how many denials were recorded in the window. platform-api records at most one per host every
	// 10 minutes per replica, so it measures how long the host has been hit, not how many requests it got.
	Count        int64
	LastDeniedAt time.Time
}

// AppHealth is the integration health of an app: its state, the telemetry of its deployments (inside App) and the
// problems derived from them.
type AppHealth struct {
	App              App
	InitiativeStatus string
	Grants           GrantCounts
	Problems         []HealthProblem
	CheckedAt        time.Time
}

// HealthRepo is the storage of the health reports.
type HealthRepo interface {
	// GrantCounts returns the grant numbers of an initiative.
	GrantCounts(ctx context.Context, initiativeID string) (GrantCounts, error)
	// OrphanHosts returns the hosts with unknown_host denials newer than since and no deployment, most recent
	// denial first, at most limit of them.
	OrphanHosts(ctx context.Context, since time.Time, limit int) ([]OrphanHost, error)
}

// healthProblems derives the problem list of an app. probeInterval is the interval of the probe, zero when it is
// disabled: then one app-level probe_disabled replaces never_probed and probe_stale (the statuses already stored
// are still reported). The findings about the app as a whole come first, then
// those of each deployment in the order of a.Deployments (by host). A deployment that cannot receive authorized
// traffic by design (a disabled app, an inactive initiative) is not reported as never or no longer seen, but its
// probe findings stay: a host served without ForwardAuth is dangerous whatever the app's state.
func healthProblems(a App, initiativeStatus string, g GrantCounts, now time.Time, probeInterval time.Duration) []HealthProblem {
	var out []HealthProblem
	add := func(code, host string) { out = append(out, HealthProblem{Code: code, Host: host}) }

	if a.Status == AppStatusDisabled {
		add(ProblemAppDisabled, "")
	}
	if initiativeStatus == InitiativeStatusInactive {
		add(ProblemInitiativeInactive, "")
	}
	if g.Grants == 0 {
		add(ProblemNoGrants, "")
	}
	if g.EffectiveManagers == 0 {
		add(ProblemNoManager, "")
	}
	if probeInterval <= 0 {
		add(ProblemProbeDisabled, "")
	}

	reachable := a.Status == AppStatusActive && initiativeStatus == InitiativeStatusActive
	for _, d := range a.Deployments {
		switch d.LastProbeStatus {
		case ProbeUnprotected:
			add(ProblemUnprotected, d.Host)
		case ProbeUnexpected:
			add(ProblemUnexpectedRedirect, d.Host)
		case ProbeUnreachable:
			add(ProblemUnreachable, d.Host)
		case "":
			if probeInterval > 0 {
				add(ProblemNeverProbed, d.Host)
			}
		}
		if probeInterval > 0 && d.LastProbeAt != nil && now.Sub(*d.LastProbeAt) > ProbeStaleFactor*probeInterval {
			add(ProblemProbeStale, d.Host)
		}
		if !reachable {
			continue
		}
		switch {
		case d.LastSeenAt == nil:
			add(ProblemNeverSeen, d.Host)
		case now.Sub(*d.LastSeenAt) > NotSeenRecentlyAfter:
			add(ProblemNotSeenRecently, d.Host)
		}
	}
	return out
}

// AppHealth reports the integration health of an app (platform admins and the managers of its initiative).
func (s *Service) AppHealth(ctx context.Context, callerID, appID string) (AppHealth, error) {
	if !appIDPattern.MatchString(appID) {
		return AppHealth{}, ErrNotFound
	}
	var out AppHealth
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = AppHealth{}
		sub, err := subject(ctx, tx, callerID)
		if err != nil {
			return err
		}
		a, err := tx.Catalog().GetApp(ctx, appID)
		if err != nil {
			return err
		}
		if _, err := authorize(sub, adminz.AppHealth, appResource(a)); err != nil {
			return err
		}
		a.PortalApp = s.isPortalApp(a)
		in, err := tx.Catalog().GetInitiative(ctx, a.InitiativeID)
		if err != nil {
			return err
		}
		g, err := tx.Health().GrantCounts(ctx, a.InitiativeID)
		if err != nil {
			return err
		}
		now := s.now()
		out = AppHealth{
			App: a, InitiativeStatus: in.Status, Grants: g, CheckedAt: now,
			Problems: healthProblems(a, in.Status, g, now, s.probeInterval),
		}
		return nil
	})
	return out, err
}

// ListOrphanHosts lists the hosts refused as unknown in the last OrphanHostWindow that have no deployment
// (platform admins only: an orphan host belongs to no app, so to no initiative a manager could see).
func (s *Service) ListOrphanHosts(ctx context.Context, callerID string) ([]OrphanHost, error) {
	var out []OrphanHost
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = nil
		if _, _, err := begin(ctx, tx, callerID, adminz.OrphanHostList, adminz.Resource{Type: TargetOrphanHost}); err != nil {
			return err
		}
		hosts, err := tx.Health().OrphanHosts(ctx, s.now().Add(-OrphanHostWindow), OrphanHostLimit)
		if err != nil {
			return err
		}
		out = hosts
		return nil
	})
	return out, err
}
