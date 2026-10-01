//go:build gts_parsercorephase0

package gotreesitter_test

import (
	"testing"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// This file is gts_parsercorephase0-tagged (positive opt-in), matching
// admission_route_equality_swift_residual_test.go's own reasoning: loading
// the swift grammar retains memory no always-on (untagged) test should pay
// for, so this pin stays out of the default suite.

// TestCompactSchedulerDeclinesContextualCloseAngleDeferralWithDistinctDetail
// pins finding F2: the shared token source splits Swift's ">>" into two
// ">" tokens (issue #983); the generic scheduler's dispatch pass defers the
// narrower one whenever the elected header's own lex mode reads the wider
// ">>" with a real action. That deferral must not be reported as a
// genuinely empty action row (diagnosticParserCoreNoTableActionDetail):
// the row was never empty, production simply must not act on it this pass.
// The decline still classifies as the typed recovery boundary (routing to
// production is unchanged), but with a distinct, accurate detail.
//
// Both inputs are the two Swift witnesses already committed to
// FuzzAdmissionRouteEquality's seed corpus (fuzz_admission_route_equality_test.go,
// issue #983 pin) -- this test additionally proves those two witnesses
// decline for the deferral-specific reason, not the empty-row one, which
// the public Parse API's route-equality assertion alone cannot observe.
func TestCompactSchedulerDeclinesContextualCloseAngleDeferralWithDistinctDetail(t *testing.T) {
	t.Cleanup(func() { grammars.PurgeEmbeddedLanguageCache() })
	entry := grammars.DetectLanguageByName("swift")
	if entry == nil {
		t.Fatal("swift is absent from the language registry")
	}
	lang := entry.Language()

	wantDetail := gts.DiagnosticParserCoreContextualCloseAngleDeferralDetailForTest()

	for _, src := range [][]byte{[]byte("0>>"), []byte(" 0%0>>")} {
		src := src
		t.Run(string(src), func(t *testing.T) {
			receipt, err := gts.RunStateDependentRelexSchedulerForTest(lang, src)
			if err != nil {
				t.Fatalf("compact scheduler: %v", err)
			}
			if receipt.Acceptance != nil {
				t.Fatalf("compact scheduler unexpectedly accepted %q; the deferral shape did not trigger", src)
			}
			if receipt.Stop.Boundary != gts.DiagnosticParserCoreRecovery {
				t.Fatalf("stop boundary = %q, want %q (routing must stay unchanged)",
					receipt.Stop.Boundary, gts.DiagnosticParserCoreRecovery)
			}
			if receipt.Stop.Detail != wantDetail {
				t.Fatalf("stop detail = %q, want %q (not the empty-row detail)", receipt.Stop.Detail, wantDetail)
			}
		})
	}
}

// TestAdmissionRouteEqualitySwiftCloseAngleDeferralWitnessesStillFallBack
// asserts no divergence for the same two seed-corpus witnesses through the
// shipped public Parse route: the compact candidate must decline (an
// engine-side fallback, never routed), so Parse already served the
// production tree within the same call and the two routes cannot diverge.
// This is the same invariant FuzzAdmissionRouteEquality's seed corpus
// checks on every run; this test pins it directly for the two named
// witnesses, independent of the fuzz corpus staying intact.
func TestAdmissionRouteEqualitySwiftCloseAngleDeferralWitnessesStillFallBack(t *testing.T) {
	t.Cleanup(func() { grammars.PurgeEmbeddedLanguageCache() })
	entry := grammars.DetectLanguageByName("swift")
	if entry == nil {
		t.Fatal("swift is absent from the language registry")
	}
	lang := entry.Language()

	for _, src := range [][]byte{[]byte("0>>"), []byte(" 0%0>>")} {
		src := src
		t.Run(string(src), func(t *testing.T) {
			gts.ResetAdmissionCandidateCountersForTest()
			parser := gts.NewParser(lang)
			parser.SetAdmissionCandidateRoute(true)
			tree, err := parser.Parse(src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			defer tree.Release()

			routed, fallback := gts.AdmissionCandidateCounters()
			if routed != 0 || fallback != 1 {
				t.Fatalf("routed=%d fallback=%d, want routed=0 fallback=1 (compact must decline, not accept a divergent derivation)", routed, fallback)
			}
		})
	}
}
