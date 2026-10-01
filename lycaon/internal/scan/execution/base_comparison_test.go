package execution

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBaseComparisonAvailabilityPreservesCurrentFindings(t *testing.T) {
	for _, scenario := range []string{"missing content", "cached clean", "partial scan", "complete scan", "missing snapshot", "current warning"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			store := scanbase.NewSQLStore(testdbfixture.Open(t, "scan.db"))
			snapshots := testSnapshots(t, store)
			root := t.TempDir()
			for _, path := range []string{"a.go", "b.go"} {
				writeScanSource(t, root, path, "package before\n")
			}
			base, err := snapshots.EnsurePath(ctx, root, sourcesnapshot.VerifyContent)
			testutil.FailErr(t, "capture base", err)
			entries, err := snapshots.EntriesUnder(ctx, base.ID, root, ".")
			testutil.FailErr(t, "read base", err)
			scanner := &scanbase.MockScanner{Result: &scanoutput.Result{ScannedPaths: []string{"a.go", "b.go"}}}
			runner := &Runner{Store: store, Snapshots: snapshots, Registry: &scanbase.MockRegistry{Scanner: scanner}, DataDir: t.TempDir()}
			if scenario == "cached clean" {
				runner.cacheFileFindings(ctx, "execution", entries, scanner.Result)
			}
			if scenario == "missing content" || scenario == "cached clean" {
				for _, path := range []string{"a.go", "b.go"} {
					writeScanSource(t, root, path, "package after\n")
				}
			}
			if scenario == "partial scan" {
				scanner.Result.ScannedPaths = []string{"a.go"}
			}
			baseID := base.ID
			if scenario == "missing snapshot" {
				baseID = "missing"
			}
			testutil.FailErr(t, "insert scan", store.Insert(ctx, api.CodeScan{
				ID: "comparison", CanonicalPath: root, Status: api.CodeScanStatusPending,
				ScannerID: scanner.ID(), SourceSnapshotID: base.ID, ExecutionFingerprint: "execution",
				TargetKind: api.ScanTargetPaths,
			}, []string{"a.go", "b.go"}, baseID))
			job, err := store.ClaimNext(ctx)
			testutil.FailErr(t, "claim scan", err)
			finding := scanfindings.FixtureFinding("rule", api.FindingLevelHigh, "current finding", "a.go", 1)
			current := &scanoutput.Result{ScannedPaths: []string{"a.go", "b.go"}, Findings: []api.SecurityFinding{finding}}
			if scenario == "current warning" {
				current.Warnings = []api.ScanWarning{{Kind: api.ScanWarningFilePartialParse, File: "a.go"}}
			}
			before, err := json.Marshal(current)
			testutil.FailErr(t, "encode current result", err)
			delta := runner.compareWithBase(ctx, job, scancatalog.ScannerContract{}, current)
			stored, err := store.Get(ctx, job.ID)
			testutil.FailErr(t, "read comparison", err)
			if stored.Delta == nil {
				t.Fatal("comparison was not recorded")
			}
			got := stored.Delta
			if scenario == "cached clean" || scenario == "complete scan" {
				if delta == nil || got.Status != "complete" || got.Counts == nil || got.Counts.Introduced != 1 {
					t.Fatalf("complete comparison=%+v", got)
				}
			} else {
				reasons := map[string]string{"missing content": "base_content_unavailable", "partial scan": "base_scan_incomplete", "missing snapshot": "base_snapshot_missing", "current warning": "current_scan_incomplete"}
				if delta != nil || got.Status != "unavailable" || got.UnavailableReason != reasons[scenario] || got.Counts != nil {
					t.Fatalf("unavailable comparison=%+v", got)
				}
				encoded, err := json.Marshal(got)
				testutil.FailErr(t, "encode delta", err)
				if strings.Contains(string(encoded), "counts") {
					t.Fatalf("unavailable comparison claimed counts: %s", encoded)
				}
				if scenario == "missing content" && !reflect.DeepEqual(got.UnavailablePaths, []string{"a.go", "b.go"}) {
					t.Fatalf("missing paths=%v", got.UnavailablePaths)
				}
			}
			after, err := json.Marshal(current)
			testutil.FailErr(t, "encode unchanged current result", err)
			if string(before) != string(after) {
				t.Fatal("historical comparison changed current coverage or findings")
			}
			staged, err := filepath.Glob(filepath.Join(runner.hostDataDir(ctx, job), scanbase.ScanResultSpillDir, job.ID, "base-*"))
			testutil.FailErr(t, "read staged directories", err)
			if len(staged) != 0 {
				t.Fatalf("staged base leaked: %v", staged)
			}
		})
	}
}

func TestBaseStagingCancellationDoesNotBecomeUnavailableHistory(t *testing.T) {
	store := scanbase.NewSQLStore(testdbfixture.Open(t, "scan.db"))
	snapshots := testSnapshots(t, store)
	root := t.TempDir()
	writeScanSource(t, root, "a.go", "package a\n")
	base, err := snapshots.EnsurePath(t.Context(), root, sourcesnapshot.VerifyContent)
	testutil.FailErr(t, "capture base", err)
	entries, err := snapshots.EntriesUnder(t.Context(), base.ID, root, ".")
	testutil.FailErr(t, "read base", err)
	runner := &Runner{Store: store, Snapshots: snapshots, DataDir: t.TempDir()}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	staged, err := runner.stageBaseVersions(ctx, &api.CodeScan{ID: "canceled", CanonicalPath: root}, entries)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled staging error=%v", err)
	}
	if staged != "" {
		if _, err := os.Stat(staged); !os.IsNotExist(err) {
			t.Fatalf("canceled staging remains: %v", err)
		}
	}
}
