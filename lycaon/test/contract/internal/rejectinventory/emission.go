package rejectinventory

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/pkg/api"
)

// emission is one recognized code-carrying node.
type emission struct {
	kind      Kind
	code      ast.Expr
	authority bool
	forwarder *forwarder
}

// emissionOf returns the code expression of an emission node.
func (s *scan) emissionOf(p *packages.Package, n ast.Node) (emission, bool) {
	switch node := n.(type) {
	case *ast.CompositeLit:
		tv, ok := p.TypesInfo.Types[node]
		if !ok || !s.isCarrier(tv.Type) {
			return emission{}, false
		}
		var code ast.Expr
		authority := false
		for _, elt := range node.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			switch key.Name {
			case "Code":
				code = kv.Value
			case "FailureClass":
				authority = authorityClass(p, kv.Value)
			}
		}
		return emission{kind: KindCarrierLiteral, code: code, authority: authority}, code != nil
	case *ast.AssignStmt:
		if len(node.Lhs) != 1 || len(node.Rhs) != 1 {
			return emission{}, false
		}
		sel, ok := node.Lhs[0].(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Code" {
			return emission{}, false
		}
		if tv, ok := p.TypesInfo.Types[sel.X]; ok && s.isCarrier(tv.Type) {
			return emission{kind: KindCarrierAssign, code: node.Rhs[0]}, true
		}
	case *ast.CallExpr:
		fn := calledFunc(p, node)
		if fn == nil {
			return emission{}, false
		}
		if fn.Pkg() != nil && fn.Pkg().Path() == safecmdPath && fn.Name() == "Reject" && len(node.Args) > 0 {
			return emission{kind: KindSafecmdReject, code: node.Args[0]}, true
		}
		if fw, ok := s.forwarders[fn.FullName()]; ok && fw.index < len(node.Args) {
			return emission{kind: KindForwarderCall, code: node.Args[fw.index], authority: fw.authority, forwarder: &fw}, true
		}
	}
	return emission{}, false
}

// authorityClass reports a FailureClass value naming a policy or isolation refusal.
func authorityClass(p *packages.Package, expr ast.Expr) bool {
	tv, ok := p.TypesInfo.Types[expr]
	if !ok || tv.Value == nil {
		return false
	}
	class := strings.Trim(tv.Value.ExactString(), `"`)
	return class == api.FailureClassPolicyRejection || class == api.FailureClassIsolationRejection
}

func authorityCode(code string) bool {
	_, ok := isolation.LookupRejection(code)
	return ok
}

func calledFunc(p *packages.Package, call *ast.CallExpr) *types.Func {
	var id *ast.Ident
	switch f := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		id = f
	case *ast.SelectorExpr:
		id = f.Sel
	case *ast.IndexExpr:
		return calledFunc(p, &ast.CallExpr{Fun: f.X})
	default:
		return nil
	}
	fn, _ := p.TypesInfo.Uses[id].(*types.Func)
	if fn != nil {
		return fn.Origin()
	}
	return nil
}

func paramIndex(p *packages.Package, fd *ast.FuncDecl, expr ast.Expr) (int, bool) {
	id, ok := ast.Unparen(stripConversion(p, expr)).(*ast.Ident)
	if !ok {
		return 0, false
	}
	obj := p.TypesInfo.Uses[id]
	i := 0
	for _, field := range fd.Type.Params.List {
		if len(field.Names) == 0 {
			i++
			continue
		}
		for _, name := range field.Names {
			if obj != nil && p.TypesInfo.Defs[name] == obj {
				return i, true
			}
			i++
		}
	}
	return 0, false
}

// stripConversion removes a type conversion such as api.ErrorCode(code).
func stripConversion(p *packages.Package, expr ast.Expr) ast.Expr {
	call, ok := ast.Unparen(expr).(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return expr
	}
	if tv, ok := p.TypesInfo.Types[call.Fun]; ok && tv.IsType() {
		return call.Args[0]
	}
	return expr
}

func (s *scan) findForwarders() {
	for changed := true; changed; {
		changed = false
		for key, d := range s.decls {
			if _, known := s.forwarders[key]; known {
				continue
			}
			ast.Inspect(d.decl.Body, func(n ast.Node) bool {
				if _, found := s.forwarders[key]; found {
					return false
				}
				em, ok := s.emissionOf(d.pkg, n)
				if !ok {
					return true
				}
				if idx, isParam := paramIndex(d.pkg, d.decl, em.code); isParam {
					s.forwarders[key] = forwarder{index: idx, authority: em.authority, decl: d, node: n}
					changed = true
					return false
				}
				return true
			})
		}
	}
}
