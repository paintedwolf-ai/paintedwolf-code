package scan

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCompletedScanReuseDoesNotCrossAChangedObservation(t *testing.T) {
	for _, changed := range []string{"source", "execution", "target", "other_root", "other_scanner", "unchanged"} {
		t.Run(changed, func(t *testing.T) {
			store := authorityTestStore(t)
			first := authorityScan("first", "first-assessment", t.TempDir(), "snapshot-a", "sast", api.ScanTargetFull, nil)
			first.CreatedAt = time.Now().Add(-time.Hour).UTC()
			testutil.FailErr(t, "insert original scan", store.Insert(t.Context(), first, nil, ""))
			completeNextScan(t, store)
			key, err := scanReuseKey(first, "")
			testutil.FailErr(t, "original reuse identity", err)
			before, err := store.FindReusableByPathScanner(t.Context(), first.CanonicalPath, first.SourceSnapshotID, first.ScannerID, key)
			testutil.FailErr(t, "reuse original", err)
			if before == nil {
				t.Fatal("unchanged complete scan was not reusable")
			}
			next := authorityScan("next", "next-assessment", first.CanonicalPath, first.SourceSnapshotID, first.ScannerID, api.ScanTargetFull, nil)
			// Equal timestamps exercise the row-order tie break.
			next.CreatedAt = first.CreatedAt
			var paths []string
			switch changed {
			case "source":
				next.SourceSnapshotID = "snapshot-b"
			case "execution":
				next.ExecutionFingerprint = "execution-b"
			case "target":
				next.TargetKind = api.ScanTargetPaths
				paths = []string{"a.go"}
			case "other_root":
				next.CanonicalPath = t.TempDir()
			case "other_scanner":
				next.ScannerID = "other"
				next.ExecutionManifest.ScannerID = "other"
			}
			testutil.FailErr(t, "insert intervening scan", store.Insert(t.Context(), next, paths, ""))
			completeNextScan(t, store)
			got, err := store.FindReusableByPathScanner(t.Context(), first.CanonicalPath, first.SourceSnapshotID, first.ScannerID, key)
			testutil.FailErr(t, "reuse after observation", err)
			reject := changed == "source" || changed == "execution" || changed == "target"
			if (got == nil) != reject {
				t.Fatalf("reuse after %s = %v; reject=%t", changed, got, reject)
			}
		})
	}
}
