package contract

import (
	"fmt"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// handlerErrorTree names a Go module whose HTTP handlers the declared-errors
// contract scans: the repository's lycaon module, or a fixture module that
// proves the scan's sensitivity.
type handlerErrorTree struct {
	dir      string
	patterns []string
	// httpLayer is the import-path prefix the handler call graph stays within.
	httpLayer string
	// register lists the registration functions (types.Func.FullName) whose
	// arguments pair an operation variable with its handler.
	register map[string]bool
	// codeType and errorBody are the error code type and the error body
	// struct whose Code field every error response is built through.
	codeType  string
	errorBody string
	env       []string
}

// handlerErrors is what one registered handler can answer.
type handlerErrors struct {
	operation string // the operation variable's name
	handler   string
	roots     []*ssa.Function
	codes     map[string]bool
	// unresolved are the code values the scan could not trace to constants.
	unresolved []string
}

// handlerErrorProgram is the SSA program of one module with the indexes the
// code resolution consults.
type handlerErrorProgram struct {
	tree  handlerErrorTree
	prog  *ssa.Program
	funcs map[*ssa.Function]bool
	// layerTypes are the named types declared in the HTTP layer, the
	// candidates for an interface call made inside it; programTypes are every
	// named type with bodies, the candidates when a code is returned.
	layerTypes   []*types.Named
	programTypes []*types.Named
	// fieldCodes, mapCodes, and globalCodes are the values stored anywhere
	// in the program into a code-typed struct field, map, or variable.
	fieldStores  map[string][]storedValue
	mapStores    map[string][]storedValue
	globalStores map[*ssa.Global][]storedValue
	flows        map[*ssa.Function]map[int]bool
	flowing      map[*ssa.Function]bool
	emitted      map[*ssa.Function]*codeSet
	// callSites are the static calls of each function, for values a function
	// stores or returns from its parameters.
	callSites map[*ssa.Function][]callSite
	// pairs memoizes each function's error bodies with details.
	pairs   map[*ssa.Function][]detailPair
	pairing map[*ssa.Function]bool
}

// callSite is one static call and the function making it.
type callSite struct {
	common *ssa.CallCommon
	fn     *ssa.Function
}

type storedValue struct {
	value ssa.Value
	fn    *ssa.Function
}

// codeSet is a resolution result: constant codes plus the sites whose code
// the scan could not trace.
type codeSet struct {
	codes      map[string]bool
	unresolved map[string]bool
}

func newCodeSet() *codeSet {
	return &codeSet{codes: map[string]bool{}, unresolved: map[string]bool{}}
}

func (c *codeSet) merge(other *codeSet) {
	for code := range other.codes {
		c.codes[code] = true
	}
	for site := range other.unresolved {
		c.unresolved[site] = true
	}
}

func (tree handlerErrorTree) load() (*handlerErrorProgram, error) {
	cfg := &packages.Config{Mode: packages.LoadSyntax, Dir: tree.dir, Env: tree.env}
	pkgs, err := packages.Load(cfg, tree.patterns...)
	if err != nil {
		return nil, err
	}
	var loadErrs []string
	for _, pkg := range pkgs {
		for _, e := range pkg.Errors {
			loadErrs = append(loadErrs, e.Error())
		}
	}
	if len(loadErrs) > 0 {
		return nil, fmt.Errorf("packages did not type-check:\n  %s", strings.Join(loadErrs[:min(len(loadErrs), 20)], "\n  "))
	}
	prog, ssaPkgs := ssautil.Packages(pkgs, ssa.InstantiateGenerics)
	created := map[*types.Package]bool{}
	var createDeps func(*types.Package)
	createDeps = func(tp *types.Package) {
		if tp == nil || created[tp] {
			return
		}
		created[tp] = true
		for _, imp := range tp.Imports() {
			createDeps(imp)
		}
		if prog.Package(tp) == nil {
			prog.CreatePackage(tp, nil, nil, true)
		}
	}
	for _, pkg := range pkgs {
		createDeps(pkg.Types)
	}
	prog.Build()
	p := &handlerErrorProgram{
		tree: tree, prog: prog, funcs: ssautil.AllFunctions(prog),
		fieldStores: map[string][]storedValue{}, mapStores: map[string][]storedValue{},
		globalStores: map[*ssa.Global][]storedValue{},
		flows:        map[*ssa.Function]map[int]bool{}, flowing: map[*ssa.Function]bool{},
		emitted: map[*ssa.Function]*codeSet{}, callSites: map[*ssa.Function][]callSite{},
		pairs: map[*ssa.Function][]detailPair{}, pairing: map[*ssa.Function]bool{},
	}
	for _, pkg := range ssaPkgs {
		if pkg == nil {
			continue
		}
		for _, member := range pkg.Members {
			typ, ok := member.(*ssa.Type)
			if !ok {
				continue
			}
			if named, ok := typ.Type().(*types.Named); ok && named.TypeParams().Len() == 0 {
				p.programTypes = append(p.programTypes, named)
				if p.inLayer(pkg.Pkg) {
					p.layerTypes = append(p.layerTypes, named)
				}
			}
		}
	}
	p.indexStores()
	return p, nil
}

func (p *handlerErrorProgram) inLayer(pkg *types.Package) bool {
	return pkg != nil && strings.HasPrefix(pkg.Path(), p.tree.httpLayer)
}

func (p *handlerErrorProgram) isCodeType(t types.Type) bool {
	return types.TypeString(t, nil) == p.tree.codeType
}

// indexStores records every value the program stores into a code-typed field,
// map, or package variable, so a code read back from one resolves to them.
func (p *handlerErrorProgram) indexStores() {
	for fn := range p.funcs {
		for _, block := range fn.Blocks {
			for _, instr := range block.Instrs {
				switch in := instr.(type) {
				case ssa.CallInstruction:
					if callee := in.Common().StaticCallee(); callee != nil {
						p.callSites[callee] = append(p.callSites[callee], callSite{in.Common(), fn})
					}
				case *ssa.Store:
					if !p.isCodeType(in.Val.Type()) {
						continue
					}
					switch addr := in.Addr.(type) {
					case *ssa.FieldAddr:
						key := fieldKey(addr.X.Type().Underlying().(*types.Pointer).Elem(), addr.Field)
						p.fieldStores[key] = append(p.fieldStores[key], storedValue{in.Val, fn})
					case *ssa.Global:
						p.globalStores[addr] = append(p.globalStores[addr], storedValue{in.Val, fn})
					}
				case *ssa.MapUpdate:
					if p.isCodeType(in.Value.Type()) {
						key := types.TypeString(in.Map.Type(), nil)
						p.mapStores[key] = append(p.mapStores[key], storedValue{in.Value, fn})
					}
				}
			}
		}
	}
}

func fieldKey(structType types.Type, field int) string {
	return fmt.Sprintf("%s#%d", types.TypeString(structType, nil), field)
}

// scan maps every registered handler to the codes it can answer.
func (p *handlerErrorProgram) scan() ([]*handlerErrors, []string) {
	var out []*handlerErrors
	var problems []string
	for fn := range p.funcs {
		if !p.inLayer(fnPackage(fn)) {
			continue
		}
		for _, block := range fn.Blocks {
			for _, instr := range block.Instrs {
				call, ok := instr.(*ssa.Call)
				if !ok {
					continue
				}
				callee := call.Call.StaticCallee()
				if callee == nil || callee.Object() == nil || !p.tree.register[callee.Object().(*types.Func).FullName()] {
					continue
				}
				entry, err := p.registration(call)
				if err != nil {
					problems = append(problems, fmt.Sprintf("%s: %v", p.position(call.Pos()), err))
					continue
				}
				out = append(out, entry)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].operation < out[j].operation })
	sort.Strings(problems)
	return out, problems
}

// registration reads one registration call: the operation is the argument
// loaded from a package variable, the handler the last argument.
func (p *handlerErrorProgram) registration(call *ssa.Call) (*handlerErrors, error) {
	args := call.Call.Args
	var operation string
	for _, arg := range args {
		if load, ok := arg.(*ssa.UnOp); ok && load.Op == token.MUL {
			if global, ok := load.X.(*ssa.Global); ok {
				operation = global.Name()
			}
		}
	}
	if operation == "" {
		return nil, fmt.Errorf("no operation variable among the arguments")
	}
	roots := handlerRoots(args[len(args)-1])
	if len(roots) == 0 {
		return nil, fmt.Errorf("%s: the handler argument is not a function value", operation)
	}
	root := roots[0]
	codes := newCodeSet()
	for fn := range p.reach(roots...) {
		if _, ok := p.emitted[fn]; !ok {
			p.emitted[fn] = p.emittedIn(fn)
		}
		codes.merge(p.emitted[fn])
	}
	unresolved := make([]string, 0, len(codes.unresolved))
	for site := range codes.unresolved {
		unresolved = append(unresolved, site)
	}
	sort.Strings(unresolved)
	module := strings.TrimSuffix(p.tree.httpLayer, "/internal/api") + "/"
	handler := strings.ReplaceAll(strings.TrimSuffix(root.String(), "$bound"), module, "")
	return &handlerErrors{operation: operation, handler: handler, roots: roots, codes: codes.codes, unresolved: unresolved}, nil
}

// handlerRoots are the functions a handler argument runs: the function value
// itself, or for a wrapper call (panic recovery, request fencing) the handler
// it wraps and the closure it returns.
func handlerRoots(v ssa.Value) []*ssa.Function {
	if fn := functionValue(v); fn != nil {
		return []*ssa.Function{fn}
	}
	call, ok := v.(*ssa.Call)
	if !ok {
		return nil
	}
	var roots []*ssa.Function
	for _, arg := range call.Call.Args {
		if fn := functionValue(arg); fn != nil {
			roots = append(roots, fn)
		}
	}
	if callee := call.Call.StaticCallee(); callee != nil {
		for _, block := range callee.Blocks {
			if ret, ok := block.Instrs[len(block.Instrs)-1].(*ssa.Return); ok && len(ret.Results) == 1 {
				if fn := functionValue(ret.Results[0]); fn != nil {
					roots = append(roots, fn)
				}
			}
		}
	}
	return roots
}

// functionValue unwraps a function value: a function, a closure, or a bound
// method, through conversions to a named function type.
func functionValue(v ssa.Value) *ssa.Function {
	for {
		switch value := v.(type) {
		case *ssa.Function:
			return value
		case *ssa.MakeClosure:
			return value.Fn.(*ssa.Function)
		case *ssa.ChangeType:
			v = value.X
		case *ssa.MakeInterface:
			v = value.X
		default:
			return nil
		}
	}
}

func fnPackage(fn *ssa.Function) *types.Package {
	if fn.Pkg != nil {
		return fn.Pkg.Pkg
	}
	if obj := fn.Object(); obj != nil {
		return obj.Pkg()
	}
	return nil
}

// reach is the handler's static call graph within the HTTP layer: static
// callees, function values it creates or passes, and every HTTP-layer method
// an interface call there may dispatch to. Synthetic wrappers are followed.
func (p *handlerErrorProgram) reach(roots ...*ssa.Function) map[*ssa.Function]bool {
	seen := map[*ssa.Function]bool{}
	queue := append([]*ssa.Function(nil), roots...)
	for len(queue) > 0 {
		fn := queue[0]
		queue = queue[1:]
		if seen[fn] {
			continue
		}
		seen[fn] = true
		for _, callee := range p.callees(fn) {
			if !seen[callee] && (callee.Synthetic != "" || p.inLayer(fnPackage(callee))) {
				queue = append(queue, callee)
			}
		}
	}
	return seen
}

func (p *handlerErrorProgram) callees(fn *ssa.Function) []*ssa.Function {
	var out []*ssa.Function
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			if call, ok := instr.(ssa.CallInstruction); ok {
				out = append(out, p.targets(call.Common())...)
			}
			for _, operand := range instr.Operands(nil) {
				if operand == nil {
					continue
				}
				if value := functionValue(*operand); value != nil {
					out = append(out, value)
				}
			}
		}
	}
	return out
}

// targets resolves a call: its static callee, or for an interface call each
// HTTP-layer type's method of that name that implements the interface.
func (p *handlerErrorProgram) targets(common *ssa.CallCommon) []*ssa.Function {
	if callee := common.StaticCallee(); callee != nil {
		return []*ssa.Function{callee}
	}
	if !common.IsInvoke() {
		if fn := functionValue(common.Value); fn != nil {
			return []*ssa.Function{fn}
		}
		return nil
	}
	iface, ok := common.Value.Type().Underlying().(*types.Interface)
	if !ok {
		return nil
	}
	var out []*ssa.Function
	for _, named := range p.layerTypes {
		for _, recv := range []types.Type{named, types.NewPointer(named)} {
			if !types.Implements(recv, iface) {
				continue
			}
			if sel := p.prog.MethodSets.MethodSet(recv).Lookup(common.Method.Pkg(), common.Method.Name()); sel != nil {
				if fn := p.prog.MethodValue(sel); fn != nil {
					out = append(out, fn)
				}
			}
			break
		}
	}
	return out
}

func (p *handlerErrorProgram) position(pos token.Pos) string {
	position := p.prog.Fset.Position(pos)
	file := position.Filename
	if rel, err := filepath.Rel(p.tree.dir, file); err == nil {
		file = filepath.ToSlash(rel)
	}
	return fmt.Sprintf("%s:%d", file, position.Line)
}
