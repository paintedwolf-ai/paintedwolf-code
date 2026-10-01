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

func TestGitArgvNeverPlacesACallerValueInAnOptionPosition(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, "lycaon", "internal", "git")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read internal/git: %v", err)
	}

	fset := token.NewFileSet()
	var findings []string
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		runner := contractcheck.ImportAliasFor(file, "github.com/lycaon/lycaon/internal/gitexec")
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Run" || len(call.Args) < 3 {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if !ok || runner == "" || pkg.Name != runner {
				return true
			}
			lit, ok := call.Args[2].(*ast.CompositeLit)
			if !ok {
				return true // Only inline slices are inspectable here.
			}
			checked++
			terminated := false
			for i, elt := range lit.Elts {
				if isArgvTerminator(elt) {
					terminated = true
					continue
				}
				if argvConstant(elt) {
					continue
				}
				if terminated || leftmostIsStringConstant(elt) {
					continue
				}
				if i > 0 && argvFlagLiteral(lit.Elts[i-1]) {
					continue
				}
				findings = append(findings, contractcheck.FmtLine(name, fset.Position(elt.Pos()).Line,
					"caller value in an option position", "element "+strconv.Itoa(i)+" of a gitexec.Run argv"))
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no inline execution arguments checked")
	}
	if len(findings) > 0 {
		t.Fatalf("git argv places a caller value where git parses options — "+
			"insert gitargv.EndOfOptions (or \"--\" for a pathspec) before it:\n  %s",
			strings.Join(findings, "\n  "))
	}
}

// argvConstant reports a plain string constant element.
func argvConstant(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	return ok && lit.Kind == token.STRING
}

func argvFlagLiteral(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	return strings.HasPrefix(strings.Trim(lit.Value, `"`), "-")
}

// isArgvTerminator reports gitargv.EndOfOptions or a literal "--".
func isArgvTerminator(e ast.Expr) bool {
	if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		return strings.Trim(lit.Value, `"`) == "--"
	}
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "gitargv" && sel.Sel.Name == "EndOfOptions"
}

func leftmostIsStringConstant(e ast.Expr) bool {
	for {
		bin, ok := e.(*ast.BinaryExpr)
		if !ok || bin.Op != token.ADD {
			return argvConstant(e)
		}
		e = bin.X
	}
}
