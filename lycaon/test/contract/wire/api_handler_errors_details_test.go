package contract

import (
	"fmt"
	"go/constant"
	"go/types"
	"sort"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// keySet is a details resolution result: literal map keys plus the sites whose
// keys the scan could not trace.
type keySet struct {
	keys       map[string]bool
	unresolved map[string]bool
}

func newKeySet() *keySet {
	return &keySet{keys: map[string]bool{}, unresolved: map[string]bool{}}
}

// detailPair ties the codes of one error body to the details keys it carries.
// Constants are resolved where the body is built; the parameters name what the
// caller supplies.
type detailPair struct {
	codeParams map[int]bool
	codes      map[string]bool
	keyParams  map[int]bool
	keys       map[string]bool
	unresolved map[string]bool
}

func newDetailPair() detailPair {
	return detailPair{
		codeParams: map[int]bool{}, codes: map[string]bool{},
		keyParams: map[int]bool{}, keys: map[string]bool{}, unresolved: map[string]bool{},
	}
}

func (d detailPair) identity() string {
	return strings.Join([]string{
		joinIntSet(d.codeParams), joinSet(d.codes), joinIntSet(d.keyParams), joinSet(d.keys), joinSet(d.unresolved),
	}, "|")
}

// detailEmissions are the details keys each code is answered with, and the
// details values the scan could not trace to literal keys.
type detailEmissions struct {
	keys       map[string]map[string]bool
	unresolved map[string]bool
}

// detailsOf collects every (code, details key) pair the handlers can answer.
func (p *handlerErrorProgram) detailsOf(handlers []*handlerErrors) detailEmissions {
	out := detailEmissions{keys: map[string]map[string]bool{}, unresolved: map[string]bool{}}
	for _, h := range handlers {
		for fn := range p.reach(h.roots...) {
			for _, pair := range p.pairsOf(fn) {
				for site := range pair.unresolved {
					out.unresolved[site] = true
				}
				for code := range pair.codes {
					for key := range pair.keys {
						if out.keys[code] == nil {
							out.keys[code] = map[string]bool{}
						}
						out.keys[code][key] = true
					}
				}
			}
		}
	}
	return out
}

// pairsOf is every error body fn builds or has built with details: the body
// fn itself stores details into, and each pair of a callee bound to the
// arguments fn passes.
func (p *handlerErrorProgram) pairsOf(fn *ssa.Function) []detailPair {
	if pairs, ok := p.pairs[fn]; ok {
		return pairs
	}
	if p.pairing[fn] {
		return nil
	}
	p.pairing[fn] = true
	base := newDetailPair()
	built := false
	seen := map[string]bool{}
	var out []detailPair
	add := func(pair detailPair) {
		if id := pair.identity(); !seen[id] {
			seen[id] = true
			out = append(out, pair)
		}
	}
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			switch in := instr.(type) {
			case *ssa.Store:
				if p.isErrorCodeField(in.Addr) {
					bindCode(base, p, in.Val, fn)
				}
				if p.isErrorBodyField(in.Addr, "Details") {
					built = true
					bindKeys(base, p, in.Val, fn)
				}
			case ssa.CallInstruction:
				common := in.Common()
				for _, target := range p.targets(common) {
					for i := range p.flowParams(target) {
						bindCode(base, p, callArg(common, i), fn)
					}
					for _, pair := range p.pairsOf(target) {
						add(p.bindPair(pair, common, fn))
					}
				}
			}
		}
	}
	if built {
		add(base)
	}
	delete(p.pairing, fn)
	p.pairs[fn] = out
	return out
}

// bindPair restates a callee's pair in the caller: parameters become the
// caller's arguments, resolved where the caller builds them.
func (p *handlerErrorProgram) bindPair(pair detailPair, common *ssa.CallCommon, fn *ssa.Function) detailPair {
	out := newDetailPair()
	for code := range pair.codes {
		out.codes[code] = true
	}
	for key := range pair.keys {
		out.keys[key] = true
	}
	for site := range pair.unresolved {
		out.unresolved[site] = true
	}
	for i := range pair.codeParams {
		bindCode(out, p, callArg(common, i), fn)
	}
	for i := range pair.keyParams {
		bindKeys(out, p, callArg(common, i), fn)
	}
	return out
}

func bindCode(pair detailPair, p *handlerErrorProgram, v ssa.Value, fn *ssa.Function) {
	for i := range paramsOf(v, fn, map[ssa.Value]bool{}) {
		pair.codeParams[i] = true
	}
	for code := range p.resolve(v, fn).codes {
		pair.codes[code] = true
	}
}

func bindKeys(pair detailPair, p *handlerErrorProgram, v ssa.Value, fn *ssa.Function) {
	for i := range paramsOf(v, fn, map[ssa.Value]bool{}) {
		pair.keyParams[i] = true
	}
	keys := newKeySet()
	p.resolveKeysInto(keys, v, fn, map[ssa.Value]bool{})
	for key := range keys.keys {
		pair.keys[key] = true
	}
	for site := range keys.unresolved {
		pair.unresolved[site] = true
	}
}

// isErrorBodyField reports a store address that is the named field of the
// error body.
func (p *handlerErrorProgram) isErrorBodyField(addr ssa.Value, name string) bool {
	field, ok := addr.(*ssa.FieldAddr)
	if !ok {
		return false
	}
	body := field.X.Type().Underlying().(*types.Pointer).Elem()
	strct, ok := body.Underlying().(*types.Struct)
	return ok && types.TypeString(body, nil) == p.tree.errorBody && strct.Field(field.Field).Name() == name
}

// resolveKeysInto traces a details map to its literal keys: the constant keys
// written into the map a function makes, through phis and conversions, and
// into the returns of the function that produces it.
func (p *handlerErrorProgram) resolveKeysInto(out *keySet, v ssa.Value, fn *ssa.Function, seen map[ssa.Value]bool) {
	if v == nil || seen[v] {
		return
	}
	seen[v] = true
	switch value := v.(type) {
	case *ssa.Const:
		if !value.IsNil() {
			out.unresolved[p.site(v, fn)] = true
		}
	case *ssa.MakeMap:
		for _, ref := range *value.Referrers() {
			update, ok := ref.(*ssa.MapUpdate)
			if !ok || update.Map != value {
				continue
			}
			key, ok := update.Key.(*ssa.Const)
			if !ok || key.Value == nil || key.Value.Kind() != constant.String {
				out.unresolved[p.site(update.Key, fn)] = true
				continue
			}
			out.keys[constant.StringVal(key.Value)] = true
		}
	case *ssa.Phi:
		for _, edge := range value.Edges {
			p.resolveKeysInto(out, edge, fn, seen)
		}
	case *ssa.ChangeType:
		p.resolveKeysInto(out, value.X, fn, seen)
	case *ssa.MakeInterface:
		p.resolveKeysInto(out, value.X, fn, seen)
	case *ssa.Parameter:
		// The caller's argument is resolved where the caller is reached.
	case *ssa.Call:
		p.resolveKeyReturns(out, &value.Call, 0, fn, seen)
	case *ssa.Extract:
		if call, ok := value.Tuple.(*ssa.Call); ok {
			p.resolveKeyReturns(out, &call.Call, value.Index, fn, seen)
		} else {
			out.unresolved[p.site(v, fn)] = true
		}
	default:
		out.unresolved[p.site(v, fn)] = true
	}
}

func (p *handlerErrorProgram) resolveKeyReturns(out *keySet, common *ssa.CallCommon, index int, fn *ssa.Function, seen map[ssa.Value]bool) {
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
			p.resolveKeysInto(out, result, target, seen)
			for i := range paramsOf(result, target, map[ssa.Value]bool{}) {
				p.resolveKeysInto(out, callArg(common, i), fn, seen)
			}
		}
	}
}

// undeclaredDetailViolations reports each details key a code is answered with
// that its notice's context_schema does not declare. Details are the notice's
// render context, so an undeclared key is a fact no client contract names.
func undeclaredDetailViolations(emitted detailEmissions, declared func(code string) (map[string]bool, bool)) []string {
	var out []string
	for code, keys := range emitted.keys {
		schema, ok := declared(code)
		if !ok {
			out = append(out, fmt.Sprintf("%s: answered with details %s but has no notice", code, joinSet(keys)))
			continue
		}
		for key := range keys {
			if !schema[key] {
				out = append(out, fmt.Sprintf("%s: details.%s is not declared by its notice context_schema", code, key))
			}
		}
	}
	for site := range emitted.unresolved {
		out = append(out, fmt.Sprintf("details at %s do not resolve to literal keys", site))
	}
	sort.Strings(out)
	return out
}

func joinSet(set map[string]bool) string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func joinIntSet(set map[int]bool) string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, fmt.Sprint(key))
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}
