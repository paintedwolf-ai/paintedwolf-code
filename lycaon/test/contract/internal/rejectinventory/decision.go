package rejectinventory

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"

	"golang.org/x/tools/go/ast/astutil"
)

// DecisionContext is one route to an emission: the function holding the
// controlling conditions, the conditions on that route, and the typed
// producers of their inputs. An emission built by a constructor with no
// condition of its own takes its decision from each caller.
type DecisionContext struct {
	Func   string
	Rel    string
	Line   int
	Guards []Guard
	Inputs []Input
}

// Guard is one condition that must hold for control to reach the emission.
type Guard struct {
	Line    int
	Expr    string
	Negated bool
}

// Input is one decision input with the typed identity that produces it.
type Input struct {
	Expr     string
	Producer string
	Resolved bool
}

// Producers returns the resolved producer identities on this route.
func (c DecisionContext) Producers() []string {
	var out []string
	for _, in := range c.Inputs {
		if in.Resolved && !slices.Contains(out, in.Producer) {
			out = append(out, in.Producer)
		}
	}
	return out
}

type condition struct {
	Guard
	expr ast.Expr
}

type callSite struct {
	decl funcDecl
	call *ast.CallExpr
}

const maxLiftDepth = 3

// callSites returns the static calls of d, including calls through an
// interface method that d's receiver type implements.
func (s *scan) callSites(d funcDecl) []callSite {
	sites := slices.Clone(s.callers[funcKey(d)])
	fn, ok := d.pkg.TypesInfo.Defs[d.decl.Name].(*types.Func)
	if !ok {
		return sites
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return sites
	}
	recv := sig.Recv().Type()
	for key, ifaceMethod := range s.interfaceMethods {
		if ifaceMethod.Name() != fn.Name() {
			continue
		}
		iface := ifaceMethod.Type().(*types.Signature).Recv().Type().Underlying().(*types.Interface)
		if types.Implements(recv, iface) {
			sites = append(sites, s.callers[key]...)
		}
	}
	return sites
}

func (s *scan) indexCallers() {
	s.callers = map[string][]callSite{}
	s.interfaceMethods = map[string]*types.Func{}
	for _, d := range s.decls {
		ast.Inspect(d.decl.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if fn := calledFunc(d.pkg, call); fn != nil {
				s.callers[fn.FullName()] = append(s.callers[fn.FullName()], callSite{decl: d, call: call})
				if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
					if _, iface := sig.Recv().Type().Underlying().(*types.Interface); iface {
						s.interfaceMethods[fn.FullName()] = fn
					}
				}
			}
			return true
		})
	}
}

// decisionContexts returns the routes to node in d. Without a condition in d,
// a node returned to the caller lifts to every call site of d.
func (s *scan) decisionContexts(d funcDecl, node ast.Node, em emission, depth int) []DecisionContext {
	path, _ := astutil.PathEnclosingInterval(d.file, node.Pos(), node.End())
	conds := s.guardsOnPath(d, path)
	rel, line := position(d.pkg, s.root, node.Pos())
	key := funcKey(d)
	if len(conds) == 0 && em.kind == KindForwarderCall && em.forwarder != nil {
		// The forwarder's own condition decides the refusal it builds.
		return s.decisionContexts(em.forwarder.decl, em.forwarder.node, emission{}, depth+1)
	}
	if len(conds) == 0 && em.code != nil {
		// A code chosen into a local carries the conditions of each choice.
		conds = s.choiceGuards(d, em.code)
	}
	if len(conds) == 0 && returnedToCaller(path) && depth < maxLiftDepth {
		var out []DecisionContext
		for _, cs := range s.callers[key] {
			out = append(out, s.decisionContexts(cs.decl, cs.call, emission{}, depth+1)...)
		}
		if len(out) > 0 {
			return out
		}
	}
	ctx := DecisionContext{Func: key, Rel: rel, Line: line}
	for _, c := range conds {
		ctx.Guards = append(ctx.Guards, c.Guard)
		for _, in := range s.inputsOf(d, c.expr, 0) {
			if !slices.Contains(ctx.Inputs, in) {
				ctx.Inputs = append(ctx.Inputs, in)
			}
		}
	}
	return []DecisionContext{ctx}
}

// choiceGuards returns the conditions on every assignment of a local code.
func (s *scan) choiceGuards(d funcDecl, code ast.Expr) []condition {
	id, ok := ast.Unparen(code).(*ast.Ident)
	if !ok {
		return nil
	}
	v, ok := d.pkg.TypesInfo.Uses[id].(*types.Var)
	if !ok || isParam(d, v) {
		return nil
	}
	var conds []condition
	ast.Inspect(d.decl.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range assign.Lhs {
			if l, ok := lhs.(*ast.Ident); ok && (d.pkg.TypesInfo.Uses[l] == v || d.pkg.TypesInfo.Defs[l] == v) {
				path, _ := astutil.PathEnclosingInterval(d.file, assign.Pos(), assign.End())
				conds = append(conds, s.guardsOnPath(d, path)...)
			}
		}
		return true
	})
	return conds
}

func funcKey(d funcDecl) string {
	if fn, ok := d.pkg.TypesInfo.Defs[d.decl.Name].(*types.Func); ok {
		return fn.FullName()
	}
	return d.decl.Name.Name
}

// returnedToCaller reports a node inside a return statement of its function,
// not of a nested function literal.
func returnedToCaller(path []ast.Node) bool {
	for _, n := range path {
		switch n.(type) {
		case *ast.ReturnStmt:
			return true
		case *ast.FuncLit, *ast.FuncDecl:
			return false
		}
	}
	return false
}

// guardsOnPath collects, innermost first, the conditions an enclosing if
// requires and the early exits a preceding if in the same block rules out.
func (s *scan) guardsOnPath(d funcDecl, path []ast.Node) []condition {
	var guards []condition
	add := func(cond ast.Expr, negated bool) {
		guards = append(guards, condition{Guard: Guard{
			Line: d.pkg.Fset.Position(cond.Pos()).Line, Expr: types.ExprString(cond), Negated: negated,
		}, expr: cond})
	}
	for i := 1; i < len(path); i++ {
		child := path[i-1]
		switch parent := path[i].(type) {
		case *ast.FuncLit, *ast.FuncDecl:
			return guards
		case *ast.IfStmt:
			switch child {
			case parent.Body:
				add(parent.Cond, false)
			case parent.Else:
				add(parent.Cond, true)
			}
		case *ast.CaseClause:
			for _, e := range parent.List {
				add(e, false)
			}
		case *ast.BlockStmt:
			for _, stmt := range parent.List {
				if stmt == child {
					break
				}
				if ifs, ok := stmt.(*ast.IfStmt); ok && ifs.Else == nil && terminates(ifs.Body) {
					add(ifs.Cond, true)
				}
			}
		}
	}
	return guards
}

func terminates(body *ast.BlockStmt) bool {
	if body == nil || len(body.List) == 0 {
		return false
	}
	switch last := body.List[len(body.List)-1].(type) {
	case *ast.ReturnStmt:
		return true
	case *ast.BranchStmt:
		return last.Tok == token.CONTINUE || last.Tok == token.BREAK || last.Tok == token.GOTO
	case *ast.ExprStmt:
		if call, ok := last.X.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "panic" {
				return true
			}
		}
	}
	return false
}
