package contract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const gateImportPath = "github.com/lycaon/lycaon/internal/gate"

// An approval posture reaches host code only through gate.ParsePosture or
// decoding, both of which refuse unknown tokens. A conversion such as
// gate.Posture(raw) would carry an unchecked token past that boundary.
func TestApprovalPosturesComeFromTheParser(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	fset := token.NewFileSet()
	var findings []string
	for _, dir := range []string{"internal", "cmd", "pkg"} {
		err := filepath.WalkDir(filepath.Join(root, "lycaon", dir), func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return walkErr
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			alias := gateImportName(f)
			if alias == "" {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) != 1 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Posture" {
					return true
				}
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == alias {
					pos := fset.Position(call.Pos())
					findings = append(findings, fmt.Sprintf("%s:%d: %s.Posture(...) conversion", filepath.ToSlash(rel), pos.Line, alias))
				}
				return true
			})
			return nil
		})
		contractcheck.FailErr(t, "scan "+dir, err)
	}
	if len(findings) != 0 {
		t.Fatalf("approval postures must come from gate.ParsePosture or decoding:\n  %s", strings.Join(findings, "\n  "))
	}
}

func gateImportName(f *ast.File) string {
	for _, spec := range f.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != gateImportPath {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name
		}
		return "gate"
	}
	return ""
}
