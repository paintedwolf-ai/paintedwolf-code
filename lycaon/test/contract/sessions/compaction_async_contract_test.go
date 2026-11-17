package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCompactionSingleWriterContract(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	allowed := map[string]bool{
		"internal/session/history.Service.runBackgroundCompactionPage": true,
		"internal/session/history.Service.forceCompact":                true,
		"internal/session/history.Service.publishReductionSynthesis":   true,
	}
	callers, err := scanCompactionViewWriterCallers(lycaonRoot)
	contractcheck.FailErr(t, "scan writeCompactionView callers", err)
	for fn, sites := range callers {
		if allowed[fn] {
			continue
		}
		t.Fatalf("writeCompactionView caller %s not allowed: %v", fn, sites)
	}
	for owner := range allowed {
		if len(callers[owner]) == 0 {
			t.Errorf("compaction publication owner %s is missing", owner)
		}
	}
}

func scanCompactionViewWriterCallers(lycaonRoot string) (map[string][]string, error) {
	fset := token.NewFileSet()
	out := make(map[string][]string)
	err := filepath.WalkDir(lycaonRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(lycaonRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			owner := filepath.ToSlash(filepath.Dir(rel)) + ".<package>"
			if fn, ok := decl.(*ast.FuncDecl); ok {
				owner = filepath.ToSlash(filepath.Dir(rel)) + "."
				if fn.Recv != nil && len(fn.Recv.List) == 1 {
					receiver := fn.Recv.List[0].Type
					if pointer, ok := receiver.(*ast.StarExpr); ok {
						receiver = pointer.X
					}
					if ident, ok := receiver.(*ast.Ident); ok {
						owner += ident.Name + "."
					}
				}
				owner += fn.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if ok && sel.Sel.Name == "writeCompactionView" {
					out[owner] = append(out[owner], fset.Position(call.Pos()).String())
				}
				return true
			})
		}
		return nil
	})
	return out, err
}
