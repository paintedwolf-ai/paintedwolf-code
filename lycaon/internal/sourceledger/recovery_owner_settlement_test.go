package sourceledger

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// An owner journal keeps its recovery manifest through collection until the
// owner settles it; only the settled outcome releases the bytes.
func TestRecoveryManifestSurvivesCollectionUntilOwnerSettles(t *testing.T) {
	cases := []struct {
		name     string
		identity string
		plan     string
		active   string
	}{
		{name: "prepared plan reference", identity: "owner", plan: `{"recovery_id":"journal"}`, active: "prepared"},
		{name: "applied plan reference", identity: "owner", plan: `{"recovery_id":"journal"}`, active: "file_applied"},
		{name: "failed plan awaiting retry", identity: "owner", plan: `{"recovery_id":"journal"}`, active: "failed"},
		{name: "diverged plan awaiting review", identity: "owner", plan: `{"recovery_id":"journal"}`, active: "diverged"},
		{name: "prepared journal identity", identity: "journal", plan: `{}`, active: "prepared"},
		{name: "applied journal identity", identity: "journal", plan: `{}`, active: "file_applied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, ctx := openLedger(t)
			seedRecoveryManifest(t, s, "journal", 2)
			_, err := s.sqlDB.ExecContext(ctx, `INSERT INTO source_mutations(id,project_id,kind,input_digest,plan_json,status,created_at,updated_at) VALUES(?,'p1','write','digest',?,?,'','')`, tc.identity, tc.plan, tc.active)
			testutil.FailErr(t, "record owner journal", err)
			for range 3 {
				testutil.FailErr(t, "collect while owned", s.Retention.SweepBlobs(ctx))
			}
			if got := recoveryEntryCount(t, s); got != 2 {
				t.Fatalf("collection removed %d owned recovery entries", 2-got)
			}
			_, err = s.sqlDB.ExecContext(ctx, `UPDATE source_mutations SET status='committed' WHERE id=?`, tc.identity)
			testutil.FailErr(t, "settle owner journal", err)
			for range 3 {
				testutil.FailErr(t, "collect after settlement", s.Retention.SweepBlobs(ctx))
			}
			if got := recoveryEntryCount(t, s); got != 0 {
				t.Fatalf("settled journal retained %d recovery entries", got)
			}
		})
	}
}

func recoveryEntryCount(t *testing.T, s *Store) int {
	t.Helper()
	var count int
	testutil.FailErr(t, "count recovery entries", s.sqlDB.QueryRowContext(t.Context(), `SELECT count(*) FROM source_recovery_entries WHERE recovery_id='journal'`).Scan(&count))
	return count
}
