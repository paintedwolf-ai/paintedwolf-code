package contract

import (
	"fmt"
	"go/token"
	"go/types"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/toolcontract"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

const lycaonModule = "github.com/lycaon/lycaon"

// writeFloor resolves a model-named path for writing. Every tool write to a
// path its arguments name crosses it.
const writeFloor = lycaonModule + "/internal/tools/projectpaths.ResolveWrite"

// toolHandlerType is the registry's handler type. Calling one dispatches
// another registered tool, which is reviewed and declared on its own.
const toolHandlerType = lycaonModule + "/internal/tools.ToolHandler"

// Invariant: handlers reaching the write floor declare writes in the catalog and are not read-only.
func TestToolWritesAreDeclaredInTheCatalog(t *testing.T) {
	t.Parallel()
	reg := toolfixture.ContractServeBootRegistry(t)
	manifest, err := nativemanifest.Load()
	contractcheck.FailErr(t, "load native tool manifest", err)
	pathMutating := map[string]bool{}
	for _, name := range manifest.PathMutatingTools() {
		pathMutating[name] = true
	}
	// Command tools declare their files through the command plan, which the
	// approval and confinement review as stream bindings.
	commandScoped := map[string]bool{}
	for _, name := range manifest.CommandScopedTools() {
		commandScoped[name] = true
	}

	handlers := map[string]uintptr{}
	for _, meta := range reg.List() {
		if def, ok := reg.Definition(meta.Name); ok && def.Handler != nil {
			handlers[meta.Name] = reflect.ValueOf(def.Handler).Pointer()
		}
	}
	if len(handlers) < 50 {
		t.Fatalf("the boot registry registered %d handlers; the fixture no longer mirrors serve boot", len(handlers))
	}

	program := loadWriteFloorProgram(t)
	names := make([]string, 0, len(handlers))
	for name := range handlers {
		names = append(names, name)
	}
	sort.Strings(names)
	var unresolved, violations, writers []string
	for _, name := range names {
		root, why := program.handlerFunction(handlers[name])
		if root == nil {
			unresolved = append(unresolved, name+": "+why)
			continue
		}
		path := program.pathToFloor(root)
		if path == nil {
			continue
		}
		writers = append(writers, name)
		lifecycle := toolcontract.LifecycleReadOnly
		if contract, ok := toolcontract.Lookup(name); ok {
			lifecycle = contract.Lifecycle
		}
		var broken []string
		if !pathMutating[name] && !commandScoped[name] {
			broken = append(broken, "missing from mutates_path")
		}
		if lifecycle == toolcontract.LifecycleReadOnly {
			broken = append(broken, "lifecycle is read_only")
		}
		if len(broken) == 0 {
			continue
		}
		violations = append(violations, fmt.Sprintf("%s (%s) reaches the write floor:\n      %s",
			name, strings.Join(broken, ", "), strings.Join(path, "\n    → ")))
	}
	t.Logf("tools reaching the write floor: %s", strings.Join(writers, ", "))
	// Content writers provide a positive control for call-graph reachability.
	for _, name := range manifest.ContentMutatingTools() {
		if _, registered := handlers[name]; registered && !slices.Contains(writers, name) {
			t.Errorf("%s authors file content but never reaches projectpaths.ResolveWrite; route its write through "+
				"the write floor, or fix the call-graph walk if it lost the path", name)
		}
	}
	if len(unresolved) > 0 {
		t.Errorf("registered handlers with no function in the analyzed program; the handler mapping needs a case:\n  %s",
			strings.Join(unresolved, "\n  "))
	}
	if len(violations) > 0 {
		t.Errorf("tools write through projectpaths.ResolveWrite without declaring it. Add the tool to "+
			"mutates_path (and mutates_content when it authors bytes) in platform/tools/native-tools.yaml with a "+
			"non-read_only lifecycle, or split the write into its own tool so the reader stays read-only:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// writeFloorProgram is the lycaon program in SSA form with a call graph and
// indexes from runtime function identity to SSA functions.
type writeFloorProgram struct {
	dir    string
	graph  *callgraph.Graph
	floor  *ssa.Function
	byName map[string]*ssa.Function
	byLine map[string][]*ssa.Function
	// valueCache holds each function's functionValues.
	valueCache map[*ssa.Function]map[*ssa.Function]bool
}

// loadWriteFloorProgram builds function bodies for lycaon packages only;
// dependencies contribute types. A function value handed to a body-less
// dependency is taken as called.
func loadWriteFloorProgram(t *testing.T) *writeFloorProgram {
	t.Helper()
	started := time.Now()
	dir := filepath.Join(contractcheck.RepoRoot(t), "lycaon")
	cfg := &packages.Config{Mode: packages.LoadSyntax, Dir: dir}
	pkgs, err := packages.Load(cfg, "./internal/...", "./pkg/...")
	contractcheck.FailErr(t, "load lycaon packages", err)
	var loadErrs []string
	for _, p := range pkgs {
		for _, e := range p.Errors {
			loadErrs = append(loadErrs, e.Error())
		}
	}
	if len(loadErrs) > 0 {
		t.Fatalf("lycaon packages did not type-check:\n  %s", strings.Join(loadErrs[:min(len(loadErrs), 20)], "\n  "))
	}
	prog, _ := ssautil.Packages(pkgs, ssa.InstantiateGenerics)
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
	for _, p := range pkgs {
		createDeps(p.Types)
	}
	prog.Build()
	funcs := ssautil.AllFunctions(prog)
	graph := vta.CallGraph(funcs, cha.CallGraph(prog))

	p := &writeFloorProgram{dir: dir, graph: graph, byName: map[string]*ssa.Function{}, byLine: map[string][]*ssa.Function{}, valueCache: map[*ssa.Function]map[*ssa.Function]bool{}}
	for fn := range funcs {
		name := fn.String()
		p.byName[name] = fn
		if name == writeFloor {
			p.floor = fn
		}
		if key := p.lineKey(prog.Fset.Position(fn.Pos()).Filename, prog.Fset.Position(fn.Pos()).Line); key != "" && fn.Synthetic == "" {
			p.byLine[key] = append(p.byLine[key], fn)
		}
	}
	if p.floor == nil {
		t.Fatalf("%s is not in the analyzed program; update writeFloor to the write-scope resolver", writeFloor)
	}
	t.Logf("write floor call graph over %d functions built in %s", len(funcs), time.Since(started).Round(time.Millisecond))
	return p
}

// lineKey names a source line relative to the lycaon module.
func (p *writeFloorProgram) lineKey(file string, line int) string {
	if file == "" || line == 0 {
		return ""
	}
	file = filepath.ToSlash(file)
	if rel, ok := strings.CutPrefix(file, lycaonModule+"/"); ok {
		return rel + ":" + strconv.Itoa(line)
	}
	rel, err := filepath.Rel(p.dir, filepath.FromSlash(file))
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.ToSlash(rel) + ":" + strconv.Itoa(line)
}

var methodValue = regexp.MustCompile(`^(.+)\.(?:\((\*?)([^)]+)\)|([^.]+))\.([^.]+)-fm$`)

// handlerFunction finds the SSA function a registered handler runs: a method
// value by receiver and name, anything else by its source line.
func (p *writeFloorProgram) handlerFunction(pc uintptr) (*ssa.Function, string) {
	rf := runtime.FuncForPC(pc)
	if rf == nil {
		return nil, "no runtime function"
	}
	name := rf.Name()
	if m := methodValue.FindStringSubmatch(name); m != nil {
		receiver := m[3]
		if receiver == "" {
			receiver = m[4]
		}
		key := "(" + m[2] + m[1] + "." + receiver + ")." + m[5]
		if fn := p.byName[key]; fn != nil {
			return fn, ""
		}
		return nil, name + " (no method " + key + ")"
	}
	if fn := p.byName[name]; fn != nil {
		return fn, ""
	}
	file, line := rf.FileLine(pc)
	candidates := p.byLine[p.lineKey(file, line)]
	if len(candidates) == 1 {
		return candidates[0], ""
	}
	return nil, fmt.Sprintf("%s at %s:%d (%d candidates)", name, file, line, len(candidates))
}

// pathToFloor returns the shortest call chain from root to the write floor.
// Dispatches to another registered tool's handler are not followed, and a
// callback is followed only into a function a caller on the chain created.
func (p *writeFloorProgram) pathToFloor(root *ssa.Function) []string {
	prev := map[*ssa.Function]*ssa.Function{root: nil}
	queue := []*ssa.Function{root}
	reach := func(from, to *ssa.Function) {
		if _, seen := prev[to]; seen {
			return
		}
		prev[to] = from
		queue = append(queue, to)
	}
	createdOnChain := func(fn, callee *ssa.Function) bool {
		for a := fn; a != nil; a = prev[a] {
			if p.values(a)[callee] {
				return true
			}
		}
		return false
	}
	for len(queue) > 0 {
		fn := queue[0]
		queue = queue[1:]
		if fn == p.floor {
			var chain []string
			for f := fn; f != nil; f = prev[f] {
				chain = append(chain, f.String())
			}
			slices.Reverse(chain)
			return chain
		}
		for _, value := range handedToBodyless(fn) {
			reach(fn, value)
		}
		node := p.graph.Nodes[fn]
		if node == nil {
			continue
		}
		for _, edge := range node.Out {
			callee := edge.Callee.Func
			if dispatchesTool(edge) || isCallback(edge, root) && !createdOnChain(fn, callee) {
				continue
			}
			reach(fn, callee)
		}
	}
	return nil
}

func (p *writeFloorProgram) values(fn *ssa.Function) map[*ssa.Function]bool {
	if cached, ok := p.valueCache[fn]; ok {
		return cached
	}
	set := map[*ssa.Function]bool{}
	for _, value := range functionValues(fn) {
		set[value] = true
	}
	p.valueCache[fn] = set
	return set
}

// functionValues are the functions fn takes as values: closures it makes and
// functions it references other than as a direct call's callee.
func functionValues(fn *ssa.Function) []*ssa.Function {
	var out []*ssa.Function
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			if mc, ok := instr.(*ssa.MakeClosure); ok {
				out = append(out, mc.Fn.(*ssa.Function))
			}
			var direct ssa.Value
			if call, ok := instr.(ssa.CallInstruction); ok && !call.Common().IsInvoke() {
				direct = call.Common().Value
			}
			for _, op := range instr.Operands(nil) {
				if op == nil || *op == direct {
					continue
				}
				if f, ok := (*op).(*ssa.Function); ok {
					out = append(out, f)
				}
			}
		}
	}
	return out
}

// handedToBodyless are the functions fn passes to a callee with no body in
// the program, which is assumed to call them.
func handedToBodyless(fn *ssa.Function) []*ssa.Function {
	var out []*ssa.Function
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			call, ok := instr.(ssa.CallInstruction)
			if !ok {
				continue
			}
			callee := call.Common().StaticCallee()
			if callee == nil || len(callee.Blocks) > 0 {
				continue
			}
			for _, arg := range call.Common().Args {
				switch v := arg.(type) {
				case *ssa.Function:
					out = append(out, v)
				case *ssa.MakeClosure:
					out = append(out, v.Fn.(*ssa.Function))
				}
			}
		}
	}
	return out
}

// isCallback reports a call through a function value the caller passed in.
// What the root handler captured was bound when it was registered, so those
// values are dependencies, not callbacks.
func isCallback(edge *callgraph.Edge, root *ssa.Function) bool {
	if edge.Site == nil {
		return false
	}
	common := edge.Site.Common()
	if common.IsInvoke() || common.StaticCallee() != nil {
		return false
	}
	return (&argumentTrace{root: root, seen: map[ssa.Value]bool{}}).fromParameter(common.Value)
}

// argumentTrace follows a called value back to where it was bound.
type argumentTrace struct {
	root *ssa.Function
	seen map[ssa.Value]bool
}

// fromParameter reports a value that is always one of the enclosing call's
// arguments: a parameter, a captured variable bound to one, or a cell that
// only ever holds one. A dependency captured at registration is not.
func (a *argumentTrace) fromParameter(v ssa.Value) bool {
	if a.seen[v] {
		return true
	}
	a.seen[v] = true
	switch value := v.(type) {
	case *ssa.Parameter:
		return true
	case *ssa.FreeVar:
		return a.allBindings(value, a.fromParameter)
	case *ssa.ChangeType:
		return a.fromParameter(value.X)
	case *ssa.UnOp:
		return value.Op == token.MUL && a.cellHoldsParameters(value.X)
	case *ssa.Phi:
		for _, edge := range value.Edges {
			if !a.fromParameter(edge) {
				return false
			}
		}
		return true
	}
	return false
}

// cellHoldsParameters reports a variable cell whose every store is a parameter.
func (a *argumentTrace) cellHoldsParameters(addr ssa.Value) bool {
	switch cell := addr.(type) {
	case *ssa.Alloc:
		stored := false
		for _, ref := range *cell.Referrers() {
			store, ok := ref.(*ssa.Store)
			if !ok || store.Addr != cell {
				continue
			}
			stored = true
			if !a.fromParameter(store.Val) {
				return false
			}
		}
		return stored
	case *ssa.FreeVar:
		return a.allBindings(cell, a.cellHoldsParameters)
	}
	return false
}

// allBindings applies check to every value a closure's creators bind to fv.
// The root's own captures end the trace.
func (a *argumentTrace) allBindings(fv *ssa.FreeVar, check func(ssa.Value) bool) bool {
	closure := fv.Parent()
	index := slices.Index(closure.FreeVars, fv)
	if closure == a.root || index < 0 || closure.Parent() == nil {
		return false
	}
	bound := false
	for _, block := range closure.Parent().Blocks {
		for _, instr := range block.Instrs {
			mc, ok := instr.(*ssa.MakeClosure)
			if !ok || mc.Fn != closure {
				continue
			}
			bound = true
			if !check(mc.Bindings[index]) {
				return false
			}
		}
	}
	return bound
}

func dispatchesTool(edge *callgraph.Edge) bool {
	if edge.Site == nil {
		return false
	}
	common := edge.Site.Common()
	if common.IsInvoke() || common.StaticCallee() != nil {
		return false
	}
	named, ok := common.Value.Type().(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path()+"."+obj.Name() == toolHandlerType
}
