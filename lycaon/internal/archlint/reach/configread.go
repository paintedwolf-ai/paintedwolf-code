package reach

import (
	"go/types"
	"reflect"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/archlint"

	"golang.org/x/tools/go/callgraph/rta"
	"golang.org/x/tools/go/ssa"
)

// KindUnreadConfigField is a decoded configuration key no product code reads.
const KindUnreadConfigField = "unread-config-field"

const yamlPackage = "gopkg.in/yaml.v3"

// encoderPackages read every field of a struct they serialize.
var encoderPackages = map[string]bool{"encoding/json": true, yamlPackage: true}

// configFlow is what the product reads from the structs it decodes YAML into.
type configFlow struct {
	targets    map[string]*types.Named
	fieldReads map[string]bool
	// judged holds every stable field name walked in this pass; unread maps
	// the ones nothing read to their finding.
	judged map[string]bool
	unread map[string]archlint.Finding
}

func scanConfig(prog *ssa.Program, res *rta.Result, cfg Config) configFlow {
	flow := configFlow{targets: map[string]*types.Named{}, fieldReads: map[string]bool{}, judged: map[string]bool{}}
	wrappers := decoderWrappers(res)
	for fn := range res.Reachable {
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				flow.scan(instr, wrappers)
			}
		}
	}
	flow.unread = flow.judge(prog, cfg)
	return flow
}

// unreadConfigFields reports a key unread in every GOOS that decodes it.
func unreadConfigFields(passes []*pass) []archlint.Finding {
	found := map[string]archlint.Finding{}
	read := map[string]bool{}
	for _, p := range passes {
		for key := range p.config.judged {
			if f, ok := p.config.unread[key]; ok {
				found[key] = f
			} else {
				read[key] = true
			}
		}
	}
	var out []archlint.Finding
	for key, f := range found {
		if !read[key] {
			out = append(out, f)
		}
	}
	return out
}

func (f configFlow) scan(instr ssa.Instruction, wrappers map[*ssa.Function][]int) {
	switch in := instr.(type) {
	case *ssa.FieldAddr:
		if fieldReadThrough(in) {
			if field := fieldOf(in.X.Type(), in.Field); field != nil {
				f.fieldReads[fieldKey(field)] = true
			}
		}
	case *ssa.Field:
		if field := fieldOf(in.X.Type(), in.Field); field != nil {
			f.fieldReads[fieldKey(field)] = true
		}
	case ssa.CallInstruction:
		call := in.Common()
		callee := call.StaticCallee()
		if callee == nil {
			return
		}
		if positions, ok := decodeTargets(callee, wrappers); ok {
			for _, i := range positions {
				if i < len(call.Args) {
					if named := targetStruct(call.Args[i]); named != nil {
						f.targets[named.Obj().Pkg().Path()+"."+named.Obj().Name()] = named
					}
				}
			}
		}
		if callee.Pkg != nil && encoderPackages[callee.Pkg.Pkg.Path()] && strings.HasPrefix(callee.Name(), "Marshal") || isEncodeMethod(callee) {
			for _, arg := range call.Args {
				f.readAll(argType(arg), map[types.Type]bool{})
			}
		}
	}
}

// fieldReadThrough reports a field address used for anything but a store into it.
func fieldReadThrough(fa *ssa.FieldAddr) bool {
	refs := fa.Referrers()
	if refs == nil {
		return false
	}
	for _, ref := range *refs {
		if store, ok := ref.(*ssa.Store); ok && store.Addr == fa {
			continue
		}
		return true
	}
	return false
}

func (f configFlow) readAll(t types.Type, seen map[types.Type]bool) {
	if ptr, ok := t.Underlying().(*types.Pointer); ok {
		t = ptr.Elem()
	}
	if seen[t] {
		return
	}
	seen[t] = true
	st, ok := t.Underlying().(*types.Struct)
	if !ok {
		return
	}
	for i := range st.NumFields() {
		field := st.Field(i)
		f.fieldReads[fieldKey(field)] = true
		f.readAll(field.Type(), seen)
	}
}

// decoderWrappers finds reachable functions that hand one of their parameters
// to a YAML decoder, directly or through another wrapper.
func decoderWrappers(res *rta.Result) map[*ssa.Function][]int {
	wrappers := map[*ssa.Function][]int{}
	for changed := true; changed; {
		changed = false
		for fn := range res.Reachable {
			if _, done := wrappers[fn]; done {
				continue
			}
			for _, b := range fn.Blocks {
				for _, instr := range b.Instrs {
					call, ok := instr.(ssa.CallInstruction)
					if !ok || call.Common().StaticCallee() == nil {
						continue
					}
					positions, ok := decodeTargets(call.Common().StaticCallee(), wrappers)
					if !ok {
						continue
					}
					for _, i := range positions {
						if i >= len(call.Common().Args) {
							continue
						}
						if param, ok := unwrapInterface(call.Common().Args[i]).(*ssa.Parameter); ok {
							if index := paramIndex(fn, param); index >= 0 {
								wrappers[fn] = append(wrappers[fn], index)
								changed = true
							}
						}
					}
				}
			}
		}
	}
	return wrappers
}

// decodeTargets names the argument positions a call decodes into.
func decodeTargets(callee *ssa.Function, wrappers map[*ssa.Function][]int) ([]int, bool) {
	if positions, ok := wrappers[callee]; ok {
		return positions, true
	}
	if callee.Pkg == nil || callee.Pkg.Pkg.Path() != yamlPackage {
		return nil, false
	}
	switch {
	case callee.Signature.Recv() == nil && callee.Name() == "Unmarshal":
		return []int{1}, true
	case callee.Signature.Recv() != nil && callee.Name() == "Decode":
		return []int{1}, true
	}
	return nil, false
}

func isEncodeMethod(callee *ssa.Function) bool {
	if callee.Signature.Recv() == nil || callee.Name() != "Encode" {
		return false
	}
	named := receiverNamed(callee)
	return named != nil && named.Obj().Pkg() != nil && encoderPackages[named.Obj().Pkg().Path()]
}

func paramIndex(fn *ssa.Function, param *ssa.Parameter) int {
	for i, p := range fn.Params {
		if p == param {
			return i
		}
	}
	return -1
}

func unwrapInterface(v ssa.Value) ssa.Value {
	for {
		switch x := v.(type) {
		case *ssa.MakeInterface:
			v = x.X
		case *ssa.ChangeInterface:
			v = x.X
		default:
			return v
		}
	}
}

func argType(v ssa.Value) types.Type { return unwrapInterface(v).Type() }

// targetStruct names the struct a decode call fills, when the argument is a
// pointer to a named struct.
func targetStruct(v ssa.Value) *types.Named {
	ptr, ok := argType(v).Underlying().(*types.Pointer)
	if !ok {
		return nil
	}
	named, ok := ptr.Elem().(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return nil
	}
	if _, ok := named.Underlying().(*types.Struct); !ok {
		return nil
	}
	return named
}

// judge walks each decoded struct's keys, skipping types that decode
// themselves, and reports the keys nothing reads.
func (f configFlow) judge(prog *ssa.Program, cfg Config) map[string]archlint.Finding {
	out := map[string]archlint.Finding{}
	seen := map[*types.Named]bool{}
	var walk func(named *types.Named)
	walk = func(named *types.Named) {
		if seen[named] || !cfg.reported(named.Obj().Pkg().Path()) || decodesItself(named) {
			return
		}
		seen[named] = true
		st := named.Underlying().(*types.Struct)
		owner := named.Obj().Pkg().Path() + "." + named.Obj().Name()
		for i := range st.NumFields() {
			field := st.Field(i)
			name, inline, ok := yamlKey(field, st.Tag(i))
			if !ok {
				continue
			}
			key := owner + "." + field.Name()
			if !inline {
				f.judged[key] = true
			}
			if !inline && !f.fieldReads[fieldKey(field)] {
				out[key] = archlint.Finding{
					Kind: KindUnreadConfigField, Key: key, Pos: relPos(cfg.Dir, prog.Fset.Position(field.Pos())),
					Message: "configuration key " + name + " (" + key + ") is decoded but no shipped program reads it",
				}
			}
			for _, inner := range structsIn(field.Type()) {
				walk(inner)
			}
		}
	}
	keys := make([]string, 0, len(f.targets))
	for key := range f.targets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		walk(f.targets[key])
	}
	return out
}

// yamlKey returns the key a field decodes from, whether its fields are inlined,
// and whether to judge it. A field tagged archlint:"unread" is accepted so a
// strict decoder takes the key, and nothing in the product acts on it.
func yamlKey(field *types.Var, tag string) (string, bool, bool) {
	if !field.Exported() {
		return "", false, false
	}
	if reflect.StructTag(tag).Get("archlint") == "unread" {
		return "", false, false
	}
	value, _ := reflect.StructTag(tag).Lookup("yaml")
	name, opts, _ := strings.Cut(value, ",")
	if name == "-" {
		return "", false, false
	}
	if name == "" {
		name = strings.ToLower(field.Name())
	}
	inline := strings.Contains(","+opts+",", ",inline,")
	return name, inline, true
}

// decodesItself reports a type with its own UnmarshalYAML, whose fields are
// filled however that method chooses.
func decodesItself(named *types.Named) bool {
	for _, t := range []types.Type{named, types.NewPointer(named)} {
		set := types.NewMethodSet(t)
		for i := range set.Len() {
			if set.At(i).Obj().Name() == "UnmarshalYAML" {
				return true
			}
		}
	}
	return false
}

// structsIn lists the named structs a field holds directly or as elements.
func structsIn(t types.Type) []*types.Named {
	switch x := t.(type) {
	case *types.Pointer:
		return structsIn(x.Elem())
	case *types.Slice:
		return structsIn(x.Elem())
	case *types.Array:
		return structsIn(x.Elem())
	case *types.Map:
		return structsIn(x.Elem())
	case *types.Named:
		if _, ok := x.Underlying().(*types.Struct); ok && x.Obj().Pkg() != nil {
			return []*types.Named{x}
		}
	}
	return nil
}
