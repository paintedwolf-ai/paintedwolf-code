package reach

import (
	"fmt"
	"go/constant"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/callgraph/rta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
)

// productFlow is what the product's reachable code does with identifiers:
// string constants it sends somewhere, struct fields it writes, and map keys
// it fills from a field.
type productFlow struct {
	flows map[string]bool
	// prefixes start names the product builds at run time, such as a fact
	// family completed by a field name.
	prefixes    map[string]bool
	fieldWrites map[string]bool
	keyFields   map[string]map[string]bool
}

// decoderPackages fill a struct they receive by address through reflection.
var decoderPackages = map[string]bool{"encoding/json": true, "gopkg.in/yaml.v3": true}

func productMains(lc *packages.Config, pkgs []*ssa.Package, patterns []string) ([]*ssa.Function, error) {
	paths, err := mainPaths(lc, patterns)
	if err != nil {
		return nil, err
	}
	var roots []*ssa.Function
	for _, pkg := range pkgs {
		if pkg != nil && paths[pkg.Pkg.Path()] && pkg.Pkg.Name() == "main" {
			roots = append(roots, pkg.Func("init"), pkg.Func("main"))
		}
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("no product main packages in %v", patterns)
	}
	return roots, nil
}

func scanProduct(prog *ssa.Program, res *rta.Result, cfg Config) productFlow {
	flow := productFlow{flows: map[string]bool{}, prefixes: map[string]bool{}, fieldWrites: map[string]bool{}, keyFields: map[string]map[string]bool{}}
	plumbing := map[string]bool{}
	var declarations []string
	if cfg.Emitters != nil {
		declarations = cfg.Emitters.Plumbing
	}
	for _, rel := range declarations {
		plumbing[filepath.Join(cfg.Dir, filepath.FromSlash(rel))] = true
	}
	for fn := range res.Reachable {
		providers := map[ssa.Value]*providedEntry{}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				// Package initializers gather every file's variables, so the
				// instruction's own position decides.
				pos := instr.Pos()
				if !pos.IsValid() {
					pos = fn.Pos()
				}
				declaring := pos.IsValid() && plumbing[prog.Fset.Position(pos).Filename]
				if declaring {
					recordProvider(providers, instr)
				}
				flow.scan(instr, declaring)
			}
		}
		for _, entry := range providers {
			if entry.provider {
				for _, name := range entry.names {
					flow.flows[name] = true
				}
			}
		}
	}
	return flow
}

// providedEntry is one declaration literal: the names it holds and whether it
// carries a function that produces their value.
type providedEntry struct {
	names    []string
	provider bool
}

// recordProvider groups the stores into one declaration literal, so a name
// declared together with its provider function counts as produced.
func recordProvider(entries map[ssa.Value]*providedEntry, instr ssa.Instruction) {
	store, ok := instr.(*ssa.Store)
	if !ok {
		return
	}
	fa, ok := store.Addr.(*ssa.FieldAddr)
	if !ok {
		return
	}
	entry := entries[fa.X]
	if entry == nil {
		entry = &providedEntry{}
		entries[fa.X] = entry
	}
	if name, ok := stringConst(store.Val); ok {
		entry.names = append(entry.names, name)
	}
	if _, isFunc := store.Val.Type().Underlying().(*types.Signature); isFunc {
		if c, isConst := store.Val.(*ssa.Const); !isConst || !c.IsNil() {
			entry.provider = true
		}
	}
}

func (f productFlow) scan(instr ssa.Instruction, declaring bool) {
	switch in := instr.(type) {
	case *ssa.MapUpdate:
		if key, ok := stringConst(in.Key); ok {
			if field := sourceField(in.Value); field != "" {
				if f.keyFields[key] == nil {
					f.keyFields[key] = map[string]bool{}
				}
				f.keyFields[key][field] = true
				return
			}
			// A declaration table maps names to constants; any other value is
			// produced under that name.
			if _, isConst := in.Value.(*ssa.Const); !isConst || !declaring {
				f.flows[key] = true
			}
		}
		if !declaring {
			f.constants(in.Value)
		}
		return
	case *ssa.Store:
		f.writes(in.Addr, in.Val.Type())
	case *ssa.Call:
		f.decodes(in.Common())
	case *ssa.BinOp:
		if in.Op == token.ADD && !declaring {
			if prefix, ok := stringConst(in.X); ok {
				f.prefixes[prefix] = true
			}
		}
		// Comparing an identifier consumes it.
		return
	case *ssa.Lookup, *ssa.If:
		return
	}
	if declaring {
		return
	}
	for _, op := range instr.Operands(nil) {
		if op != nil && *op != nil {
			f.constants(*op)
		}
	}
}

func (f productFlow) constants(v ssa.Value) {
	if s, ok := stringConst(v); ok {
		f.flows[s] = true
	}
}

// writes records a store through a field address, or through a pointer to a
// whole struct, as a write of every field it covers.
func (f productFlow) writes(addr ssa.Value, stored types.Type) {
	if fa, ok := addr.(*ssa.FieldAddr); ok {
		field := fieldOf(fa.X.Type(), fa.Field)
		if field != nil {
			f.markField(field)
		}
		return
	}
	f.markStruct(stored, map[types.Type]bool{})
}

// decodes treats a struct handed to a reflective decoder as fully written.
func (f productFlow) decodes(call *ssa.CallCommon) {
	callee := call.StaticCallee()
	if callee == nil || callee.Pkg == nil || !decoderPackages[callee.Pkg.Pkg.Path()] {
		if call.IsInvoke() || callee == nil || callee.Signature.Recv() == nil {
			return
		}
		if named := receiverNamed(callee); named == nil || named.Obj().Pkg() == nil || !decoderPackages[named.Obj().Pkg().Path()] {
			return
		}
	}
	for _, arg := range call.Args {
		if ptr, ok := arg.Type().Underlying().(*types.Pointer); ok {
			f.markStruct(ptr.Elem(), map[types.Type]bool{})
		}
		if mi, ok := arg.(*ssa.MakeInterface); ok {
			if ptr, ok := mi.X.Type().Underlying().(*types.Pointer); ok {
				f.markStruct(ptr.Elem(), map[types.Type]bool{})
			}
		}
	}
}

func (f productFlow) markField(field *types.Var) {
	f.fieldWrites[fieldKey(field)] = true
	f.markStruct(field.Type(), map[types.Type]bool{})
}

func (f productFlow) markStruct(t types.Type, seen map[types.Type]bool) {
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
		f.fieldWrites[fieldKey(field)] = true
		f.markStruct(field.Type(), seen)
	}
}

// sourceField names the field a value was loaded from, looking through
// conversions and single-argument helpers such as a nil-to-empty wrapper.
func sourceField(v ssa.Value) string {
	for range 8 {
		switch x := v.(type) {
		case *ssa.UnOp:
			if fa, ok := x.X.(*ssa.FieldAddr); ok {
				if field := fieldOf(fa.X.Type(), fa.Field); field != nil {
					return fieldKey(field)
				}
			}
			v = x.X
		case *ssa.Field:
			if field := fieldOf(x.X.Type(), x.Field); field != nil {
				return fieldKey(field)
			}
			return ""
		case *ssa.MakeInterface:
			v = x.X
		case *ssa.ChangeType:
			v = x.X
		case *ssa.Convert:
			v = x.X
		case *ssa.Call:
			if len(x.Call.Args) != 1 {
				return ""
			}
			v = x.Call.Args[0]
		default:
			return ""
		}
	}
	return ""
}

func fieldOf(t types.Type, index int) *types.Var {
	if ptr, ok := t.Underlying().(*types.Pointer); ok {
		t = ptr.Elem()
	}
	st, ok := t.Underlying().(*types.Struct)
	if !ok || index >= st.NumFields() {
		return nil
	}
	return st.Field(index)
}

// fieldKey identifies a field by its declaring package, position-independent.
func fieldKey(field *types.Var) string {
	pkg := ""
	if field.Pkg() != nil {
		pkg = field.Pkg().Path()
	}
	return fmt.Sprintf("%s.%s@%d", pkg, field.Name(), field.Origin().Pos())
}

func receiverNamed(fn *ssa.Function) *types.Named {
	t := fn.Signature.Recv().Type()
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, _ := t.(*types.Named)
	return named
}

func stringConst(v ssa.Value) (string, bool) {
	c, ok := v.(*ssa.Const)
	if !ok || c.Value == nil || c.Value.Kind() != constant.String {
		return "", false
	}
	s := constant.StringVal(c.Value)
	return s, strings.TrimSpace(s) != ""
}
