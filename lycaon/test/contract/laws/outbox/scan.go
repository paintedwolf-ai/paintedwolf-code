package outbox

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
)

// outboxTree names the persistence layer of a module the outbox laws scan.
type outboxTree struct {
	// module is the module path; the scan covers module/internal/...
	module string
	// dbPackage is the import path of the sqlc Queries package.
	dbPackage string
	// queriesDir holds the sqlc query files that name each Queries method.
	queriesDir string
	// enqueue names the outbox methods, as types.Func.FullName reports them.
	enqueue map[string]bool
	// floor lists event tables no direct write discovers.
	floor []string
}

const lycaonModule = "github.com/lycaon/lycaon"

// lycaonOutbox is the host's persistence layer.
var lycaonOutbox = outboxTree{
	module:     lycaonModule,
	dbPackage:  lycaonModule + "/internal/db",
	queriesDir: "lycaon/internal/db/queries",
	enqueue: map[string]bool{
		"(*" + lycaonModule + "/internal/eventoutbox.Outbox).EnqueueTx":        true,
		"(*" + lycaonModule + "/internal/eventoutbox.Outbox).EnqueueProjectTx": true,
	},
	floor: []string{"worker_jobs", "workflow_runs", "code_scans"},
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
			writers = []string{"the event table floor"}
		}
		parts = append(parts, table+" announced by "+strings.Join(writers, ", "))
	}
	return strings.Join(parts, "; ")
}

// scanOf is the outbox call graph of a tree, built once per corpus.
func scanOf(r *laws.Run, tree outboxTree) *outboxScan {
	return laws.Part(r.Corpus, "outbox scan", func() (*outboxScan, error) {
		return tree.scan(r)
	})
}

// scan resolves every call in the module's production functions through
// go/types: package functions and concrete methods by their object, and
// interface methods to each implementation declared in the module. Global
// name matching would let unrelated functions that share a name stand in
// for each other.
func (tree outboxTree) scan(r *laws.Run) (*outboxScan, error) {
	queries, err := mutatingQueryTables(r.Tree(tree.queriesDir, ".sql"))
	if err != nil {
		return nil, err
	}
	prog := tree.program(r.Types())
	scan := &outboxScan{
		announces:       map[*outboxFunc]bool{},
		callerAnnounces: map[*outboxFunc]bool{},
		eventTables:     map[string]bool{},
		writers:         map[string][]string{},
	}
	for _, table := range tree.floor {
		scan.eventTables[table] = true
	}
	byObj := map[*types.Func]*outboxFunc{}
	direct := map[*outboxFunc]bool{}
	for _, pkg := range prog.scanned {
		for _, file := range pkg.Files {
			rel := file.Rel
			if isOutboxTestSupport(rel) {
				continue
			}
			for _, decl := range file.AST.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				obj, _ := pkg.Info.Defs[fn.Name].(*types.Func)
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
					for _, target := range prog.callTargets(pkg.Info, call) {
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

// outboxProgram indexes the type-checked module for call resolution.
type outboxProgram struct {
	fset    *token.FileSet
	scanned []*laws.TypedPackage
	// byMethod indexes the module's concrete named types by method name.
	byMethod map[string][]*types.Named
	impls    map[implKey][]*types.Func
}

type implKey struct {
	iface  *types.Interface
	method string
}

// program indexes the module's internal and pkg packages; the scan covers
// internal.
func (tree outboxTree) program(typed *laws.Program) *outboxProgram {
	prog := &outboxProgram{fset: typed.Fset, byMethod: map[string][]*types.Named{}, impls: map[implKey][]*types.Func{}}
	for _, path := range typed.Order {
		pkg := typed.Packages[path]
		if !strings.HasPrefix(path, tree.module+"/internal/") && !strings.HasPrefix(path, tree.module+"/pkg/") {
			continue
		}
		prog.indexMethods(pkg.Types)
		if strings.HasPrefix(path, tree.module+"/internal/") {
			prog.scanned = append(prog.scanned, pkg)
		}
	}
	return prog
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
	return strings.HasPrefix(rel, "internal/testutil/") ||
		strings.HasPrefix(rel, "internal/testdbseed/") ||
		strings.HasSuffix(rel, "_testsupport.go")
}
