package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

const recoveryComposeRoot = "../../../internal/app"

// Reconciliation covers durable state without a shared recovery journal.
func TestRegistryCarriesBothEntryKinds(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	kinds := map[string]int{}

	walkComposeRoot(t, fset, func(_ string, file *ast.File) {
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isRecoveryEntryLit(lit) {
				return true
			}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != "Kind" {
					continue
				}
				if sel, ok := kv.Value.(*ast.SelectorExpr); ok {
					kinds[sel.Sel.Name]++
				}
			}
			return true
		})
	})

	if kinds["KindJournal"] == 0 {
		t.Fatal("no journal-replay recovery entry is registered")
	}
	if kinds["KindReconcile"] == 0 {
		t.Fatal("no reconcile recovery entry is registered; the registry must " +
			"keep accepting the shape that has no journal to replay")
	}
}

// These recoveries can launch turns and require the completed service graph.
var requiredServePhaseRecovery = map[string]string{
	"workflow-verdicts":       "a recovered verdict can auto-advance a workflow",
	"workflow-review-repairs": "a replayed repair can pause a run and hold its workers",
	"prompt-submissions":      "a recovered submission launches a coordinator turn",
	"worker-merge-applies":    "resuming a merge can close out worker work",
}

func TestTurnLaunchingRecoveryStaysInTheServePhase(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	phase := map[string]string{}

	walkComposeRoot(t, fset, func(_ string, file *ast.File) {
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isRecoveryEntryLit(lit) {
				return true
			}
			var name, entryPhase string
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				switch key.Name {
				case "Name":
					if s, ok := kv.Value.(*ast.BasicLit); ok {
						name = strings.Trim(s.Value, `"`)
					}
				case "Phase":
					if sel, ok := kv.Value.(*ast.SelectorExpr); ok {
						entryPhase = sel.Sel.Name
					}
				}
			}
			if name != "" {
				phase[name] = entryPhase
			}
			return true
		})
	})

	for name, why := range requiredServePhaseRecovery {
		got, registered := phase[name]
		if !registered {
			t.Fatalf("recovery entry %q is not registered; %s", name, why)
		}
		if got != "PhaseServe" {
			t.Fatalf("recovery entry %q runs in %s, want PhaseServe: %s", name, got, why)
		}
	}
}

func walkComposeRoot(t *testing.T, fset *token.FileSet, visit func(string, *ast.File)) {
	t.Helper()
	err := filepath.WalkDir(recoveryComposeRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		visit(path, file)
		return nil
	})
	testutil.FailErr(t, "walk app composition owners", err)
}

func isRecoveryEntryLit(lit *ast.CompositeLit) bool {
	sel, ok := lit.Type.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "bootrecovery" && sel.Sel.Name == "Entry"
}

func lineOf(fset *token.FileSet, pos token.Pos) string {
	return strings.TrimPrefix(fset.Position(pos).String(), fset.Position(pos).Filename+":")
}
