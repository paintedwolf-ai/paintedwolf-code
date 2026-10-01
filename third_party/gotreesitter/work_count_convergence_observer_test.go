//go:build gts_workcount

package gotreesitter_test

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/odvcencio/gotreesitter/internal/benchfixtures"
)

func TestDiagnosticWorkCountConvergenceDoesNotChangeCanonicalTree(t *testing.T) {
	fixtures, err := benchfixtures.LoadGoFullParseFixtures()
	if err != nil {
		t.Fatal(err)
	}
	fixture := fixtures[0]
	if fixture.Fixture.ID != "query_compile" {
		t.Fatalf("first canonical fixture=%q want query_compile", fixture.Fixture.ID)
	}
	lang := grammars.GoLanguage()
	parser := gotreesitter.NewParser(lang)
	baseline, err := parser.Parse(fixture.Source)
	if err != nil {
		t.Fatal(err)
	}
	defer baseline.Release()
	baselineInspection, err := benchfixtures.InspectGoTree(baseline.RootNode(), lang)
	if err != nil {
		t.Fatal(err)
	}

	gotreesitter.BeginDiagnosticWorkCount()
	observed, err := parser.Parse(fixture.Source)
	counts := gotreesitter.EndDiagnosticWorkCount()
	if err != nil {
		if observed != nil {
			observed.Release()
		}
		t.Fatal(err)
	}
	defer observed.Release()
	observedInspection, err := benchfixtures.InspectGoTree(observed.RootNode(), lang)
	if err != nil {
		t.Fatal(err)
	}
	if baselineInspection.SHA256 != fixture.Fixture.DeepTreeSHA256 || observedInspection.SHA256 != baselineInspection.SHA256 {
		t.Fatalf("deep digest baseline=%s observed=%s frozen=%s", baselineInspection.SHA256, observedInspection.SHA256, fixture.Fixture.DeepTreeSHA256)
	}
	t.Logf("deep_tree_receipt fixture=query_compile format=%s sha256=%s", benchfixtures.DeepTreeDigestVersion, observedInspection.SHA256)
	if counts.Convergence.EventsSeen == 0 || counts.Convergence.VocabularyViolation || counts.Convergence.ArithmeticOverflow {
		t.Fatalf("convergence ledger events=%d vocabulary=%v overflow=%v", counts.Convergence.EventsSeen, counts.Convergence.VocabularyViolation, counts.Convergence.ArithmeticOverflow)
	}
	logWorkCountConvergenceReceipt(t, "query_compile", counts)
}

func logWorkCountConvergenceReceipt(t *testing.T, label string, counts gotreesitter.DiagnosticWorkCount) {
	t.Helper()
	c := counts.Convergence
	t.Logf(
		"convergence_receipt fixture=%s attempts=%d events_seen=%d events_retained=%d truncated=%v first_rejects=%d phases=%+v outcomes=%+v reasons=%+v cap_hits=%d snapshot_capped=%v snapshot_unknown=%v path_work=%d/%d path_exhausted=%v",
		label,
		len(counts.Attempts),
		c.EventsSeen,
		len(c.Events),
		c.EventTruncated,
		len(c.FirstRejects),
		c.Aggregates.Phases,
		c.Aggregates.Outcomes,
		c.Aggregates.Rejections,
		c.Aggregates.CapHits,
		c.SnapshotCountCapped,
		c.SnapshotCountUnknown,
		c.PathWalkWorkUsed,
		c.PathWalkWorkBudget,
		c.PathWalkWorkExhausted,
	)
}
