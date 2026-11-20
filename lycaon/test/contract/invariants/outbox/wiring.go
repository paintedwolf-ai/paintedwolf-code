package outbox

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

const outboxSetterName = "SetEventOutbox"

// Every package that declares SetEventOutbox is wired by the app
// composition. Setters resolve by their declared method owner; a call
// through an interface peer resolves to the concrete store its constructor
// binds.
func everyOutboxBearingStoreIsWired(r *invariants.Run) {
	declared := map[string]string{}
	for _, f := range r.GoFiles(invariants.Production, "internal") {
		f.Funcs(func(fn *ast.FuncDecl) {
			if fn.Name.Name == outboxSetterName && fn.Recv != nil {
				declared[f.Pkg] = f.Site(fn.Pos())
			}
		})
	}
	r.Expect(len(declared), outboxSetterName+" declarations")
	wired := map[string]bool{}
	for _, pkg := range r.TypedPackages("internal/app") {
		for _, file := range pkg.Syntax() {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != outboxSetterName {
					return true
				}
				method := pkg.Info.Selections[sel]
				if method == nil || method.Obj().Pkg() == nil {
					r.Fatalf("%s: outbox method owner is unresolved", r.Types().Fset.Position(call.Pos()))
				}
				owner := method.Obj().Pkg().Path()
				if _, ok := pkg.Info.TypeOf(sel.X).Underlying().(*types.Interface); ok {
					owner = concreteOutboxOwner(r, pkg, sel)
				}
				wired[owner] = true
				return true
			})
		}
	}
	for _, pkg := range sortedKeys(declared) {
		if !wired[pkg] {
			r.Report("%s declares %s at %s but the app never wires it", pkg, outboxSetterName, declared[pkg])
		}
	}
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

// Interface peers count only when their actual constructor binds a concrete
// store to the selected field. Merely implementing the interface is not wiring.
func concreteOutboxOwner(r *invariants.Run, pkg *invariants.TypedPackage, method *ast.SelectorExpr) string {
	field, ok := method.X.(*ast.SelectorExpr)
	if !ok {
		r.Fatalf("%s: interface outbox receiver must be a constructed peer", r.Types().Fset.Position(method.Pos()))
	}
	local, ok := field.X.(*ast.Ident)
	if !ok {
		r.Fatalf("%s: interface peer has no local constructor", r.Types().Fset.Position(field.Pos()))
	}
	initializer := outboxLocalInitializer(r, pkg, pkg.Info.Uses[local])
	call, ok := initializer.(*ast.CallExpr)
	if !ok {
		r.Fatalf("interface outbox peer does not originate in a constructor call")
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		r.Fatalf("interface outbox constructor has no typed package owner")
	}
	constructor, ok := pkg.Info.Uses[selector.Sel].(*types.Func)
	if !ok || constructor.Pkg() == nil {
		r.Fatalf("outbox constructor owner is unresolved")
	}
	var owner string
	for _, source := range r.TypedPackages("internal") {
		if source.Path != constructor.Pkg().Path() {
			continue
		}
		for _, file := range source.Syntax() {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || source.Info.Defs[fn.Name] != constructor {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					ret, ok := n.(*ast.ReturnStmt)
					if !ok {
						return true
					}
					if len(ret.Results) != 1 {
						r.Fatalf("outbox constructor return must identify one repository")
					}
					address, ok := ret.Results[0].(*ast.UnaryExpr)
					if !ok || address.Op != token.AND {
						r.Fatalf("outbox constructor must return its concrete repository")
					}
					literal, ok := address.X.(*ast.CompositeLit)
					if !ok || !types.Identical(source.Info.TypeOf(address), pkg.Info.TypeOf(call)) {
						r.Fatalf("outbox constructor repository type differs from wired peer")
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
							r.Fatalf("outbox peer must bind its initialized concrete store")
						}
						init := outboxLocalInitializer(r, source, source.Info.Uses[value])
						concrete, ok := init.(*ast.UnaryExpr)
						if !ok || concrete.Op != token.AND {
							r.Fatalf("outbox peer concrete store is not initialized")
						}
						if _, ok := concrete.X.(*ast.CompositeLit); !ok {
							r.Fatalf("outbox peer initializer is not a concrete store")
						}
						concreteType := source.Info.TypeOf(concrete)
						if !types.AssignableTo(concreteType, pkg.Info.TypeOf(field)) {
							r.Fatalf("outbox concrete store does not implement the wired peer")
						}
						selected := types.NewMethodSet(concreteType).Lookup(nil, outboxSetterName)
						if selected == nil || selected.Obj().Pkg() == nil {
							r.Fatalf("outbox concrete store setter is unresolved")
						}
						next := selected.Obj().Pkg().Path()
						if owner != "" && owner != next {
							r.Fatalf("outbox constructor returns different store owners")
						}
						owner = next
					}
					if !bound {
						r.Fatalf("outbox constructor does not bind the wired interface peer")
					}
					return false
				})
			}
		}
	}
	if owner == "" {
		r.Fatalf("%s: concrete outbox constructor binding missing", constructor.FullName())
	}
	return owner
}

func outboxLocalInitializer(r *invariants.Run, pkg *invariants.TypedPackage, object types.Object) ast.Expr {
	if object == nil {
		r.Fatalf("outbox binding local is unresolved")
	}
	var initializer ast.Expr
	assignments := 0
	for _, file := range pkg.Syntax() {
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
				if pkg.Info.Defs[ident] != object && pkg.Info.Uses[ident] != object {
					continue
				}
				assignments++
				if len(assignment.Lhs) != len(assignment.Rhs) {
					r.Fatalf("outbox binding requires an explicit initializer")
				}
				initializer = assignment.Rhs[i]
			}
			return true
		})
	}
	if assignments != 1 || initializer == nil {
		r.Fatalf("outbox binding %s must have exactly one initializer, found %d", object.Name(), assignments)
	}
	return initializer
}
