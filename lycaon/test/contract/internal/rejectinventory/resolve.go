package rejectinventory

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"slices"
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
			return s.resolveFieldRead(declaringType(sel), e.Sel.Name)
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
		return s.resolveReturns(callee, index)
	}
	if index == 0 && len(call.Args) == 1 && stringToString(fn) {
		return s.resolve(d, call.Args[0], depth+1)
	}
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		if iface, ok := sig.Recv().Type().Underlying().(*types.Interface); ok {
			return s.resolveImplementations(iface, fn, index)
		}
	}
	return unresolved("callee " + fn.FullName() + " is outside the scanned packages")
}

// resolveFieldRead resolves a field read. A carrier's code forwards that
// carrier's emission; any other struct field resolves through every producer
// that writes it.
func (s *scan) resolveFieldRead(tn *types.TypeName, field string) resolution {
	if tn == nil {
		return unresolved("field read on an unnamed type")
	}
	if field == "Code" && s.carriers[typeKey(tn)] {
		return resolution{outcome: OutcomeForwardedCarrier, upstream: typeKey(tn)}
	}
	return s.resolveFieldProducers(tn, field)
}

// resolveReturns resolves result index of every return in d.
func (s *scan) resolveReturns(d funcDecl, index int) resolution {
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
		sites := s.callSites(d)
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
