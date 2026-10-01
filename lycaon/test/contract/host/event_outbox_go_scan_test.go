package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// outboxTree names one Go module whose production functions the outbox
// contract scans: the repository's lycaon module, or a fixture module that
// proves the scan's sensitivity.
type outboxTree struct {
	// moduleDir holds go.mod; the scan covers moduleDir/internal/...
	moduleDir string
	// keyPrefix turns a module-relative path into the repo-relative key prefix.
	keyPrefix string
	// dbPackage is the import path of the sqlc Queries package.
	dbPackage string
	// queriesDir holds the sqlc query files that name each Queries method.
	queriesDir string
	// enqueue names the outbox methods, as types.Func.FullName reports them.
	enqueue map[string]bool
	// env overrides the go command environment (fixtures clear GOFLAGS).
	env []string
}

func repoOutboxTree(root string) outboxTree {
	const module = "github.com/lycaon/lycaon"
	return outboxTree{
		moduleDir:  filepath.Join(root, "lycaon"),
		keyPrefix:  "lycaon/",
		dbPackage:  module + "/internal/db",
		queriesDir: filepath.Join(root, "lycaon", "internal", "db", "queries"),
		enqueue: map[string]bool{
			"(*" + module + "/internal/eventoutbox.Outbox).EnqueueTx":        true,
			"(*" + module + "/internal/eventoutbox.Outbox).EnqueueProjectTx": true,
		},
	}
}

type outboxFunc struct {
	key    string
	pkg    string
	tables map[string]bool
	// queryLayer marks functions of the sqlc package: they are queries, and
	// their callers answer for announcing what they write.
	queryLayer bool
	// txScoped marks functions handed their caller's transaction (a *sql.Tx
	// or tx-bound Queries): their writes commit with the caller's.
	txScoped bool
	// targets are the functions each call can reach: the static callee, or
	// every implementation of an interface method in the scanned module.
	targets []*types.Func
}

func (f *outboxFunc) mutatesEventTable(event map[string]bool) bool {
	for table := range f.tables {
		if event[table] {
			return true
		}
	}
	return false
}

func (f *outboxFunc) tableNames(event map[string]bool) []string {
	out := make([]string, 0, len(f.tables))
	for table := range f.tables {
		if event[table] {
			out = append(out, table)
		}
	}
	sort.Strings(out)
	return out
}

type enqueueSite struct {
	where  string
	arg    string
	heldTx bool
}

type outboxScan struct {
	funcs           []*outboxFunc
	announces       map[*outboxFunc]bool
	callerAnnounces map[*outboxFunc]bool
	eventTables     map[string]bool
	// writers names the announcing functions that make each table
	// event-carrying.
	writers      map[string][]string
	enqueueSites []enqueueSite
}

// silentMutators lists functions that write an event-carrying table without
// enqueueing the transition themselves, through a callee, or under a caller
// that does.
func (scan *outboxScan) silentMutators() map[string][]string {
	out := map[string][]string{}
	for _, fn := range scan.funcs {
		if fn.queryLayer || !fn.mutatesEventTable(scan.eventTables) || scan.announces[fn] || scan.callerAnnounces[fn] {
			continue
		}
		out[fn.key] = fn.tableNames(scan.eventTables)
	}
	return out
}

// describeTables names each table with up to two announcing writers, the
// functions that make it event-carrying.
func (scan *outboxScan) describeTables(tables []string) string {
	parts := make([]string, 0, len(tables))
	for _, table := range tables {
		writers := append([]string(nil), scan.writers[table]...)
		sort.Strings(writers)
		if len(writers) > 2 {
			writers = append(writers[:2], "…")
		}
		if len(writers) == 0 {
			writers = []string{"outboxTableFloor"}
		}
		parts = append(parts, table+" announced by "+strings.Join(writers, ", "))
	}
	return strings.Join(parts, "; ")
}

var repoOutboxScan struct {
	once sync.Once
	scan *outboxScan
	err  error
}

// scanOutboxStores type-checks the repository once for every outbox test.
func scanOutboxStores(t *testing.T, root string) *outboxScan {
	t.Helper()
	repoOutboxScan.once.Do(func() {
		repoOutboxScan.scan, repoOutboxScan.err = repoOutboxTree(root).scan(outboxTableFloor)
	})
	contractcheck.FailErr(t, "scan the outbox call graph", repoOutboxScan.err)
	return repoOutboxScan.scan
}

// scan resolves every call in the module's production functions through
// go/types: package functions and concrete methods by their object, and
// interface methods to each implementation declared in the module. Global
// name matching would let unrelated functions that share a name stand in
// for each other.
func (tree outboxTree) scan(floor []string) (*outboxScan, error) {
	queries, err := mutatingQueryTables(tree.queriesDir)
	if err != nil {
		return nil, err
	}
	prog, err := loadOutboxProgram(tree)
	if err != nil {
		return nil, err
	}
	scan := &outboxScan{
		announces:       map[*outboxFunc]bool{},
		callerAnnounces: map[*outboxFunc]bool{},
		eventTables:     map[string]bool{},
		writers:         map[string][]string{},
	}
	for _, table := range floor {
		scan.eventTables[table] = true
	}
	byObj := map[*types.Func]*outboxFunc{}
	direct := map[*outboxFunc]bool{}
	for _, pkg := range prog.scanned {
		for i, file := range pkg.files {
			rel := pkg.rels[i]
			if isOutboxTestSupport(rel) {
				continue
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				obj, _ := pkg.info.Defs[fn.Name].(*types.Func)
				if obj == nil {
					return nil, fmt.Errorf("%s: %s has no function object", rel, fn.Name.Name)
				}
				info := &outboxFunc{
					key: rel + ":" + fn.Name.Name, pkg: obj.Pkg().Path(), tables: map[string]bool{},
					queryLayer: obj.Pkg().Path() == tree.dbPackage,
					txScoped:   tree.takesTransaction(obj),
				}
				byObj[obj] = info
				held := heldTransactions(fn)
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					for _, target := range prog.callTargets(pkg.info, call) {
						info.targets = append(info.targets, target)
						if tree.enqueue[target.FullName()] {
							direct[info] = true
							scan.enqueueSites = append(scan.enqueueSites, enqueueTxSite(prog.fset, rel, call, held))
						}
						if tree.isQuery(target) {
							for _, table := range queries[target.Name()] {
								info.tables[table] = true
							}
						}
					}
					return true
				})
				scan.funcs = append(scan.funcs, info)
			}
		}
	}
	// Hand-written Queries methods (paged inserts and the like) are part of the
	// query layer: a call to one writes what its sqlc queries write.
	for _, fn := range scan.funcs {
		for _, target := range fn.targets {
			if callee := byObj[target]; callee != nil && callee.queryLayer && tree.isQuery(target) {
				for table := range callee.tables {
					fn.tables[table] = true
				}
			}
		}
	}
	// A function announces when it enqueues, or when it calls an announcing
	// function of its own package or hands its transaction to one. Reaching an
	// enqueue anywhere else does not put that event in this function's
	// transaction.
	for fn := range direct {
		scan.announces[fn] = true
	}
	for changed := true; changed; {
		changed = false
		for _, fn := range scan.funcs {
			if scan.announces[fn] {
				continue
			}
			for _, target := range fn.targets {
				if callee := byObj[target]; callee != nil && scan.announces[callee] && fn.shares(callee) {
					scan.announces[fn] = true
					changed = true
					break
				}
			}
		}
	}
	for _, fn := range scan.funcs {
		if !scan.announces[fn] {
			continue
		}
		for table := range fn.tables {
			scan.eventTables[table] = true
			scan.writers[table] = append(scan.writers[table], fn.key)
		}
	}
	// An announcing function covers its own package's helpers, and a covered
	// function covers the helpers it hands its transaction to.
	for changed := true; changed; {
		changed = false
		for _, fn := range scan.funcs {
			if !scan.announces[fn] && !scan.callerAnnounces[fn] {
				continue
			}
			for _, target := range fn.targets {
				callee := byObj[target]
				if callee == nil || scan.callerAnnounces[callee] {
					continue
				}
				if callee.txScoped || scan.announces[fn] && callee.pkg == fn.pkg {
					scan.callerAnnounces[callee] = true
					changed = true
				}
			}
		}
	}
	return scan, nil
}

// shares reports whether a call to callee runs in fn's transaction scope: the
// callee is fn's own package's helper or is handed fn's transaction.
func (fn *outboxFunc) shares(callee *outboxFunc) bool {
	return callee.pkg == fn.pkg || callee.txScoped
}

// takesTransaction reports a *sql.Tx or Queries parameter.
func (tree outboxTree) takesTransaction(fn *types.Func) bool {
	params := fn.Signature().Params()
	for i := range params.Len() {
		typ := params.At(i).Type()
		if ptr, ok := typ.(*types.Pointer); ok {
			typ = ptr.Elem()
		}
		named, ok := typ.(*types.Named)
		if !ok || named.Obj().Pkg() == nil {
			continue
		}
		pkg, name := named.Obj().Pkg().Path(), named.Obj().Name()
		if pkg == "database/sql" && name == "Tx" || pkg == tree.dbPackage && name == "Queries" {
			return true
		}
	}
	return false
}

// isQuery reports a method of the sqlc Queries type, generated or hand-written.
func (tree outboxTree) isQuery(fn *types.Func) bool {
	return fn.Pkg() != nil && fn.Pkg().Path() == tree.dbPackage && isQueriesMethod(fn)
}

func isQueriesMethod(fn *types.Func) bool {
	recv := fn.Signature().Recv()
	if recv == nil {
		return false
	}
	typ := recv.Type()
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}
	named, ok := typ.(*types.Named)
	return ok && named.Obj().Name() == "Queries"
}

// outboxProgram is the module's production packages type-checked from
// source, with the standard library and other modules read from export data.
type outboxProgram struct {
	fset    *token.FileSet
	scanned []*outboxPackage
	// byMethod indexes the module's concrete named types by method name.
	byMethod map[string][]*types.Named
	impls    map[implKey][]*types.Func
}

type outboxPackage struct {
	files []*ast.File
	rels  []string
	info  *types.Info
}

type implKey struct {
	iface  *types.Interface
	method string
}

type listedPackage struct {
	ImportPath string
	Dir        string
	GoFiles    []string
	CgoFiles   []string
	Imports    []string
	Export     string
	Module     *struct{ Main bool }
	Error      *struct{ Err string }
}

func loadOutboxProgram(tree outboxTree) (*outboxProgram, error) {
	// The go command reports canonical directories; compare canonical paths.
	moduleDir, err := filepath.EvalSymlinks(tree.moduleDir)
	if err != nil {
		return nil, err
	}
	tree.moduleDir = moduleDir
	listed, err := tree.goList("-deps", "./internal/...")
	if err != nil {
		return nil, err
	}
	var local []listedPackage
	isLocal := map[string]bool{}
	for _, pkg := range listed {
		if pkg.Error != nil {
			return nil, fmt.Errorf("go list %s: %s", pkg.ImportPath, pkg.Error.Err)
		}
		if pkg.Module != nil && pkg.Module.Main {
			local = append(local, pkg)
			isLocal[pkg.ImportPath] = true
		}
	}
	external := map[string]bool{}
	for _, pkg := range local {
		for _, path := range pkg.Imports {
			if !isLocal[path] && path != "C" && path != "unsafe" {
				external[path] = true
			}
		}
	}
	exports := map[string]string{}
	if len(external) > 0 {
		args := []string{"-deps", "-export"}
		for path := range external {
			args = append(args, path)
		}
		sort.Strings(args[2:])
		deps, err := tree.goList(args...)
		if err != nil {
			return nil, err
		}
		for _, pkg := range deps {
			exports[pkg.ImportPath] = pkg.Export
		}
	}

	prog := &outboxProgram{fset: token.NewFileSet(), byMethod: map[string][]*types.Named{}, impls: map[implKey][]*types.Func{}}
	imp := &outboxImporter{
		local: map[string]*types.Package{},
		export: importer.ForCompiler(prog.fset, "gc", func(path string) (io.ReadCloser, error) {
			file := exports[path]
			if file == "" {
				return nil, fmt.Errorf("no export data for %s", path)
			}
			return os.Open(file)
		}),
	}
	internalDir := filepath.Join(tree.moduleDir, "internal") + string(filepath.Separator)
	for _, pkg := range local {
		if pkg.Dir, err = filepath.EvalSymlinks(pkg.Dir); err != nil {
			return nil, err
		}
		files := make([]*ast.File, 0, len(pkg.GoFiles)+len(pkg.CgoFiles))
		rels := make([]string, 0, cap(files))
		for _, name := range append(append([]string(nil), pkg.GoFiles...), pkg.CgoFiles...) {
			path := filepath.Join(pkg.Dir, name)
			file, err := parser.ParseFile(prog.fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return nil, err
			}
			rel, err := filepath.Rel(tree.moduleDir, path)
			if err != nil {
				return nil, err
			}
			files = append(files, file)
			rels = append(rels, tree.keyPrefix+filepath.ToSlash(rel))
		}
		info := &types.Info{
			Defs:       map[*ast.Ident]types.Object{},
			Uses:       map[*ast.Ident]types.Object{},
			Selections: map[*ast.SelectorExpr]*types.Selection{},
		}
		var typeErrs []string
		conf := types.Config{
			Importer:    imp,
			FakeImportC: len(pkg.CgoFiles) > 0,
			Error:       func(err error) { typeErrs = append(typeErrs, err.Error()) },
		}
		checked, _ := conf.Check(pkg.ImportPath, prog.fset, files, info)
		// FakeImportC leaves C values untyped, so cgo files report follow-on
		// errors; the package's Go API still type-checks for its importers.
		if len(typeErrs) > 0 && len(pkg.CgoFiles) == 0 {
			if len(typeErrs) > 5 {
				typeErrs = typeErrs[:5]
			}
			return nil, fmt.Errorf("type-check %s:\n  %s", pkg.ImportPath, strings.Join(typeErrs, "\n  "))
		}
		imp.local[pkg.ImportPath] = checked
		prog.indexMethods(checked)
		if strings.HasPrefix(pkg.Dir+string(filepath.Separator), internalDir) {
			prog.scanned = append(prog.scanned, &outboxPackage{files: files, rels: rels, info: info})
		}
	}
	return prog, nil
}

func (tree outboxTree) goList(args ...string) ([]listedPackage, error) {
	cmd := exec.Command("go", append([]string{"list", "-e", "-json=ImportPath,Dir,GoFiles,CgoFiles,Imports,Export,Module,Error"}, args...)...)
	cmd.Dir = tree.moduleDir
	cmd.Env = append(append(os.Environ(), "PWD="+tree.moduleDir), tree.env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list %s: %w\n%s", strings.Join(args, " "), err, stderr.String())
	}
	var pkgs []listedPackage
	for dec := json.NewDecoder(bytes.NewReader(out)); dec.More(); {
		var pkg listedPackage
		if err := dec.Decode(&pkg); err != nil {
			return nil, err
		}
		pkgs = append(pkgs, pkg)
	}
	return pkgs, nil
}

type outboxImporter struct {
	local  map[string]*types.Package
	export types.Importer
}

func (i *outboxImporter) Import(path string) (*types.Package, error) {
	if pkg, ok := i.local[path]; ok {
		return pkg, nil
	}
	return i.export.Import(path)
}

func (prog *outboxProgram) indexMethods(pkg *types.Package) {
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		tn, ok := scope.Lookup(name).(*types.TypeName)
		if !ok || tn.IsAlias() {
			continue
		}
		named, ok := tn.Type().(*types.Named)
		if !ok || types.IsInterface(named) || named.TypeParams().Len() > 0 {
			continue
		}
		mset := types.NewMethodSet(types.NewPointer(named))
		for i := range mset.Len() {
			method := mset.At(i).Obj().Name()
			prog.byMethod[method] = append(prog.byMethod[method], named)
		}
	}
}

// callTargets resolves one call to the functions it can run.
func (prog *outboxProgram) callTargets(info *types.Info, call *ast.CallExpr) []*types.Func {
	fun := ast.Unparen(call.Fun)
	switch index := fun.(type) {
	case *ast.IndexExpr:
		fun = index.X
	case *ast.IndexListExpr:
		fun = index.X
	}
	var obj types.Object
	var recv types.Type
	switch fun := fun.(type) {
	case *ast.Ident:
		obj = info.Uses[fun]
	case *ast.SelectorExpr:
		if sel := info.Selections[fun]; sel != nil {
			obj, recv = sel.Obj(), sel.Recv()
		} else {
			obj = info.Uses[fun.Sel]
		}
	}
	fn, ok := obj.(*types.Func)
	if !ok {
		return nil
	}
	fn = fn.Origin()
	if recv == nil || !types.IsInterface(recv) {
		return []*types.Func{fn}
	}
	iface, ok := recv.Underlying().(*types.Interface)
	if !ok {
		return nil
	}
	return prog.implementations(iface, fn.Name())
}

// implementations are the module's methods an interface call can dispatch
// to (class-hierarchy analysis over the scanned module's named types).
func (prog *outboxProgram) implementations(iface *types.Interface, method string) []*types.Func {
	key := implKey{iface: iface, method: method}
	if out, ok := prog.impls[key]; ok {
		return out
	}
	var out []*types.Func
	seen := map[*types.Func]bool{}
	for _, named := range prog.byMethod[method] {
		ptr := types.NewPointer(named)
		if !types.Implements(named, iface) && !types.Implements(ptr, iface) {
			continue
		}
		obj, _, _ := types.LookupFieldOrMethod(ptr, true, named.Obj().Pkg(), method)
		if fn, ok := obj.(*types.Func); ok && !seen[fn.Origin()] {
			seen[fn.Origin()] = true
			out = append(out, fn.Origin())
		}
	}
	prog.impls[key] = out
	return out
}

// heldTransactions names the *sql.Tx values a function holds: its own
// parameters, and locals it began itself.
func heldTransactions(fn *ast.FuncDecl) map[string]bool {
	held := map[string]bool{}
	if fn.Type.Params != nil {
		for _, param := range fn.Type.Params.List {
			if !isSQLTxType(param.Type) {
				continue
			}
			for _, name := range param.Names {
				held[name.Name] = true
			}
		}
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) == 0 || len(assign.Rhs) != 1 {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok || outboxCallName(call) != "BeginTx" {
			return true
		}
		if id, ok := assign.Lhs[0].(*ast.Ident); ok {
			held[id.Name] = true
		}
		return true
	})
	// A closure taking a tx parameter holds it too.
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		lit, ok := n.(*ast.FuncLit)
		if !ok || lit.Type.Params == nil {
			return true
		}
		for _, param := range lit.Type.Params.List {
			if !isSQLTxType(param.Type) {
				continue
			}
			for _, name := range param.Names {
				held[name.Name] = true
			}
		}
		return true
	})
	return held
}

func isSQLTxType(expr ast.Expr) bool {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	return ok && sel.Sel != nil && sel.Sel.Name == "Tx"
}

func enqueueTxSite(fset *token.FileSet, rel string, call *ast.CallExpr, held map[string]bool) enqueueSite {
	site := enqueueSite{where: fmt.Sprintf("%s:%d", rel, fset.Position(call.Pos()).Line), arg: "<missing>"}
	if len(call.Args) < 2 {
		return site
	}
	switch arg := call.Args[1].(type) {
	case *ast.Ident:
		site.arg = arg.Name
		site.heldTx = held[arg.Name]
	case *ast.SelectorExpr:
		site.arg = arg.Sel.Name
		site.heldTx = held[arg.Sel.Name]
	default:
		site.arg = fmt.Sprintf("%T", arg)
	}
	return site
}

func outboxCallName(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		if fun.Sel != nil {
			return fun.Sel.Name
		}
	}
	return ""
}

// isOutboxTestSupport drops fixture seeding, which writes the same tables to
// build a test database and announces nothing.
func isOutboxTestSupport(rel string) bool {
	return strings.HasPrefix(rel, "lycaon/internal/testutil/") ||
		strings.HasPrefix(rel, "lycaon/internal/testdbseed/") ||
		strings.HasSuffix(rel, "_testsupport.go")
}
