package rejectinventory

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Outcome classifies how an emission's code was established.
type Outcome int

const (
	OutcomeResolved Outcome = iota
	// OutcomeForwardedCarrier reads the code of another carrier value, whose
	// origin is that carrier's own emission.
	OutcomeForwardedCarrier
	OutcomeFamily
	OutcomeUnresolved
)

func (o Outcome) String() string {
	switch o {
	case OutcomeResolved:
		return "resolved"
	case OutcomeForwardedCarrier:
		return "forwarded_carrier"
	case OutcomeFamily:
		return "family"
	default:
		return "unresolved"
	}
}

type resolution struct {
	codes    []string
	outcome  Outcome
	upstream string
	reason   string
}

func unresolved(reason string) resolution {
	return resolution{outcome: OutcomeUnresolved, reason: reason}
}

func (r *resolution) merge(other resolution) {
	if len(other.codes) > 0 {
		seen := make(map[string]bool, len(r.codes))
		for _, code := range r.codes {
			seen[code] = true
		}
		codes := slices.Clone(r.codes)
		for _, code := range other.codes {
			if !seen[code] {
				seen[code] = true
				codes = append(codes, code)
			}
		}
		r.codes = codes
	}
	if other.outcome > r.outcome {
		r.outcome, r.reason = other.outcome, other.reason
	}
	if r.upstream == "" {
		r.upstream = other.upstream
	}
}

const maxResolveDepth = 12

// resolve finds the codes expr can hold, following the upstream typed producer
// of a dynamic value: a local's assignments, a function's returns, a constant
// map, or every producer of a non-carrier field.
func (s *scan) resolve(d funcDecl, expr ast.Expr, depth int) resolution {
	p := d.pkg
	if depth > maxResolveDepth {
		rel, line := position(p, s.root, expr.Pos())
		return unresolved(fmt.Sprintf("producer chain deeper than the inventory follows at %s:%d", rel, line))
	}
	expr = ast.Unparen(stripConversion(p, expr))
	if tv, ok := p.TypesInfo.Types[expr]; ok && tv.Value != nil && tv.Value.Kind() == constant.String {
		return resolution{codes: []string{constant.StringVal(tv.Value)}}
	}
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		if sel, ok := p.TypesInfo.Selections[e]; ok && sel.Kind() == types.FieldVal {
			return s.resolveFieldRead(declaringType(sel), e.Sel.Name, depth)
		}
	case *ast.IndexExpr:
		if tv, ok := p.TypesInfo.Types[e.X]; ok && isSequence(tv.Type) {
			return s.resolveElements(d, e.X, depth+1)
		}
		r := s.resolveMapValues(p, e)
		if r.outcome == OutcomeUnresolved {
			rel, line := position(p, s.root, e.Pos())
			r.reason = fmt.Sprintf("%s (%s at %s:%d)", r.reason, types.ExprString(e), rel, line)
		}
		return r
	case *ast.BinaryExpr:
		// A constant prefix joined to a value an external peer declares
		// names an open code family, such as MCP_SERVER_<server code>.
		if tv, ok := p.TypesInfo.Types[e.X]; ok && e.Op == token.ADD && tv.Value != nil && tv.Value.Kind() == constant.String {
			return resolution{outcome: OutcomeFamily, upstream: constant.StringVal(tv.Value)}
		}
	case *ast.CallExpr:
		return s.resolveCall(d, e, 0, depth)
	case *ast.Ident:
		return s.resolveLocal(d, e, depth+1)
	}
	return unresolved("expression has no typed producer")
}

// resolveCall resolves result index of a call to a module function or method.
func (s *scan) resolveCall(d funcDecl, call *ast.CallExpr, index, depth int) resolution {
	p := d.pkg
	fn := calledFunc(p, call)
	if fn == nil {
		return unresolved("call of a function value")
	}
	if callee, ok := s.decls[fn.FullName()]; ok {
		return s.resolveReturns(callee, index, depth+1)
	}
	if index == 0 && len(call.Args) == 1 && stringToString(fn) {
		return s.resolve(d, call.Args[0], depth+1)
	}
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		if iface, ok := sig.Recv().Type().Underlying().(*types.Interface); ok {
			return s.resolveImplementations(iface, fn, index, depth)
		}
	}
	return unresolved("callee " + fn.FullName() + " is outside the scanned packages")
}

// resolveImplementations resolves an interface method through the method of
// every scanned named type that implements the interface. An implementation
// outside the scanned packages leaves the call unresolved.
func (s *scan) resolveImplementations(iface *types.Interface, method *types.Func, index, depth int) resolution {
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
			r := s.resolveReturns(decl, index, depth+1)
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

// resolveFieldRead resolves a field read. A carrier's code forwards that
// carrier's emission; any other struct field resolves through every producer
// that writes it.
func (s *scan) resolveFieldRead(tn *types.TypeName, field string, depth int) resolution {
	if tn == nil {
		return unresolved("field read on an unnamed type")
	}
	if field == "Code" && s.carriers[typeKey(tn)] {
		return resolution{outcome: OutcomeForwardedCarrier, upstream: typeKey(tn)}
	}
	return s.resolveFieldProducers(tn, field)
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

// resolveReturns resolves result index of every return in d.
func (s *scan) resolveReturns(d funcDecl, index, _ int) resolution {
	return s.node(fmt.Sprintf("returns %s#%d", funcKey(d), index), func() resolution { return s.returnsOf(d, index) })
}

func (s *scan) returnsOf(d funcDecl, index int) resolution {
	depth := 0
	var out resolution
	found := false
	ast.Inspect(d.decl.Body, func(n ast.Node) bool {
		if _, isLit := n.(*ast.FuncLit); isLit {
			return false
		}
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		found = true
		switch {
		case len(ret.Results) == 0:
			out.merge(unresolved("bare return of a named result"))
		case index < len(ret.Results) && (len(ret.Results) > 1 || index == 0):
			out.merge(s.resolve(d, ret.Results[index], depth))
		case len(ret.Results) == 1:
			if call, ok := ast.Unparen(ret.Results[0]).(*ast.CallExpr); ok {
				out.merge(s.resolveCall(d, call, index, depth))
			} else {
				out.merge(unresolved("tuple return without a call"))
			}
		default:
			out.merge(unresolved("return arity does not match the result read"))
		}
		return true
	})
	if !found {
		return unresolved("function has no return")
	}
	return out
}

func (s *scan) resolveLocal(d funcDecl, id *ast.Ident, depth int) resolution {
	p := d.pkg
	obj, ok := p.TypesInfo.Uses[id].(*types.Var)
	if !ok || obj.Parent() == nil || obj.Parent() == obj.Pkg().Scope() {
		return unresolved("identifier is not a local")
	}
	return s.node(fmt.Sprintf("local %s@%d", funcKey(d), obj.Pos()), func() resolution {
		return s.localValues(d, id, obj, depth)
	})
}

// localValues merges every assignment of a local; a parameter also holds the
// argument of each call site.
func (s *scan) localValues(d funcDecl, id *ast.Ident, obj *types.Var, depth int) resolution {
	p, fd := d.pkg, d.decl
	var out resolution
	assigned := false
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch st := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range st.Lhs {
				l, ok := lhs.(*ast.Ident)
				if !ok || (p.TypesInfo.Defs[l] != obj && p.TypesInfo.Uses[l] != obj) {
					continue
				}
				assigned = true
				switch {
				case len(st.Rhs) == len(st.Lhs):
					out.merge(s.resolve(d, st.Rhs[i], depth))
				case len(st.Rhs) == 1:
					out.merge(s.resolveTuple(d, st.Rhs[0], i, depth))
				default:
					out.merge(unresolved("unbalanced assignment"))
				}
			}
		case *ast.ValueSpec:
			for i, name := range st.Names {
				if p.TypesInfo.Defs[name] == obj && i < len(st.Values) {
					assigned = true
					out.merge(s.resolve(d, st.Values[i], depth))
				}
			}
		case *ast.RangeStmt:
			if v, ok := st.Value.(*ast.Ident); ok && p.TypesInfo.Defs[v] == obj {
				assigned = true
				if tv, ok := p.TypesInfo.Types[st.X]; ok && isSequence(tv.Type) {
					out.merge(s.resolveElements(d, st.X, depth+1))
				} else {
					out.merge(unresolved("range value over a non-sequence"))
				}
			}
		}
		return true
	})
	if idx, ok := paramIndex(p, fd, id); ok {
		out.merge(s.resolveParam(d, idx))
		return out
	}
	if !assigned {
		return unresolved("local is never assigned a code")
	}
	return out
}

// resolveParam resolves a parameter through the argument at every call site.
func (s *scan) resolveParam(d funcDecl, idx int) resolution {
	return s.node(fmt.Sprintf("param %s#%d", funcKey(d), idx), func() resolution {
		sites := s.callers[funcKey(d)]
		if len(sites) == 0 {
			return unresolved("parameter of " + funcKey(d) + " has no static caller")
		}
		var out resolution
		for _, cs := range sites {
			if idx >= len(cs.call.Args) || cs.call.Ellipsis.IsValid() {
				out.merge(unresolved("variadic or short call of " + funcKey(d)))
				continue
			}
			r := s.resolve(cs.decl, cs.call.Args[idx], 0)
			if r.outcome == OutcomeUnresolved {
				rel, line := position(cs.decl.pkg, s.root, cs.call.Pos())
				r.reason = fmt.Sprintf("%s (caller at %s:%d)", r.reason, rel, line)
			}
			out.merge(r)
		}
		return out
	})
}

// declaringType returns the struct that declares a selected field, following
// embedded fields.
func declaringType(sel *types.Selection) *types.TypeName {
	t := sel.Recv()
	index := sel.Index()
	for _, i := range index[:len(index)-1] {
		if ptr, ok := t.(*types.Pointer); ok {
			t = ptr.Elem()
		}
		st, ok := t.Underlying().(*types.Struct)
		if !ok {
			return nil
		}
		t = st.Field(i).Type()
	}
	return namedOf(t)
}

// resolveTuple resolves element i of a multi-value right-hand side: a comma-ok
// map lookup or a call to a module function.
func (s *scan) resolveTuple(d funcDecl, rhs ast.Expr, i, depth int) resolution {
	switch e := ast.Unparen(rhs).(type) {
	case *ast.IndexExpr:
		if i == 0 {
			return s.resolveMapValues(d.pkg, e)
		}
	case *ast.CallExpr:
		return s.resolveCall(d, e, i, depth)
	}
	return unresolved("tuple element has no typed producer")
}
