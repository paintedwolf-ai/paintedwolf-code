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

// Process starts carry launch attribution through the typed boundary.
func TestProcessLaunchesUseTheTypedDoor(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	internal := filepath.Join(root, "lycaon", "internal")
	fset := token.NewFileSet()
	var findings []string
	err := filepath.WalkDir(internal, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		rel, _ := filepath.Rel(root, path)
		aliases := importAliases(f)
		allowRaw := strings.HasPrefix(filepath.ToSlash(rel), "lycaon/internal/exec/") ||
			strings.HasPrefix(filepath.ToSlash(rel), "lycaon/internal/confine/")
		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CallExpr:
				if !allowRaw && isRawProcessStart(node.Fun, aliases) {
					findings = append(findings, positionFinding(fset, node.Pos(), rel, "raw os/exec process start"))
				}
			case *ast.CompositeLit:
				if isLaunchOptionsLiteral(node.Type, f.Name.Name, aliases) && !hasKey(node, "Launch") {
					findings = append(findings, positionFinding(fset, node.Pos(), rel, "process options omit Launch"))
				}
			}
			return true
		})
		return nil
	})
	contractcheck.FailErr(t, "scan process launches", err)
	if len(findings) != 0 {
		t.Fatalf("process launch door drift:\n  %s", strings.Join(findings, "\n  "))
	}
}

func importAliases(f *ast.File) map[string]string {
	out := map[string]string{}
	for _, spec := range f.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(path)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		out[name] = path
	}
	return out
}

func isRawProcessStart(expr ast.Expr, aliases map[string]string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || (sel.Sel.Name != "Command" && sel.Sel.Name != "CommandContext") {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && aliases[pkg.Name] == "os/exec"
}

func isLaunchOptionsLiteral(expr ast.Expr, pkg string, aliases map[string]string) bool {
	switch typ := expr.(type) {
	case *ast.Ident:
		return pkg == "exec" && (typ.Name == "ExecOpts" || typ.Name == "PTYOpts")
	case *ast.SelectorExpr:
		name, ok := typ.X.(*ast.Ident)
		if !ok {
			return false
		}
		path := aliases[name.Name]
		return path == "github.com/lycaon/lycaon/internal/exec" && (typ.Sel.Name == "ExecOpts" || typ.Sel.Name == "PTYOpts") ||
			path == "github.com/lycaon/lycaon/internal/hostcmd" && typ.Sel.Name == "Request"
	default:
		return false
	}
}

func hasKey(lit *ast.CompositeLit, want string) bool {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if ok && key.Name == want {
			return true
		}
	}
	return false
}

func positionFinding(fset *token.FileSet, pos token.Pos, rel, message string) string {
	return rel + ":" + strconv.Itoa(fset.Position(pos).Line) + ": " + message
}
