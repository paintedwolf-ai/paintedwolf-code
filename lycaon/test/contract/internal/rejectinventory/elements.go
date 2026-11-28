package rejectinventory

import (
	"fmt"
	"go/ast"
	"go/types"
	"slices"
)

func isSequence(t types.Type) bool {
	switch t.Underlying().(type) {
	case *types.Slice, *types.Array:
		return true
	}
	return false
}

// resolveElements resolves the codes a slice or array can hold: literal
// elements, appended values, every write of a slice field, or a local's
// assignments.
func (s *scan) resolveElements(d funcDecl, expr ast.Expr, depth int) resolution {
	if depth > maxResolveDepth {
		return unresolved("producer chain deeper than the inventory follows")
	}
	info := d.pkg.TypesInfo
	expr = ast.Unparen(expr)
	switch e := expr.(type) {
	case *ast.CompositeLit:
		var out resolution
		for _, elt := range e.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				elt = kv.Value
			}
			out.merge(s.resolve(d, elt, depth+1))
		}
		return out
	case *ast.CallExpr:
		if id, ok := ast.Unparen(e.Fun).(*ast.Ident); ok {
			if b, ok := info.Uses[id].(*types.Builtin); ok {
				switch b.Name() {
				case "append":
					out := s.resolveElements(d, e.Args[0], depth+1)
					for _, a := range e.Args[1:] {
						if e.Ellipsis.IsValid() {
							out.merge(s.resolveElements(d, a, depth+1))
						} else {
							out.merge(s.resolve(d, a, depth+1))
						}
					}
					return out
				case "make":
					return resolution{}
				}
			}
		}
		return s.resolveCallElements(d, e, depth)
	case *ast.SelectorExpr:
		if sel, ok := info.Selections[e]; ok && sel.Kind() == types.FieldVal {
			return s.resolveSliceField(declaringType(sel), e.Sel.Name)
		}
	case *ast.Ident:
		if e.Name == "nil" {
			return resolution{}
		}
		return s.resolveLocalElements(d, e, depth)
	}
	return unresolved("sequence has no typed producer")
}

func (s *scan) resolveCallElements(d funcDecl, call *ast.CallExpr, depth int) resolution {
	fn := calledFunc(d.pkg, call)
	if fn == nil {
		return unresolved("sequence from a function value")
	}
	callee, ok := s.decls[fn.FullName()]
	if !ok {
		return unresolved("sequence from " + fn.FullName() + " outside the scanned packages")
	}
	var out resolution
	ast.Inspect(callee.decl.Body, func(n ast.Node) bool {
		if _, isLit := n.(*ast.FuncLit); isLit {
			return false
		}
		if ret, ok := n.(*ast.ReturnStmt); ok && len(ret.Results) > 0 {
			out.merge(s.resolveElements(callee, ret.Results[0], depth+1))
		}
		return true
	})
	return out
}

func (s *scan) resolveSliceField(tn *types.TypeName, field string) resolution {
	if tn == nil || !s.scansPackage(tn.Pkg()) {
		return unresolved("sequence field outside the scanned packages")
	}
	if key := typeKey(tn) + "." + field; slices.Contains(s.relays, key) {
		return resolution{outcome: OutcomeForwardedCarrier, upstream: key}
	}
	return s.node("elements "+typeKey(tn)+"."+field, func() resolution {
		var out resolution
		for _, w := range s.writesOf(tn, field) {
			out.merge(s.resolveElements(w.decl, w.value, 0))
		}
		return out
	})
}

func (s *scan) resolveLocalElements(d funcDecl, id *ast.Ident, depth int) resolution {
	info := d.pkg.TypesInfo
	v, ok := info.Uses[id].(*types.Var)
	if !ok {
		return unresolved("sequence identifier is not a variable")
	}
	var out resolution
	assigned := false
	ast.Inspect(d.decl.Body, func(n ast.Node) bool {
		switch st := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range st.Lhs {
				l, ok := lhs.(*ast.Ident)
				if !ok || (info.Defs[l] != v && info.Uses[l] != v) || len(st.Rhs) != len(st.Lhs) {
					continue
				}
				assigned = true
				out.merge(s.resolveElements(d, st.Rhs[i], depth+1))
			}
		case *ast.ValueSpec:
			for i, name := range st.Names {
				if info.Defs[name] == v {
					assigned = true
					if i < len(st.Values) {
						out.merge(s.resolveElements(d, st.Values[i], depth+1))
					}
				}
			}
		}
		return true
	})
	if !assigned {
		if idx, ok := paramIndex(d.pkg, d.decl, id); ok {
			return s.resolveParamElements(d, idx)
		}
		return unresolved("sequence local is never assigned")
	}
	return out
}

func (s *scan) resolveParamElements(d funcDecl, idx int) resolution {
	return s.node(fmt.Sprintf("elements %s#%d", funcKey(d), idx), func() resolution {
		sites := s.callers[funcKey(d)]
		if len(sites) == 0 {
			return unresolved("sequence parameter of " + funcKey(d) + " has no static caller")
		}
		var out resolution
		for _, cs := range sites {
			if idx >= len(cs.call.Args) {
				out.merge(unresolved("short call of " + funcKey(d)))
				continue
			}
			out.merge(s.resolveElements(cs.decl, cs.call.Args[idx], 0))
		}
		return out
	})
}
