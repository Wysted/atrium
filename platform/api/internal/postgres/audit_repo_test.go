package postgres_test

import (
	"context"
	"testing"

	"platform/api/internal/audit"
	"platform/api/internal/postgres"
)

func TestAuditRepoInitiativeID(t *testing.T) {
	t.Parallel()
	pool := newTestDB(t)
	w := world{pool: pool}
	init := w.initiative(t, "alpha", "active")
	repo := postgres.NewAuditRepo(pool)

	tests := []struct {
		name         string
		initiativeID string
		wantNull     bool
	}{
		{"scoped to an initiative", init, false},
		{"global target stores NULL", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action := "test." + tt.name
			err := repo.RecordAudit(context.Background(), audit.Event{
				InitiativeID: tt.initiativeID, Action: action, TargetType: "test", TargetID: "1",
			})
			if err != nil {
				t.Fatalf("RecordAudit: %v", err)
			}
			got := scalar[*string](t, pool, `SELECT initiative_id::text FROM audit_events WHERE action = $1`, action)
			if tt.wantNull != (got == nil) || (got != nil && *got != tt.initiativeID) {
				t.Fatalf("initiative_id = %v, want %q (null=%v)", got, tt.initiativeID, tt.wantNull)
			}
		})
	}
	if err := repo.RecordAudit(context.Background(), audit.Event{InitiativeID: "not-a-uuid", Action: "x", TargetType: "t", TargetID: "1"}); err == nil {
		t.Fatal("malformed initiative id must fail")
	}
}
