package execution

import (
	"testing"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDiffFindingsMatchesByIdentityThenByShape(t *testing.T) {
	keep := scanfindings.FixtureFinding("rule-keep", api.FindingLevelHigh, "keep", "a.go", 3)
	movedBefore := scanfindings.FixtureFinding("rule-moved", api.FindingLevelHigh, "moved", "a.go", 10)
	movedAfter := scanfindings.FixtureFinding("rule-moved", api.FindingLevelHigh, "moved", "a.go", 14)
	movedAfter.Message = movedBefore.Message
	gone := scanfindings.FixtureFinding("rule-gone", api.FindingLevelMedium, "gone", "a.go", 20)
	fresh := scanfindings.FixtureFinding("rule-fresh", api.FindingLevelCritical, "fresh", "a.go", 30)

	delta := diffFindings(
		[]api.SecurityFinding{keep, movedBefore, gone},
		[]api.SecurityFinding{keep, movedAfter, fresh},
	)
	if delta.Persisted != 2 {
		t.Fatalf("persisted = %d, want the kept finding and the moved one", delta.Persisted)
	}
	if len(delta.Introduced) != 1 || delta.Introduced[0].RuleID != "rule-fresh" {
		t.Fatalf("introduced = %+v, want only rule-fresh", delta.Introduced)
	}
	if len(delta.Fixed) != 1 || delta.Fixed[0].RuleID != "rule-gone" {
		t.Fatalf("fixed = %+v, want only rule-gone", delta.Fixed)
	}
}

func TestDiffFindingsTreatsAnAbsentBaseAsAllIntroduced(t *testing.T) {
	fresh := scanfindings.FixtureFinding("rule-fresh", api.FindingLevelCritical, "fresh", "a.go", 1)
	delta := diffFindings(nil, []api.SecurityFinding{fresh})
	if len(delta.Introduced) != 1 || len(delta.Fixed) != 0 || delta.Persisted != 0 {
		t.Fatalf("delta = %+v", delta)
	}
}

func TestBaseTargetsNamesChangedAndDeletedFilesOnly(t *testing.T) {
	store := scanbase.NewSQLStore(testdbfixture.Open(t, "store.db"))
	snapshots := testSnapshots(t, store)
	root := t.TempDir()
	for _, rel := range []string{"a.go", "pkg/b.go", "pkg/c.go", "gone.go", "other.go"} {
		writeScanSource(t, root, rel, rel)
	}
	base, err := snapshots.EnsurePath(t.Context(), root, sourcesnapshot.VerifyStat)
	testutil.FailErr(t, "publish base", err)
	targets, err := baseTargets(t.Context(), snapshots, base, []string{"a.go", "pkg"}, []string{"gone.go"})
	testutil.FailErr(t, "base targets", err)
	got := make(map[string]bool, len(targets))
	for _, entry := range targets {
		got[entry.Path] = true
	}
	for _, want := range []string{"a.go", "pkg/b.go", "pkg/c.go", "gone.go"} {
		if !got[want] {
			t.Fatalf("base targets %v miss %s", got, want)
		}
	}
	if len(got) != 4 {
		t.Fatalf("base targets = %v", got)
	}
}

func TestSingleFileRejectsSpanningFindings(t *testing.T) {
	finding := scanfindings.FixtureFinding("rule", api.FindingLevelHigh, "x", "pkg/deep/file.go", 7)
	file, single := singleFile(finding)
	if !single || file != "pkg/deep/file.go" {
		t.Fatalf("singleFile = %q %v", file, single)
	}
	spanning := finding
	spanning.Locations = append(spanning.Locations, api.SecurityFindingLocation{URI: "other.go", StartLine: 1})
	if _, single := singleFile(spanning); single {
		t.Fatal("a finding across files counted as single-file")
	}
}
