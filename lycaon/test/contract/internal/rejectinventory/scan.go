// Package rejectinventory is a type-checked inventory of structured reject
// emission sites: the codes each site can emit, the decision that controls it,
// and the typed producers of that decision's inputs.
package rejectinventory

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"golang.org/x/tools/go/packages"

	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/guidancescan"
)

// Kind names the syntactic form of an emission.
type Kind string

const (
	KindCarrierLiteral Kind = "carrier_literal"
	KindCarrierAssign  Kind = "carrier_code_assign"
	KindForwarderCall  Kind = "forwarder_call"
	KindSafecmdReject  Kind = "safecmd_reject"
)

// Site is one emission whose code is resolved, an open family, or read from
// another carrier value.
type Site struct {
	Codes     []string
	Outcome   Outcome
	Kind      Kind
	Rel       string
	Line      int
	Func      string
	Upstream  string
	Authority bool
	Decisions []DecisionContext
}

// Unresolved is an emission whose code has no finite typed producer.
type Unresolved struct {
	Rel    string
	Line   int
	Func   string
	Expr   string
	Kind   Kind
	Reason string
}

// Inventory lists every recognized emission under the scanned packages.
type Inventory struct {
	Sites      []Site
	Unresolved []Unresolved
}

// Enumerator lists the finite codes a serialized-data producer can hold,
// using that data's production loader.
type Enumerator func() ([]string, error)

// Options selects the scanned packages and the loaders for codes authored in
// serialized data, keyed by the declaring field ("<pkg path>.<Type>.<Field>").
type Options struct {
	Patterns []string
	Decoded  map[string]Enumerator
}

var cache sync.Map // root|patterns|decoded keys → *cacheEntry

type cacheEntry struct {
	once sync.Once
	inv  Inventory
	err  error
}

// Scan inventories reject emissions in the packages opts selects, relative to
// root. The result is cached per root, pattern set and decoded producer set.
func Scan(root string, opts Options) (Inventory, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return Inventory{}, err
	}
	decoded := make([]string, 0, len(opts.Decoded))
	for key := range opts.Decoded {
		decoded = append(decoded, key)
	}
	slices.Sort(decoded)
	raw, _ := cache.LoadOrStore(abs+"|"+strings.Join(opts.Patterns, ",")+"|"+strings.Join(decoded, ","), &cacheEntry{})
	entry := raw.(*cacheEntry)
	entry.once.Do(func() { entry.inv, entry.err = build(abs, opts) })
	return entry.inv, entry.err
}

type scan struct {
	root     string
	pkgs     []*packages.Package
	carriers map[string]bool
	// forwarders maps a function to the parameter position it hands on as a code.
	forwarders  map[string]forwarder
	decls       map[string]funcDecl
	callers     map[string][]callSite
	fieldWrites map[string][]fieldWrite
	memo        map[string]resolution
	active      map[string]int
	lowHit      int
	locals      map[*types.Var][]Input
	decoded     map[string]Enumerator
}

type forwarder struct {
	index     int
	authority bool
	decl      funcDecl
	node      ast.Node
}

type funcDecl struct {
	pkg  *packages.Package
	file *ast.File
	decl *ast.FuncDecl
}

const safecmdPath = "github.com/lycaon/lycaon/internal/tools/safecmd"

func build(root string, opts Options) (Inventory, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports,
		Dir: root,
	}
	pkgs, err := packages.Load(cfg, opts.Patterns...)
	if err != nil {
		return Inventory{}, err
	}
	s := &scan{
		root: root, carriers: map[string]bool{}, forwarders: map[string]forwarder{},
		decls: map[string]funcDecl{}, fieldWrites: map[string][]fieldWrite{}, memo: map[string]resolution{}, active: map[string]int{}, locals: map[*types.Var][]Input{},
		decoded: opts.Decoded,
	}
	seen := map[*types.Package]bool{}
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			return Inventory{}, fmt.Errorf("load %s: %v", p.PkgPath, p.Errors[0])
		}
		s.pkgs = append(s.pkgs, p)
		s.collectCarriers(p.Types, seen)
		for _, f := range p.Syntax {
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
					if fn, ok := p.TypesInfo.Defs[fd.Name].(*types.Func); ok {
						s.decls[fn.FullName()] = funcDecl{pkg: p, file: f, decl: fd}
					}
				}
			}
		}
	}
	s.findForwarders()
	s.indexCallers()
	return s.inventory(), nil
}

// collectCarriers finds reject carriers in pkg and everything it imports:
// struct types with Code and Data fields whose pointer has an Error method.
func (s *scan) collectCarriers(pkg *types.Package, seen map[*types.Package]bool) {
	if pkg == nil || seen[pkg] {
		return
	}
	seen[pkg] = true
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		tn, ok := scope.Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}
		st, ok := tn.Type().Underlying().(*types.Struct)
		if !ok || !hasStructField(st, "Code") || !hasStructField(st, "Data") {
			continue
		}
		if ms := types.NewMethodSet(types.NewPointer(tn.Type())); ms.Lookup(pkg, "Error") != nil {
			s.carriers[typeKey(tn)] = true
		}
	}
	for _, imp := range pkg.Imports() {
		s.collectCarriers(imp, seen)
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

func typeKey(tn *types.TypeName) string {
	if tn == nil || tn.Pkg() == nil {
		return ""
	}
	return tn.Pkg().Path() + "." + tn.Name()
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

func (s *scan) isCarrier(t types.Type) bool {
	return s.carriers[typeKey(namedOf(t))]
}

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

func (s *scan) inventory() Inventory {
	var inv Inventory
	keys := make([]string, 0, len(s.decls))
	for key := range s.decls {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		d := s.decls[key]
		_, isForwarder := s.forwarders[key]
		rel, err := filepath.Rel(s.root, d.pkg.Fset.Position(d.decl.Pos()).Filename)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		ast.Inspect(d.decl.Body, func(n ast.Node) bool {
			em, ok := s.emissionOf(d.pkg, n)
			if !ok {
				return true
			}
			if _, isParam := paramIndex(d.pkg, d.decl, em.code); isParam && isForwarder {
				return true
			}
			s.record(&inv, d, key, rel, n, em)
			return true
		})
	}
	return inv
}

func (s *scan) record(inv *Inventory, d funcDecl, key, rel string, n ast.Node, em emission) {
	line := d.pkg.Fset.Position(n.Pos()).Line
	res := s.resolve(d, em.code, 0)
	if res.outcome == OutcomeUnresolved {
		inv.Unresolved = append(inv.Unresolved, Unresolved{
			Rel: rel, Line: line, Func: key, Expr: types.ExprString(em.code), Kind: em.kind, Reason: res.reason,
		})
		return
	}
	site := Site{
		Outcome: res.outcome, Kind: em.kind, Rel: rel, Line: line,
		Func: key, Upstream: res.upstream, Authority: em.authority,
	}
	for _, code := range res.codes {
		if guidancescan.HintCodeShape.MatchString(code) && !slices.Contains(site.Codes, code) {
			site.Codes = append(site.Codes, code)
			site.Authority = site.Authority || authorityCode(code)
		}
	}
	slices.Sort(site.Codes)
	if site.Authority {
		site.Decisions = s.decisionContexts(d, n, em, 0)
	}
	inv.Sites = append(inv.Sites, site)
}

func position(p *packages.Package, root string, pos token.Pos) (string, int) {
	at := p.Fset.Position(pos)
	rel, err := filepath.Rel(root, at.Filename)
	if err != nil {
		rel = at.Filename
	}
	return rel, at.Line
}
