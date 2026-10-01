package contract

import (
	"fmt"
	"go/constant"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// emittedIn is the codes fn itself hands to an error body: stored into the
// body's Code field, or passed to a parameter that reaches one.
func (p *handlerErrorProgram) emittedIn(fn *ssa.Function) *codeSet {
	out := newCodeSet()
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			switch in := instr.(type) {
			case *ssa.Store:
				if p.isErrorCodeField(in.Addr) {
					out.merge(p.resolve(in.Val, fn))
				}
			case ssa.CallInstruction:
				common := in.Common()
				for _, target := range p.targets(common) {
					for i := range p.flowParams(target) {
						if arg := callArg(common, i); arg != nil {
							out.merge(p.resolve(arg, fn))
						}
					}
				}
			}
		}
	}
	return out
}

// isErrorCodeField reports a store address that is the error body's Code.
func (p *handlerErrorProgram) isErrorCodeField(addr ssa.Value) bool {
	field, ok := addr.(*ssa.FieldAddr)
	if !ok {
		return false
	}
	body := field.X.Type().Underlying().(*types.Pointer).Elem()
	strct, ok := body.Underlying().(*types.Struct)
	return ok && types.TypeString(body, nil) == p.tree.errorBody && strct.Field(field.Field).Name() == "Code"
}

// flowParams is the set of fn's parameters whose value reaches an error
// body's Code field, directly or through the functions fn calls.
func (p *handlerErrorProgram) flowParams(fn *ssa.Function) map[int]bool {
	if flows, ok := p.flows[fn]; ok {
		return flows
	}
	if p.flowing[fn] {
		return nil
	}
	p.flowing[fn] = true
	out := map[int]bool{}
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			switch in := instr.(type) {
			case *ssa.Store:
				if p.isErrorCodeField(in.Addr) {
					for i := range paramsOf(in.Val, fn, map[ssa.Value]bool{}) {
						out[i] = true
					}
				}
			case ssa.CallInstruction:
				common := in.Common()
				for _, target := range p.targets(common) {
					for i := range p.flowParams(target) {
						for j := range paramsOf(callArg(common, i), fn, map[ssa.Value]bool{}) {
							out[j] = true
						}
					}
				}
			}
		}
	}
	delete(p.flowing, fn)
	p.flows[fn] = out
	return out
}

// callArg is the value a call binds to the target's parameter i. An interface
// call carries its receiver outside Args.
func callArg(common *ssa.CallCommon, i int) ssa.Value {
	if common.IsInvoke() {
		if i == 0 {
			return common.Value
		}
		i--
	}
	if i < len(common.Args) {
		return common.Args[i]
	}
	return nil
}

// paramsOf is the set of fn's parameters v is copied from.
func paramsOf(v ssa.Value, fn *ssa.Function, seen map[ssa.Value]bool) map[int]bool {
	out := map[int]bool{}
	if v == nil || seen[v] {
		return out
	}
	seen[v] = true
	switch value := v.(type) {
	case *ssa.Parameter:
		for i, param := range fn.Params {
			if param == value {
				out[i] = true
			}
		}
	case *ssa.Phi:
		for _, edge := range value.Edges {
			for i := range paramsOf(edge, fn, seen) {
				out[i] = true
			}
		}
	case *ssa.ChangeType:
		return paramsOf(value.X, fn, seen)
	case *ssa.Convert:
		return paramsOf(value.X, fn, seen)
	case *ssa.MakeInterface:
		return paramsOf(value.X, fn, seen)
	}
	return out
}

// resolve traces a code value to constants: through phis and conversions,
// into the returns of the functions that produce it (a parameter returned is
// the caller's argument), and to every value stored anywhere into the struct
// field, map, or package variable it is read from.
func (p *handlerErrorProgram) resolve(v ssa.Value, fn *ssa.Function) *codeSet {
	out := newCodeSet()
	p.resolveInto(out, v, fn, map[ssa.Value]bool{})
	return out
}

func (p *handlerErrorProgram) resolveInto(out *codeSet, v ssa.Value, fn *ssa.Function, seen map[ssa.Value]bool) {
	if v == nil || seen[v] {
		return
	}
	seen[v] = true
	switch value := v.(type) {
	case *ssa.Const:
		// The empty string is the zero value of an unset code, not a code.
		if value.Value != nil && value.Value.Kind() == constant.String && constant.StringVal(value.Value) != "" {
			out.codes[constant.StringVal(value.Value)] = true
		}
	case *ssa.Phi:
		for _, edge := range value.Edges {
			p.resolveInto(out, edge, fn, seen)
		}
	case *ssa.ChangeType:
		p.resolveInto(out, value.X, fn, seen)
	case *ssa.Convert:
		p.resolveInto(out, value.X, fn, seen)
	case *ssa.MakeInterface:
		p.resolveInto(out, value.X, fn, seen)
	case *ssa.Parameter:
		// The caller's argument is resolved where the caller is reached.
	case *ssa.Call:
		if !p.isCodeType(value.Type()) {
			// A plain string computed by a call is not followed: string
			// helpers would pull every string in the program into the set.
			out.unresolved[p.site(v, fn)] = true
			return
		}
		p.resolveReturns(out, &value.Call, 0, fn, seen)
	case *ssa.Extract:
		if call, ok := value.Tuple.(*ssa.Call); ok && p.isCodeType(value.Type()) {
			p.resolveReturns(out, &call.Call, value.Index, fn, seen)
		} else {
			out.unresolved[p.site(v, fn)] = true
		}
	case *ssa.UnOp:
		p.resolveLoad(out, value, fn, seen)
	case *ssa.Field:
		p.resolveStored(out, p.fieldStores[fieldKey(value.X.Type(), value.Field)], v, fn, seen)
	case *ssa.Lookup:
		p.resolveStored(out, p.mapStores[types.TypeString(value.X.Type(), nil)], v, fn, seen)
	case *ssa.FreeVar:
		p.resolveCaptured(out, value, fn, seen)
	default:
		out.unresolved[p.site(v, fn)] = true
	}
}

func (p *handlerErrorProgram) resolveLoad(out *codeSet, load *ssa.UnOp, fn *ssa.Function, seen map[ssa.Value]bool) {
	switch addr := load.X.(type) {
	case *ssa.FieldAddr:
		key := fieldKey(addr.X.Type().Underlying().(*types.Pointer).Elem(), addr.Field)
		p.resolveStored(out, p.fieldStores[key], load, fn, seen)
	case *ssa.Global:
		p.resolveStored(out, p.globalStores[addr], load, fn, seen)
	default:
		out.unresolved[p.site(load, fn)] = true
	}
}

func (p *handlerErrorProgram) resolveStored(out *codeSet, stores []storedValue, v ssa.Value, fn *ssa.Function, seen map[ssa.Value]bool) {
	if len(stores) == 0 {
		out.unresolved[p.site(v, fn)] = true
		return
	}
	for _, store := range stores {
		p.resolveInto(out, store.value, store.fn, seen)
		// A constructor stores its parameter: resolve what its callers pass.
		for i := range paramsOf(store.value, store.fn, map[ssa.Value]bool{}) {
			for _, site := range p.callSites[store.fn] {
				p.resolveInto(out, callArg(site.common, i), site.fn, seen)
			}
		}
	}
}

// resolveReturns resolves result index of every function the call may reach,
// across the whole program: an error type declared outside the HTTP layer
// may carry the code.
func (p *handlerErrorProgram) resolveReturns(out *codeSet, common *ssa.CallCommon, index int, fn *ssa.Function, seen map[ssa.Value]bool) {
	targets := p.implementations(common)
	if len(targets) == 0 {
		out.unresolved[p.site(common.Value, fn)] = true
		return
	}
	for _, target := range targets {
		if len(target.Blocks) == 0 {
			out.unresolved[fmt.Sprintf("%s (returned by %s, which has no body)", p.position(common.Pos()), target)] = true
			continue
		}
		for _, block := range target.Blocks {
			ret, ok := block.Instrs[len(block.Instrs)-1].(*ssa.Return)
			if !ok || index >= len(ret.Results) {
				continue
			}
			result := ret.Results[index]
			p.resolveInto(out, result, target, seen)
			for i := range paramsOf(result, target, map[ssa.Value]bool{}) {
				p.resolveInto(out, callArg(common, i), fn, seen)
			}
		}
	}
}

// implementations resolves a call for code resolution: the static callee, or
// every method of that name on a program type implementing the interface.
func (p *handlerErrorProgram) implementations(common *ssa.CallCommon) []*ssa.Function {
	if !common.IsInvoke() {
		return p.targets(common)
	}
	iface, ok := common.Value.Type().Underlying().(*types.Interface)
	if !ok {
		return nil
	}
	var out []*ssa.Function
	for _, named := range p.programTypes {
		for _, recv := range []types.Type{named, types.NewPointer(named)} {
			if !types.Implements(recv, iface) {
				continue
			}
			if sel := p.prog.MethodSets.MethodSet(recv).Lookup(common.Method.Pkg(), common.Method.Name()); sel != nil {
				if target := p.prog.MethodValue(sel); target != nil {
					out = append(out, target)
				}
			}
			break
		}
	}
	return out
}

// resolveCaptured resolves a closure's free variable through the bindings of
// every closure its parent creates from it.
func (p *handlerErrorProgram) resolveCaptured(out *codeSet, free *ssa.FreeVar, fn *ssa.Function, seen map[ssa.Value]bool) {
	index := -1
	for i, candidate := range fn.FreeVars {
		if candidate == free {
			index = i
		}
	}
	parent := fn.Parent()
	if parent == nil || index < 0 {
		out.unresolved[p.site(free, fn)] = true
		return
	}
	for _, block := range parent.Blocks {
		for _, instr := range block.Instrs {
			if closure, ok := instr.(*ssa.MakeClosure); ok && closure.Fn == fn {
				p.resolveInto(out, closure.Bindings[index], parent, seen)
			}
		}
	}
}

func (p *handlerErrorProgram) site(v ssa.Value, fn *ssa.Function) string {
	pos := fn.Pos()
	if v != nil && v.Pos().IsValid() {
		pos = v.Pos()
	}
	return fmt.Sprintf("%s (%T in %s)", p.position(pos), v, fn.Name())
}
