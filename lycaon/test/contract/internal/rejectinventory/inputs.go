package rejectinventory

import (
	"go/ast"
	"go/token"
	"go/types"
)

const maxInputDepth = 8

// inputsOf names the typed producers of a condition's operands: parameters,
// struct fields, package variables, and called functions or methods. A call
// of a function value, or a local without a traceable assignment, stays
// unresolved.
func (s *scan) inputsOf(d funcDecl, expr ast.Expr, depth int) []Input {
	info := d.pkg.TypesInfo
	var out []Input
	add := func(e ast.Expr, producer string, resolved bool) {
		out = append(out, Input{Expr: types.ExprString(e), Producer: producer, Resolved: resolved})
	}
	var walk func(e ast.Expr)
	walk = func(e ast.Expr) {
		if e == nil {
			return
		}
		if tv, ok := info.Types[e]; ok && tv.Value != nil {
			return
		}
		switch x := e.(type) {
		case *ast.ParenExpr:
			walk(x.X)
		case *ast.UnaryExpr:
			walk(x.X)
		case *ast.StarExpr:
			walk(x.X)
		case *ast.BinaryExpr:
			walk(x.X)
			walk(x.Y)
		case *ast.IndexExpr:
			walk(x.X)
			walk(x.Index)
		case *ast.SliceExpr:
			walk(x.X)
		case *ast.TypeAssertExpr:
			walk(x.X)
		case *ast.SelectorExpr:
			if sel, ok := info.Selections[x]; ok {
				add(x, selectionProducer(sel), true)
				// The receiver's producer decides which value the field is read from.
				walk(x.X)
				return
			}
			switch obj := info.Uses[x.Sel].(type) {
			case *types.Var:
				add(x, "var "+obj.Pkg().Path()+"."+obj.Name(), true)
			case *types.Func:
				add(x, obj.FullName(), true)
			}
		case *ast.CompositeLit:
			add(x, "literal "+types.TypeString(info.TypeOf(x), nil), true)
			for _, elt := range x.Elts {
				if kv, ok := elt.(*ast.KeyValueExpr); ok {
					walk(kv.Value)
				} else {
					walk(elt)
				}
			}
		case *ast.FuncLit:
			add(x, "function literal", true)
		case *ast.CallExpr:
			out = append(out, s.callInputs(d, x, depth)...)
		case *ast.Ident:
			out = append(out, s.identInputs(d, x, depth)...)
		}
	}
	walk(expr)
	return out
}

func (s *scan) callInputs(d funcDecl, call *ast.CallExpr, depth int) []Input {
	info := d.pkg.TypesInfo
	var out []Input
	operands := func(exprs ...ast.Expr) {
		for _, e := range exprs {
			out = append(out, s.inputsOf(d, e, depth)...)
		}
	}
	if tv, ok := info.Types[call.Fun]; ok && tv.IsType() {
		operands(call.Args...)
		return out
	}
	if id, ok := ast.Unparen(call.Fun).(*ast.Ident); ok {
		if _, builtin := info.Uses[id].(*types.Builtin); builtin {
			operands(call.Args...)
			return out
		}
	}
	expr := types.ExprString(call)
	switch fn := calledFunc(d.pkg, call); {
	case fn != nil:
		out = append(out, Input{Expr: expr, Producer: fn.FullName(), Resolved: true})
	case funcField(info, call.Fun) != nil:
		// An injected function field is the typed decision boundary it names.
		out = append(out, Input{Expr: expr, Producer: selectionProducer(funcField(info, call.Fun)), Resolved: true})
	default:
		if id, ok := ast.Unparen(call.Fun).(*ast.Ident); ok {
			out = append(out, s.identInputs(d, id, depth)...)
		} else {
			out = append(out, Input{Expr: expr, Producer: "call of a function value", Resolved: false})
		}
	}
	if sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr); ok {
		if _, isMethod := info.Selections[sel]; isMethod {
			operands(sel.X)
		}
	}
	operands(call.Args...)
	return out
}

func funcField(info *types.Info, fun ast.Expr) *types.Selection {
	sel, ok := ast.Unparen(fun).(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	if selection, ok := info.Selections[sel]; ok && selection.Kind() == types.FieldVal {
		return selection
	}
	return nil
}

func selectionProducer(sel *types.Selection) string {
	recv := sel.Recv()
	if ptr, ok := recv.(*types.Pointer); ok {
		recv = ptr.Elem()
	}
	name := types.TypeString(recv, nil)
	switch sel.Kind() {
	case types.FieldVal:
		return "field " + name + "." + sel.Obj().Name()
	default:
		if fn, ok := sel.Obj().(*types.Func); ok {
			return fn.FullName()
		}
		return name + "." + sel.Obj().Name()
	}
}

// identInputs resolves a bare identifier: a parameter or receiver of the
// enclosing function, a package variable, or a local traced through its
// assignments.
func (s *scan) identInputs(d funcDecl, id *ast.Ident, depth int) []Input {
	info := d.pkg.TypesInfo
	v, ok := info.Uses[id].(*types.Var)
	if !ok {
		return nil
	}
	expr := types.ExprString(id)
	if v.Parent() == v.Pkg().Scope() {
		return []Input{{Expr: expr, Producer: "var " + v.Pkg().Path() + "." + v.Name(), Resolved: true}}
	}
	if isParam(d, v) {
		return []Input{{Expr: expr, Producer: "param " + funcKey(d) + "." + v.Name(), Resolved: true}}
	}
	if cached, ok := s.locals[v]; ok {
		return cached
	}
	if depth >= maxInputDepth {
		return []Input{{Expr: expr, Producer: "local " + v.Name(), Resolved: false}}
	}
	// A local assigned from itself contributes nothing beyond its other writes.
	s.locals[v] = nil
	out := s.localInputs(d, v, depth)
	if len(out) == 0 {
		out = []Input{{Expr: expr, Producer: "local " + v.Name(), Resolved: false}}
	}
	s.locals[v] = out
	return out
}

func (s *scan) localInputs(d funcDecl, v *types.Var, depth int) []Input {
	info := d.pkg.TypesInfo
	var out []Input
	written := false
	ast.Inspect(d.decl.Body, func(n ast.Node) bool {
		switch st := n.(type) {
		case *ast.CallExpr:
			// A call handed the local's address, such as errors.As, writes it.
			for _, arg := range st.Args {
				if u, ok := arg.(*ast.UnaryExpr); ok && u.Op == token.AND {
					if id, ok := u.X.(*ast.Ident); ok && info.Uses[id] == v {
						written = true
						out = append(out, s.callInputs(d, st, depth+1)...)
					}
				}
			}
		case *ast.AssignStmt:
			for i, lhs := range st.Lhs {
				l, ok := lhs.(*ast.Ident)
				if !ok || (info.Defs[l] != v && info.Uses[l] != v) {
					continue
				}
				rhs := st.Rhs[0]
				if len(st.Rhs) == len(st.Lhs) {
					rhs = st.Rhs[i]
				}
				written = true
				out = append(out, s.inputsOf(d, rhs, depth+1)...)
			}
		case *ast.ValueSpec:
			for i, name := range st.Names {
				if info.Defs[name] == v && i < len(st.Values) {
					written = true
					out = append(out, s.inputsOf(d, st.Values[i], depth+1)...)
				}
			}
		case *ast.RangeStmt:
			for _, bound := range []ast.Expr{st.Key, st.Value} {
				if b, ok := bound.(*ast.Ident); ok && info.Defs[b] == v {
					out = append(out, s.inputsOf(d, st.X, depth+1)...)
				}
			}
		case *ast.TypeSwitchStmt:
			assign, ok := st.Assign.(*ast.AssignStmt)
			if !ok || len(assign.Rhs) != 1 {
				return true
			}
			for _, clause := range st.Body.List {
				if info.Implicits[clause] == v {
					out = append(out, s.inputsOf(d, assign.Rhs[0], depth+1)...)
					break
				}
			}
		}
		return true
	})
	if written && len(out) == 0 {
		return []Input{{Expr: v.Name(), Producer: "constant", Resolved: true}}
	}
	return out
}

func isParam(d funcDecl, v *types.Var) bool {
	fields := []*ast.FieldList{d.decl.Type.Params, d.decl.Recv, d.decl.Type.Results}
	ast.Inspect(d.decl.Body, func(n ast.Node) bool {
		if lit, ok := n.(*ast.FuncLit); ok {
			fields = append(fields, lit.Type.Params)
		}
		return true
	})
	for _, list := range fields {
		if list == nil {
			continue
		}
		for _, f := range list.List {
			for _, name := range f.Names {
				if d.pkg.TypesInfo.Defs[name] == v {
					return true
				}
			}
		}
	}
	return false
}
