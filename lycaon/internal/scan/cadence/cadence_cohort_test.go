package cadence

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCadenceAssessmentMembersKeepTheirOwnDeltaAndTrigger(t *testing.T) {
	for _, tc := range []struct {
		name             string
		differentBase    bool
		differentTrigger bool
	}{
		{"same target and trigger", false, false},
		{"different scanner baselines", true, false},
		{"different scanner triggers", false, true},
		{"different baselines and triggers", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			clock := newTestClock()
			cadence, store := newTestCadence(t, clock)
			dir := t.TempDir()
			testutil.FailErr(t, "write original file", os.WriteFile(filepath.Join(dir, "obsolete.go"), []byte("package obsolete\n"), 0o644))
			testutil.FailErr(t, "record initial base", cadence.BaselineRoot(ctx, dir))
			canonical, err := scanbase.CanonicalPath(dir)
			testutil.FailErr(t, "canonical path", err)
			testutil.FailErr(t, "write older file", os.WriteFile(filepath.Join(dir, "older.go"), []byte("package older\n"), 0o644))
			testutil.FailErr(t, "delete original file", os.Remove(filepath.Join(dir, "obsolete.go")))
			testutil.FailErr(t, "notify older changes", noteCadenceWrites(ctx, cadence, dir, []string{"older.go", "obsolete.go"}))
			intermediate, _, err := cadence.Coordinator.PublishSourceGeneration(ctx, canonical)
			testutil.FailErr(t, "publish intermediate generation", err)
			testutil.FailErr(t, "write newer file", os.WriteFile(filepath.Join(dir, "newer.go"), []byte("package newer\n"), 0o644))
			testutil.FailErr(t, "notify newer file", noteCadenceWrites(ctx, cadence, dir, []string{"newer.go"}))
			for _, scanner := range []string{"lycaon-sca", "lycaon-secrets"} {
				row, err := store.GetSeries(ctx, canonical, scanner)
				testutil.FailErr(t, "read scanner series", err)
				if tc.differentBase {
					row.LastCoveredSnapshotID = intermediate.ID
				}
				if tc.differentTrigger && scanner == "lycaon-secrets" {
					row.DesiredTrigger = api.ScanTriggerAuthorityRefresh
				}
				testutil.FailErr(t, "retain scanner state", store.UpsertSeries(ctx, *row))
			}
			clock.Advance(5 * time.Second)
			records, failures := cadence.dispatchRoot(ctx, canonical)
			if len(failures) != 0 || len(records) != 3 {
				t.Fatalf("dispatch records=%d failures=%+v; want all three scanners admitted without retries", len(records), failures)
			}
			assessments := make(map[string]bool)
			for _, record := range records {
				assessments[record.AssessmentID] = true
				wantPaths := []string{"newer.go", "older.go"}
				wantDeleted := []string{"obsolete.go"}
				if tc.differentBase && record.ScannerID != "lycaon-sast" {
					wantPaths = []string{"newer.go"}
					wantDeleted = nil
				}
				wantTrigger := api.ScanTriggerWriteBurst
				if tc.differentTrigger && record.ScannerID == "lycaon-secrets" {
					wantTrigger = api.ScanTriggerAuthorityRefresh
				}
				if !slices.Equal(record.TargetPaths, wantPaths) || record.Trigger != wantTrigger {
					t.Fatalf("%s paths=%v trigger=%s; want paths=%v trigger=%s", record.ScannerID, record.TargetPaths, record.Trigger, wantPaths, wantTrigger)
				}
				if !slices.Equal(record.DeletedPaths, wantDeleted) {
					t.Fatalf("%s deleted paths=%v, want %v", record.ScannerID, record.DeletedPaths, wantDeleted)
				}
				if record.SourceSnapshotID != records[0].SourceSnapshotID {
					t.Fatal("cohort partition changed the shared source generation")
				}
			}
			if len(assessments) != 1 {
				t.Fatalf("assessment count=%d, want the complete scanner group in one assessment", len(assessments))
			}
			view, err := store.AssessmentView(ctx, []string{canonical})
			testutil.FailErr(t, "read combined assessment", err)
			if len(view.LatestAttempt) != 3 {
				t.Fatalf("combined assessment retained %d scanners, want all three", len(view.LatestAttempt))
			}
		})
	}
}
