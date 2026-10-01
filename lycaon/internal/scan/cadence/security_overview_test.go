package cadence

import (
	"context"
	"testing"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSecurityOverviewStatesBaselineThenFullPass(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	dir := t.TempDir()
	seedNonEmptyProject(t, dir)
	testutil.FailErr(t, "attach", cadence.BaselineRoot(context.Background(), dir))

	before, err := cadence.SecurityOverview(context.Background(), dir, map[string]string{"lycaon-sast": "Code analysis"})
	testutil.FailErr(t, "overview before", err)
	if !before.Enabled || before.Baseline == nil || before.Baseline.FileCount != 1 || before.LastFull != nil || before.Running != nil {
		t.Fatalf("overview before a full pass = %+v", before)
	}
	if before.Coverage != api.ScanCoveragePartial {
		t.Fatalf("coverage without a full pass = %q, want partial", before.Coverage)
	}
	if len(before.Scanners) != 3 {
		t.Fatalf("scanners = %+v", before.Scanners)
	}
	for _, scanner := range before.Scanners {
		if !scanner.Available || scanner.LastFullAt != nil || scanner.Running {
			t.Fatalf("scanner state before a pass = %+v", scanner)
		}
		if scanner.ID == "lycaon-sast" && scanner.Label != "Code analysis" {
			t.Fatalf("catalog label not applied: %+v", scanner)
		}
	}

	_, err = cadence.RequestFull(context.Background(), dir, nil, api.ScanTriggerManual, scanbase.FullScanContext{})
	testutil.FailErr(t, "request full", err)
	running, err := cadence.SecurityOverview(context.Background(), dir, nil)
	testutil.FailErr(t, "overview while running", err)
	if running.Running == nil || len(running.Running.Members) != 3 || running.LastFull != nil {
		t.Fatalf("overview while running = %+v", running)
	}
	for _, member := range running.Running.Members {
		if member.Phase != api.FullPassMemberStarted || member.Scan == nil || member.Scan.AssessmentID != running.Running.AssessmentID {
			t.Fatalf("running member = %+v, want a started scan of the pass", member)
		}
	}
	for _, scanner := range running.Scanners {
		if !scanner.Running {
			t.Fatalf("scanner %s not marked running: %+v", scanner.ID, scanner)
		}
	}

	completeCadenceScans(t, cadence, store)
	after, err := cadence.SecurityOverview(context.Background(), dir, nil)
	testutil.FailErr(t, "overview after", err)
	if after.Running != nil || after.LastFull == nil || after.LastFull.CompletedAt == nil || after.LastFull.CoverageStatus != api.ScanCoverageComplete {
		t.Fatalf("overview after a full pass = %+v", after)
	}
	if after.Coverage != api.ScanCoverageComplete {
		t.Fatalf("coverage after a full pass = %q", after.Coverage)
	}
	for _, scanner := range after.Scanners {
		if scanner.LastFullAt == nil || scanner.Running {
			t.Fatalf("scanner state after a pass = %+v", scanner)
		}
	}
}

func TestSecurityOverviewIncludesIncrementalActivity(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	dir := t.TempDir()
	seedNonEmptyProject(t, dir)
	testutil.FailErr(t, "attach", cadence.BaselineRoot(t.Context(), dir))
	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "canonical project", err)
	run := authorityScan("incremental", "assessment", canonical, "snapshot", "lycaon-sast", api.ScanTargetPaths, nil)
	testutil.FailErr(t, "insert incremental scan", store.Insert(t.Context(), run, []string{"a.go"}, ""))
	for _, status := range []string{"pending", "running"} {
		overview, err := cadence.SecurityOverview(t.Context(), dir, nil)
		testutil.FailErr(t, "read incremental overview", err)
		if overview.Running != nil || overview.LastFull != nil {
			t.Fatalf("incremental scan became a full pass: %+v", overview)
		}
		for _, scanner := range overview.Scanners {
			if scanner.Running != (scanner.ID == "lycaon-sast") {
				t.Fatalf("%s scanner activity = %+v", status, scanner)
			}
		}
		if status == "pending" {
			_, err := store.ClaimNext(t.Context())
			testutil.FailErr(t, "claim incremental scan", err)
		}
	}
}

func TestSecurityOverviewInvalidatesCoverageAfterScannerChanges(t *testing.T) {
	for _, change := range []string{"replace", "engine", "add", "remove"} {
		t.Run(change, func(t *testing.T) {
			cadence, store := newTestCadence(t, newTestClock())
			dir := t.TempDir()
			seedNonEmptyProject(t, dir)
			_, err := cadence.RequestFull(t.Context(), dir, nil, api.ScanTriggerManual, scanbase.FullScanContext{})
			testutil.FailErr(t, "request initial full scan", err)
			completeCadenceScans(t, cadence, store)
			registry := cadence.Registry.(*scanbase.MockRegistry)
			switch change {
			case "replace":
				registry.Scanners[0] = &scanbase.MockScanner{IDVal: "replacement", CategoryList: []api.ScanCategory{api.ScanCategorySCA}}
			case "engine":
				registry.Command = []string{"changed-engine"}
			case "add":
				registry.Scanners = append(registry.Scanners, &scanbase.MockScanner{IDVal: "additional", CategoryList: []api.ScanCategory{api.ScanCategorySAST}})
			case "remove":
				registry.Scanners = nil
			}
			overview, err := cadence.SecurityOverview(t.Context(), dir, nil)
			testutil.FailErr(t, "read changed scanner coverage", err)
			want := api.ScanCoveragePartial
			if change == "remove" {
				want = api.ScanCoverageUnavailable
			}
			if overview.Coverage != want || overview.LastFull == nil || overview.LastFull.CoverageStatus != api.ScanCoverageComplete {
				t.Fatalf("current coverage = %s; historical pass = %+v", overview.Coverage, overview.LastFull)
			}
		})
	}
}

func TestOverviewCoverageRequiresEverySelectedExecution(t *testing.T) {
	for _, historical := range []api.ScanCoverageStatus{api.ScanCoverageComplete, api.ScanCoverageBounded, api.ScanCoveragePartial, api.ScanCoverageUnavailable} {
		for _, drift := range []string{"none", "missing", "changed", "unknown", "unavailable"} {
			t.Run(string(historical)+"/"+drift, func(t *testing.T) {
				last := &api.SecurityFullPass{CoverageStatus: historical, Members: []api.SecurityFullPassMember{
					{ScannerID: "a", Scan: &api.CodeScan{Status: api.CodeScanStatusComplete, ExecutionFingerprint: "a-v1"}},
					{ScannerID: "b", Scan: &api.CodeScan{Status: api.CodeScanStatusComplete, ExecutionFingerprint: "b-v1"}},
				}}
				scanners := []api.SecurityScannerState{{ID: "b", Available: true}, {ID: "a", Available: true}}
				executions := map[string]string{"a": "a-v1", "b": "b-v1"}
				switch drift {
				case "missing":
					last.Members = last.Members[:1]
				case "changed":
					executions["b"] = "b-v2"
				case "unknown":
					delete(executions, "b")
				case "unavailable":
					scanners[0].Available = false
				}
				want := historical
				if drift != "none" && historical != api.ScanCoverageUnavailable {
					want = api.ScanCoveragePartial
				}
				if got := overviewCoverage(last, scanners, executions); got != want {
					t.Fatalf("current coverage = %s, want %s", got, want)
				}
				if last.CoverageStatus != historical {
					t.Fatal("current selection rewrote historical coverage")
				}
			})
		}
	}
}
