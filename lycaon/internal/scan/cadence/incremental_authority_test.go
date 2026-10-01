package cadence

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/sandbox"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestIncrementalFindingSetRequiresExactAncestor(t *testing.T) {
	for _, baseID := range []string{"", "absent", "snapshot-1"} {
		t.Run("ancestor="+baseID, func(t *testing.T) {
			store := authorityTestStore(t)
			root := t.TempDir()
			first := authorityScan("first", "first-assessment", root, "snapshot-1", "sast", api.ScanTargetFull, nil)
			testutil.FailErr(t, "insert ancestor", store.Insert(t.Context(), first, nil, ""))
			original := scanfindings.FixtureFinding("original", api.FindingLevelHigh, "original", "keep.go", 1)
			completeNextScan(t, store, original)
			unrelated := authorityScan("unrelated", "other-assessment", root, "snapshot-other", "sast", api.ScanTargetFull, nil)
			testutil.FailErr(t, "insert unrelated base", store.Insert(t.Context(), unrelated, nil, ""))
			completeNextScan(t, store, scanfindings.FixtureFinding("stale", api.FindingLevelHigh, "stale", "deleted.go", 1))
			changed := authorityScan("changed", "changed-assessment", root, "snapshot-2", "sast", api.ScanTargetPaths, nil)
			testutil.FailErr(t, "insert target", store.Insert(t.Context(), changed, []string{"a.go"}, baseID))
			result := completeNextScan(t, store, scanfindings.FixtureFinding("new", api.FindingLevelHigh, "new", "a.go", 1))
			wantCount, wantCoverage := 1, api.ScanCoveragePartial
			if baseID == "snapshot-1" {
				wantCount, wantCoverage = 2, api.ScanCoverageComplete
			}
			if len(result.Findings) != wantCount || result.CoverageStatus != wantCoverage {
				t.Fatalf("target findings=%#v coverage=%q, want %d/%s", result.Findings, result.CoverageStatus, wantCount, wantCoverage)
			}
			for _, finding := range result.Findings {
				if scanfindings.PrimaryURI(finding) == "deleted.go" {
					t.Fatal("unrelated snapshot finding was republished")
				}
			}
		})
	}
}

func TestCadenceExecutionChangeKeepsDeltasAndReportsStaleAuthority(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	root, err := scanbase.CanonicalPath(t.TempDir())
	testutil.FailErr(t, "canonical root", err)
	seedSizedProject(t, root, 40)
	path := filepath.Join(root, "changed.go")
	testutil.FailErr(t, "write original", os.WriteFile(path, []byte("package main\n"), 0o600))
	testutil.FailErr(t, "attach", cadence.BaselineRoot(t.Context(), root))
	_, err = cadence.RequestFull(t.Context(), root, nil, api.ScanTriggerManual, scanbase.FullScanContext{})
	testutil.FailErr(t, "full pass", err)
	completeCadenceScans(t, cadence, store)

	dispatchChange := func(i int) *api.CodeScan {
		t.Helper()
		testutil.FailErr(t, "change file", os.WriteFile(path, []byte("package main\nvar value = "+strconv.Itoa(i)+"\n"), 0o600))
		testutil.FailErr(t, "note change", noteCadenceWrites(t.Context(), cadence, root, []string{"changed.go"}))
		clock.Advance(time.Minute)
		cadence.Tick(t.Context())
		series, err := store.GetSeries(t.Context(), root, "lycaon-sast")
		testutil.FailErr(t, "read series", err)
		if series == nil || series.ActiveScanID == "" {
			t.Fatal("source change did not schedule a scan")
		}
		active, err := store.Get(t.Context(), series.ActiveScanID)
		testutil.FailErr(t, "read dispatched scan", err)
		if active.TargetKind != api.ScanTargetPaths {
			t.Fatalf("change %d scanned %q, want the delta", i, active.TargetKind)
		}
		completeCadenceScans(t, cadence, store)
		completed, err := store.Get(t.Context(), active.ID)
		testutil.FailErr(t, "read completed generation", err)
		return completed
	}

	// Rule or engine changes invalidate the full pass's coverage.
	registry := cadence.Registry.(*scanbase.MockRegistry)
	registry.Command = []string{"new-engine-or-rules"}
	stale := dispatchChange(1)
	if stale.CoverageStatus != api.ScanCoveragePartial {
		t.Fatalf("delta after an execution change reports %q, want partial", stale.CoverageStatus)
	}
	series, err := store.GetSeries(t.Context(), root, "lycaon-sast")
	testutil.FailErr(t, "read settled series", err)
	if series.LastCoveredExecutionFingerprint != stale.ExecutionFingerprint {
		t.Fatal("settled identity did not advance with the delta")
	}

	// A new full pass restores authority; deltas after it are complete.
	_, err = cadence.RequestFull(t.Context(), root, nil, api.ScanTriggerManual, scanbase.FullScanContext{})
	testutil.FailErr(t, "full pass after change", err)
	completeCadenceScans(t, cadence, store)
	fresh := dispatchChange(2)
	if fresh.CoverageStatus != api.ScanCoverageComplete {
		t.Fatalf("delta after a new full pass reports %q, want complete", fresh.CoverageStatus)
	}
}

func TestCadenceGenerationSelectionIsPerScanner(t *testing.T) {
	clock := newTestClock()
	cadence, _ := newTestCadence(t, clock)
	root, err := scanbase.CanonicalPath(t.TempDir())
	testutil.FailErr(t, "canonical root", err)
	seedNonEmptyProject(t, root)
	snapshot, _, err := cadence.Coordinator.PublishSourceGeneration(t.Context(), root)
	testutil.FailErr(t, "publish snapshot", err)
	for _, scenario := range []struct {
		name, secondSnapshot, secondFingerprint, secondPass string
		secondBaseline                                      bool
	}{
		{name: "unchanged", secondSnapshot: snapshot.ID, secondFingerprint: "current"},
		{name: "execution changed without source change", secondSnapshot: snapshot.ID, secondFingerprint: "previous"},
		{name: "base no longer retained", secondSnapshot: "other-snapshot", secondFingerprint: "current", secondBaseline: true},
		{name: "no base yet", secondSnapshot: "", secondFingerprint: "current", secondBaseline: true},
		{name: "full pass asked for", secondSnapshot: snapshot.ID, secondFingerprint: "current", secondPass: "pass-1"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			rows := []scanbase.SeriesRow{
				{ScannerID: "first", LastCoveredSnapshotID: snapshot.ID, LastCoveredExecutionFingerprint: "current"},
				{ScannerID: "second", LastCoveredSnapshotID: scenario.secondSnapshot, LastCoveredExecutionFingerprint: scenario.secondFingerprint, DispatchPassID: scenario.secondPass},
			}
			executions := map[string]dispatchExecution{"first": {Fingerprint: "current"}, "second": {Fingerprint: "current"}}
			selections, err := cadence.dispatchTargetSelections(t.Context(), rows, snapshot, executions)
			testutil.FailErr(t, "select generation scope", err)
			if first := selections["first"]; first.target != nil || first.baseline {
				t.Fatalf("first scanner already covers this generation, got %#v", first)
			}
			second := selections["second"]
			switch {
			case scenario.secondPass != "":
				if second.target == nil || second.target.Kind != api.ScanTargetFull {
					t.Fatalf("want a full pass for the second scanner, got %#v", second)
				}
			case scenario.secondBaseline:
				if !second.baseline || second.target != nil {
					t.Fatalf("want the second scanner to take the generation as its base, got %#v", second)
				}
			default:
				if second.target != nil || second.baseline {
					t.Fatalf("unchanged source scheduled %#v", second)
				}
			}
			unchanged, baselined, dispatching := splitSelections(rows, selections)
			full := scenario.secondPass != ""
			if len(dispatching) != btoi(full) || len(baselined) != btoi(scenario.secondBaseline) ||
				len(unchanged) != 2-btoi(full)-btoi(scenario.secondBaseline) {
				t.Fatalf("split = %d unchanged, %d baselined, %d dispatching", len(unchanged), len(baselined), len(dispatching))
			}
		})
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestCadenceBoundedGenerationBasesTheNextDelta(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	root, err := scanbase.CanonicalPath(t.TempDir())
	testutil.FailErr(t, "canonical root", err)
	seedSizedProject(t, root, 4)
	testutil.FailErr(t, "mkdir bulk", os.MkdirAll(filepath.Join(root, "bulk"), 0o755))
	for i := range 8 {
		testutil.FailErr(t, "write bulk file", os.WriteFile(filepath.Join(root, "bulk", "f"+strconv.Itoa(i)), []byte("x"), 0o600))
	}
	coord := cadence.Coordinator.(*scanbase.CoordinatorImpl)
	coord.SnapshotStore().SetScopes(boundedScopes{directoryEntries: 4})
	testutil.FailErr(t, "attach", cadence.BaselineRoot(t.Context(), root))
	series, err := store.GetSeries(t.Context(), root, "lycaon-sast")
	testutil.FailErr(t, "read series", err)
	if series == nil || series.LastCoveredSnapshotID == "" {
		t.Fatalf("a bounded generation did not establish the scanner's base: %+v", series)
	}
	full, err := cadence.RequestFull(t.Context(), root, []string{"lycaon-sast"}, api.ScanTriggerManual, scanbase.FullScanContext{})
	testutil.FailErr(t, "full pass", err)
	completeCadenceScans(t, cadence, store)
	completed, err := store.Get(t.Context(), full.ScanIDs()[0])
	testutil.FailErr(t, "read full pass", err)
	if completed.CoverageStatus != api.ScanCoverageBounded {
		t.Fatalf("full pass coverage = %q, want bounded", completed.CoverageStatus)
	}

	path := filepath.Join(root, "changed.go")
	testutil.FailErr(t, "write change", os.WriteFile(path, []byte("package main\nvar value = 1\n"), 0o600))
	testutil.FailErr(t, "note change", noteCadenceWrites(t.Context(), cadence, root, []string{"changed.go"}))
	clock.Advance(time.Minute)
	cadence.Tick(t.Context())
	series, err = store.GetSeries(t.Context(), root, "lycaon-sast")
	testutil.FailErr(t, "read armed series", err)
	if series == nil || series.ActiveScanID == "" {
		t.Fatal("the write did not dispatch a scan")
	}
	active, err := store.Get(t.Context(), series.ActiveScanID)
	testutil.FailErr(t, "read dispatched scan", err)
	if active.TargetKind != api.ScanTargetPaths || len(active.TargetPaths) != 1 {
		t.Fatalf("bounded repo rescanned %q with %d paths, want the one-file delta", active.TargetKind, len(active.TargetPaths))
	}
}

// boundedScopes gives the capture plane a per-directory cap and nothing else.
type boundedScopes struct{ directoryEntries int }

func (b boundedScopes) Capture(_ context.Context, root string) *sourcescope.Scope {
	return sourcescope.New(root, sourcescope.Options{Plane: sourcescope.Plane{
		Budgets: sandbox.SurveyBudgets{DirectoryEntries: b.directoryEntries, SubtreeEntries: 1 << 20, WalkEntries: 1 << 20},
	}})
}
