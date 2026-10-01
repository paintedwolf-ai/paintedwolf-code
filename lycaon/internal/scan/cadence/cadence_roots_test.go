package cadence

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCadenceRetirementPreservesAttachedOrUnknownRoots(t *testing.T) {
	lookupErr := errors.New("registry unavailable")
	for _, tc := range []struct {
		name     string
		attached bool
		err      error
	}{
		{name: "concurrent attachment", attached: true},
		{name: "registry unavailable", err: lookupErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cadence, store := newTestCadence(t, newTestClock())
			root, err := scanbase.CanonicalPath(t.TempDir())
			testutil.FailErr(t, "canonical root", err)
			testutil.FailErr(t, "baseline", cadence.BaselineRoot(t.Context(), root))
			ctx, release, _ := cadence.beginRootDispatch(t.Context(), root, "pending")
			defer release()
			err = cadence.RetireRoot(t.Context(), root, func(context.Context, string) (bool, error) {
				return tc.attached, tc.err
			})
			if !errors.Is(err, tc.err) {
				t.Fatalf("retirement error = %v, want %v", err, tc.err)
			}
			rows, err := store.ListSeriesForPath(t.Context(), root)
			testutil.FailErr(t, "retained series", err)
			if len(rows) != 3 || ctx.Err() != nil {
				t.Fatalf("retained root: series=%d dispatch=%v", len(rows), ctx.Err())
			}
		})
	}
}

func TestCadenceRetiredRootIgnoresLateWritesAndCanReattach(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	ctx := t.Context()
	dir := t.TempDir()
	testutil.FailErr(t, "attach", cadence.BaselineRoot(ctx, dir))
	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "canonical root", err)
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))
	testutil.FailErr(t, "queue write", noteCadenceWrites(ctx, cadence, dir, []string{"main.go"}))
	testutil.FailErr(t, "retire", cadence.RetireRoot(ctx, dir, nil))
	for range 3 {
		testutil.FailErr(t, "late baseline", cadence.BaselineRoot(ctx, dir))
		testutil.FailErr(t, "late write", noteCadenceWrites(ctx, cadence, dir, []string{"main.go"}))
		testutil.FailErr(t, "late inventory", cadence.NoteTree(ctx, dir, 1))
		clock.Advance(time.Minute)
		cadence.Tick(ctx)
	}
	rows, err := store.ListSeriesForPath(ctx, canonical)
	testutil.FailErr(t, "retired series", err)
	if len(rows) != 0 {
		t.Fatalf("retired root recreated %d series", len(rows))
	}
	cadence.RootAttached(dir)
	testutil.FailErr(t, "reattach baseline", cadence.BaselineRoot(ctx, dir))
	requireSeriesBased(t, cadence, dir)
	testutil.FailErr(t, "edit reattached source", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package changed\n"), 0o644))
	testutil.FailErr(t, "queue reattached write", noteCadenceWrites(ctx, cadence, dir, []string{"main.go"}))
	clock.Advance(time.Minute)
	cadence.Tick(ctx)
	scans, err := store.ListByCanonicalPath(ctx, canonical)
	testutil.FailErr(t, "reattached scans", err)
	if len(scans) != 3 {
		t.Fatalf("reattached scans = %d, want 3", len(scans))
	}
}

func TestCadenceRetirementCancelsOnlyItsDispatches(t *testing.T) {
	cadence, _ := newTestCadence(t, newTestClock())
	root, err := scanbase.CanonicalPath(t.TempDir())
	testutil.FailErr(t, "canonical root", err)
	other := t.TempDir()
	ctx, release, current := cadence.beginRootDispatch(t.Context(), root, "old")
	if !current {
		t.Fatal("initial dispatch was refused")
	}
	defer release()
	otherCtx, releaseOther, _ := cadence.beginRootDispatch(t.Context(), other, "other")
	defer releaseOther()
	testutil.FailErr(t, "remove root directory", os.Remove(root))
	testutil.FailErr(t, "retire missing root", cadence.RetireRoot(t.Context(), root, nil))
	if ctx.Err() != context.Canceled || otherCtx.Err() != nil {
		t.Fatalf("dispatch contexts: retired=%v other=%v", ctx.Err(), otherCtx.Err())
	}
	if _, _, accepted := cadence.beginRootDispatch(t.Context(), root, "late"); accepted {
		t.Fatal("retired root accepted another dispatch")
	}
}

func TestCadenceRetirementKeepsScanHistoryWithoutRearming(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	dir := t.TempDir()
	ctx := t.Context()
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))
	_, err := cadence.RequestFull(ctx, dir, nil, api.ScanTriggerManual, scanbase.FullScanContext{})
	testutil.FailErr(t, "request scans", err)
	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "canonical root", err)
	before, err := store.ListByCanonicalPath(ctx, canonical)
	testutil.FailErr(t, "scan history before retirement", err)
	if len(before) == 0 {
		t.Fatal("no scans were queued")
	}
	testutil.FailErr(t, "retire", cadence.RetireRoot(ctx, dir, nil))
	completeCadenceScans(t, cadence, store)
	clock.Advance(time.Hour)
	cadence.Tick(ctx)
	rows, err := store.ListSeriesForPath(ctx, canonical)
	testutil.FailErr(t, "series after late completion", err)
	after, err := store.ListByCanonicalPath(ctx, canonical)
	testutil.FailErr(t, "retained scan history", err)
	if len(rows) != 0 || len(after) != len(before) {
		t.Fatalf("after retirement: series=%d scans=%d, prior scans=%d", len(rows), len(after), len(before))
	}
}
