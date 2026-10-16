package portal

import (
	"slices"
	"testing"
	"time"
)

func TestHealthProblems(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	const probeEvery = 5 * time.Minute
	healthy := GrantCounts{Grants: 2, EffectiveManagers: 1}
	dep := func(host string, seen *time.Time, probeAt *time.Time, status string) Deployment {
		return Deployment{Host: host, LastSeenAt: seen, LastProbeAt: probeAt, LastProbeStatus: status}
	}
	good := dep("a.example.test", ago(time.Minute), ago(time.Minute), ProbeOK)

	tests := []struct {
		name       string
		app        App
		initiative string
		grants     GrantCounts
		want       []HealthProblem
	}{
		{"healthy", App{Status: AppStatusActive, Deployments: []Deployment{good}}, InitiativeStatusActive, healthy, nil},
		{"app without deployments", App{Status: AppStatusActive}, InitiativeStatusActive, healthy, nil},
		{"never seen and never probed", App{Status: AppStatusActive, Deployments: []Deployment{dep("a.example.test", nil, nil, "")}},
			InitiativeStatusActive, healthy,
			[]HealthProblem{{ProblemNeverProbed, "a.example.test"}, {ProblemNeverSeen, "a.example.test"}}},
		{"seen just inside the threshold", App{Status: AppStatusActive, Deployments: []Deployment{dep("a.example.test", ago(NotSeenRecentlyAfter), ago(0), ProbeOK)}},
			InitiativeStatusActive, healthy, nil},
		{"not seen recently", App{Status: AppStatusActive, Deployments: []Deployment{dep("a.example.test", ago(NotSeenRecentlyAfter+time.Second), ago(0), ProbeOK)}},
			InitiativeStatusActive, healthy, []HealthProblem{{ProblemNotSeenRecently, "a.example.test"}}},
		{"unprotected", App{Status: AppStatusActive, Deployments: []Deployment{dep("a.example.test", ago(time.Hour), ago(0), ProbeUnprotected)}},
			InitiativeStatusActive, healthy, []HealthProblem{{ProblemUnprotected, "a.example.test"}}},
		{"unexpected redirect", App{Status: AppStatusActive, Deployments: []Deployment{dep("a.example.test", ago(time.Hour), ago(0), ProbeUnexpected)}},
			InitiativeStatusActive, healthy, []HealthProblem{{ProblemUnexpectedRedirect, "a.example.test"}}},
		{"unreachable", App{Status: AppStatusActive, Deployments: []Deployment{dep("a.example.test", ago(time.Hour), ago(0), ProbeUnreachable)}},
			InitiativeStatusActive, healthy, []HealthProblem{{ProblemUnreachable, "a.example.test"}}},
		{"probe result exactly at the stale limit", App{Status: AppStatusActive, Deployments: []Deployment{dep("a.example.test", ago(time.Hour), ago(ProbeStaleFactor*probeEvery), ProbeOK)}},
			InitiativeStatusActive, healthy, nil},
		{"probe result stale", App{Status: AppStatusActive, Deployments: []Deployment{dep("a.example.test", ago(time.Hour), ago(ProbeStaleFactor*probeEvery+time.Second), ProbeOK)}},
			InitiativeStatusActive, healthy, []HealthProblem{{ProblemProbeStale, "a.example.test"}}},
		{"stale and unprotected", App{Status: AppStatusActive, Deployments: []Deployment{dep("a.example.test", ago(time.Hour), ago(time.Hour), ProbeUnprotected)}},
			InitiativeStatusActive, healthy, []HealthProblem{{ProblemUnprotected, "a.example.test"}, {ProblemProbeStale, "a.example.test"}}},
		{"no grants and no manager", App{Status: AppStatusActive, Deployments: []Deployment{good}}, InitiativeStatusActive, GrantCounts{},
			[]HealthProblem{{ProblemNoGrants, ""}, {ProblemNoManager, ""}}},
		{"grants but no effective manager", App{Status: AppStatusActive, Deployments: []Deployment{good}}, InitiativeStatusActive, GrantCounts{Grants: 3},
			[]HealthProblem{{ProblemNoManager, ""}}},
		{"disabled app: no traffic is expected, the probe still counts",
			App{Status: AppStatusDisabled, Deployments: []Deployment{dep("a.example.test", nil, ago(0), ProbeUnprotected)}},
			InitiativeStatusActive, healthy,
			[]HealthProblem{{ProblemAppDisabled, ""}, {ProblemUnprotected, "a.example.test"}}},
		{"inactive initiative", App{Status: AppStatusActive, Deployments: []Deployment{dep("a.example.test", ago(72*time.Hour), ago(0), ProbeOK)}},
			InitiativeStatusInactive, healthy, []HealthProblem{{ProblemInitiativeInactive, ""}}},
		{"app-level findings first, then hosts in order",
			App{Status: AppStatusDisabled, Deployments: []Deployment{
				dep("a.example.test", nil, nil, ""), dep("b.example.test", nil, ago(0), ProbeUnreachable),
			}},
			InitiativeStatusInactive, GrantCounts{},
			[]HealthProblem{
				{ProblemAppDisabled, ""}, {ProblemInitiativeInactive, ""}, {ProblemNoGrants, ""}, {ProblemNoManager, ""},
				{ProblemNeverProbed, "a.example.test"}, {ProblemUnreachable, "b.example.test"},
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := healthProblems(tt.app, tt.initiative, tt.grants, now, probeEvery)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("problems = %v\nwant       %v", got, tt.want)
			}
		})
	}
}

// With the probe disabled one app-level finding replaces the probe-age ones; stored statuses are still reported.
func TestHealthProblemsWithTheProbeDisabled(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	healthy := GrantCounts{Grants: 1, EffectiveManagers: 1}
	a := App{Status: AppStatusActive, Deployments: []Deployment{
		{Host: "a.example.test", LastSeenAt: ago(time.Minute)},                                                                  // never probed
		{Host: "b.example.test", LastSeenAt: ago(time.Minute), LastProbeAt: ago(30 * 24 * time.Hour), LastProbeStatus: ProbeOK}, // ancient
		{Host: "c.example.test", LastSeenAt: ago(time.Minute), LastProbeAt: ago(time.Hour), LastProbeStatus: ProbeUnprotected},
	}}
	want := []HealthProblem{{ProblemProbeDisabled, ""}, {ProblemUnprotected, "c.example.test"}}
	if got := healthProblems(a, InitiativeStatusActive, healthy, now, 0); !slices.Equal(got, want) {
		t.Fatalf("problems = %v, want %v", got, want)
	}
}
