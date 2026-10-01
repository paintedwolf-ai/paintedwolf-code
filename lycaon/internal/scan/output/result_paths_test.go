package output_test

import (
	"path/filepath"
	"testing"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestNormalizeResultPathsMakesSnapshotFindingsStable(t *testing.T) {
	rootA := filepath.Join(t.TempDir(), "snapshot-a")
	rootB := filepath.Join(t.TempDir(), "snapshot-b")
	resultA := &scanoutput.Result{Findings: []api.SecurityFinding{
		scanfindings.FixtureFinding("rule-1", api.FindingLevelHigh, "finding", filepath.Join(rootA, "src", "main.go"), 7),
	}}
	resultB := &scanoutput.Result{Findings: []api.SecurityFinding{
		scanfindings.FixtureFinding("rule-1", api.FindingLevelHigh, "finding", filepath.Join(rootB, "src", "main.go"), 7),
	}}

	scanoutput.NormalizeResultPaths(resultA, rootA)
	scanoutput.NormalizeResultPaths(resultB, rootB)
	gotA := resultA.Findings[0]
	gotB := resultB.Findings[0]
	if gotA.Locations[0].URI != "src/main.go" || gotB.Locations[0].URI != "src/main.go" {
		t.Fatalf("normalized locations = %q, %q", gotA.Locations[0].URI, gotB.Locations[0].URI)
	}
	if gotA.Fingerprints.Primary != gotB.Fingerprints.Primary {
		t.Fatalf("snapshot identity leaked into fingerprint: %q != %q", gotA.Fingerprints.Primary, gotB.Fingerprints.Primary)
	}
}

func TestNormalizeResultPathsNormalizesWarningsButPreservesExternalAbsolutePath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "snapshot")
	external := filepath.Join(t.TempDir(), "external.go")
	result := &scanoutput.Result{
		Findings: []api.SecurityFinding{
			scanfindings.FixtureFinding("rule-1", api.FindingLevelHigh, "finding", external, 1),
		},
		Warnings: []api.ScanWarning{{File: filepath.Join(root, "pkg", "warning.go")}},
	}

	scanoutput.NormalizeResultPaths(result, root)
	if result.Findings[0].Locations[0].URI != filepath.ToSlash(external) {
		t.Fatalf("external location = %q", result.Findings[0].Locations[0].URI)
	}
	if result.Warnings[0].File != "pkg/warning.go" {
		t.Fatalf("warning file = %q", result.Warnings[0].File)
	}
}

func TestNormalizeResultPathsHandlesFileURIAndPreservesNonFileURI(t *testing.T) {
	root := filepath.Join(t.TempDir(), "snapshot")
	result := &scanoutput.Result{Findings: []api.SecurityFinding{
		scanfindings.FixtureFinding("file-rule", api.FindingLevelHigh, "file", "file://"+filepath.ToSlash(filepath.Join(root, "src", "main.go")), 1),
		scanfindings.FixtureFinding("web-rule", api.FindingLevelLow, "web", "https://scanner.example/rules/1", 1),
	}}

	scanoutput.NormalizeResultPaths(result, root)
	if result.Findings[0].Locations[0].URI != "src/main.go" {
		t.Fatalf("file URI = %q", result.Findings[0].Locations[0].URI)
	}
	if result.Findings[1].Locations[0].URI != "https://scanner.example/rules/1" {
		t.Fatalf("non-file URI = %q", result.Findings[1].Locations[0].URI)
	}
}
