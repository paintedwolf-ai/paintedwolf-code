package cadence

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/scantest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCadenceSettlesWholeBurstBeforeScanning(t *testing.T) {
	cadence, store := newTestCadence(t, newTestClock())
	cadence.Now = nil
	cadence.Gates.Gates.Cadence.WriteBurstSettleMs = 120
	root := t.TempDir()
	testutil.FailErr(t, "attach root", cadence.BaselineRoot(t.Context(), root))
	canonical, err := scanbase.CanonicalPath(root)
	testutil.FailErr(t, "canonical root", err)
	scantest.RunService(t, cadence.Run)
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		testutil.FailErr(t, "write burst member", os.WriteFile(filepath.Join(root, name), []byte("package main\n"), 0o600))
		testutil.FailErr(t, "note burst member", noteCadenceWrites(t.Context(), cadence, root, []string{name}))
		time.Sleep(30 * time.Millisecond)
		rows, err := store.ListByCanonicalPath(t.Context(), canonical)
		testutil.FailErr(t, "check unsettled burst", err)
		if len(rows) != 0 {
			t.Fatalf("unsettled burst already enqueued %d scans", len(rows))
		}
	}
	testutil.WaitFor(t, 10*time.Second, func() bool {
		rows, err := store.ListByCanonicalPath(t.Context(), canonical)
		return err == nil && len(rows) == 3
	})
	rows, err := store.ListByCanonicalPath(t.Context(), canonical)
	testutil.FailErr(t, "read settled burst", err)
	for _, row := range rows {
		if row.TargetKind != api.ScanTargetPaths || len(row.TargetPaths) != 3 {
			t.Fatalf("burst did not coalesce into one delta per scanner: %+v", row)
		}
	}
}

func TestCadenceRecoversPersistedDeadlineWithoutNotification(t *testing.T) {
	cadence, store := newTestCadence(t, newTestClock())
	cadence.Now = nil
	cadence.Gates.Gates.Cadence.WriteBurstSettleMs = 100
	root := t.TempDir()
	testutil.FailErr(t, "attach root", cadence.BaselineRoot(t.Context(), root))
	canonical, err := scanbase.CanonicalPath(root)
	testutil.FailErr(t, "canonical root", err)
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600))
	testutil.FailErr(t, "persist demand", noteCadenceWrites(t.Context(), cadence, root, []string{"main.go"}))
	// A newly constructed repository has no process-local notifications.
	cadence.Store = scanbase.NewSQLStore(store.DB())
	scantest.RunService(t, cadence.Run)
	testutil.WaitFor(t, 10*time.Second, func() bool {
		rows, err := store.ListByCanonicalPath(t.Context(), canonical)
		return err == nil && len(rows) == 3
	})
}

func TestCadenceUnchangedNotificationLeavesNoRecurringDemand(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	root := t.TempDir()
	testutil.FailErr(t, "write baseline", os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600))
	testutil.FailErr(t, "attach root", cadence.BaselineRoot(t.Context(), root))
	canonical, err := scanbase.CanonicalPath(root)
	testutil.FailErr(t, "canonical root", err)
	testutil.FailErr(t, "note unchanged bytes", noteCadenceWrites(t.Context(), cadence, root, []string{"main.go"}))
	for range 3 {
		clock.Advance(20 * time.Minute)
		cadence.Tick(t.Context())
	}
	rows, err := store.ListByCanonicalPath(t.Context(), canonical)
	testutil.FailErr(t, "read scans after no-op", err)
	if len(rows) != 0 {
		t.Fatalf("unchanged notification created %d scans", len(rows))
	}
	series, err := store.ListSeriesForPath(t.Context(), canonical)
	testutil.FailErr(t, "read settled series", err)
	for _, row := range series {
		if row.WantsDispatch() || !row.DueAt.IsZero() {
			t.Fatalf("no-op retained recurring demand: %+v", row)
		}
	}
}
