package interactions

import (
	"go/constant"
	"go/token"
	"go/types"
	"slices"
	"strings"

	"golang.org/x/tools/go/ssa"
)

type mapSinkKind int

const (
	sinkUnknown mapSinkKind = iota
	sinkHITL
	sinkTemplate
	sinkOARReject
	sinkOARPutReject
)

type mapSink struct {
	kind       mapSinkKind
	nestedRoot string
	rejectID   string
}

type mapCarrierInfo struct {
	skip          bool
	onlyTemplate  bool
	nestedRoot    string
	onlyOARAnchor string
	onlyRejectIDs []string
}

func (g *ProducerGraph) classifyMapCarrier(fn *ssa.Function, m ssa.Value) mapCarrierInfo {
	if unop, ok := m.(*ssa.UnOp); ok && unop.Op == token.MUL {
		if fa, isFA := unop.X.(*ssa.FieldAddr); isFA {
			return carrierFromSinks([]mapSink{classifyFieldAddrSink(fa)})
		}
	}
	var sinks []mapSink
	g.collectMapValueSinks(m, fn, 0, map[ssa.Value]bool{}, &sinks)
	return carrierFromSinks(sinks)
}

func carrierFromSinks(sinks []mapSink) mapCarrierInfo {
	if len(sinks) == 0 {
		return mapCarrierInfo{}
	}
	first := sinks[0]
	if first.kind == sinkUnknown {
		return mapCarrierInfo{}
	}
	sameNested := true
	allHaveReject := first.rejectID != ""
	rejectSet := map[string]bool{}
	if first.rejectID != "" {
		rejectSet[first.rejectID] = true
	}
	for _, s := range sinks[1:] {
		if s.kind != first.kind {
			return mapCarrierInfo{}
		}
		if s.nestedRoot != first.nestedRoot {
			sameNested = false
		}
		if s.rejectID == "" {
			allHaveReject = false
		} else {
			rejectSet[s.rejectID] = true
		}
	}
	nested := first.nestedRoot
	if !sameNested {
		nested = ""
	}
	var rejectIDs []string
	if allHaveReject && len(rejectSet) > 0 {
		rejectIDs = sortedKeysOf(rejectSet)
	}
	switch first.kind {
	case sinkHITL:
		return mapCarrierInfo{skip: true}
	case sinkTemplate:
		return mapCarrierInfo{onlyTemplate: true, nestedRoot: nested}
	case sinkOARReject:
		return mapCarrierInfo{onlyOARAnchor: "tool.rejected", onlyRejectIDs: rejectIDs}
	case sinkOARPutReject:
		return mapCarrierInfo{onlyRejectIDs: rejectIDs}
	default:
		return mapCarrierInfo{}
	}
}

func (g *ProducerGraph) collectMapValueSinks(v ssa.Value, fn *ssa.Function, depth int, seen map[ssa.Value]bool, out *[]mapSink) {
	if v == nil || seen[v] {
		return
	}
	seen[v] = true
	if depth > 7 || v.Referrers() == nil {
		*out = append(*out, mapSink{kind: sinkUnknown})
		return
	}
	for _, ref := range *v.Referrers() {
		switch r := ref.(type) {
		case *ssa.MapUpdate:
			if r.Map == v {
				continue
			}
			*out = append(*out, mapSink{kind: sinkUnknown})
		case *ssa.Lookup, *ssa.DebugRef:
			continue
		case *ssa.Range:
			g.collectRangeCopySinks(r, fn, depth+1, seen, out)
		case *ssa.ChangeType:
			g.collectMapValueSinks(r, fn, depth+1, seen, out)
		case *ssa.MakeInterface:
			g.collectMapValueSinks(r, fn, depth+1, seen, out)
		case *ssa.Phi:
			g.collectMapValueSinks(r, fn, depth+1, seen, out)
		case *ssa.Store:
			if r.Val == v {
				if fa, ok := r.Addr.(*ssa.FieldAddr); ok {
					*out = append(*out, classifyFieldAddrSink(fa))
					continue
				}
				if ia, ok := r.Addr.(*ssa.IndexAddr); ok {
					g.collectSliceElementMapSinks(ia.X, fn, depth+1, seen, out)
					continue
				}
			}
			*out = append(*out, mapSink{kind: sinkUnknown})
		case *ssa.Return:
			g.collectReturnSinks(r, v, fn, depth, seen, out)
		case ssa.CallInstruction:
			g.classifyMapCallSink(r, v, depth, seen, out)
		default:
			*out = append(*out, mapSink{kind: sinkUnknown})
		}
	}
}

func (g *ProducerGraph) collectRangeCopySinks(rng *ssa.Range, fn *ssa.Function, depth int, seen map[ssa.Value]bool, out *[]mapSink) {
	if rng == nil || rng.Referrers() == nil {
		return
	}
	for _, rRef := range *rng.Referrers() {
		nxt, isNext := rRef.(*ssa.Next)
		if !isNext || nxt.Referrers() == nil {
			continue
		}
		for _, nRef := range *nxt.Referrers() {
			ex, isEx := nRef.(*ssa.Extract)
			if !isEx || ex.Index != 2 || ex.Referrers() == nil {
				continue
			}
			for _, eRef := range *ex.Referrers() {
				if mu, isMU := eRef.(*ssa.MapUpdate); isMU && mu.Value == ex {
					g.collectMapValueSinks(mu.Map, fn, depth+1, seen, out)
				} else {
					*out = append(*out, mapSink{kind: sinkUnknown})
				}
			}
		}
	}
}

func (g *ProducerGraph) collectSliceElementMapSinks(sliceBase ssa.Value, fn *ssa.Function, depth int, seen map[ssa.Value]bool, out *[]mapSink) {
	if sliceBase == nil || seen[sliceBase] {
		return
	}
	seen[sliceBase] = true
	if depth > 7 || sliceBase.Referrers() == nil {
		*out = append(*out, mapSink{kind: sinkUnknown})
		return
	}
	for _, ref := range *sliceBase.Referrers() {
		switch r := ref.(type) {
		case *ssa.IndexAddr, *ssa.DebugRef:
			continue
		case *ssa.Slice:
			g.collectSliceElementMapSinks(r, fn, depth+1, seen, out)
		case *ssa.Phi:
			g.collectSliceElementMapSinks(r, fn, depth+1, seen, out)
		case *ssa.MakeInterface:
			g.collectSliceElementMapSinks(r, fn, depth+1, seen, out)
		case *ssa.MapUpdate:
			if r.Value == sliceBase {
				var parentSinks []mapSink
				g.collectMapValueSinks(r.Map, fn, depth+1, map[ssa.Value]bool{}, &parentSinks)
				parentCarrier := carrierFromSinks(parentSinks)
				if parentCarrier.onlyTemplate {
					nested := "slice_elem"
					if c, isConst := r.Key.(*ssa.Const); isConst && c.Value != nil && c.Value.Kind() == constant.String {
						if k := constant.StringVal(c.Value); k != "" {
							nested = k
						}
					}
					*out = append(*out, mapSink{kind: sinkTemplate, nestedRoot: nested})
					continue
				}
			}
			*out = append(*out, mapSink{kind: sinkUnknown})
		case ssa.CallInstruction:
			if b, isBuiltin := r.Common().Value.(*ssa.Builtin); isBuiltin && b.Name() == "append" {
				if cv, ok := r.(ssa.Value); ok {
					g.collectSliceElementMapSinks(cv, fn, depth+1, seen, out)
					continue
				}
			}
			*out = append(*out, mapSink{kind: sinkUnknown})
		default:
			*out = append(*out, mapSink{kind: sinkUnknown})
		}
	}
}

func (g *ProducerGraph) collectReturnSinks(r *ssa.Return, v ssa.Value, fn *ssa.Function, depth int, seen map[ssa.Value]bool, out *[]mapSink) {
	csList := g.callSites[fn]
	if len(csList) == 0 {
		*out = append(*out, mapSink{kind: sinkUnknown})
		return
	}
	resIdx := slices.Index(r.Results, v)
	for _, cs := range csList {
		cv, ok := cs.instr.(ssa.Value)
		if !ok {
			*out = append(*out, mapSink{kind: sinkUnknown})
			continue
		}
		if len(r.Results) == 1 {
			g.collectMapValueSinks(cv, cs.caller, depth+1, seen, out)
			continue
		}
		if cv.Referrers() != nil {
			for _, exRef := range *cv.Referrers() {
				if ex, isEx := exRef.(*ssa.Extract); isEx && ex.Index == resIdx {
					g.collectMapValueSinks(ex, cs.caller, depth+1, seen, out)
				}
			}
		}
	}
}

func (g *ProducerGraph) classifyMapCallSink(call ssa.CallInstruction, v ssa.Value, depth int, seen map[ssa.Value]bool, out *[]mapSink) {
	common := call.Common()
	if b, isBuiltin := common.Value.(*ssa.Builtin); isBuiltin {
		if b.Name() == "len" {
			return
		}
		*out = append(*out, mapSink{kind: sinkUnknown})
		return
	}
	if common.IsInvoke() {
		mName := common.Method.Name()
		if mName == "RenderTemplate" || mName == "Render" || mName == "RenderWithReader" {
			*out = append(*out, mapSink{kind: sinkTemplate})
			return
		}
		*out = append(*out, mapSink{kind: sinkUnknown})
		return
	}
	callee := common.StaticCallee()
	if callee == nil {
		*out = append(*out, mapSink{kind: sinkUnknown})
		return
	}
	pkgPath := ""
	if callee.Pkg != nil && callee.Pkg.Pkg != nil {
		pkgPath = callee.Pkg.Pkg.Path()
	}
	switch {
	case callee.Name() == "PutRejectData" && len(common.Args) >= 3 && common.Args[2] == v:
		code := ""
		if c, ok := common.Args[1].(*ssa.Const); ok && c.Value != nil && c.Value.Kind() == constant.String {
			code = constant.StringVal(c.Value)
		}
		*out = append(*out, mapSink{kind: sinkOARPutReject, rejectID: code})
	case strings.HasSuffix(pkgPath, "/internal/coordinator/kick") && callee.Name() == "WithVars":
		*out = append(*out, mapSink{kind: sinkTemplate})
	case strings.HasSuffix(pkgPath, "/internal/prompts") && strings.HasPrefix(callee.Name(), "Render"):
		*out = append(*out, mapSink{kind: sinkTemplate})
	case strings.HasSuffix(pkgPath, "/internal/hitl"):
		*out = append(*out, mapSink{kind: sinkHITL})
	default:
		argIdx := slices.Index(common.Args, v)
		if argIdx >= 0 && argIdx < len(callee.Params) && len(callee.Blocks) > 0 && depth < 6 {
			g.collectMapValueSinks(callee.Params[argIdx], callee, depth+1, seen, out)
			return
		}
		*out = append(*out, mapSink{kind: sinkUnknown})
	}
}

func classifyFieldAddrSink(fa *ssa.FieldAddr) mapSink {
	k, ok := fieldAddrKey(fa)
	if !ok {
		return mapSink{kind: sinkUnknown}
	}
	fieldName := structFieldOnlyName(fa.X.Type(), fa.Field)
	switch {
	case strings.Contains(k.structName, "/internal/hitl."):
		return mapSink{kind: sinkHITL}
	case strings.HasSuffix(k.structName, "/internal/coordinator/anchor.Envelope"):
		if fieldName == "WorkerDecisionRequest" {
			return mapSink{kind: sinkTemplate, nestedRoot: "last_worker_decision_request"}
		}
		return mapSink{kind: sinkTemplate}
	case strings.Contains(k.structName, "/internal/coordinator/kick."):
		if fieldName == "WorkerDecisionRequest" {
			return mapSink{kind: sinkTemplate, nestedRoot: "last_worker_decision_request"}
		}
		return mapSink{kind: sinkTemplate}
	case strings.HasSuffix(k.structName, "/internal/toolrejection.ToolReject") && fieldName == "Data":
		return mapSink{kind: sinkOARReject, rejectID: extractConstantToolRejectCode(fa.X)}
	default:
		return mapSink{kind: sinkUnknown}
	}
}

func structFieldOnlyName(t types.Type, fieldIdx int) string {
	if t == nil {
		return ""
	}
	if ptr, ok := t.Underlying().(*types.Pointer); ok {
		t = ptr.Elem()
	}
	st, ok := t.Underlying().(*types.Struct)
	if !ok || fieldIdx < 0 || fieldIdx >= st.NumFields() {
		return ""
	}
	return st.Field(fieldIdx).Name()
}

func extractConstantToolRejectCode(base ssa.Value) string {
	alloc, ok := base.(*ssa.Alloc)
	if !ok || alloc.Referrers() == nil {
		return ""
	}
	code := ""
	for _, ref := range *alloc.Referrers() {
		fa, isFA := ref.(*ssa.FieldAddr)
		if !isFA || structFieldOnlyName(fa.X.Type(), fa.Field) != "Code" || fa.Referrers() == nil {
			continue
		}
		for _, fRef := range *fa.Referrers() {
			st, isSt := fRef.(*ssa.Store)
			if !isSt || st.Addr != fa {
				continue
			}
			c, isConst := st.Val.(*ssa.Const)
			if !isConst || c.Value == nil || c.Value.Kind() != constant.String {
				return ""
			}
			val := constant.StringVal(c.Value)
			if code != "" && code != val {
				return ""
			}
			code = val
		}
	}
	return code
}
