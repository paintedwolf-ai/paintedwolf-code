package parsercorephase0

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCondenseWithOutcomeAtomicProvenanceRatchet is a structural regression
// test for the historical-provenance-propagation fix in
// condenseWithOutcomeAtomic (core.go). It parses the package's own source
// and asserts that condenseWithOutcomeAtomic constructs every non-empty
// condenseOutcome result through the buildOutcome closure, never through a
// second, ad hoc condenseOutcome{...} literal that could silently omit the
// historicalBoundarySplit / historicalConvergedSplit /
// historicalForestDeterministic / historicalCleanPathRank /
// historicalLineage fields buildOutcome carries.
//
// This exists because the defect this fix addresses is not observable
// behaviorally through any input reachable via the public Core API: a live
// incumbent (needed to reach the shallow-fold early returns this fix
// touches) and a historical predecessor are mutually exclusive within one
// condenseWithOutcomeAtomic call, since the historical branch
// unconditionally zeroes oldID before the duplicate/shallow-fold logic ever
// runs. link_fold_provenance_test.go's TestFold*PropagatesProvenance tests
// confirm the fold paths do not fabricate history, but (confirmed by
// reverting the fix locally and re-running them) they cannot by themselves
// catch a regression back to the pre-fix bare literals, because the
// discarded fields are always the zero value at those call sites either
// way. This ratchet is what actually catches that regression: it fails if a
// future edit reintroduces a bare `condenseOutcome{head: ..., change:
// ...}` literal instead of routing through buildOutcome.
func TestCondenseWithOutcomeAtomicProvenanceRatchet(t *testing.T) {
	const targetFunction = "condenseWithOutcomeAtomic"
	const targetType = "condenseOutcome"
	const builder = "buildOutcome"

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var fn *ast.FuncDecl
	var fnFile string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Clean(name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil || function.Name.Name != targetFunction {
				continue
			}
			if fn != nil {
				t.Fatalf("%s declared more than once (in %s and %s)", targetFunction, fnFile, name)
			}
			fn = function
			fnFile = name
		}
	}
	if fn == nil {
		t.Fatalf("%s not found in package source", targetFunction)
	}

	// Locate buildOutcome's own closure body so the walk below can skip it:
	// that is the one legitimate place a full condenseOutcome{...} literal
	// with the historical fields is constructed.
	var buildOutcomeBody ast.Node
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		assign, ok := node.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		ident, ok := assign.Lhs[0].(*ast.Ident)
		if !ok || ident.Name != builder {
			return true
		}
		literal, ok := assign.Rhs[0].(*ast.FuncLit)
		if !ok {
			return true
		}
		buildOutcomeBody = literal.Body
		return false
	})
	if buildOutcomeBody == nil {
		t.Fatalf("%s: %s closure not found", targetFunction, builder)
	}

	violations := 0
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		if node == buildOutcomeBody {
			return false
		}
		composite, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		ident, ok := composite.Type.(*ast.Ident)
		if !ok || ident.Name != targetType {
			return true
		}
		if len(composite.Elts) == 0 {
			// condenseOutcome{} is the zero value used on error returns;
			// there is no head/provenance to propagate there.
			return true
		}
		violations++
		t.Errorf(
			"%s: %s constructs a non-empty %s{...} literal at %s outside %s -- "+
				"this bypasses historical provenance propagation; route it through "+
				"%s(head, change) instead",
			fnFile, targetFunction, targetType, fset.Position(composite.Pos()), builder, builder,
		)
		return true
	})
	if violations != 0 {
		t.Fatalf("%d %s literal(s) in %s bypass %s", violations, targetType, targetFunction, builder)
	}
}
