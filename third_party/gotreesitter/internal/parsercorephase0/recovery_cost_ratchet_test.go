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

// TestRecoveryCostNoOutsideCallSitesRatchet is the compile-time,
// clean-path-zero-cost guardrail for recovery_cost.go (campaign v7 tranche
// B3 stage S2, design section 8 gates G2/G3): every symbol recovery_cost.go
// declares at package scope must be referenced from nowhere else in this
// package's non-test source, proving by construction that ordinary
// condense, shift, reduce, and materialization code paths cannot reach the
// cost model. This is the same pattern as
// TestCondenseWithOutcomeAtomicProvenanceRatchet
// (condense_outcome_provenance_ratchet_test.go), generalized from one
// function name to a whole file's declared surface.
//
// Method names (Reset, Len, lookup, store, ...) are deliberately excluded
// from the banned set: they are selected through a receiver, not referenced
// as bare package-scope identifiers, and some are common enough (Core.Reset,
// for one) that banning them by name would false-positive on unrelated
// methods. What this ratchet protects is the set of package-scope
// declarations recovery_cost.go introduces: its exported API plus its
// unexported helpers. The root scheduler uses only the exported API.
//
// SCOPE CORRECTION. This ratchet reads only THIS package's files, and
// recovery_cost.go's API is exported, so a caller in another package never
// trips it. Such a caller now exists: the root package's
// parsercore_phase0_recovery_cost_source.go implements RecoveryCostSource and
// prices recovery lineages with this model. Read the guarantee as "no caller
// inside the compact core", which is still worth holding, and not as "no
// caller anywhere", which is no longer true.
func TestRecoveryCostNoOutsideCallSitesRatchet(t *testing.T) {
	const homeFile = "recovery_cost.go"

	fset := token.NewFileSet()
	home, err := parser.ParseFile(fset, homeFile, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", homeFile, err)
	}

	banned := map[string]bool{}
	for _, decl := range home.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv != nil {
				// A method: selected through a receiver, not a bare
				// package-scope identifier. Excluded (see doc comment).
				continue
			}
			banned[d.Name.Name] = true
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					banned[s.Name.Name] = true
				case *ast.ValueSpec:
					for _, name := range s.Names {
						if name.Name != "_" {
							banned[name.Name] = true
						}
					}
				}
			}
		}
	}
	if len(banned) == 0 {
		t.Fatalf("collected zero package-scope identifiers from %s; the ratchet would vacuously pass", homeFile)
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	violations := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == homeFile {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Clean(name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			ident, ok := node.(*ast.Ident)
			if !ok || !banned[ident.Name] {
				return true
			}
			violations++
			t.Errorf("%s references %s from recovery_cost.go; "+
				"the compact core must not call the recovery model directly",
				fset.Position(ident.Pos()), ident.Name)
			return true
		})
	}
	if violations != 0 {
		t.Fatalf("%d reference(s) to recovery_cost.go declarations found outside recovery_cost.go and its own tests", violations)
	}
}
