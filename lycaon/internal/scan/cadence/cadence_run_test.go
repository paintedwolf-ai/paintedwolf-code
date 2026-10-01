package cadence

import (
	"context"
	"errors"
	"testing"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCadenceRunPreparesIndependentRootsAndDrainsOnCancel(t *testing.T) {
	clock := newTestClock()
	cadence, store, coord := newBlockingCadence(t, clock)
	roots := []string{t.TempDir(), t.TempDir()}
	for _, root := range roots {
		seedNonEmptyProject(t, root)
		testutil.FailErr(t, "record source change", noteCadenceWrites(t.Context(), cadence, root, []string{"main.go"}))
	}
	clock.Advance(time.Minute)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- cadence.Run(ctx) }()
	waitFor(t, func() bool { coord.mu.Lock(); defer coord.mu.Unlock(); return coord.waited == len(roots) })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("stop cadence: %v", err)
	}
	for _, root := range roots {
		canonical, err := scanbase.CanonicalPath(root)
		testutil.FailErr(t, "canonical root", err)
		rows, err := store.ListSeriesForPath(t.Context(), canonical)
		testutil.FailErr(t, "read claims after shutdown", err)
		if len(rows) == 0 {
			t.Fatal("fixture did not create series")
		}
		for _, row := range rows {
			if row.DispatchToken != "" || row.DueAt.IsZero() {
				t.Fatalf("shutdown stranded scanner %s: %+v", row.ScannerID, row)
			}
		}
	}
}
