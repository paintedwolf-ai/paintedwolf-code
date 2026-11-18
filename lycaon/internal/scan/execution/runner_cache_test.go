package execution

import (
	"github.com/lycaon/lycaon/internal/repochange/repochangetest"

	"github.com/lycaon/lycaon/internal/scan/findings/findingstest"
	"os"
	"path/filepath"
	"testing"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"

	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCurrentScanCacheSurvivesUnavailableBaseBytes(t *testing.T) {
	store := scanbase.NewSQLStore(testdbfixture.Open(t, "cache.db"))
	snapshots := testSnapshots(t, store)
	root := t.TempDir()

	repochangetest.Watch(t, root)
	repochangetest.Acquire(t)
	writeScanSource(t, root, "a.go", "package a\n")
	base, err := snapshots.EnsurePath(t.Context(), root, sourcesnapshot.VerifyContent)
	testutil.FailErr(t, "capture base", err)
	if base.Quality != sourcesnapshot.CaptureExact {
		t.Fatalf("fixture capture quality = %s", base.Quality)
	}
	entries, err := snapshots.EntriesUnder(t.Context(), base.ID, root, ".")
	testutil.FailErr(t, "base entries", err)
	finding := findingstest.Finding("rule", api.FindingLevelHigh, "found", "a.go", 1)
	runner := &Runner{reads: store.Reads, runs: store.Runs, findings: store.Findings, ignores: store.Ignores, database: store.DB(), Snapshots: snapshots}
	result := &scanoutput.Result{ScannedPaths: []string{"a.go"}, Findings: []api.SecurityFinding{finding}}
	runner.cacheCurrentFindings(t.Context(), &api.CodeScan{CanonicalPath: root, SourceSnapshotID: base.ID, SourceCaptureQuality: string(base.Quality), ExecutionFingerprint: "execution"}, scancatalog.ScannerContract{}, result)
	testutil.FailErr(t, "remove old source", os.Remove(filepath.Join(root, "a.go")))
	cached, err := runner.baseFindings(t.Context(), &api.CodeScan{ExecutionFingerprint: "execution"}, entries)
	testutil.FailErr(t, "compare without source bytes or engine", err)
	if len(cached) != 1 || cached[0].Fingerprints.Primary != finding.Fingerprints.Primary {
		t.Fatalf("cached=%+v", cached)
	}
	if result.Findings[0].Locations[0].URI != "a.go" {
		t.Fatal("cache mutated live findings")
	}
	if _, ok, err := store.Findings.BlobFindings(t.Context(), "execution", entries[0].ContentID(), "renamed.go"); err != nil || ok {
		t.Fatalf("path-dependent rules reused across rename: present=%v error=%v", ok, err)
	}
}

func TestFileCacheRequiresCompleteSingleFileCoverage(t *testing.T) {
	store := scanbase.NewSQLStore(testdbfixture.Open(t, "cache.db"))
	runner := &Runner{reads: store.Reads, runs: store.Runs, findings: store.Findings, ignores: store.Ignores, database: store.DB()}
	entries := []sourcesnapshot.Entry{}
	for _, rel := range []string{"clean.go", "unscanned.go", "moved.go", "partial.go", "first.go", "second.go"} {
		entries = append(entries, sourcesnapshot.Entry{Path: rel, SHA256: rel})
	}
	spanning := findingstest.Finding("flow", api.FindingLevelHigh, "flow", "first.go", 1)
	spanning.Locations = append(spanning.Locations, api.SecurityFindingLocation{URI: "second.go", StartLine: 1})
	result := &scanoutput.Result{
		ScannedPaths: []string{"clean.go", "moved.go", "partial.go", "first.go", "second.go"},
		Findings:     []api.SecurityFinding{spanning},
		Warnings:     []api.ScanWarning{{Kind: api.ScanWarningSourceMoved, File: "moved.go"}, {Kind: api.ScanWarningFilePartialParse, File: "partial.go"}},
	}
	runner.cacheFileFindings(t.Context(), "execution", entries, result)
	for _, entry := range entries {
		found, ok, err := store.Findings.BlobFindings(t.Context(), "execution", entry.ContentID(), entry.Path)
		testutil.FailErr(t, "read eligible cache", err)
		if ok != (entry.Path == "clean.go") || len(found) != 0 {
			t.Fatalf("%s present=%v findings=%v", entry.Path, ok, found)
		}
	}
	result.Warnings = append(result.Warnings, api.ScanWarning{Kind: api.ScanWarningRuleParseError})
	if len(fileFindingCache(result)) != 0 {
		t.Fatal("global coverage failure cached as complete")
	}
}
