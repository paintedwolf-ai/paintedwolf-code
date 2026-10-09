package contract

import (
	"go/ast"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestMatchContextDirectWorkflowConstructionProhibited(t *testing.T) {
	t.Parallel()
	repoRoot := contractcheck.RepoRoot(t)
	corpus, err := contractcheck.LoadGoASTCorpus(filepath.Join(repoRoot, "lycaon", "internal"))
	contractcheck.FailErr(t, "LoadGoASTCorpus", err)

	for _, gf := range corpus.Production() {
		// binding.go defines RunMatch itself
		if strings.HasSuffix(gf.Rel, "internal/coordinator/anchor/binding.go") {
			continue
		}

		ast.Inspect(gf.AST, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}

			isMatchContext := false
			switch t := cl.Type.(type) {
			case *ast.SelectorExpr:
				if t.Sel.Name == "MatchContext" {
					if id, ok := t.X.(*ast.Ident); ok && id.Name == "anchor" {
						isMatchContext = true
					}
				}
			case *ast.Ident:
				if t.Name == "MatchContext" && strings.Contains(gf.Rel, "internal/coordinator/anchor") {
					isMatchContext = true
				}
			}

			if !isMatchContext {
				return true
			}

			for _, elt := range cl.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				keyIdent, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				if keyIdent.Name == "Workflow" {
					pos := corpus.Fset.Position(cl.Pos())
					rel, _ := filepath.Rel(repoRoot, pos.Filename)
					t.Errorf("%s:%d: direct construction of anchor.MatchContext{Workflow: ...} is prohibited; use anchor.RunMatch(run, surface, phase) to guarantee workflow version binding", filepath.ToSlash(rel), pos.Line)
				}
			}

			return true
		})
	}
}
