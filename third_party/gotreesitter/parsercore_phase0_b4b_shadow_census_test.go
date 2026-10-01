//go:build gts_parsercorephase0

package gotreesitter_test

// B4b stage 2a three-proof census runner, smoke-corpus half (spec.b4b-
// alternative-set.v2 section 7). Opt-in and diagnostic-only, mirroring
// TestAdmissionCandidateScorecard206's own gating: it drives the compact
// candidate route across every registered language's smoke fixture,
// resetting the census (parsercore_phase0_alternative_set_census.go) around
// each source so the per-language scalar/v1/v2 and class1/2/3/4 counts can be
// attributed and reported. The four-canonical-fixture and six-certified-
// language halves live in
// parsercore_phase0_b4b_shadow_census_internal_test.go (package
// gotreesitter), which reuses the canonical fixture loader already wired for
// TestDiagnosticParserCoreCanonicalAdmissions.
//
// Run with:
//
//	GTS_B4B_SHADOW_CENSUS_REPORT=1 go test -tags gts_parsercorephase0 \
//	  -run TestDiagnosticParserCoreB4bShadowCensusSmokeCorpusReport -v .
//
// Gate: the stage 2b flip requires zero class-1 (v2-admits-where-scalar-
// declines) elections whose compact tree diverges from the C-oracle-
// adjudicated production route (campaign amendment 2026-08-02). This report
// tallies and logs class 1 candidates per language; it does not itself run
// the tree/C-oracle differential (parsercore_phase0_alternative_set_v2_class1_differential_test.go
// and the cgo_harness C-oracle adjudication cover that). A non-zero class-1
// count here is expected and desired (spec section 7: "the class the stage 1
// census structurally missed"), so it is reported, not failed.
//
// See also parsercore_phase0_b4b_shadow_census_internal_test.go for the
// v2-opt-in-decider corpus-wide differential (every smoke source re-parsed
// with v2 substituted for the scalar decider, tree digest compared against
// production).

import (
	"os"
	"sort"
	"testing"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	core "github.com/odvcencio/gotreesitter/internal/parsercorephase0"
)

type b4bThreeProofCensusRow struct {
	name   string
	totals gts.DiagnosticParserCoreThreeProofCensusTotals
}

func TestDiagnosticParserCoreB4bShadowCensusSmokeCorpusReport(t *testing.T) {
	if os.Getenv("GTS_B4B_SHADOW_CENSUS_REPORT") != "1" {
		t.Skip("set GTS_B4B_SHADOW_CENSUS_REPORT=1 to run the B4b stage 2a three-proof census over the smoke corpus")
	}
	// The census comparison switch and the recording switch are independent
	// cached reads of the same environment variable, so the ForTest override
	// must enable both.
	restore := gts.SetDiagnosticParserCoreShadowCensusEnabledForTest(true)
	defer restore()
	restoreRecording := core.SetAlternativeSetRecordingEnabledForTest(true)
	defer restoreRecording()
	t.Cleanup(func() { grammars.PurgeEmbeddedLanguageCache() })

	var rows []b4bThreeProofCensusRow
	var allClass1 []gts.DiagnosticParserCoreClass1Candidate

	entries := grammars.AllLanguages()
	for _, entry := range entries {
		gts.DiagnosticParserCoreShadowCensusResetForTest()
		func() {
			defer func() { recover() }() // a candidate parse failure/panic must not lose the census so far
			runB4bShadowCensusLanguageSmoke(entry)
		}()
		totals := gts.DiagnosticParserCoreThreeProofCensusSnapshotForTest()
		rows = append(rows, b4bThreeProofCensusRow{name: entry.Name, totals: totals})
		allClass1 = append(allClass1, gts.DiagnosticParserCoreClass1CandidatesForTest()...)
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })

	var grand gts.DiagnosticParserCoreThreeProofCensusTotals
	t.Logf("=== B4b stage 2a three-proof census: smoke corpus (%d languages) ===", len(rows))
	for _, row := range rows {
		total := row.totals
		grand.Elections += total.Elections
		grand.ScalarProved += total.ScalarProved
		grand.V1Proved += total.V1Proved
		grand.V2Proved += total.V2Proved
		grand.Class1V2AdmitsScalarDeclines += total.Class1V2AdmitsScalarDeclines
		grand.Class2ScalarProvesV2Declines += total.Class2ScalarProvesV2Declines
		grand.Class3V1ProvesV2Declines += total.Class3V1ProvesV2Declines
		grand.BlendedVetoFirings += total.BlendedVetoFirings
		grand.OverflowDeclines += total.OverflowDeclines
		grand.SpillElections += total.SpillElections
		grand.SpillObserved += total.SpillObserved
		if total.MaxBranchOrdinalObserved > grand.MaxBranchOrdinalObserved {
			grand.MaxBranchOrdinalObserved = total.MaxBranchOrdinalObserved
		}
		if total.Elections == 0 {
			continue // no converged-split drop elections observed for this source
		}
		t.Logf("%-20s elections=%-5d scalar=%-5d v1=%-5d v2=%-5d class1=%-4d class2=%-4d class3=%-4d blendedVeto=%-4d overflow=%-4d maxBranch=%-3d",
			row.name, total.Elections, total.ScalarProved, total.V1Proved, total.V2Proved,
			total.Class1V2AdmitsScalarDeclines, total.Class2ScalarProvesV2Declines, total.Class3V1ProvesV2Declines,
			total.BlendedVetoFirings, total.OverflowDeclines, total.MaxBranchOrdinalObserved)
	}
	t.Logf("--- totals: elections=%d scalar=%d v1=%d v2=%d class1=%d class2=%d class3=%d blendedVeto=%d overflow=%d spill=%d/%d maxBranch=%d ---",
		grand.Elections, grand.ScalarProved, grand.V1Proved, grand.V2Proved,
		grand.Class1V2AdmitsScalarDeclines, grand.Class2ScalarProvesV2Declines, grand.Class3V1ProvesV2Declines,
		grand.BlendedVetoFirings, grand.OverflowDeclines, grand.SpillObserved, grand.SpillElections, grand.MaxBranchOrdinalObserved)

	if len(allClass1) > 0 {
		t.Logf("class-1 (v2-admits-where-scalar-declines) candidates: %d -- see the class-1 differential harness for the tree/C-oracle adjudication gate", len(allClass1))
		for index, candidate := range allClass1 {
			t.Logf("CLASS1[%d]: %s", index, candidate.Detail)
		}
	}
}

func runB4bShadowCensusLanguageSmoke(entry grammars.LangEntry) {
	lang := entry.Language()
	if lang == nil {
		return
	}
	support := grammars.EvaluateParseSupport(entry, lang)
	if support.Backend != grammars.ParseBackendDFA {
		return
	}
	source := []byte(grammars.ParseSmokeSample(entry.Name))
	candidate := gts.NewParser(lang)
	candidate.SetAdmissionCandidateRoute(true)
	tree, err := candidate.Parse(source)
	if err != nil || tree == nil {
		return
	}
	tree.Release()
}
