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

// dotPathExemptions covers non-workflow map traversal.
var dotPathExemptions = map[string]string{
	"lycaon/internal/detectionpack/action_semantics.go:lookupArgPath": "Sigma rule field lookup over model-authored tool arguments",
}

func TestDotPathReadsHaveOneAuthority(t *testing.T) {
	t.Parallel()
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal")
	owner := filepath.Join(root, "conditions")

	var findings []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return walkErr
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.HasPrefix(path, owner+string(os.PathSeparator)) {
			return nil
		}
		file, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			return perr
		}
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !walksDotSeparatedPath(fn.Body) {
				return true
			}
			rel, _ := filepath.Rel(contractcheck.RepoRoot(t), path)
			key := filepath.ToSlash(rel) + ":" + fn.Name.Name
			if _, exempt := dotPathExemptions[key]; !exempt {
				findings = append(findings, key)
			}
			return true
		})
		return nil
	})
	contractcheck.FailErr(t, "scan internal for dotted-path variable walks", err)

	if len(findings) != 0 {
		t.Fatalf("dotted-path variable walk outside internal/conditions — call conditions.DotPathGet:\n  %s",
			strings.Join(findings, "\n  "))
	}
}

// walksDotSeparatedPath detects string-keyed dotted-path reads.
func walksDotSeparatedPath(body *ast.BlockStmt) bool {
	splitsOnDot, assertsStringMap := false, false
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			if contractcheck.CallName(node) == "strings.Split" && len(node.Args) == 2 {
				if lit, ok := node.Args[1].(*ast.BasicLit); ok && lit.Value == `"."` {
					splitsOnDot = true
				}
			}
		case *ast.TypeAssertExpr:
			if mp, ok := node.Type.(*ast.MapType); ok {
				if key, ok := mp.Key.(*ast.Ident); ok && key.Name == "string" && isEmptyInterface(mp.Value) {
					assertsStringMap = true
				}
			}
		}
		return true
	})
	return splitsOnDot && assertsStringMap
}

// isEmptyInterface recognizes interface{} and any.
func isEmptyInterface(e ast.Expr) bool {
	switch t := e.(type) {
	case *ast.InterfaceType:
		return t.Methods == nil || len(t.Methods.List) == 0
	case *ast.Ident:
		return t.Name == "any"
	}
	return false
}
