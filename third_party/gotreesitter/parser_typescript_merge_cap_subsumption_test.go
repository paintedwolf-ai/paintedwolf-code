package gotreesitter_test

import (
	"strings"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// ironwoodParseNoFatal parses source with the named grammar and returns the
// tree and language without failing on a parse-level ERROR, so the caller can
// assert the error state itself.
func ironwoodParseNoFatal(t *testing.T, name, src string) (*gotreesitter.Tree, *gotreesitter.Language) {
	t.Helper()
	var entry grammars.LangEntry
	var report grammars.ParseSupport
	for _, e := range grammars.AllLanguages() {
		if e.Name == name {
			entry = e
		}
	}
	for _, r := range grammars.AuditParseSupport() {
		if r.Name == name {
			report = r
		}
	}
	lang := entry.Language()
	parser := gotreesitter.NewParser(lang)
	srcBytes := []byte(src)
	var tree *gotreesitter.Tree
	var err error
	if report.Backend == grammars.ParseBackendTokenSource {
		tree, err = parser.ParseWithTokenSource(srcBytes, entry.TokenSourceFactory(srcBytes, lang))
	} else {
		tree, err = parser.Parse(srcBytes)
	}
	if err != nil {
		t.Fatalf("%s parse failed: %v", name, err)
	}
	return tree, lang
}

// typeScriptMergeCapSubsumptionShapes are the source shapes that four
// now-retired per-shape TypeScript merge-width detectors were added to fix:
//   - bare default parameter ("function f(a = 1)") and its array-element,
//     renamed-property, arrow, and method variants (PR #389 detector 1)
//   - destructured arrow return type (PR #389 detector 2)
//   - typed-parameter arrow return type ("(a: A): B =>", PR #409 / issue #402
//     detector 3)
//   - typed arrow parameters with no return type ("(a: A) => body",
//     PR #389 detector 4)
//
// The detectors were retired in favor of the structure-aware cap-two steady
// state (typeScriptSteadyStateMergeCap), which the tests below prove subsumes
// every shape on its own; see PR #416. Each entry lists node types that a
// correct parse must retain.
var typeScriptMergeCapSubsumptionShapes = []struct {
	name        string
	lang        string
	src         string
	mustContain []string
}{
	{"bareDefault", "typescript", "function f(a = 1) {}\n", []string{"required_parameter"}},
	{"bareDefaultTSX", "tsx", "function f(a = 1) {}\n", []string{"required_parameter"}},
	{"multipleBareDefault", "typescript", "function f(a = 1, b = 2) {}\n", []string{"required_parameter"}},
	{"arrayElementDefault", "typescript", "function f([a = 1]) {}\n", []string{"required_parameter"}},
	{"renamedPropertyDefault", "typescript", "function f({a: b = 1}) {}\n", []string{"required_parameter"}},
	{"arrowDefault", "typescript", "const g = (a = 1) => a;\n", []string{"required_parameter"}},
	{"methodDefault", "typescript", "class C { m(a = 1) {} }\n", []string{"required_parameter"}},
	{"typedParameterArrowReturn", "typescript", "const f = (a: A): B => { return a; };\n", []string{"arrow_function"}},
	{"typedParameterArrowReturnTSX", "tsx", "const f = (a: A): B => { return a; };\n", []string{"arrow_function"}},
	{"typedParameterArrowReturnExport", "typescript", "export const f = (a: A): B => { return a; };\n", []string{"arrow_function"}},
	{"destructuredArrowReturn", "typescript", "const remainingPaths = arrayFrom(allFileNames.entries(), ([fileName, { isRedirect, isInNodeModules }]): ModulePath => ({ path: fileName, isRedirect, isInNodeModules }));\n", []string{"arrow_function", "array_pattern", "type_annotation"}},
	{"typedArrowNoReturn", "typescript", "const f = (a: A) => a;\n", []string{"arrow_function", "required_parameter"}},
	{"typedArrowNoReturnTSX", "tsx", "const f = (a: A) => a;\n", []string{"arrow_function", "required_parameter"}},
}

// TestTypeScriptMergeCapSubsumesDetectors is the subsumption proof for the
// structure-aware cap-two steady state: it asserts that cap-two alone parses
// every detector-era bug-family shape cleanly, with no source-text detector
// in the loop at all (the detectors this test used to disable are deleted;
// cap-two is the only configuration now). A pass confirms the detectors were
// redundant per-shape band-aids that the cap-two merge policy subsumes,
// rather than the sole thing that ever kept those shapes correct.
func TestTypeScriptMergeCapSubsumesDetectors(t *testing.T) {
	for _, s := range typeScriptMergeCapSubsumptionShapes {
		s := s
		t.Run(s.name, func(t *testing.T) {
			tree, lang := ironwoodParseNoFatal(t, s.lang, s.src)
			defer tree.Release()
			root := tree.RootNode()
			sexpr := root.SExpr(lang)
			if root.HasError() || strings.Contains(sexpr, "ERROR") {
				t.Fatalf("%s (%s) has error at cap-two: %s", s.name, s.lang, sexpr)
			}
			for _, m := range s.mustContain {
				if !strings.Contains(sexpr, m) {
					t.Fatalf("%s (%s) missing %q at cap-two: %s", s.name, s.lang, m, sexpr)
				}
			}
		})
	}
}
