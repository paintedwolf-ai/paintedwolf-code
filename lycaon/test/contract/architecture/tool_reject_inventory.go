package contract

// Type-checked inventory of structured reject emission sites, so every code a
// production handler can emit is registry-backed and renders as a structured
// Rejected:/Code: block.

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/types"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/tools/go/packages"

	"github.com/lycaon/lycaon/test/contract/internal/guidancescan"
)

// toolRejectSite is one emission of a resolved code.
type toolRejectSite struct {
	Code string
	Rel  string
	Line int
}

// unresolvedRejectSite is an emission whose code is not a constant, a
// forwarded parameter, an upstream code-carrying value, or a local or function
// result that only ever holds constants.
type unresolvedRejectSite struct {
	Rel  string
	Line int
	Func string
	Expr string
}

type toolRejectInventory struct {
	Sites      []toolRejectSite
	Unresolved []unresolvedRejectSite
}

var toolRejectInventoryCache sync.Map // abs lycaon root → *toolRejectInventoryEntry

type toolRejectInventoryEntry struct {
	once sync.Once
	inv  toolRejectInventory
	err  error
}

// scanToolRejectEmission inventories reject emissions under lycaon/internal.
func scanToolRejectEmission(lycaonRoot string) (toolRejectInventory, error) {
	abs, err := filepath.Abs(lycaonRoot)
	if err != nil {
		return toolRejectInventory{}, err
	}
	raw, _ := toolRejectInventoryCache.LoadOrStore(abs, &toolRejectInventoryEntry{})
	entry := raw.(*toolRejectInventoryEntry)
	entry.once.Do(func() { entry.inv, entry.err = buildToolRejectInventory(abs) })
	return entry.inv, entry.err
}

type rejectScan struct {
	root     string
	pkgs     []*packages.Package
	carriers map[*types.TypeName]bool
	// forwarders maps a function to the parameter position it hands on as a code.
	forwarders map[*types.Func]int
	decls      map[*types.Func]funcDecl
}

type funcDecl struct {
	pkg  *packages.Package
	decl *ast.FuncDecl
}

const safecmdPath = "github.com/lycaon/lycaon/internal/tools/safecmd"

func buildToolRejectInventory(lycaonRoot string) (toolRejectInventory, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports,
		Dir: lycaonRoot,
	}
	pkgs, err := packages.Load(cfg, "./internal/...")
	if err != nil {
		return toolRejectInventory{}, err
	}
	s := &rejectScan{root: lycaonRoot, carriers: map[*types.TypeName]bool{}, forwarders: map[*types.Func]int{}, decls: map[*types.Func]funcDecl{}}
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			return toolRejectInventory{}, fmt.Errorf("load %s: %v", p.PkgPath, p.Errors[0])
		}
		s.pkgs = append(s.pkgs, p)
		s.collectCarriers(p)
		for _, f := range p.Syntax {
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
					if fn, ok := p.TypesInfo.Defs[fd.Name].(*types.Func); ok {
						s.decls[fn] = funcDecl{pkg: p, decl: fd}
					}
				}
			}
		}
	}
	s.findForwarders()
	return s.inventory(), nil
}

// collectCarriers finds reject carriers: struct types with Code and Data
// fields whose value or pointer has an Error method.
func (s *rejectScan) collectCarriers(p *packages.Package) {
	scope := p.Types.Scope()
	for _, name := range scope.Names() {
		tn, ok := scope.Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}
		st, ok := tn.Type().Underlying().(*types.Struct)
		if !ok || !hasStructField(st, "Code") || !hasStructField(st, "Data") {
			continue
		}
		if ms := types.NewMethodSet(types.NewPointer(tn.Type())); ms.Lookup(p.Types, "Error") != nil {
			s.carriers[tn] = true
		}
	}
}

func hasStructField(st *types.Struct, name string) bool {
	for i := 0; i < st.NumFields(); i++ {
		if st.Field(i).Name() == name {
			return true
		}
	}
	return false
}

func namedOf(t types.Type) *types.TypeName {
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	if named, ok := t.(*types.Named); ok {
		return named.Obj()
	}
	return nil
}

// codeArg returns the code expression of an emission node.
func (s *rejectScan) codeArg(p *packages.Package, n ast.Node) (ast.Expr, bool) {
	switch node := n.(type) {
	case *ast.CompositeLit:
		tv, ok := p.TypesInfo.Types[node]
		if !ok || !s.carriers[namedOf(tv.Type)] {
			return nil, false
		}
		for _, elt := range node.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Code" {
					return kv.Value, true
				}
			}
		}
		return nil, false
	case *ast.CallExpr:
		fn := calledFunc(p, node)
		if fn == nil {
			return nil, false
		}
		if fn.Pkg() != nil && fn.Pkg().Path() == safecmdPath && fn.Name() == "Reject" && len(node.Args) > 0 {
			return node.Args[0], true
		}
		if idx, ok := s.forwarders[fn]; ok && idx < len(node.Args) {
			return node.Args[idx], true
		}
	}
	return nil, false
}

func calledFunc(p *packages.Package, call *ast.CallExpr) *types.Func {
	var id *ast.Ident
	switch f := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		id = f
	case *ast.SelectorExpr:
		id = f.Sel
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

func (s *rejectScan) findForwarders() {
	for changed := true; changed; {
		changed = false
		for fn, d := range s.decls {
			if _, known := s.forwarders[fn]; known {
				continue
			}
			ast.Inspect(d.decl.Body, func(n ast.Node) bool {
				if _, found := s.forwarders[fn]; found {
					return false
				}
				arg, ok := s.codeArg(d.pkg, n)
				if !ok {
					return true
				}
				if idx, isParam := paramIndex(d.pkg, d.decl, arg); isParam {
					s.forwarders[fn] = idx
					changed = true
					return false
				}
				return true
			})
		}
	}
}

func (s *rejectScan) inventory() toolRejectInventory {
	var inv toolRejectInventory
	for fn, d := range s.decls {
		_, isForwarder := s.forwarders[fn]
		rel, _ := filepath.Rel(s.root, d.pkg.Fset.Position(d.decl.Pos()).Filename)
		if !strings.HasPrefix(rel, "internal/") {
			continue
		}
		ast.Inspect(d.decl.Body, func(n ast.Node) bool {
			arg, ok := s.codeArg(d.pkg, n)
			if !ok {
				return true
			}
			if _, isParam := paramIndex(d.pkg, d.decl, arg); isParam && isForwarder {
				return true
			}
			line := d.pkg.Fset.Position(n.Pos()).Line
			codes, outcome := s.resolve(d.pkg, d.decl, arg, 0)
			switch outcome {
			case resolvedCodes:
				for _, code := range codes {
					if guidancescan.HintCodeShape.MatchString(code) {
						inv.Sites = append(inv.Sites, toolRejectSite{Code: code, Rel: rel, Line: line})
					}
				}
			case unresolvedCode:
				inv.Unresolved = append(inv.Unresolved, unresolvedRejectSite{Rel: rel, Line: line, Func: d.decl.Name.Name, Expr: types.ExprString(arg)})
			}
			return true
		})
	}
	return inv
}

type resolution int

const (
	resolvedCodes resolution = iota
	forwardedCode
	unresolvedCode
)

const maxResolveDepth = 4

// resolve finds the constant codes expr can hold. A read of a Code field or
// method on another value forwards that value's code, whose origin is a
// separate emission.
func (s *rejectScan) resolve(p *packages.Package, fd *ast.FuncDecl, expr ast.Expr, depth int) ([]string, resolution) {
	if depth > maxResolveDepth {
		return nil, unresolvedCode
	}
	expr = ast.Unparen(stripConversion(p, expr))
	if tv, ok := p.TypesInfo.Types[expr]; ok && tv.Value != nil && tv.Value.Kind() == constant.String {
		return []string{constant.StringVal(tv.Value)}, resolvedCodes
	}
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		if sel, ok := p.TypesInfo.Selections[e]; ok && (sel.Kind() == types.FieldVal || sel.Kind() == types.MethodVal) {
			return nil, forwardedCode
		}
	case *ast.CallExpr:
		fn := calledFunc(p, e)
		if callee, ok := s.decls[fn]; fn != nil && ok {
			return s.resolveReturns(callee, depth+1)
		}
		if sel, ok := ast.Unparen(e.Fun).(*ast.SelectorExpr); ok {
			if selection, ok := p.TypesInfo.Selections[sel]; ok && selection.Kind() == types.MethodVal {
				return nil, forwardedCode
			}
		}
		return nil, unresolvedCode
	case *ast.Ident:
		return s.resolveLocal(p, fd, e, depth+1)
	}
	return nil, unresolvedCode
}

func (s *rejectScan) resolveReturns(d funcDecl, depth int) ([]string, resolution) {
	var codes []string
	outcome := resolvedCodes
	ast.Inspect(d.decl.Body, func(n ast.Node) bool {
		if _, isLit := n.(*ast.FuncLit); isLit {
			return false
		}
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) == 0 {
			return true
		}
		got, r := s.resolve(d.pkg, d.decl, ret.Results[0], depth)
		codes = append(codes, got...)
		outcome = max(outcome, r)
		return true
	})
	return codes, outcome
}

func (s *rejectScan) resolveLocal(p *packages.Package, fd *ast.FuncDecl, id *ast.Ident, depth int) ([]string, resolution) {
	obj, ok := p.TypesInfo.Uses[id].(*types.Var)
	if !ok || obj.Parent() == nil || obj.Parent() == obj.Pkg().Scope() {
		return nil, unresolvedCode
	}
	var codes []string
	outcome := resolvedCodes
	assigned := false
	note := func(rhs ast.Expr) {
		assigned = true
		got, r := s.resolve(p, fd, rhs, depth)
		codes = append(codes, got...)
		outcome = max(outcome, r)
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch st := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range st.Lhs {
				l, ok := lhs.(*ast.Ident)
				if !ok || (p.TypesInfo.Defs[l] != obj && p.TypesInfo.Uses[l] != obj) {
					continue
				}
				if len(st.Rhs) == len(st.Lhs) {
					note(st.Rhs[i])
				} else {
					assigned, outcome = true, unresolvedCode
				}
			}
		case *ast.ValueSpec:
			for i, name := range st.Names {
				if p.TypesInfo.Defs[name] == obj && i < len(st.Values) {
					note(st.Values[i])
				}
			}
		case *ast.RangeStmt:
			if v, ok := st.Value.(*ast.Ident); ok && p.TypesInfo.Defs[v] == obj {
				assigned, outcome = true, unresolvedCode
			}
		}
		return true
	})
	if !assigned {
		return nil, unresolvedCode
	}
	return codes, outcome
}
