package interactions

import (
	"fmt"
	"go/constant"
	"go/token"
	"go/types"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/configlayout"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// GraphStats records bounded SSA producer-graph construction metrics for the
// qualification ledger.
type GraphStats struct {
	RootPackageCount       int   `json:"root_package_count"`
	InScopePackageCount    int   `json:"in_scope_package_count"`
	IndexedFunctionCount   int   `json:"indexed_function_count"`
	DefaultGraphBuildCount int64 `json:"default_graph_build_count"`
}

// UnresolvedEdge records a dynamic map key or indirect/interface call target
// encountered during SSA producer def-use traversal. It carries no pre-filled
// witness string; closure requires joining EdgeID to an ExecutedEdgeAssertion
// or an ApprovedEdgeExclusion in InteractionLedger.
type UnresolvedEdge struct {
	EdgeID           string `json:"edge_id"`
	Fact             string `json:"fact"`
	ProducerLocation string `json:"producer_location"`
	Reason           string `json:"reason"`
}

// FactTrace captures the primitive producer inputs and any unresolved dynamic
// edges discovered by SSA def-use traversal for one template or OAR fact.
type FactTrace struct {
	Fact            string           `json:"fact"`
	ProducerSites   []string         `json:"producer_sites"`
	PrimitiveInputs []string         `json:"primitive_inputs"`
	Unresolved      []UnresolvedEdge `json:"unresolved,omitempty"`
}

type rawUnresolved struct {
	producerLocation string
	reason           string
}

type factWrite struct {
	fact          string
	isPrefix      bool
	onlyTemplate  bool
	onlyOARAnchor string
	onlyRejectIDs []string
	site          string
	inputs        []string
	unresolved    []rawUnresolved
	val           ssa.Value
	fn            *ssa.Function
	block         *ssa.BasicBlock
	pos           token.Pos
}

type ssaCallSite struct {
	instr  ssa.CallInstruction
	common *ssa.CallCommon
	caller *ssa.Function
	block  *ssa.BasicBlock
}

type structFieldKey struct {
	structName string
	fieldIdx   int
}

func fieldAddrKey(fa *ssa.FieldAddr) (structFieldKey, bool) {
	if fa == nil || fa.X == nil || fa.X.Type() == nil {
		return structFieldKey{}, false
	}
	t := fa.X.Type()
	if ptr, ok := t.Underlying().(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj() == nil {
		return structFieldKey{}, false
	}
	pkgPath := ""
	if named.Obj().Pkg() != nil {
		pkgPath = named.Obj().Pkg().Path()
	}
	return structFieldKey{structName: pkgPath + "." + named.Obj().Name(), fieldIdx: fa.Field}, true
}

// ProducerGraph holds the indexed SSA program for fact-to-primitive-input tracing.
type ProducerGraph struct {
	fset            *token.FileSet
	baseDir         string
	scopePrefixes   []string
	stats           GraphStats
	exactWrites     map[string][]factWrite
	prefixWrites    []factWrite
	providerFacts   map[string]bool
	callSites       map[*ssa.Function][]ssaCallSite
	closureBindings map[*ssa.FreeVar][]ssa.Value
	fieldStores     map[structFieldKey][]ssa.Value
	globalStores    map[*ssa.Global][]ssa.Value
	methodsByName   map[string][]*ssa.Function
	wrapperHelpers  map[*ssa.Function]bool
	memo            map[string]FactTrace
	mu              sync.Mutex
}

var defaultEntryPointPatterns = []string{
	"./internal/app/workerruntime/...",
	"./internal/coordinator/...",
	"./internal/guidance/...",
	"./internal/oar/...",
	"./internal/prompts/...",
	"./internal/session/...",
	"./internal/toolfeedback/...",
	"./internal/toolexecution/...",
	"./internal/tools/...",
	"./internal/worker/...",
}

var defaultGraphBuilds atomic.Int64

var loadDefaultGraphOnce = sync.OnceValues(func() (*ProducerGraph, error) {
	defaultGraphBuilds.Add(1)
	modRoot := configlayout.FindModuleRoot()
	g, err := LoadProducerGraphFromDir(modRoot, defaultEntryPointPatterns...)
	if err != nil {
		return nil, err
	}
	g.stats.DefaultGraphBuildCount = defaultGraphBuilds.Load()
	return g, nil
})

// DefaultGraphBuildCount reports how many times the current-tree SSA graph was built in this process.
func DefaultGraphBuildCount() int64 {
	return defaultGraphBuilds.Load()
}

// DefaultProducerGraph returns the process-wide cached SSA producer graph built
// once from the host delivery entry points across transitive internal imports.
func DefaultProducerGraph() (*ProducerGraph, error) {
	g, err := loadDefaultGraphOnce()
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	g.stats.DefaultGraphBuildCount = defaultGraphBuilds.Load()
	g.mu.Unlock()
	return g, nil
}

// Stats returns the package, function, and build-count metrics for g.
func (g *ProducerGraph) Stats() GraphStats {
	if g == nil {
		return GraphStats{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.stats
	if st.DefaultGraphBuildCount == 0 {
		st.DefaultGraphBuildCount = defaultGraphBuilds.Load()
	}
	return st
}

// LoadProducerGraphFromDir loads Go packages in dir matching patterns along with
// their full transitive import graph in two bounded phases:
//  1. Transitive dependency walk (NeedDeps) to fail closed on any import/list error
//     and collect all module-internal package paths reachable from patterns.
//  2. Full syntax + type-info load (without NeedDeps) over those internal packages,
//     reusing export data for external dependencies while failing closed on any
//     type or partial-load error across every internal package.
func LoadProducerGraphFromDir(dir string, patterns ...string) (*ProducerGraph, error) {
	depCfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports |
			packages.NeedDeps | packages.NeedModule,
		Dir: dir,
	}
	depLoaded, err := packages.Load(depCfg, patterns...)
	if err != nil {
		return nil, fmt.Errorf("packages.Load(%s): %w", dir, err)
	}
	if len(depLoaded) == 0 {
		return nil, fmt.Errorf("packages.Load(%s): matched 0 root packages", dir)
	}

	rootModPath := resolveRootModulePath(depLoaded)
	internalSet := map[string]bool{}
	var loadErrs []string

	packages.Visit(depLoaded, nil, func(pkg *packages.Package) {
		for _, e := range pkg.Errors {
			loadErrs = append(loadErrs, fmt.Sprintf("%s: %s", pkg.PkgPath, e.Error()))
		}
		isInternal := pkg.PkgPath == rootModPath || strings.HasPrefix(pkg.PkgPath, rootModPath+"/")
		if isInternal && len(pkg.GoFiles) > 0 {
			internalSet[pkg.PkgPath] = true
		}
	})
	if len(loadErrs) > 0 {
		slices.Sort(loadErrs)
		return nil, fmt.Errorf("fail-closed package load errors (%d): %s", len(loadErrs), strings.Join(slices.Compact(loadErrs), "; "))
	}
	if len(internalSet) == 0 {
		return nil, fmt.Errorf("packages.Load(%s): 0 internal packages with Go source files", dir)
	}

	internalPatterns := sortedKeysOf(internalSet)
	synCfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports |
			packages.NeedModule,
		Dir: dir,
	}
	synLoaded, err := packages.Load(synCfg, internalPatterns...)
	if err != nil {
		return nil, fmt.Errorf("packages.Load syntax(%s): %w", dir, err)
	}

	inScope := make(map[string]*packages.Package, len(synLoaded))
	var scopePrefixes []string
	for _, pkg := range synLoaded {
		for _, e := range pkg.Errors {
			loadErrs = append(loadErrs, fmt.Sprintf("%s: %s", pkg.PkgPath, e.Error()))
		}
		if pkg.IllTyped {
			loadErrs = append(loadErrs, fmt.Sprintf("%s: package is ill-typed", pkg.PkgPath))
		}
		if pkg.Types == nil || (len(pkg.GoFiles) > 0 && (len(pkg.Syntax) == 0 || pkg.TypesInfo == nil)) {
			loadErrs = append(loadErrs, fmt.Sprintf("%s: partial load missing types, syntax, or TypesInfo", pkg.PkgPath))
			continue
		}
		if len(pkg.Syntax) > 0 {
			inScope[pkg.PkgPath] = pkg
			scopePrefixes = append(scopePrefixes, pkg.PkgPath)
		}
	}
	if len(loadErrs) > 0 {
		slices.Sort(loadErrs)
		return nil, fmt.Errorf("fail-closed package/type errors (%d): %s", len(loadErrs), strings.Join(slices.Compact(loadErrs), "; "))
	}
	if len(inScope) == 0 {
		return nil, fmt.Errorf("packages.Load(%s): 0 in-scope syntax packages", dir)
	}
	slices.Sort(scopePrefixes)

	prog := buildScopedSSAProgram(synLoaded[0].Fset, inScope)
	g := &ProducerGraph{
		fset:            synLoaded[0].Fset,
		baseDir:         dir,
		scopePrefixes:   scopePrefixes,
		stats:           GraphStats{RootPackageCount: len(depLoaded), InScopePackageCount: len(inScope)},
		exactWrites:     map[string][]factWrite{},
		providerFacts:   map[string]bool{},
		callSites:       map[*ssa.Function][]ssaCallSite{},
		closureBindings: map[*ssa.FreeVar][]ssa.Value{},
		fieldStores:     map[structFieldKey][]ssa.Value{},
		globalStores:    map[*ssa.Global][]ssa.Value{},
		methodsByName:   map[string][]*ssa.Function{},
		wrapperHelpers:  map[*ssa.Function]bool{},
		memo:            map[string]FactTrace{},
	}
	g.indexProgram(prog)
	g.compact()
	return g, nil
}

func resolveRootModulePath(loaded []*packages.Package) string {
	for _, pkg := range loaded {
		if pkg.Module != nil && pkg.Module.Path != "" {
			return pkg.Module.Path
		}
	}
	root := loaded[0].PkgPath
	if idx := strings.IndexByte(root, '/'); idx > 0 {
		parts := strings.Split(root, "/")
		if len(parts) >= 2 {
			return strings.Join(parts[:2], "/")
		}
	}
	return root
}

func buildScopedSSAProgram(fset *token.FileSet, inScope map[string]*packages.Package) *ssa.Program {
	prog := ssa.NewProgram(fset, ssa.InstantiateGenerics)
	created := map[*types.Package]bool{}
	var createPkg func(*types.Package)
	createPkg = func(tp *types.Package) {
		if tp == nil || created[tp] {
			return
		}
		created[tp] = true
		for _, imp := range tp.Imports() {
			createPkg(imp)
		}
		if srcPkg, ok := inScope[tp.Path()]; ok {
			prog.CreatePackage(tp, srcPkg.Syntax, srcPkg.TypesInfo, true)
			return
		}
		prog.CreatePackage(tp, nil, nil, true)
	}
	for _, pkg := range inScope {
		createPkg(pkg.Types)
		for _, obj := range pkg.TypesInfo.Uses {
			if obj != nil && obj.Pkg() != nil {
				createPkg(obj.Pkg())
			}
		}
	}
	prog.Build()
	return prog
}

// IsCapabilityProviderFact reports whether fact (or its paintedwolf.-prefixed
// form) is registered as a lazy host capability/recovery provider in the SSA graph.
func (g *ProducerGraph) IsCapabilityProviderFact(fact string) bool {
	if g == nil {
		return false
	}
	short := strings.TrimPrefix(fact, "paintedwolf.")
	return g.providerFacts[fact] || g.providerFacts["paintedwolf."+short] || g.providerFacts[short]
}

func (g *ProducerGraph) inScopePkg(fn *ssa.Function) bool {
	if fn == nil || fn.Pkg == nil || fn.Pkg.Pkg == nil {
		return false
	}
	p := fn.Pkg.Pkg.Path()
	for _, pref := range g.scopePrefixes {
		if p == pref || strings.HasPrefix(p, pref+"/") {
			return true
		}
	}
	return false
}

func (g *ProducerGraph) relPos(pos token.Pos) string {
	if !pos.IsValid() || g.fset == nil {
		return "<unknown>"
	}
	p := g.fset.Position(pos)
	rel, err := filepath.Rel(g.baseDir, p.Filename)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = filepath.Base(p.Filename)
	}
	return fmt.Sprintf("%s:%d", filepath.ToSlash(rel), p.Line)
}

func (g *ProducerGraph) indexProgram(prog *ssa.Program) {
	allFuncs := ssautil.AllFunctions(prog)
	var funcs []*ssa.Function
	for fn := range allFuncs {
		if g.inScopePkg(fn) && len(fn.Blocks) > 0 {
			funcs = append(funcs, fn)
		}
	}
	slices.SortFunc(funcs, func(a, b *ssa.Function) int {
		return strings.Compare(a.String(), b.String())
	})
	g.stats.IndexedFunctionCount = len(funcs)

	for _, fn := range funcs {
		if fn.Signature != nil && fn.Signature.Recv() != nil {
			g.methodsByName[fn.Name()] = append(g.methodsByName[fn.Name()], fn)
		}
		for _, blk := range fn.Blocks {
			for _, instr := range blk.Instrs {
				if mc, ok := instr.(*ssa.MakeClosure); ok {
					if cfn, isFn := mc.Fn.(*ssa.Function); isFn {
						for i, fv := range cfn.FreeVars {
							if i < len(mc.Bindings) {
								g.closureBindings[fv] = append(g.closureBindings[fv], mc.Bindings[i])
							}
						}
					}
				}
				if call, ok := instr.(ssa.CallInstruction); ok {
					if callee := call.Common().StaticCallee(); callee != nil && callee.Name() == "RegisterProvider" && len(fn.Params) == 2 {
						g.wrapperHelpers[fn] = true
					}
				}
				if st, ok := instr.(*ssa.Store); ok {
					if fa, isFA := st.Addr.(*ssa.FieldAddr); isFA {
						if k, okKey := fieldAddrKey(fa); okKey {
							g.fieldStores[k] = append(g.fieldStores[k], st.Val)
						}
					}
					if gl, isGL := st.Addr.(*ssa.Global); isGL {
						g.globalStores[gl] = append(g.globalStores[gl], st.Val)
					}
				}
			}
		}
	}

	for _, fn := range funcs {
		for _, blk := range fn.Blocks {
			for _, instr := range blk.Instrs {
				if call, ok := instr.(ssa.CallInstruction); ok {
					cs := ssaCallSite{instr: call, common: call.Common(), caller: fn, block: blk}
					if callee := call.Common().StaticCallee(); callee != nil {
						g.callSites[callee] = append(g.callSites[callee], cs)
					} else {
						for _, tFn := range g.resolveClosureTargets(call.Common().Value) {
							g.callSites[tFn] = append(g.callSites[tFn], cs)
						}
					}
					g.indexProviderRegistration(fn, blk, call)
					g.indexWrapperRegistration(fn, blk, call)
				}
			}
		}
	}

	for _, fn := range funcs {
		for _, blk := range fn.Blocks {
			for _, instr := range blk.Instrs {
				if mu, ok := instr.(*ssa.MapUpdate); ok {
					g.indexMapUpdate(fn, blk, mu)
				}
			}
		}
	}
}

func (g *ProducerGraph) resolveClosureTargets(v ssa.Value) []*ssa.Function {
	return g.resolveClosureTargetsVisited(v, map[ssa.Value]bool{})
}

func (g *ProducerGraph) resolveClosureTargetsVisited(v ssa.Value, seen map[ssa.Value]bool) []*ssa.Function {
	if v == nil || seen[v] {
		return nil
	}
	seen[v] = true
	switch tv := v.(type) {
	case *ssa.Function:
		return []*ssa.Function{tv}
	case *ssa.MakeClosure:
		if fn, ok := tv.Fn.(*ssa.Function); ok {
			return []*ssa.Function{fn}
		}
	case *ssa.Parameter:
		var out []*ssa.Function
		pIdx := slices.Index(tv.Parent().Params, tv)
		if pIdx >= 0 {
			for _, cs := range g.callSites[tv.Parent()] {
				if pIdx < len(cs.common.Args) {
					out = append(out, g.resolveClosureTargetsVisited(cs.common.Args[pIdx], seen)...)
				}
			}
		}
		return out
	case *ssa.FreeVar:
		var out []*ssa.Function
		for _, b := range g.closureBindings[tv] {
			out = append(out, g.resolveClosureTargetsVisited(b, seen)...)
		}
		return out
	case *ssa.UnOp:
		if tv.Op == token.MUL {
			if out := g.resolveDereferencedPointer(tv.X, seen); len(out) > 0 {
				return out
			}
		}
	}
	return nil
}

func (g *ProducerGraph) resolveDereferencedPointer(ptr ssa.Value, seen map[ssa.Value]bool) []*ssa.Function {
	switch pv := ptr.(type) {
	case *ssa.Alloc:
		var out []*ssa.Function
		for _, ref := range *pv.Referrers() {
			if st, isStore := ref.(*ssa.Store); isStore && st.Addr == pv {
				out = append(out, g.resolveClosureTargetsVisited(st.Val, seen)...)
			}
		}
		return out
	case *ssa.FreeVar:
		var out []*ssa.Function
		for _, b := range g.closureBindings[pv] {
			out = append(out, g.resolveDereferencedPointer(b, seen)...)
		}
		return out
	case *ssa.FieldAddr:
		if k, okKey := fieldAddrKey(pv); okKey {
			var out []*ssa.Function
			for _, sv := range g.fieldStores[k] {
				out = append(out, g.resolveClosureTargetsVisited(sv, seen)...)
			}
			return out
		}
	case *ssa.Global:
		var out []*ssa.Function
		for _, sv := range g.globalStores[pv] {
			out = append(out, g.resolveClosureTargetsVisited(sv, seen)...)
		}
		return out
	}
	return nil
}

func (g *ProducerGraph) indexWrapperRegistration(fn *ssa.Function, blk *ssa.BasicBlock, call ssa.CallInstruction) {
	common := call.Common()
	if len(common.Args) != 2 {
		return
	}
	c, ok := common.Args[0].(*ssa.Const)
	if !ok || c.Value == nil || c.Value.Kind() != constant.String {
		return
	}
	targets := g.resolveClosureTargets(common.Value)
	isWrapper := slices.ContainsFunc(targets, func(t *ssa.Function) bool { return g.wrapperHelpers[t] })
	if !isWrapper {
		return
	}
	bare := constant.StringVal(c.Value)
	compArg := common.Args[1]
	compFns := g.resolveClosureTargets(compArg)
	targetFn := fn
	if len(compFns) > 0 {
		targetFn = compFns[0]
	}
	g.providerFacts[bare] = true
	g.providerFacts["paintedwolf."+bare] = true
	g.exactWrites[bare] = append(g.exactWrites[bare], factWrite{
		fact: bare, val: compArg, fn: targetFn, block: blk, pos: call.Pos(),
	})
	g.exactWrites["paintedwolf."+bare] = append(g.exactWrites["paintedwolf."+bare], factWrite{
		fact: "paintedwolf." + bare, val: compArg, fn: targetFn, block: blk, pos: call.Pos(),
	})
}

func (g *ProducerGraph) indexProviderRegistration(fn *ssa.Function, blk *ssa.BasicBlock, call ssa.CallInstruction) {
	common := call.Common()
	callee := common.StaticCallee()
	if callee == nil || callee.Name() != "RegisterProvider" || len(common.Args) < 3 {
		return
	}
	keyArg := common.Args[1]
	providerArg := common.Args[2]
	targets := g.resolveClosureTargets(providerArg)
	targetFn := fn
	if len(targets) > 0 {
		targetFn = targets[0]
	}
	if c, ok := keyArg.(*ssa.Const); ok && c.Value != nil && c.Value.Kind() == constant.String {
		fact := constant.StringVal(c.Value)
		g.providerFacts[fact] = true
		g.exactWrites[fact] = append(g.exactWrites[fact], factWrite{
			fact: fact, val: providerArg, fn: targetFn, block: blk, pos: call.Pos(),
		})
		return
	}
	if prefix, ok := extractStringPrefixAdd(keyArg); ok && prefix != "" {
		if bin, isBin := keyArg.(*ssa.BinOp); isBin {
			if _, isParam := bin.Y.(*ssa.Parameter); isParam && g.wrapperHelpers[fn] {
				return
			}
		}
		g.prefixWrites = append(g.prefixWrites, factWrite{
			fact: prefix, isPrefix: true, val: providerArg, fn: targetFn, block: blk, pos: call.Pos(),
		})
	}
}

func (g *ProducerGraph) indexMapUpdate(fn *ssa.Function, blk *ssa.BasicBlock, mu *ssa.MapUpdate) {
	if fn != nil && fn.Pkg != nil && fn.Pkg.Pkg != nil {
		p := fn.Pkg.Pkg.Path()
		if strings.Contains(p, "/internal/api") || strings.Contains(p, "/internal/db") || strings.Contains(p, "/internal/mcp") {
			return
		}
	}
	if !isStringKeyedFactMap(mu.Map.Type()) {
		return
	}
	carrier := g.classifyMapCarrier(fn, mu.Map)
	if carrier.skip {
		return
	}
	if c, ok := mu.Key.(*ssa.Const); ok && c.Value != nil && c.Value.Kind() == constant.String {
		key := constant.StringVal(c.Value)
		if key == "" {
			return
		}
		if carrier.nestedRoot != "" {
			nested := carrier.nestedRoot + "." + key
			g.exactWrites[nested] = append(g.exactWrites[nested], factWrite{
				fact: nested, onlyTemplate: carrier.onlyTemplate,
				val: mu.Value, fn: fn, block: blk, pos: mu.Pos(),
			})
			return
		}
		g.exactWrites[key] = append(g.exactWrites[key], factWrite{
			fact: key, onlyTemplate: carrier.onlyTemplate,
			onlyOARAnchor: carrier.onlyOARAnchor, onlyRejectIDs: carrier.onlyRejectIDs,
			val: mu.Value, fn: fn, block: blk, pos: mu.Pos(),
		})
		if !carrier.onlyTemplate && !strings.HasPrefix(key, "paintedwolf.") {
			g.exactWrites["paintedwolf."+key] = append(g.exactWrites["paintedwolf."+key], factWrite{
				fact:          "paintedwolf." + key,
				onlyOARAnchor: carrier.onlyOARAnchor, onlyRejectIDs: carrier.onlyRejectIDs,
				val: mu.Value, fn: fn, block: blk, pos: mu.Pos(),
			})
		}
		return
	}
	if prefix, ok := extractStringPrefixAdd(mu.Key); ok && prefix != "" {
		g.prefixWrites = append(g.prefixWrites, factWrite{
			fact: prefix, isPrefix: true, onlyTemplate: carrier.onlyTemplate,
			onlyOARAnchor: carrier.onlyOARAnchor, onlyRejectIDs: carrier.onlyRejectIDs,
			val: mu.Value, fn: fn, block: blk, pos: mu.Pos(),
		})
	}
}

func isStringKeyedFactMap(t types.Type) bool {
	if t == nil {
		return false
	}
	mt, ok := t.Underlying().(*types.Map)
	if !ok {
		return false
	}
	bt, ok := mt.Key().Underlying().(*types.Basic)
	if !ok || bt.Kind() != types.String {
		return false
	}
	switch vt := mt.Elem().Underlying().(type) {
	case *types.Interface:
		return vt.NumMethods() == 0
	case *types.Basic:
		return vt.Kind() == types.Bool || vt.Kind() == types.String || vt.Kind() == types.Int
	default:
		return false
	}
}

func extractStringPrefixAdd(v ssa.Value) (string, bool) {
	bin, ok := v.(*ssa.BinOp)
	if !ok || bin.Op != token.ADD {
		return "", false
	}
	if c, isConst := bin.X.(*ssa.Const); isConst && c.Value != nil && c.Value.Kind() == constant.String {
		return constant.StringVal(c.Value), true
	}
	return "", false
}
