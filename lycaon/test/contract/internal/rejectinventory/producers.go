package rejectinventory

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/types"
	"strings"

	"golang.org/x/tools/go/packages"
)

// resolveImplementations resolves an interface method through the method of
// every scanned named type that implements the interface. An implementation
// outside the scanned packages leaves the call unresolved.
func (s *scan) resolveImplementations(iface *types.Interface, method *types.Func, index int) resolution {
	var out resolution
	found := false
	for _, p := range s.pkgs {
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || tn.IsAlias() {
				continue
			}
			if _, isIface := tn.Type().Underlying().(*types.Interface); isIface {
				continue
			}
			ptr := types.NewPointer(tn.Type())
			if !types.Implements(tn.Type(), iface) && !types.Implements(ptr, iface) {
				continue
			}
			sel := types.NewMethodSet(ptr).Lookup(method.Pkg(), method.Name())
			if sel == nil {
				continue
			}
			impl, ok := sel.Obj().(*types.Func)
			if !ok {
				continue
			}
			decl, ok := s.decls[impl.FullName()]
			if !ok {
				out.merge(unresolved("implementation " + impl.FullName() + " has no scanned body"))
				continue
			}
			found = true
			r := s.resolveReturns(decl, index)
			if r.outcome == OutcomeUnresolved {
				r.reason = fmt.Sprintf("%s (implementation %s)", r.reason, impl.FullName())
			}
			out.merge(r)
		}
	}
	if !found {
		return unresolved("interface method " + method.FullName() + " has no scanned implementation")
	}
	return out
}

// stringToString reports a function outside the module that maps one string
// to another, such as strings.TrimSpace; it keeps its argument's origin.
func stringToString(fn *types.Func) bool {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() != nil || sig.Params().Len() != 1 || sig.Results().Len() != 1 {
		return false
	}
	isString := func(t types.Type) bool {
		b, ok := t.Underlying().(*types.Basic)
		return ok && b.Info()&types.IsString != 0
	}
	return fn.Pkg() != nil && !strings.HasPrefix(fn.Pkg().Path(), "github.com/lycaon/") &&
		isString(sig.Params().At(0).Type()) && isString(sig.Results().At(0).Type())
}

// resolveMapValues resolves a lookup in a package-level map whose literal
// initializer holds only constant values.
func (s *scan) resolveMapValues(p *packages.Package, e *ast.IndexExpr) resolution {
	id, ok := ast.Unparen(e.X).(*ast.Ident)
	if !ok {
		return unresolved("index into a non-identifier")
	}
	obj, ok := p.TypesInfo.Uses[id].(*types.Var)
	if !ok || obj.Parent() != obj.Pkg().Scope() {
		return unresolved("index into a non-package map")
	}
	for _, f := range p.Syntax {
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if p.TypesInfo.Defs[name] != obj || i >= len(vs.Values) {
						continue
					}
					return constMapValues(p, vs.Values[i])
				}
			}
		}
	}
	return unresolved("map has no literal initializer")
}

func constMapValues(p *packages.Package, init ast.Expr) resolution {
	lit, ok := ast.Unparen(init).(*ast.CompositeLit)
	if !ok {
		return unresolved("map initializer is not a literal")
	}
	var codes []string
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			return unresolved("map literal element without key")
		}
		tv, ok := p.TypesInfo.Types[kv.Value]
		if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
			return unresolved("map literal holds a non-constant value")
		}
		codes = append(codes, constant.StringVal(tv.Value))
	}
	return resolution{codes: codes}
}
