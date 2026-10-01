package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// An enqueue against a nil outbox drops the event silently, so every package
// that can hold an outbox receives the builder's outbox in the composition
// root. Resolution is by package: several packages declare SetEventOutbox on a
// type named SQLStore.

const outboxComposeRoot = "../../../internal/app"

// outboxSetterName is the method a store exposes to receive the outbox.
const outboxSetterName = "SetEventOutbox"

// outboxBuilderField is the single constructed outbox every store must get.
const outboxBuilderField = "eventOutbox"

func TestEveryOutboxBearingStoreIsWired(t *testing.T) {
	t.Parallel()
	declared := outboxSetterPackages(t)
	if len(declared) == 0 {
		t.Fatal("no SetEventOutbox declarations found; the scan is broken, not the wiring")
	}
	wired := outboxWiredPackages(t)

	var violations []string
	for pkg, where := range declared {
		if !wired[pkg] {
			violations = append(violations, pkg+" declares "+outboxSetterName+
				" at "+where+" but internal/app never wires it — its events would be dropped silently")
		}
	}
	contractcheck.FailViolations(t, "event-outbox wiring drift", violations)
}

// TestOutboxWiringPassesTheBuiltOutbox fails when a call site hands a store
// something other than the builder's field. A literal nil, or a second outbox
// nobody starts, drops events exactly as an unwired store does.
func TestOutboxWiringPassesTheBuiltOutbox(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	var violations []string

	walkOutboxComposeRoot(t, fset, func(path string, file *ast.File) {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != outboxSetterName || len(call.Args) != 1 {
				return true
			}
			if !isBuilderOutboxArg(call.Args[0]) {
				violations = append(violations, filepath.Base(path)+":"+
					lineOf(fset, call.Pos())+" passes "+exprText(call.Args[0])+
					" instead of b."+outboxBuilderField)
			}
			return true
		})
	})
	contractcheck.FailViolations(t, "event-outbox wiring argument drift", violations)
}

// TestOutboxConstructionIsGuarded fails if the composition root stops refusing
// to boot on a nil outbox. Without that refusal the wiring above is satisfied
// by calls that hand every store the same nil.
func TestOutboxConstructionIsGuarded(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "lycaon", "internal/app/build_session.go"))
	contractcheck.FailErr(t, "read build_session.go", err)
	body := string(src)
	if !strings.Contains(body, "b."+outboxBuilderField+" = eventoutbox.New(") {
		t.Fatal("build_session.go must construct the event outbox")
	}
	if !strings.Contains(body, "if b."+outboxBuilderField+" == nil {") {
		t.Fatal("build_session.go must refuse to boot when the constructed outbox is nil — " +
			"every store below would silently drop its events")
	}
}

// outboxSetterPackages maps each package declaring SetEventOutbox to the
// file:line that declares it.
func outboxSetterPackages(t *testing.T) map[string]string {
	t.Helper()
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal")
	out := map[string]string{}
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return perr
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != outboxSetterName || fn.Recv == nil {
				continue
			}
			rel, _ := filepath.Rel(root, path)
			if _, seen := out[file.Name.Name]; !seen {
				out[file.Name.Name] = "internal/" + filepath.ToSlash(rel) + ":" + lineOf(fset, fn.Pos())
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk internal for outbox setters", err)
	return out
}

// outboxWiredPackages reports which packages the composition root wires. A call
// target resolves through the serveBuilder field it is invoked on, or through a
// local whose constructor names its package.
func outboxWiredPackages(t *testing.T) map[string]bool {
	t.Helper()
	fields := builderFieldTypes(t)
	fset := token.NewFileSet()
	wired := map[string]bool{}

	walkOutboxComposeRoot(t, fset, func(_ string, file *ast.File) {
		locals := localConstructorPackages(file)
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != outboxSetterName {
				return true
			}
			switch target := sel.X.(type) {
			case *ast.SelectorExpr:
				if pkg := packageOf(fields[target.Sel.Name]); pkg != "" {
					wired[pkg] = true
				}
			case *ast.Ident:
				if pkg := locals[target.Name]; pkg != "" {
					wired[pkg] = true
				}
			}
			return true
		})
	})
	return wired
}

// localConstructorPackages maps a local variable to the package of the
// constructor that produced it, for `x := pkg.NewThing(...)`.
func localConstructorPackages(file *ast.File) map[string]string {
	out := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) == 0 || len(assign.Rhs) != 1 {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if name, ok := assign.Lhs[0].(*ast.Ident); ok {
			out[name.Name] = pkg.Name
		}
		return true
	})
	return out
}

// packageOf renders the package half of a pkg.Type name.
func packageOf(typeName string) string {
	if idx := strings.Index(typeName, "."); idx > 0 {
		return typeName[:idx]
	}
	return ""
}

// builderFieldTypes maps serveBuilder field names to their bare type names.
func builderFieldTypes(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	path := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal/app/build.go")
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	contractcheck.FailErr(t, "parse build.go", err)

	out := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok || spec.Name.Name != "serveBuilder" {
			return true
		}
		st, ok := spec.Type.(*ast.StructType)
		if !ok {
			return false
		}
		for _, field := range st.Fields.List {
			name := receiverTypeName(field.Type)
			if name == "" {
				continue
			}
			for _, ident := range field.Names {
				out[ident.Name] = name
			}
		}
		return false
	})
	return out
}

// receiverTypeName renders *pkg.T, pkg.T, *T, and T as their bare type name.
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

func isBuilderOutboxArg(arg ast.Expr) bool {
	sel, ok := arg.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	recv, ok := sel.X.(*ast.Ident)
	return ok && recv.Name == "b" && sel.Sel.Name == outboxBuilderField
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

func walkOutboxComposeRoot(t *testing.T, fset *token.FileSet, visit func(string, *ast.File)) {
	t.Helper()
	entries, err := os.ReadDir(outboxComposeRoot)
	contractcheck.FailErr(t, "read composition root", err)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(outboxComposeRoot, name)
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		contractcheck.FailErr(t, "parse "+name, err)
		visit(path, file)
	}
}
