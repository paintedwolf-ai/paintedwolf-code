package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
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

func TestEveryOutboxBearingStoreIsWired(t *testing.T) {
	t.Parallel()
	declared := outboxSetterPackages(t)
	if len(declared) == 0 {
		t.Fatal("no SetEventOutbox declarations found; the scan is broken")
	}
	wired := map[string]bool{}
	walkOutboxCalls(t, func(pkg *packages.Package, call *ast.CallExpr, sel *ast.SelectorExpr) {
		method := pkg.TypesInfo.Selections[sel]
		if method == nil || method.Obj().Pkg() == nil {
			t.Fatalf("%s: outbox method owner is unresolved", pkg.Fset.Position(call.Pos()))
		}
		owner := method.Obj().Pkg().Path()
		if _, ok := pkg.TypesInfo.TypeOf(sel.X).Underlying().(*types.Interface); ok {
			owner = concreteOutboxOwner(t, pkg, sel)
		}
		wired[owner] = true
	})
	var violations []string
	for pkg, where := range declared {
		if !wired[pkg] {
			violations = append(violations, pkg+" declares SetEventOutbox at "+where+" but app never wires it")
		}
	}
	contractcheck.FailViolations(t, "event-outbox wiring drift", violations)
}

func TestOutboxWiringPassesTheBuiltOutbox(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	// The typed dependency ports below derive from the one eventing runtime.
	for file, bindings := range map[string][]string{
		"build_infra.go":     {"eventRuntime, err := eventing.Build(", "b.events = eventRuntime"},
		"build_workflows.go": {"delegations.New(b.storage.Database, b.events.Outbox,", "EventsOutbox:        b.events.Outbox", "Events:                b.events.Outbox"},
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
			outboxImportPrefix + "internal/app/eventing":    "b.Outbox",
			outboxImportPrefix + "internal/app/delegations": "outbox",
			outboxImportPrefix + "internal/app/scanning":    "deps.Events",
			outboxImportPrefix + "internal/app/sessions":    "deps.EventsOutbox",
			outboxImportPrefix + "internal/app/workflows":   "deps.EventsOutbox",
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

// receiverTypeName is shared with the pongo template owner contract.
func receiverTypeName(expr ast.Expr) string {
	switch typ := expr.(type) {
	case *ast.StarExpr:
		return receiverTypeName(typ.X)
	case *ast.SelectorExpr:
		if pkg, ok := typ.X.(*ast.Ident); ok {
			return pkg.Name + "." + typ.Sel.Name
		}
		return typ.Sel.Name
	case *ast.Ident:
		return typ.Name
	}
	return ""
}

// Interface peers count only when their actual constructor binds a concrete
// store to the selected field. Merely implementing the interface is not wiring.
func concreteOutboxOwner(t *testing.T, pkg *packages.Package, method *ast.SelectorExpr) string {
	t.Helper()
	field, ok := method.X.(*ast.SelectorExpr)
	if !ok {
		t.Fatalf("%s: interface outbox receiver must be a constructed peer", pkg.Fset.Position(method.Pos()))
	}
	local, ok := field.X.(*ast.Ident)
	if !ok {
		t.Fatalf("%s: interface peer has no local constructor", pkg.Fset.Position(field.Pos()))
	}
	initializer := outboxLocalInitializer(t, pkg, pkg.TypesInfo.Uses[local])
	call, ok := initializer.(*ast.CallExpr)
	if !ok {
		t.Fatal("interface outbox peer does not originate in a constructor call")
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		t.Fatal("interface outbox constructor has no typed package owner")
	}
	constructor, ok := pkg.TypesInfo.Uses[selector.Sel].(*types.Func)
	if !ok || constructor.Pkg() == nil {
		t.Fatal("outbox constructor owner is unresolved")
	}
	var owner string
	for _, source := range outboxComposePackages(t) {
		if source.PkgPath != constructor.Pkg().Path() {
			continue
		}
		for _, file := range source.Syntax {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || source.TypesInfo.Defs[fn.Name] != constructor {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					ret, ok := n.(*ast.ReturnStmt)
					if !ok {
						return true
					}
					if len(ret.Results) != 1 {
						t.Fatal("outbox constructor return must identify one repository")
					}
					address, ok := ret.Results[0].(*ast.UnaryExpr)
					if !ok || address.Op != token.AND {
						t.Fatal("outbox constructor must return its concrete repository")
					}
					literal, ok := address.X.(*ast.CompositeLit)
					if !ok || !types.Identical(source.TypesInfo.TypeOf(address), pkg.TypesInfo.TypeOf(call)) {
						t.Fatal("outbox constructor repository type differs from wired peer")
					}
					var bound bool
					for _, element := range literal.Elts {
						kv, ok := element.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						key, ok := kv.Key.(*ast.Ident)
						if !ok || key.Name != field.Sel.Name {
							continue
						}
						bound = true
						value, ok := kv.Value.(*ast.Ident)
						if !ok {
							t.Fatal("outbox peer must bind its initialized concrete store")
						}
						init := outboxLocalInitializer(t, source, source.TypesInfo.Uses[value])
						concrete, ok := init.(*ast.UnaryExpr)
						if !ok || concrete.Op != token.AND {
							t.Fatal("outbox peer concrete store is not initialized")
						}
						if _, ok := concrete.X.(*ast.CompositeLit); !ok {
							t.Fatal("outbox peer initializer is not a concrete store")
						}
						concreteType := source.TypesInfo.TypeOf(concrete)
						if !types.AssignableTo(concreteType, pkg.TypesInfo.TypeOf(field)) {
							t.Fatal("outbox concrete store does not implement the wired peer")
						}
						selected := types.NewMethodSet(concreteType).Lookup(nil, outboxSetterName)
						if selected == nil || selected.Obj().Pkg() == nil {
							t.Fatal("outbox concrete store setter is unresolved")
						}
						next := selected.Obj().Pkg().Path()
						if owner != "" && owner != next {
							t.Fatal("outbox constructor returns different store owners")
						}
						owner = next
					}
					if !bound {
						t.Fatal("outbox constructor does not bind the wired interface peer")
					}
					return false
				})
			}
		}
	}
	if owner == "" {
		t.Fatalf("%s: concrete outbox constructor binding missing", constructor.FullName())
	}
	return owner
}

func outboxLocalInitializer(t *testing.T, pkg *packages.Package, object types.Object) ast.Expr {
	t.Helper()
	if object == nil {
		t.Fatal("outbox binding local is unresolved")
	}
	var initializer ast.Expr
	assignments := 0
	for _, file := range pkg.Syntax {
		ast.Inspect(file, func(n ast.Node) bool {
			assignment, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, lhs := range assignment.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok {
					continue
				}
				if pkg.TypesInfo.Defs[ident] != object && pkg.TypesInfo.Uses[ident] != object {
					continue
				}
				assignments++
				if len(assignment.Lhs) != len(assignment.Rhs) {
					t.Fatal("outbox binding requires an explicit initializer")
				}
				initializer = assignment.Rhs[i]
			}
			return true
		})
	}
	if assignments != 1 || initializer == nil {
		t.Fatalf("outbox binding %s must have exactly one initializer, found %d", object.Name(), assignments)
	}
	return initializer
}
