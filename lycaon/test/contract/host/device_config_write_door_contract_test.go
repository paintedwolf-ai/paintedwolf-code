package contract

import (
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

var deviceConfigWritePackages = []string{
	"lycaon/internal/app",
	"lycaon/internal/bootrecovery",
	"lycaon/internal/db",
}

var deviceConfigWriteSelectors = map[string]bool{
	"PutModelPolicy":   true,
	"ApplyModelPolicy": true,
	"ApplyGlobal":      true,
	"ApplyProject":     true,
}

// app, bootrecovery, and db do not write model-policy.
func TestDeviceConfigWriteDoor(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	fset := token.NewFileSet()
	var findings []string
	for _, rel := range deviceConfigWritePackages {
		dir := filepath.Join(root, rel)
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			fileRel, _ := filepath.Rel(root, path)
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				name := sel.Sel.Name
				if deviceConfigWriteSelectors[name] || isPolicyStoreWrite(sel, name) {
					pos := fset.Position(call.Pos())
					findings = append(findings, fileRel+":"+strconv.Itoa(pos.Line)+" "+name)
				}
				return true
			})
			return nil
		})
		contractcheck.FailErr(t, "walk "+rel, err)
	}
	if len(findings) > 0 {
		t.Fatalf("device-config write from boot/wipe/recovery:\n  %s", strings.Join(findings, "\n  "))
	}
}

func isPolicyStoreWrite(sel *ast.SelectorExpr, name string) bool {
	if name != "PutGlobal" && name != "PutProject" {
		return false
	}
	switch recv := sel.X.(type) {
	case *ast.Ident:
		return recv.Name == "Policy" || recv.Name == "policy"
	case *ast.SelectorExpr:
		return recv.Sel.Name == "Policy"
	default:
		return false
	}
}
