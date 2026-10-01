package cadence

import (
	"testing"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCadenceFullPassKeepsExplicitScannerSelection(t *testing.T) {
	for _, scannerID := range []string{"lycaon-sca", "lycaon-secrets", "lycaon-sast"} {
		t.Run(scannerID, func(t *testing.T) {
			cadence, store := newTestCadence(t, newTestClock())
			dir := t.TempDir()
			seedNonEmptyProject(t, dir)
			testutil.FailErr(t, "attach root", cadence.BaselineRoot(t.Context(), dir))

			got, err := cadence.RequestFull(t.Context(), dir, []string{scannerID}, api.ScanTriggerManual, scanbase.FullScanContext{})
			testutil.FailErr(t, "request selected scanner", err)
			if len(got.Members) != 1 || got.Members[0].ScannerID != scannerID || got.Members[0].Phase != api.FullPassMemberStarted {
				t.Fatalf("full pass members = %+v, want only %s started", got.Members, scannerID)
			}
			canonical, err := scanbase.CanonicalPath(dir)
			testutil.FailErr(t, "resolve canonical path", err)
			scans, err := store.ListByCanonicalPath(t.Context(), canonical)
			testutil.FailErr(t, "list queued scans", err)
			if len(scans) != 1 || scans[0].ScannerID != scannerID {
				t.Fatalf("queued scans = %+v, want only %s", scans, scannerID)
			}
		})
	}
}
