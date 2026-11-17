package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

type agentsMDWriteScan struct {
	Violations []string
}

func scanHardcodedAgentsMDWrites(lycaonRoot string) (*agentsMDWriteScan, error) {
	out := &agentsMDWriteScan{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(filepath.Join(lycaonRoot, "internal"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "WriteFile" {
				return true
			}
			if len(call.Args) == 0 {
				return true
			}
			pathLit := contractcheck.AstStringLit(call.Args[0])
			if pathLit == "" {
				return true
			}
			if strings.Contains(pathLit, "AGENTS.md") {
				rel, _ := filepath.Rel(lycaonRoot, path)
				out.Violations = append(out.Violations, rel+": hardcoded AGENTS.md write bypasses the reviewed file-change path")
			}
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
