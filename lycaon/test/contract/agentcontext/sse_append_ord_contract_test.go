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
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestPublishMessageAppendNeverUsesPreStampValueCopy: AppendMessages stamps ord/seq
// onto its variadic slice, so PublishMessageAppend publishes that stamped copy. The
// pre-stamp local carries ord=0 and would pin the card at the top of the transcript.
func TestPublishMessageAppendNeverUsesPreStampValueCopy(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	internal := filepath.Join(root, "lycaon", "internal")
	err := filepath.WalkDir(internal, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		fset := token.NewFileSet()
		f, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			checkAppendThenPublishValueCopy(t, fset, path, fn)
		}
		return nil
	})
	testutil.FailErr(t, "walk internal", err)
}

func checkAppendThenPublishValueCopy(t *testing.T, fset *token.FileSet, path string, fn *ast.FuncDecl) {
	t.Helper()
	// Names of locals that were passed by value (no ...) into AppendMessages.
	valueCopied := map[string]token.Pos{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := callSelectorName(call)
		switch name {
		case "AppendMessages":
			if len(call.Args) < 3 {
				return true
			}
			last := call.Args[len(call.Args)-1]
			if _, isEllipsis := last.(*ast.Ellipsis); isEllipsis {
				return true
			}
			if id, ok := last.(*ast.Ident); ok {
				valueCopied[id.Name] = last.Pos()
			}
		case "PublishMessageAppend":
			if len(call.Args) < 4 {
				return true
			}
			last := call.Args[len(call.Args)-1]
			id, ok := last.(*ast.Ident)
			if !ok {
				return true
			}
			if pos, hit := valueCopied[id.Name]; hit {
				t.Errorf("%s:%s: PublishMessageAppend(%s) after AppendMessages(%s) — "+
					"ord/seq stamps stay on the variadic copy; publish the stamped slice "+
					"(e.g. batch := []Message{msg}; AppendMessages(..., batch...); Publish(..., batch[0]))",
					path, fset.Position(call.Pos()), id.Name, fset.Position(pos))
			}
		}
		return true
	})
}

func callSelectorName(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		if fun.Sel != nil {
			return fun.Sel.Name
		}
	case *ast.Ident:
		return fun.Name
	}
	return ""
}
