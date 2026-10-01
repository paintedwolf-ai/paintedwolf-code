package cadence

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCadenceLargeBurstUpgradesToAuthorityRefresh(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	dir := t.TempDir()
	seedSizedProject(t, dir, 40)
	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(context.Background(), dir))
	completeCadenceScans(t, cadence, store)

	var paths []string
	for i := 0; i < 16; i++ {
		name := fmt.Sprintf("extra%02d.go", i)
		testutil.FailErr(t, "write burst file", os.WriteFile(filepath.Join(dir, name), []byte("package p\n"), 0o644))
		paths = append(paths, name)
	}
	testutil.FailErr(t, "NoteWrites", noteCadenceWrites(context.Background(), cadence, dir, paths))
	clock.Advance(5 * time.Second)
	cadence.Tick(context.Background())

	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "CanonicalPath", err)
	scans, err := store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "ListByCanonicalPath", err)
	var refresh int
	for _, sc := range scans {
		if sc.Trigger == api.ScanTriggerAuthorityRefresh {
			refresh++
			// A covered base limits refresh work to the generation diff.
			if sc.TargetKind != api.ScanTargetPaths || len(sc.TargetPaths) != 16 {
				t.Fatalf("refresh scanned %q with %d paths, want the 16-file delta", sc.TargetKind, len(sc.TargetPaths))
			}
		}
		if sc.Trigger == api.ScanTriggerWriteBurst {
			t.Fatalf("large burst carried its paths as desire: %+v", sc)
		}
	}
	if refresh != 3 {
		t.Fatalf("authority_refresh rows = %d want one per scanner", refresh)
	}
}

func TestCadenceOverlayStaysPathScopedWithoutAFullPass(t *testing.T) {
	clock := newTestClock()
	cadence, _ := newTestCadence(t, clock)
	dir := t.TempDir()
	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(context.Background(), dir))
	testutil.FailErr(t, "write landed", os.WriteFile(filepath.Join(dir, "landed.go"), []byte("package main\n"), 0o644))

	plan, err := cadence.PrepareOverlayPromotion(context.Background(), api.WorkerTask{
		ID: "job-1", WorkspacePath: dir, DelegationID: "dep-1", WorkflowRunID: "run-1",
	}, []string{"landed.go"}, nil)
	testutil.FailErr(t, "PrepareOverlayPromotion", err)
	if !plan.Required {
		t.Fatalf("plan not required: %#v", plan)
	}
	if !plan.PathScoped {
		t.Fatal("a landed change scans what landed; a full pass is never implied")
	}
}

func TestCadenceOverlayObligationDropsLandedPathsFromDesire(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	dir := t.TempDir()
	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(context.Background(), dir))
	testutil.FailErr(t, "write landed", os.WriteFile(filepath.Join(dir, "landed.go"), []byte("package main\n"), 0o644))
	testutil.FailErr(t, "NoteWrites landed", noteCadenceWrites(context.Background(), cadence, dir, []string{"landed.go"}))

	plan, err := cadence.PrepareOverlayPromotion(context.Background(), api.WorkerTask{
		ID: "job-1", WorkspacePath: dir, DelegationID: "dep-1", WorkflowRunID: "run-1",
	}, []string{"landed.go"}, nil)
	testutil.FailErr(t, "PrepareOverlayPromotion", err)
	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "CanonicalPath", err)
	testutil.FailErr(t, "insert landing scan", store.Insert(context.Background(), api.CodeScan{
		ID:               plan.ScanID,
		CanonicalPath:    canonical,
		Categories:       []api.ScanCategory{api.ScanCategorySAST},
		ScannerID:        plan.ScannerID,
		Status:           api.CodeScanStatusPending,
		CreatedAt:        time.Now().UTC(),
		DelegationID:     plan.DelegationID,
		SourceSnapshotID: api.SourceSnapshotWarming,
		Trigger:          api.ScanTriggerLandedChange,
	}, nil, ""))
	testutil.FailErr(t, "PublishObligation", cadence.PublishObligation(context.Background(), plan))

	series, err := store.GetSeries(context.Background(), canonical, plan.ScannerID)
	testutil.FailErr(t, "GetSeries", err)
	if series == nil || len(series.DesiredPaths) != 0 || !series.DueAt.IsZero() {
		t.Fatalf("the obligation scans landed.go; the series must not scan it again: %+v", series)
	}
	scans, err := store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "ListByCanonicalPath", err)
	if len(scans) != 1 {
		t.Fatalf("obligation created %d scans, want only the landed-change scan", len(scans))
	}
}

func TestCadenceFloorSizedBurstOnLargeRepoStaysPathScoped(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	dir := t.TempDir()
	seedSizedProject(t, dir, 200)
	testutil.FailErr(t, "mkdir pkg", os.MkdirAll(filepath.Join(dir, "pkg"), 0o755))
	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(context.Background(), dir))
	completeCadenceScans(t, cadence, store)

	var paths []string
	for i := 0; i < 8; i++ {
		name := fmt.Sprintf("pkg/extra%02d.go", i)
		testutil.FailErr(t, "write burst file", os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte("package p\n"), 0o644))
		paths = append(paths, name)
	}
	testutil.FailErr(t, "NoteWrites", noteCadenceWrites(context.Background(), cadence, dir, paths))
	clock.Advance(5 * time.Second)
	cadence.Tick(context.Background())

	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "CanonicalPath", err)
	scans, err := store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "ListByCanonicalPath", err)
	var bursts int
	for _, sc := range scans {
		if sc.Trigger == api.ScanTriggerAuthorityRefresh {
			t.Fatalf("small burst on large repo promoted to authority refresh: %+v", sc)
		}
		if sc.Trigger == api.ScanTriggerWriteBurst {
			bursts++
		}
	}
	if bursts != 3 {
		t.Fatalf("write_burst rows = %d want one per scanner", bursts)
	}
}

func TestDrifted(t *testing.T) {
	cfg := scancfg.CadenceConfig{}
	cases := []struct {
		name     string
		delta    int
		baseline int
		want     bool
	}{
		{"below floor", 7, 10, false},
		{"floor met ratio not met", 8, 100, false},
		{"floor and ratio met", 8, 32, true},
		{"floor met no baseline", 8, 0, true},
		{"zero delta", 0, 0, false},
		{"ratio boundary", 25, 100, true},
		{"just under ratio", 24, 100, false},
	}
	for _, tc := range cases {
		if got := drifted(tc.delta, tc.baseline, cfg); got != tc.want {
			t.Errorf("%s: drifted(%d, %d) = %v want %v", tc.name, tc.delta, tc.baseline, got, tc.want)
		}
	}
}

func TestCadenceNoteTreeDriftArmsRefresh(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	dir := t.TempDir()
	seedNonEmptyProject(t, dir)
	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(context.Background(), dir))
	completeCadenceScans(t, cadence, store)

	seedSizedProject(t, dir, 40)
	testutil.FailErr(t, "NoteTree drift", cadence.NoteTree(context.Background(), dir, 41))
	clock.Advance(45 * time.Second)
	cadence.Tick(context.Background())

	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "CanonicalPath", err)
	scans, err := store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "ListByCanonicalPath", err)
	var refresh int
	for _, sc := range scans {
		if sc.Trigger == api.ScanTriggerAuthorityRefresh {
			refresh++
		}
	}
	if refresh < 3 {
		t.Fatalf("drift refresh rows = %d want >= 3; total=%d", refresh, len(scans))
	}
}
