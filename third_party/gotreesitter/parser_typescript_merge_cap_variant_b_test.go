package gotreesitter_test

import (
	"strings"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

// Variant B is an ELIMINATED alternative to the shipping cap-two policy, kept
// here only as a reproducible test seam so the head-to-head evaluation can be
// re-run. Variant B keeps the TypeScript full-parse cap at one and, at the
// cap-one discard site (stackCompareMergeSmallCapOne), prefers the
// structurally-richer fork before score instead of after it (see
// typeScriptCapOneStructurePreference and
// SetTypeScriptCapOneStructurePreferenceForTests).
//
// At the unit level variant B looks attractive: it parses every detector-era
// bug family cleanly while keeping the tight cap-one width
// (TestTypeScriptMergeCapVariantBSubsumesDetectors below passes). A small
// 48-file diagnostic even showed it leaving the a<b>(c) generic-call ambiguity
// family byte-identical to baseline.
//
// The full 42,861-file TypeScript and TSX corpus differential refutes it. The
// lower-score preference contradicts tree-sitter dynamic-precedence semantics
// (higher score is the intended winner) for ordinary expression parsing:
// variant B turned 1905 previously-clean real-world files (route handlers,
// component modules, index files) into ERROR, while fixing only 132 of the
// broken shapes. The shipping cap-two policy, by contrast, fixes 221 files and
// regresses 5. Variant B is therefore eliminated; the seam remains only to make
// that measurement reproducible. It is never enabled in production.

// TestTypeScriptMergeCapVariantBSubsumesDetectors documents that variant B
// still parses every detector-era bug-family shape cleanly at the unit level.
// It is the unit-level half of the evaluation whose full-corpus half
// eliminated the variant; see the file comment above.
func TestTypeScriptMergeCapVariantBSubsumesDetectors(t *testing.T) {
	restoreVar := gotreesitter.SetTypeScriptCapOneStructurePreferenceForTests(true)
	defer restoreVar()
	for _, s := range typeScriptMergeCapSubsumptionShapes {
		s := s
		t.Run(s.name, func(t *testing.T) {
			tree, lang := ironwoodParseNoFatal(t, s.lang, s.src)
			defer tree.Release()
			root := tree.RootNode()
			sexpr := root.SExpr(lang)
			if root.HasError() || strings.Contains(sexpr, "ERROR") {
				t.Fatalf("%s (%s) has error under variant B: %s", s.name, s.lang, sexpr)
			}
			for _, m := range s.mustContain {
				if !strings.Contains(sexpr, m) {
					t.Fatalf("%s (%s) missing %q under variant B: %s", s.name, s.lang, m, sexpr)
				}
			}
		})
	}
}
