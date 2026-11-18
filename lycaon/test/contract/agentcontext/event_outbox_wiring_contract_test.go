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

func TestOutboxWiringPassesTheBuiltOutbox(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	// The typed dependency ports below derive from the one eventing runtime.
	for file, bindings := range map[string][]string{
		"build_infra.go":     {"eventRuntime, err := eventing.Build(", "b.events = eventRuntime"},
		"build_workflows.go": {"workerruntime.New(b.storage.Database, b.events.Outbox,", "EventsOutbox:        b.events.Outbox", "Events:                b.events.Outbox"},
		"build_session.go":   {"EventsOutbox:    b.events.Outbox"},
	} {
		source := contractcheck.ReadRepoFile(t, root, "lycaon/internal/app/"+file)
		for _, binding := range bindings {
			if !strings.Contains(source, binding) {
				t.Fatalf("%s missing canonical outbox binding %q", file, binding)
			}
		}
	}
	var violations []string
	count := 0
	walkOutboxCalls(t, func(pkg *packages.Package, call *ast.CallExpr, _ *ast.SelectorExpr) {
		count++
		allowed := map[string]string{
			outboxImportPrefix + "internal/app/eventing":      "b.Outbox",
			outboxImportPrefix + "internal/app/workerruntime": "outbox",
			outboxImportPrefix + "internal/app/scanning":      "deps.Events",
			outboxImportPrefix + "internal/app/sessions":      "deps.EventsOutbox",
			outboxImportPrefix + "internal/app/workflows":     "deps.EventsOutbox",
		}
		if len(call.Args) != 1 || allowed[pkg.PkgPath] == "" || exprText(call.Args[0]) != allowed[pkg.PkgPath] {
			violations = append(violations, pkg.Fset.Position(call.Pos()).String()+": setter does not receive its canonical outbox port")
		}
	})
	if count == 0 {
		t.Fatal("no outbox wiring calls found")
	}
	contractcheck.FailViolations(t, "event-outbox wiring argument drift", violations)
}

func TestOutboxConstructionIsGuarded(t *testing.T) {
	t.Parallel()
	source := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), "lycaon/internal/app/eventing/runtime.go")
	construct := strings.Index(source, "b.Outbox = eventoutbox.New(")
	guard := strings.Index(source, "if b.Outbox == nil {")
	wire := strings.Index(source, ".SetEventOutbox(b.Outbox)")
	if construct < 0 || guard <= construct || wire <= guard {
		t.Fatal("eventing must construct and refuse a nil outbox before wiring stores")
	}
	if !strings.Contains(source[guard:wire], "return nil,") {
		t.Fatal("nil-outbox guard must refuse boot")
	}
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
