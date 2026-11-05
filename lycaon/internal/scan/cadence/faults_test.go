package cadence

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

const testDigestHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func brokenBundledContract(scannerID string) scancatalog.ScannerContract {
	return scancatalog.ScannerContract{
		ScannerID:             scannerID,
		Engine:                "opengrep",
		Driver:                scancatalog.DriverBundled,
		Scope:                 scancatalog.ScopeSourceHostFloor,
		DefinitionFingerprint: testDigestHex,
		RulesSHA256:           testDigestHex,
		ExclusionsSHA256:      testDigestHex,
		EngineSHA256:          "",
	}
}

func healthyBundledContract(scannerID string) scancatalog.ScannerContract {
	contract := brokenBundledContract(scannerID)
	contract.EngineSHA256 = testDigestHex
	return contract
}

func TestCadenceConfigFaultMarksSeriesUnavailableWithoutRequeueingAndClearsOnEngineChange(t *testing.T) {
	ctx := t.Context()
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	registry := cadence.Registry.(*scanbase.MockRegistry)
	registry.Contracts = map[string]scancatalog.ScannerContract{
		"lycaon-sast": brokenBundledContract("lycaon-sast"),
	}

	dir := t.TempDir()
	seedNonEmptyProject(t, dir)
	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "canonical path", err)

	testutil.FailErr(t, "BaselineRoot with broken scanner", cadence.BaselineRoot(ctx, dir))

	sastRow, err := store.GetSeries(ctx, canonical, "lycaon-sast")
	testutil.FailErr(t, "GetSeries lycaon-sast", err)
	if sastRow == nil || !sastRow.Unavailable() {
		t.Fatalf("lycaon-sast series = %+v, want marked unavailable", sastRow)
	}
	if sastRow.UnavailableReason != scancatalog.ConfigFaultEngineIdentityMissing {
		t.Fatalf("unavailable reason = %q, want %q", sastRow.UnavailableReason, scancatalog.ConfigFaultEngineIdentityMissing)
	}
	if !sastRow.DueAt.IsZero() || sastRow.ConsecutiveFailures != 0 {
		t.Fatalf("unavailable series armed or counted transient failure: %+v", sastRow)
	}

	overview, err := cadence.SecurityOverview(ctx, dir, nil)
	testutil.FailErr(t, "SecurityOverview while unavailable", err)
	for _, scanner := range overview.Scanners {
		if scanner.ID == "lycaon-sast" {
			if scanner.Available || scanner.Unavailable != "scanner lycaon-sast has no engine byte identity" {
				t.Fatalf("lycaon-sast overview state = %+v, want unavailable with engine identity reason", scanner)
			}
		} else if !scanner.Available {
			t.Fatalf("healthy scanner %s marked unavailable: %+v", scanner.ID, scanner)
		}
	}

	// Subsequent writes and ticks must not re-arm or requeue the unavailable scanner.
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(dir, "changed.go"), []byte("package main\n"), 0o644))
	testutil.FailErr(t, "NoteWrites", noteCadenceWrites(ctx, cadence, dir, []string{"changed.go"}))
	for range 4 {
		clock.Advance(5 * time.Second)
		cadence.Tick(ctx)
	}
	scans, err := store.ListByCanonicalPath(ctx, canonical)
	testutil.FailErr(t, "ListByCanonicalPath while sast unavailable", err)
	if len(scans) != 2 {
		t.Fatalf("scans len = %d, want 2 (only healthy scanners)", len(scans))
	}
	completeCadenceScans(t, cadence, store)

	sastRow, err = store.GetSeries(ctx, canonical, "lycaon-sast")
	testutil.FailErr(t, "GetSeries lycaon-sast after ticks", err)
	if !sastRow.Unavailable() || !sastRow.DueAt.IsZero() {
		t.Fatalf("unavailable scanner re-armed during writes/ticks: %+v", sastRow)
	}

	// Restoring engine byte identity clears the fault in SecurityOverview and re-enables scheduling.
	registry.Contracts["lycaon-sast"] = healthyBundledContract("lycaon-sast")
	overviewAfterFix, err := cadence.SecurityOverview(ctx, dir, nil)
	testutil.FailErr(t, "SecurityOverview after engine fix", err)
	for _, scanner := range overviewAfterFix.Scanners {
		if !scanner.Available || scanner.Unavailable != "" {
			t.Fatalf("scanner %s after engine fix = %+v, want available", scanner.ID, scanner)
		}
	}

	cadence.Tick(ctx)
	sastRow, err = store.GetSeries(ctx, canonical, "lycaon-sast")
	testutil.FailErr(t, "GetSeries lycaon-sast after recovery tick", err)
	if sastRow.Unavailable() || sastRow.LastCoveredSnapshotID == "" {
		t.Fatalf("lycaon-sast did not recover baseline after engine fix: %+v", sastRow)
	}
}

func TestCadenceIncrementalConfigFaultPreservesPendingPathsUntilEngineRecovers(t *testing.T) {
	ctx := t.Context()
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	registry := cadence.Registry.(*scanbase.MockRegistry)
	registry.Contracts = map[string]scancatalog.ScannerContract{
		"lycaon-sast": healthyBundledContract("lycaon-sast"),
	}

	dir := t.TempDir()
	seedNonEmptyProject(t, dir)
	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "canonical path", err)
	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(ctx, dir))

	// Break lycaon-sast before incremental writes.
	registry.Contracts["lycaon-sast"] = brokenBundledContract("lycaon-sast")
	testutil.FailErr(t, "write first.go", os.WriteFile(filepath.Join(dir, "first.go"), []byte("package main\n"), 0o644))
	testutil.FailErr(t, "NoteWrites first.go", noteCadenceWrites(ctx, cadence, dir, []string{"first.go"}))

	clock.Advance(5 * time.Second)
	cadence.Tick(ctx)
	completeCadenceScans(t, cadence, store)

	// Write another file while lycaon-sast is unavailable and verify it does not spin.
	testutil.FailErr(t, "write second.go", os.WriteFile(filepath.Join(dir, "second.go"), []byte("package main\n"), 0o644))
	testutil.FailErr(t, "NoteWrites second.go", noteCadenceWrites(ctx, cadence, dir, []string{"second.go"}))
	for range 3 {
		clock.Advance(5 * time.Second)
		cadence.Tick(ctx)
	}
	completeCadenceScans(t, cadence, store)

	scansBeforeFix, err := store.ListByCanonicalPath(ctx, canonical)
	testutil.FailErr(t, "ListByCanonicalPath before fix", err)
	for _, scan := range scansBeforeFix {
		if scan.ScannerID == "lycaon-sast" {
			t.Fatalf("unavailable lycaon-sast enqueued scan: %+v", scan)
		}
	}

	// Restore engine identity and verify a single tick dispatches the delta for both files.
	registry.Contracts["lycaon-sast"] = healthyBundledContract("lycaon-sast")
	cadence.Tick(ctx)

	scansAfterFix, err := store.ListByCanonicalPath(ctx, canonical)
	testutil.FailErr(t, "ListByCanonicalPath after fix", err)
	var sastScan *api.CodeScan
	for i := range scansAfterFix {
		if scansAfterFix[i].ScannerID == "lycaon-sast" {
			sastScan = &scansAfterFix[i]
			break
		}
	}
	if sastScan == nil {
		t.Fatal("expected lycaon-sast scan after engine recovery")
	}
	if !slices.Equal(sastScan.TargetPaths, []string{"first.go", "second.go"}) {
		t.Fatalf("recovered lycaon-sast target paths = %v, want [first.go second.go]", sastScan.TargetPaths)
	}
}

func TestCadenceTransientFailureAppliesBoundedExponentialBackoffAndResetsOnSuccess(t *testing.T) {
	ctx := t.Context()
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	dir := t.TempDir()
	seedNonEmptyProject(t, dir)
	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "canonical path", err)
	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(ctx, dir))

	// Fail 7 times across 3 scanners = 21 Enqueue calls, then succeed.
	cadence.Coordinator = &failingCoordinator{ScanCoordinator: cadence.Coordinator, failures: 21}

	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n// v2\n"), 0o644))
	testutil.FailErr(t, "NoteWrites", noteCadenceWrites(ctx, cadence, dir, []string{"main.go"}))

	// Initial settle is 4s.
	clock.Advance(4 * time.Second)
	wantDelays := []time.Duration{
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		32 * time.Second,
		64 * time.Second,
		120 * time.Second,
		120 * time.Second,
	}

	for i, wantDelay := range wantDelays {
		attempt := i + 1
		attemptAt := clock.Now()
		cadence.Tick(ctx)

		row, err := store.GetSeries(ctx, canonical, "lycaon-sast")
		testutil.FailErr(t, "GetSeries after transient failure", err)
		if row.ConsecutiveFailures != attempt {
			t.Fatalf("attempt %d: ConsecutiveFailures = %d, want %d", attempt, row.ConsecutiveFailures, attempt)
		}
		wantDue := attemptAt.Add(wantDelay)
		if !row.DueAt.Equal(wantDue) {
			t.Fatalf("attempt %d: DueAt = %v, want %v (delay %v)", attempt, row.DueAt, wantDue, wantDelay)
		}

		// A write during backoff must not pull DueAt earlier than the backoff deadline.
		testutil.FailErr(t, "NoteWrites during backoff", noteCadenceWrites(ctx, cadence, dir, []string{"main.go"}))
		rowAfterWrite, err := store.GetSeries(ctx, canonical, "lycaon-sast")
		testutil.FailErr(t, "GetSeries after write during backoff", err)
		if !rowAfterWrite.DueAt.Equal(wantDue) {
			t.Fatalf("attempt %d: write during backoff pulled DueAt to %v, want %v", attempt, rowAfterWrite.DueAt, wantDue)
		}

		// Advancing less than the backoff interval must not trigger another attempt.
		if wantDelay > 4*time.Second {
			clock.Advance(4 * time.Second)
			cadence.Tick(ctx)
			checkRow, err := store.GetSeries(ctx, canonical, "lycaon-sast")
			testutil.FailErr(t, "GetSeries after flat settle advance", err)
			if checkRow.ConsecutiveFailures != attempt {
				t.Fatalf("attempt %d: retried after flat settle interval (ConsecutiveFailures=%d)", attempt, checkRow.ConsecutiveFailures)
			}
			clock.Advance(wantDelay - 4*time.Second)
		} else {
			clock.Advance(wantDelay)
		}
	}

	// Next tick succeeds for all 3 scanners and resets ConsecutiveFailures to 0.
	cadence.Tick(ctx)
	scans, err := store.ListByCanonicalPath(ctx, canonical)
	testutil.FailErr(t, "ListByCanonicalPath after recovery", err)
	if len(scans) != 3 {
		t.Fatalf("scans after transient recovery = %d, want 3", len(scans))
	}
	series, err := store.ListSeriesForPath(ctx, canonical)
	testutil.FailErr(t, "ListSeriesForPath after recovery", err)
	for _, row := range series {
		if row.ConsecutiveFailures != 0 {
			t.Fatalf("scanner %s ConsecutiveFailures = %d after success, want 0", row.ScannerID, row.ConsecutiveFailures)
		}
	}
}
