package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"golang.org/x/tools/go/packages"
)

const outboxSetterName = "SetEventOutbox"

const outboxImportPrefix = "github.com/lycaon/lycaon/"

var outboxGraph struct {
	once     sync.Once
	packages []*packages.Package
	errors   []string
}

// Resolve setters by their declared Go method owner, including typed peers in
// app subpackages. Package aliases and similarly named store types are distinct.
func outboxComposePackages(t *testing.T) []*packages.Package {
	t.Helper()
	outboxGraph.once.Do(func() {
		cfg := &packages.Config{Dir: filepath.Join(contractcheck.RepoRoot(t), "lycaon"), Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo}
		patterns := []string{"./internal/app/..."}
		for owner := range outboxSetterPackages(t) {
			patterns = append(patterns, owner)
		}
		sort.Strings(patterns)
		pkgs, err := packages.Load(cfg, patterns...)
		if err != nil {
			outboxGraph.errors = append(outboxGraph.errors, err.Error())
		}
		for _, pkg := range pkgs {
			for _, err := range pkg.Errors {
				outboxGraph.errors = append(outboxGraph.errors, err.Error())
			}
		}
		outboxGraph.packages = pkgs
	})
	if len(outboxGraph.errors) != 0 {
		t.Fatalf("load typed app composition: %s", strings.Join(outboxGraph.errors, "\n"))
	}
	if len(outboxGraph.packages) == 0 {
		t.Fatal("typed app composition scan is empty")
	}
	return outboxGraph.packages
}

func walkOutboxCalls(t *testing.T, visit func(*packages.Package, *ast.CallExpr, *ast.SelectorExpr)) {
	t.Helper()
	for _, pkg := range outboxComposePackages(t) {
		if !strings.HasPrefix(pkg.PkgPath, outboxImportPrefix+"internal/app/") && pkg.PkgPath != outboxImportPrefix+"internal/app" {
			continue
		}
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if ok && sel.Sel.Name == outboxSetterName {
					visit(pkg, call, sel)
				}
				return true
			})
		}
	}
}

func outboxSetterPackages(t *testing.T) map[string]string {
	t.Helper()
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal")
	out := map[string]string{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != outboxSetterName || fn.Recv == nil {
				continue
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			pkg := outboxImportPrefix + "internal/" + filepath.ToSlash(filepath.Dir(rel))
			out[pkg] = "internal/" + filepath.ToSlash(rel) + ":" + lineOf(fset, fn.Pos())
		}
		return nil
	})
	contractcheck.FailErr(t, "walk internal for outbox setters", err)
	return out
}

func exprText(expr ast.Expr) string {
	switch typ := expr.(type) {
	case *ast.Ident:
		return typ.Name
	case *ast.SelectorExpr:
		return exprText(typ.X) + "." + typ.Sel.Name
	}
	return "a non-field expression"
}
