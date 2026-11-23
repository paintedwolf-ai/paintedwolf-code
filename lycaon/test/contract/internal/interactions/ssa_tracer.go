package interactions

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"runtime"
	"slices"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// TraceFact traces a discovered template or OAR fact backward through SSA
// def-use edges to primitive request, ledger, event, or catalog inputs.
func (g *ProducerGraph) TraceFact(fact string) FactTrace {
	return g.traceFactScoped(fact, nil)
}

// TraceFactForSite traces fact restricted to producer writes whose carrier type
// can reach site's delivery family, OAR anchor, and rejection code.
func (g *ProducerGraph) TraceFactForSite(fact string, site InstructionSite) FactTrace {
	return g.traceFactScoped(fact, &site)
}

func siteScopeMemoKey(fact string, site *InstructionSite) string {
	if site == nil {
		return fact
	}
	isOAR := site.Family == "oar_next_step" || site.Family == "oar_copy_fix"
	if !isOAR {
		return fact + "@tpl"
	}
	var guardSig strings.Builder
	for _, gc := range site.Guards {
		guardSig.WriteString("|")
		guardSig.WriteString(gc.Expression)
	}
	return fact + "@" + site.DeliverySite + guardSig.String()
}

func writeAppliesToSite(w factWrite, site *InstructionSite) bool {
	if site == nil {
		return true
	}
	isOAR := site.Family == "oar_next_step" || site.Family == "oar_copy_fix"
	if w.onlyTemplate && isOAR {
		return false
	}
	if w.onlyOARAnchor != "" {
		if !isOAR || !strings.HasPrefix(site.DeliverySite, "oar:"+w.onlyOARAnchor+":") {
			return false
		}
	}
	if len(w.onlyRejectIDs) > 0 && isOAR {
		parts := strings.SplitN(site.DeliverySite, ":", 3)
		if len(parts) == 3 && slices.Contains(w.onlyRejectIDs, parts[2]) {
			return true
		}
		for _, rid := range w.onlyRejectIDs {
			for _, gc := range site.Guards {
				if strings.Contains(gc.Expression, rid) {
					return true
				}
			}
		}
		return false
	}
	return true
}

type compactWriteKey struct {
	pos   token.Pos
	val   ssa.Value
	fn    *ssa.Function
	block *ssa.BasicBlock
}

type compactWriteResult struct {
	site       string
	inputs     []string
	unresolved []rawUnresolved
}

// compact pre-traces every indexed factWrite into plain string slices and clears
// all SSA references so the SSA program can be garbage-collected immediately.
func (g *ProducerGraph) compact() {
	seen := map[compactWriteKey]compactWriteResult{}
	compactSlice := func(ws []factWrite) {
		for i := range ws {
			k := compactWriteKey{pos: ws[i].pos, val: ws[i].val, fn: ws[i].fn, block: ws[i].block}
			res, ok := seen[k]
			if !ok {
				inputs := map[string]bool{}
				unresolvedMap := map[string]UnresolvedEdge{}
				visitedVal := map[ssa.Value]bool{}
				visitedFn := map[*ssa.Function]bool{}
				const placeholderFact = "_"
				if closure, isCl := ws[i].val.(*ssa.MakeClosure); isCl {
					if cfn, okFn := closure.Fn.(*ssa.Function); okFn {
						g.traceFunctionBody(placeholderFact, cfn, inputs, unresolvedMap, visitedVal, visitedFn, 0)
					}
					for _, b := range closure.Bindings {
						g.traceValue(placeholderFact, b, ws[i].fn, inputs, unresolvedMap, visitedVal, visitedFn, 0)
					}
				} else {
					g.traceValue(placeholderFact, ws[i].val, ws[i].fn, inputs, unresolvedMap, visitedVal, visitedFn, 0)
				}
				g.traceBlockConditions(placeholderFact, ws[i].block, ws[i].fn, inputs, unresolvedMap, visitedVal, visitedFn, 0)
				rawU := make([]rawUnresolved, 0, len(unresolvedMap))
				for _, u := range unresolvedMap {
					rawU = append(rawU, rawUnresolved{
						producerLocation: u.ProducerLocation,
						reason:           u.Reason,
					})
				}
				slices.SortFunc(rawU, func(a, b rawUnresolved) int {
					return strings.Compare(a.producerLocation+"#"+a.reason, b.producerLocation+"#"+b.reason)
				})
				res = compactWriteResult{
					site:       g.relPos(ws[i].pos),
					inputs:     sortedKeysOf(inputs),
					unresolved: rawU,
				}
				seen[k] = res
			}
			ws[i].site = res.site
			ws[i].inputs = res.inputs
			ws[i].unresolved = res.unresolved
			ws[i].val = nil
			ws[i].fn = nil
			ws[i].block = nil
			ws[i].pos = 0
		}
	}
	for k, ws := range g.exactWrites {
		compactSlice(ws)
		g.exactWrites[k] = ws
	}
	compactSlice(g.prefixWrites)
	g.fset = nil
	g.callSites = nil
	g.closureBindings = nil
	g.fieldStores = nil
	g.globalStores = nil
	g.methodsByName = nil
	g.wrapperHelpers = nil
	runtime.GC()
}

func (g *ProducerGraph) traceFactScoped(fact string, site *InstructionSite) FactTrace {
	memoKey := siteScopeMemoKey(fact, site)
	g.mu.Lock()
	if cached, ok := g.memo[memoKey]; ok {
		g.mu.Unlock()
		return cached
	}
	g.mu.Unlock()

	if fact == "tool" {
		tr := FactTrace{
			Fact:            fact,
			ProducerSites:   []string{"internal/oar/guard_context.go"},
			PrimitiveInputs: []string{"GuardContext.Tool"},
		}
		g.storeMemo(memoKey, tr)
		return tr
	}
	if strings.HasPrefix(fact, "reach.status:") || strings.HasPrefix(fact, "reach.program:") {
		tr := FactTrace{
			Fact:            fact,
			ProducerSites:   []string{"internal/oar/next_steps.go"},
			PrimitiveInputs: []string{"toolreach.Request." + strings.TrimPrefix(fact, "reach.")},
		}
		g.storeMemo(memoKey, tr)
		return tr
	}

	root := fact
	if idx := strings.IndexByte(fact, '.'); idx >= 0 && !strings.HasPrefix(fact, "paintedwolf.") {
		root = fact[:idx]
	}

	var candidates []factWrite
	candidates = append(candidates, g.exactWrites[fact]...)
	if root != fact {
		candidates = append(candidates, g.exactWrites[root]...)
	}
	for _, pw := range g.prefixWrites {
		if strings.HasPrefix(fact, pw.fact) || strings.HasPrefix(strings.TrimPrefix(fact, "paintedwolf."), pw.fact) {
			candidates = append(candidates, pw)
		}
	}
	var writes []factWrite
	for _, w := range candidates {
		if writeAppliesToSite(w, site) {
			writes = append(writes, w)
		}
	}

	sites := map[string]bool{}
	inputs := map[string]bool{}
	unresolvedMap := map[string]UnresolvedEdge{}

	for _, w := range writes {
		if w.site != "" {
			sites[w.site] = true
		}
		for _, inp := range w.inputs {
			inputs[inp] = true
		}
		for _, ru := range w.unresolved {
			eid := fmt.Sprintf("%s@%s#%s", fact, ru.producerLocation, ru.reason)
			unresolvedMap[eid] = UnresolvedEdge{
				EdgeID:           eid,
				Fact:             fact,
				ProducerLocation: ru.producerLocation,
				Reason:           ru.reason,
			}
		}
	}

	if len(writes) == 0 {
		inputs["occurrence_or_event:"+fact] = true
	}

	var uList []UnresolvedEdge
	for _, u := range unresolvedMap {
		uList = append(uList, u)
	}
	slices.SortFunc(uList, func(a, b UnresolvedEdge) int {
		return strings.Compare(a.EdgeID, b.EdgeID)
	})
	tr := FactTrace{
		Fact:            fact,
		ProducerSites:   sortedKeysOf(sites),
		PrimitiveInputs: sortedKeysOf(inputs),
		Unresolved:      uList,
	}
	g.storeMemo(memoKey, tr)
	return tr
}

func (g *ProducerGraph) storeMemo(fact string, tr FactTrace) {
	g.mu.Lock()
	g.memo[fact] = tr
	g.mu.Unlock()
}

func (g *ProducerGraph) traceFunctionBody(
	fact string,
	fn *ssa.Function,
	inputs map[string]bool,
	unresolved map[string]UnresolvedEdge,
	visitedVal map[ssa.Value]bool,
	visitedFn map[*ssa.Function]bool,
	depth int,
) {
	if fn == nil || depth > 8 || visitedFn[fn] {
		return
	}
	visitedFn[fn] = true
	for _, blk := range fn.Blocks {
		for _, instr := range blk.Instrs {
			switch it := instr.(type) {
			case *ssa.Return:
				for _, res := range it.Results {
					g.traceValue(fact, res, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
				}
				g.traceBlockConditions(fact, blk, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
			case *ssa.MapUpdate:
				g.traceValue(fact, it.Value, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
				g.traceBlockConditions(fact, blk, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
			}
		}
	}
}

func (g *ProducerGraph) traceBlockConditions(
	fact string,
	blk *ssa.BasicBlock,
	fn *ssa.Function,
	inputs map[string]bool,
	unresolved map[string]UnresolvedEdge,
	visitedVal map[ssa.Value]bool,
	visitedFn map[*ssa.Function]bool,
	depth int,
) {
	if blk == nil || depth > 6 {
		return
	}
	seenBlk := map[*ssa.BasicBlock]bool{}
	queue := []*ssa.BasicBlock{blk}
	for len(queue) > 0 && len(seenBlk) < 12 {
		cur := queue[0]
		queue = queue[1:]
		if cur == nil || seenBlk[cur] {
			continue
		}
		seenBlk[cur] = true
		for _, pred := range cur.Preds {
			if len(pred.Instrs) > 0 {
				if ifInstr, ok := pred.Instrs[len(pred.Instrs)-1].(*ssa.If); ok {
					g.traceValue(fact, ifInstr.Cond, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
				}
			}
			queue = append(queue, pred)
		}
	}
}

func isPrimitiveBoundaryType(t types.Type) bool {
	if t == nil {
		return false
	}
	if ptr, ok := t.Underlying().(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj() == nil {
		return false
	}
	switch named.Obj().Name() {
	case "ReaderInput", "GuardContext", "ToolReject", "CoordinatorRunContext", "Session", "Request":
		return true
	default:
		return false
	}
}

func (g *ProducerGraph) traceValue(
	fact string,
	val ssa.Value,
	fn *ssa.Function,
	inputs map[string]bool,
	unresolved map[string]UnresolvedEdge,
	visitedVal map[ssa.Value]bool,
	visitedFn map[*ssa.Function]bool,
	depth int,
) {
	if val == nil || depth > 10 || visitedVal[val] {
		return
	}
	visitedVal[val] = true

	switch v := val.(type) {
	case *ssa.Const, *ssa.Global:
		if gl, ok := v.(*ssa.Global); ok && gl.Pkg != nil {
			inputs["catalog_global:"+gl.Name()] = true
		}
	case *ssa.Parameter:
		g.traceParameter(fact, v, inputs, unresolved, visitedVal, visitedFn, depth)
	case *ssa.FreeVar:
		bindings := g.closureBindings[v]
		if len(bindings) == 0 {
			inputs["closure_binding:"+v.Name()] = true
		}
		for _, b := range bindings {
			g.traceValue(fact, b, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
		}
	case *ssa.MakeClosure:
		if cfn, ok := v.Fn.(*ssa.Function); ok {
			g.traceFunctionBody(fact, cfn, inputs, unresolved, visitedVal, visitedFn, depth+1)
		}
		for _, b := range v.Bindings {
			g.traceValue(fact, b, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
		}
	case *ssa.FieldAddr:
		if name := structFieldInputName(v.X.Type(), v.Field); name != "" {
			inputs[name] = true
			if isPrimitiveBoundaryType(v.X.Type()) {
				return
			}
		}
		g.traceValue(fact, v.X, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
	case *ssa.Field:
		if name := structFieldInputName(v.X.Type(), v.Field); name != "" {
			inputs[name] = true
			if isPrimitiveBoundaryType(v.X.Type()) {
				return
			}
		}
		g.traceValue(fact, v.X, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
	case *ssa.UnOp:
		if v.Op == token.MUL {
			if alloc, ok := v.X.(*ssa.Alloc); ok {
				for _, ref := range *alloc.Referrers() {
					if st, isStore := ref.(*ssa.Store); isStore && st.Addr == alloc {
						g.traceValue(fact, st.Val, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
					}
				}
			}
		}
		g.traceValue(fact, v.X, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
	case *ssa.BinOp:
		g.traceValue(fact, v.X, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
		g.traceValue(fact, v.Y, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
	case *ssa.Phi:
		for _, edge := range v.Edges {
			g.traceValue(fact, edge, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
		}
	case *ssa.Lookup:
		if c, ok := v.Index.(*ssa.Const); ok && c.Value != nil && c.Value.Kind() == constant.String {
			inputs["map_lookup:"+constant.StringVal(c.Value)] = true
		}
		g.traceValue(fact, v.X, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
		g.traceValue(fact, v.Index, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
	case *ssa.IndexAddr:
		g.traceValue(fact, v.X, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
	case *ssa.Index:
		g.traceValue(fact, v.X, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
	case *ssa.Slice:
		g.traceValue(fact, v.X, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
	case *ssa.Extract:
		g.traceValue(fact, v.Tuple, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
	case *ssa.ChangeType:
		g.traceValue(fact, v.X, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
	case *ssa.Convert:
		g.traceValue(fact, v.X, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
	case *ssa.MakeInterface:
		g.traceValue(fact, v.X, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
	case *ssa.TypeAssert:
		g.traceValue(fact, v.X, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
	case *ssa.Call:
		g.traceCall(fact, &v.Call, v.Pos(), fn, inputs, unresolved, visitedVal, visitedFn, depth)
	}
}

func (g *ProducerGraph) traceParameter(
	fact string,
	p *ssa.Parameter,
	inputs map[string]bool,
	unresolved map[string]UnresolvedEdge,
	visitedVal map[ssa.Value]bool,
	visitedFn map[*ssa.Function]bool,
	depth int,
) {
	parent := p.Parent()
	if parent == nil {
		return
	}
	if ast.IsExported(parent.Name()) {
		inputs[parent.Name()+"."+p.Name()] = true
	}
	if isPrimitiveBoundaryType(p.Type()) || depth > 6 {
		return
	}
	pIdx := slices.Index(parent.Params, p)
	if pIdx < 0 {
		return
	}
	sites := g.callSites[parent]
	for idx, cs := range sites {
		if idx >= 6 {
			break
		}
		if pIdx < len(cs.common.Args) {
			g.traceValue(fact, cs.common.Args[pIdx], cs.caller, inputs, unresolved, visitedVal, visitedFn, depth+1)
		}
	}
}

func (g *ProducerGraph) traceCall(
	fact string,
	common *ssa.CallCommon,
	pos token.Pos,
	fn *ssa.Function,
	inputs map[string]bool,
	unresolved map[string]UnresolvedEdge,
	visitedVal map[ssa.Value]bool,
	visitedFn map[*ssa.Function]bool,
	depth int,
) {
	if common.IsInvoke() {
		if common.Value != nil && common.Value.Type() != nil {
			if rt := common.Value.Type().String(); rt == "context.Context" || rt == "error" {
				return
			}
			if iface, ok := common.Value.Type().Underlying().(*types.Interface); ok {
				var impls []*ssa.Function
				for _, cand := range g.methodsByName[common.Method.Name()] {
					if cand.Signature != nil && cand.Signature.Recv() != nil && types.Implements(cand.Signature.Recv().Type(), iface) {
						impls = append(impls, cand)
					}
				}
				if len(impls) >= 1 && len(impls) <= 4 {
					for _, arg := range common.Args {
						if arg != nil && arg.Type() != nil && arg.Type().String() == "context.Context" {
							continue
						}
						g.traceValue(fact, arg, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
					}
					for _, imp := range impls {
						if depth <= 6 {
							g.traceFunctionBody(fact, imp, inputs, unresolved, visitedVal, visitedFn, depth+1)
						}
					}
					return
				}
				if len(impls) == 0 {
					inputs[ifaceInputLabel(common.Value.Type(), common.Method.Name())] = true
					if common.Value != nil {
						g.traceValue(fact, common.Value, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
					}
					return
				}
			}
		}
		loc := g.relPos(pos)
		reason := "interface_invoke:" + common.Method.Name()
		eid := fmt.Sprintf("%s@%s#%s", fact, loc, reason)
		unresolved[eid] = UnresolvedEdge{
			EdgeID:           eid,
			Fact:             fact,
			ProducerLocation: loc,
			Reason:           reason,
		}
		if common.Value != nil {
			g.traceValue(fact, common.Value, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
		}
		return
	}
	callee := common.StaticCallee()
	if callee == nil {
		if _, ok := common.Value.(*ssa.Builtin); ok {
			for _, arg := range common.Args {
				g.traceValue(fact, arg, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
			}
			return
		}
		if targets := g.resolveClosureTargets(common.Value); len(targets) > 0 {
			for _, arg := range common.Args {
				g.traceValue(fact, arg, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
			}
			for _, tFn := range targets {
				if g.inScopePkg(tFn) && len(tFn.Blocks) > 0 && depth <= 6 {
					g.traceFunctionBody(fact, tFn, inputs, unresolved, visitedVal, visitedFn, depth+1)
				}
			}
			return
		}
		loc := g.relPos(pos)
		reason := "indirect_function_value_call"
		eid := fmt.Sprintf("%s@%s#%s", fact, loc, reason)
		unresolved[eid] = UnresolvedEdge{
			EdgeID:           eid,
			Fact:             fact,
			ProducerLocation: loc,
			Reason:           reason,
		}
		return
	}

	if prim := classifyPrimitiveCallee(callee); prim != "" {
		inputs[prim] = true
		for _, arg := range common.Args {
			if arg != nil && arg.Type() != nil && arg.Type().String() == "context.Context" {
				continue
			}
			g.traceValue(fact, arg, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
		}
		return
	}
	for _, arg := range common.Args {
		if arg != nil && arg.Type() != nil && arg.Type().String() == "context.Context" {
			continue
		}
		g.traceValue(fact, arg, fn, inputs, unresolved, visitedVal, visitedFn, depth+1)
	}
	if g.inScopePkg(callee) && len(callee.Blocks) > 0 && depth <= 5 {
		g.traceFunctionBody(fact, callee, inputs, unresolved, visitedVal, visitedFn, depth+1)
	}
}

func structFieldInputName(t types.Type, fieldIdx int) string {
	if t == nil {
		return ""
	}
	if ptr, ok := t.Underlying().(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, isNamed := t.(*types.Named)
	st, ok := t.Underlying().(*types.Struct)
	if !ok || fieldIdx < 0 || fieldIdx >= st.NumFields() {
		return ""
	}
	fieldName := st.Field(fieldIdx).Name()
	if isNamed && named.Obj() != nil {
		return named.Obj().Name() + "." + fieldName
	}
	return fieldName
}

func ifaceInputLabel(t types.Type, method string) string {
	if named, ok := t.(*types.Named); ok && named.Obj() != nil {
		return named.Obj().Name() + "." + method
	}
	return "store_iface." + method
}

func classifyPrimitiveCallee(callee *ssa.Function) string {
	if callee == nil || callee.Pkg == nil || callee.Pkg.Pkg == nil {
		return ""
	}
	pkgPath := callee.Pkg.Pkg.Path()
	switch {
	case strings.HasSuffix(pkgPath, "/internal/toolreach"):
		return "toolreach." + callee.Name()
	case strings.HasSuffix(pkgPath, "/internal/toolschema"):
		return "toolschema." + callee.Name()
	case strings.HasSuffix(pkgPath, "/internal/toolcontract"):
		return "toolcontract." + callee.Name()
	case strings.HasSuffix(pkgPath, "/internal/toolprofiles"):
		return "toolprofiles." + callee.Name()
	case strings.HasSuffix(pkgPath, "/internal/gitrepo"):
		return "gitrepo." + callee.Name()
	case strings.HasSuffix(pkgPath, "/internal/extpacks"):
		return "extpacks." + callee.Name()
	case strings.HasSuffix(pkgPath, "/internal/surfacecatalog") || strings.HasSuffix(pkgPath, "/coordinator/surfacecatalog"):
		return "surfacecatalog." + callee.Name()
	default:
		return ""
	}
}
