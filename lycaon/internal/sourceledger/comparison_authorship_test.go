package sourceledger

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestContributionLabelsAllowUnnamedChats(t *testing.T) {
	store, ctx := openLedger(t)
	_, err := store.sqlDB.ExecContext(ctx, `INSERT INTO sessions (id,project_id, owner_person_id,posture,created_at,activity_at,updated_at) VALUES ('unnamed','p1', (SELECT id FROM people WHERE role = 'owner'),'build','2026-09-13T00:00:00Z','2026-09-13T00:00:00Z','2026-09-13T00:00:00Z')`)
	testutil.FailErr(t, "create unnamed chat", err)
	labels, err := store.contributionLabels(ctx, []TextContribution{{SessionID: "unnamed"}})
	testutil.FailErr(t, "resolve unnamed chat", err)
	if label, exists := labels["unnamed"]; !exists || label != "" {
		t.Fatalf("unnamed chat label = %q, present=%v", label, exists)
	}
}
