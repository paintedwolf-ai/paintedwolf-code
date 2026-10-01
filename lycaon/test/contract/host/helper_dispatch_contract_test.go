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

func TestHelperDispatchIsConfineInit(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, "lycaon", "internal", "confine")
	fset := token.NewFileSet()

	var initDispatches bool
	var exportedHelper bool
	var commandRefusesNest bool

	entries, err := os.ReadDir(dir)
	contractcheck.FailErr(t, "read internal/confine", err)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		f, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			continue
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil || fn.Body == nil {
				continue
			}
			switch fn.Name.Name {
			case "RunHelperIfInvoked":
				exportedHelper = true
			case "init":
				if fn.Recv == nil && callsIdent(fn.Body, "runHelperIfInvoked") {
					initDispatches = true
				}
			case "Command":
				if fn.Recv == nil && callsIdent(fn.Body, "IsHelperInvocation") && callsIdent(fn.Body, "ErrNestedHelper") {
					commandRefusesNest = true
				}
			}
		}
	}

	var missing []string
	if !initDispatches {
		missing = append(missing, "init does not call runHelperIfInvoked")
	}
	if exportedHelper {
		missing = append(missing, "RunHelperIfInvoked is exported")
	}
	if !commandRefusesNest {
		missing = append(missing, "Command does not refuse IsHelperInvocation with ErrNestedHelper")
	}
	contractcheck.FailViolations(t, "confine helper dispatch", missing)
}

func TestEnableAutoConfineIsProductionOptIn(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	fset := token.NewFileSet()
	var testCalls []string
	var prodCalls []string

	err := filepath.WalkDir(lycaonRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			base := d.Name()
			if base == "testdata" || base == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		testFile := strings.HasSuffix(path, "_test.go")
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := contractcheck.CallName(call)
			if name != "EnableAutoConfine" && name != "confine.EnableAutoConfine" {
				return true
			}
			pos := fset.Position(call.Pos())
			loc := rel + ":" + strconv.Itoa(pos.Line)
			if testFile {
				testCalls = append(testCalls, loc)
			} else {
				prodCalls = append(prodCalls, loc)
			}
			return true
		})
		return nil
	})
	contractcheck.FailErr(t, "walk lycaon for EnableAutoConfine", err)

	contractcheck.FailViolations(t, "EnableAutoConfine in a test file", testCalls)
	if len(prodCalls) == 0 {
		t.Fatal("no production EnableAutoConfine call")
	}
}

func callsIdent(body *ast.BlockStmt, name string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if ok && id.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}
