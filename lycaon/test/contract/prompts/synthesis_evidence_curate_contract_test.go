package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestSynthesisArcBNoSummarizeCompact(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, rel := range []string{
		"lycaon/internal/survey/synthesis_snapshot.go",
		"lycaon/internal/survey/synthesis_curate.go",
		"lycaon/internal/survey/synthesis_evidence.go",
		"lycaon/internal/workflow/review/fanout_evidence.go",
		"lycaon/internal/session/closeoutassembly/synthesis_evidence.go",
	} {
		path := filepath.Join(root, rel)
		body, err := os.ReadFile(path)
		contractcheck.FailErr(t, "read "+rel, err)
		if strings.Contains(string(body), ".Summarize(") || strings.Contains(string(body), ".Compact(") {
			t.Fatalf("%s must not call Summarize/Compact on worker union", rel)
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, body, 0)
		contractcheck.FailErr(t, "parse "+rel, err)
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "Summarize", "Compact":
				t.Fatalf("%s must not invoke %s on worker union", rel, sel.Sel.Name)
			}
			return true
		})
	}
}
