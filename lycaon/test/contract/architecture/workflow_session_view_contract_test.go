package contract

import (
	"fmt"
	"go/ast"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestSessionPackageHasNoWorkflowTypeAssertions(t *testing.T) {
	t.Parallel()
	corpus, err := contractcheck.LoadGoASTCorpus(filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "session"))
	contractcheck.FailErr(t, "load session syntax", err)
	var hits []string
	for _, source := range corpus.Files() {
		if strings.HasSuffix(source.Rel, "_test.go") {
			continue
		}
		ast.Inspect(source.AST, func(node ast.Node) bool {
			assertion, ok := node.(*ast.TypeAssertExpr)
			if !ok {
				return true
			}
			field, ok := assertion.X.(*ast.SelectorExpr)
			if ok && field.Sel.Name == "workflows" {
				hits = append(hits, fmt.Sprintf("%s: workflow capabilities must use their typed port", source.Rel))
			}
			return true
		})
	}
	contractcheck.FailViolations(t, "session workflow type assertions", hits)
}
