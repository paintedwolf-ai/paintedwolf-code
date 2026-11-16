// Package reach finds production code no shipped program reaches: functions,
// context keys read but never set, and tables nothing reads.
//
// A program is loaded once per target GOOS without tests. A function is dead
// only when every GOOS that compiles it leaves it unreachable from the roots.
package reach

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/archlint"

	"golang.org/x/tools/go/callgraph/rta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// archlint.Finding kinds.
const (
	KindDeadFunc       = "dead-func"
	KindUnsetCtxKey    = "unset-context-key"
	KindWriteOnlyTable = "write-only-table"
)

// Config selects the program and what is reported.
type Config struct {
	Dir      string
	Patterns []string // main packages that ship or run
	// Scope adds packages whose functions are judged even when no root
	// imports them, so a whole unimported package is reported too.
	Scope []string
	GOOS  []string
	Tags  []string
	// Report limits findings to matching package paths; Skip removes
	// test-support packages that only tests import.
	Report *regexp.Regexp
	Skip   []*regexp.Regexp
	// Schema is the complete fresh schema; empty skips table liveness.
	Schema string
}

// pass is one GOOS load: what it compiles and what its roots reach.
type pass struct {
	compiled  map[string]archlint.Finding // dead-func key → finding for every reported source func
	reachable map[string]bool
	reads     map[string]bool // tables a reachable function names in a read
	keysSet   map[string]bool
	keysRead  map[string]archlint.Finding
}

// Run loads each GOOS and combines the passes.
func Run(cfg Config) ([]archlint.Finding, error) {
	if len(cfg.GOOS) == 0 {
		return nil, fmt.Errorf("reach: no GOOS")
	}
	abs, err := filepath.Abs(cfg.Dir)
	if err != nil {
		return nil, err
	}
	cfg.Dir = abs
	var passes []*pass
	for _, goos := range cfg.GOOS {
		p, err := load(cfg, goos)
		if err != nil {
			return nil, fmt.Errorf("reach: GOOS=%s: %w", goos, err)
		}
		passes = append(passes, p)
	}
	out := deadFuncs(passes)
	out = append(out, unsetKeys(passes)...)
	if cfg.Schema != "" {
		tables, err := ParseSchema(cfg.Schema)
		if err != nil {
			return nil, err
		}
		for i := range tables.Tables {
			if rel, err := filepath.Rel(cfg.Dir, tables.Tables[i].Pos); err == nil && !strings.HasPrefix(rel, "..") {
				tables.Tables[i].Pos = rel
			}
		}
		out = append(out, writeOnlyTables(tables, passes)...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

func (cfg Config) reported(pkgPath string) bool {
	if cfg.Report != nil && !cfg.Report.MatchString(pkgPath) {
		return false
	}
	return !slices.ContainsFunc(cfg.Skip, func(re *regexp.Regexp) bool { return re.MatchString(pkgPath) })
}

func load(cfg Config, goos string) (*pass, error) {
	env := append(os.Environ(), "GOOS="+goos)
	lc := &packages.Config{
		Dir:  cfg.Dir,
		Env:  env,
		Mode: packages.LoadAllSyntax | packages.NeedModule,
	}
	if len(cfg.Tags) > 0 {
		lc.BuildFlags = []string{"-tags=" + strings.Join(cfg.Tags, ",")}
	}
	initial, err := packages.Load(lc, append(append([]string(nil), cfg.Patterns...), cfg.Scope...)...)
	if err != nil {
		return nil, err
	}
	if packages.PrintErrors(initial) > 0 {
		return nil, fmt.Errorf("packages contain errors")
	}
	prog, pkgs := ssautil.AllPackages(initial, ssa.InstantiateGenerics)
	rootPaths, err := mainPaths(lc, cfg.Patterns)
	if err != nil {
		return nil, err
	}
	var roots []*ssa.Function
	for _, main := range ssautil.MainPackages(pkgs) {
		if rootPaths[main.Pkg.Path()] {
			roots = append(roots, main.Func("init"), main.Func("main"))
		}
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("no main packages in %v", cfg.Patterns)
	}

	type source struct {
		fn  *ssa.Function
		key string
	}
	var sources []source
	interfaces := map[*types.Package][]*types.Interface{}
	packages.Visit(initial, nil, func(p *packages.Package) {
		if !cfg.reported(p.PkgPath) {
			return
		}
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			if tn, ok := scope.Lookup(name).(*types.TypeName); ok && types.IsInterface(tn.Type()) {
				interfaces[p.Types] = append(interfaces[p.Types], tn.Type().Underlying().(*types.Interface))
			}
		}
		for _, file := range p.Syntax {
			// A generated file is judged through its generator, which is itself
			// a command reach checks.
			if ast.IsGenerated(file) {
				continue
			}
			for _, decl := range file.Decls {
				if fd, ok := decl.(*ast.FuncDecl); ok {
					fn := prog.FuncValue(p.TypesInfo.Defs[fd.Name].(*types.Func))
					sources = append(sources, source{fn, p.PkgPath + "." + prettyName(fn)})
				}
			}
		}
	})
	prog.Build()
	res := rta.Analyze(roots, false)

	p := &pass{
		compiled:  map[string]archlint.Finding{},
		reachable: map[string]bool{},
		reads:     map[string]bool{},
		keysSet:   map[string]bool{},
		keysRead:  map[string]archlint.Finding{},
	}
	reachablePos := map[token.Position]bool{}
	for fn := range res.Reachable {
		if fn.Pos().IsValid() {
			reachablePos[prog.Fset.Position(fn.Pos())] = true
		}
		scanFunction(prog.Fset, fn, cfg, p)
	}
	for _, s := range sources {
		if isMarkerMethod(s.fn, interfaces[s.fn.Pkg.Pkg]) {
			continue
		}
		pos := prog.Fset.Position(s.fn.Pos())
		// cgo writes its generated sources to the build cache; only the
		// module's own files can be edited.
		if !strings.HasPrefix(pos.Filename, cfg.Dir+string(filepath.Separator)) {
			continue
		}
		p.compiled[s.key] = archlint.Finding{
			Kind: KindDeadFunc, Key: s.key, Pos: relPos(cfg.Dir, pos),
			Message: "no shipped program reaches " + s.key,
		}
		if reachablePos[pos] {
			p.reachable[s.key] = true
		}
	}
	return p, nil
}

// mainPaths lists the import paths the root patterns name, without loading
// their syntax a second time.
func mainPaths(lc *packages.Config, patterns []string) (map[string]bool, error) {
	light := *lc
	light.Mode = packages.NeedName
	pkgs, err := packages.Load(&light, patterns...)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, p := range pkgs {
		out[p.PkgPath] = true
	}
	return out, nil
}

// deadFuncs reports a function dead when no GOOS that compiles it reaches it.
// Platform variants share a key, so each of their declarations is reported,
// keyed by its file.
func deadFuncs(passes []*pass) []archlint.Finding {
	dead := map[string]map[string]archlint.Finding{}
	live := map[string]bool{}
	for _, p := range passes {
		for key, f := range p.compiled {
			if p.reachable[key] {
				live[key] = true
				continue
			}
			if dead[key] == nil {
				dead[key] = map[string]archlint.Finding{}
			}
			dead[key][declFile(f.Pos)] = f
		}
	}
	var out []archlint.Finding
	for key, decls := range dead {
		if live[key] {
			continue
		}
		for file, f := range decls {
			if len(decls) > 1 {
				f.Key = key + "@" + path.Base(file)
			}
			out = append(out, f)
		}
	}
	return out
}

// declFile is the file part of a "file:line:column" position.
func declFile(pos string) string {
	file, _, _ := strings.Cut(pos, ":")
	return file
}

func prettyName(fn *ssa.Function) string {
	if recv := fn.Signature.Recv(); recv != nil {
		t := recv.Type()
		if ptr, ok := t.(*types.Pointer); ok {
			t = ptr.Elem()
		}
		if named, ok := t.(*types.Named); ok {
			return named.Obj().Name() + "." + fn.Name()
		}
	}
	return fn.Name()
}

// isMarkerMethod reports an empty unexported method that seals a package
// interface; nothing calls it by design.
func isMarkerMethod(fn *ssa.Function, interfaces []*types.Interface) bool {
	if fn.Signature.Recv() == nil || ast.IsExported(fn.Name()) || fn.Signature.Params() != nil || fn.Signature.Results() != nil {
		return false
	}
	decl, ok := fn.Syntax().(*ast.FuncDecl)
	if !ok || decl.Body == nil || len(decl.Body.List) > 0 {
		return false
	}
	return slices.ContainsFunc(interfaces, func(iface *types.Interface) bool {
		return types.Implements(fn.Signature.Recv().Type(), iface)
	})
}
