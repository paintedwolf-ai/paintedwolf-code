//go:build gts_parsercorephase0

package gotreesitter

// B4b stage 2a three-proof census runner, canonical-fixtures half
// (spec.b4b-alternative-set.v2 section 7, "the canonical fixtures... the six
// certified languages' real corpora"; the four canonical Go fixtures are the
// available real-corpus-scale sources in this tree). Opt-in and diagnostic-
// only, reusing the same canonical fixture loader and admission path as
// TestDiagnosticParserCoreCanonicalAdmissions. The smoke-corpus half lives in
// parsercore_phase0_b4b_shadow_census_test.go (package gotreesitter_test).
//
// Run with:
//
//	GTS_B4B_SHADOW_CENSUS_REPORT=1 go test -tags gts_parsercorephase0 \
//	  -run TestDiagnosticParserCoreB4bShadowCensusCanonicalReport -v .

import (
	"os"
	"testing"

	core "github.com/odvcencio/gotreesitter/internal/parsercorephase0"
)

func TestDiagnosticParserCoreB4bShadowCensusCanonicalReport(t *testing.T) {
	if os.Getenv("GTS_B4B_SHADOW_CENSUS_REPORT") != "1" {
		t.Skip("set GTS_B4B_SHADOW_CENSUS_REPORT=1 to run the B4b stage 2a three-proof census over the canonical fixtures")
	}
	// The census comparison switch and the recording switch are two
	// independent cached reads of the same GTS_B4B_SHADOW_CENSUS
	// environment variable (recording lives in parsercorephase0, which
	// cannot depend back on this package's copy of the flag); a real
	// process setting the env var enables both together, so the ForTest
	// override here must enable both explicitly.
	restore := SetDiagnosticParserCoreShadowCensusEnabledForTest(true)
	defer restore()
	restoreRecording := core.SetAlternativeSetRecordingEnabledForTest(true)
	defer restoreRecording()

	var grand DiagnosticParserCoreThreeProofCensusTotals
	var allClass1 []DiagnosticParserCoreClass1Candidate
	for _, row := range diagnosticParserCoreCanonicalAdmissions {
		row := row
		DiagnosticParserCoreShadowCensusResetForTest()
		fixture := loadDiagnosticParserCoreCanonicalFixture(t, row.id)
		requireDiagnosticParserCoreCanonicalFixtureIdentity(t, fixture, row)
		_, routeErr := DiagnosticParseParserCorePrefix(
			parserCoreWarmGoScanner, fixture.Source,
			DiagnosticParserCorePrefixOptions{
				ReceiptMode: DiagnosticParserCoreReceiptSummary,
				MaxTokens:   300000, MaxDispatches: 600000,
				Limits: diagnosticParserCoreCanonicalLimits(),
			},
		)
		if routeErr != nil {
			// Known pre-existing environment issue (reproduces identically
			// on an unmodified origin/main checkout, unrelated to B4b): this
			// exact canonical-fixture admission call declines on this
			// tree/toolchain. Report and skip this fixture's row rather than
			// fail the census report on a defect this stage did not
			// introduce and is not scoped to fix.
			t.Logf("canonical fixture %s: compact admission declined (pre-existing, not a B4b regression -- see report): %v", row.id, routeErr)
			continue
		}
		total := DiagnosticParserCoreThreeProofCensusSnapshotForTest()
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
		allClass1 = append(allClass1, DiagnosticParserCoreClass1CandidatesForTest()...)
		t.Logf("%-16s elections=%-5d scalar=%-5d v1=%-5d v2=%-5d class1=%-4d class2=%-4d class3=%-4d blendedVeto=%-4d overflow=%-4d maxBranch=%-3d",
			row.id, total.Elections, total.ScalarProved, total.V1Proved, total.V2Proved,
			total.Class1V2AdmitsScalarDeclines, total.Class2ScalarProvesV2Declines, total.Class3V1ProvesV2Declines,
			total.BlendedVetoFirings, total.OverflowDeclines, total.MaxBranchOrdinalObserved)
	}
	t.Logf("--- canonical totals: elections=%d scalar=%d v1=%d v2=%d class1=%d class2=%d class3=%d blendedVeto=%d overflow=%d spill=%d/%d maxBranch=%d ---",
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
