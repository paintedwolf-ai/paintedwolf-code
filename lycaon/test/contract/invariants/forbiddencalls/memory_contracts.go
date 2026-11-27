package forbiddencalls

import (
	"go/ast"
	"go/token"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

// contractMemoryDirs are the ordinary contract suites and their shared
// analysis helpers governed by the test-memory structural invariants.
var contractMemoryDirs = []string{"test/contract", "pkg/testcorpus"}

var memoryContracts = []invariants.Invariant{{
	// Ordinary contract suites and their shared analysis helpers must not
	// force garbage collection or heap scavenging to improve reported headroom.
	Name: "contract-suites-avoid-forced-gc",
	Anchors: []invariants.Anchor{
		invariants.Dir("lycaon/test/contract"),
		invariants.Package("pkg/testcorpus"),
	},
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.AnyGo, contractMemoryDirs...) {
			checkForcedGC(r, f)
		}
	},
}, {
	// Pure once-initialized analysis memos (structs guarded only by sync.Once,
	// without a mutex) must never have their stored result fields overwritten
	// outside the owning once.Do initialization.
	Name: "shared-once-memo-results-stay-immutable",
	Anchors: []invariants.Anchor{
		invariants.Decl("test/contract/invariants", "onceValue"),
		invariants.Decl("pkg/testcorpus", "cacheEntry"),
		invariants.Decl("pkg/testcorpus", "goCacheEntry"),
	},
	Check: func(r *invariants.Run) {
		byDir := map[string][]*invariants.GoFile{}
		for _, f := range r.GoFiles(invariants.AnyGo, contractMemoryDirs...) {
			byDir[f.Dir] = append(byDir[f.Dir], f)
		}
		for _, files := range byDir {
			checkOnceMemoOwnership(r, files)
		}
	},
}}

func checkForcedGC(r *invariants.Run, f *invariants.GoFile) {
	f.Funcs(func(fn *ast.FuncDecl) {
		shadowed := localBindings(fn)
		checkForcedGCNode(r, f, fn.Body, shadowed)
	})
	for _, decl := range f.AST.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.VAR {
			checkForcedGCNode(r, f, gen, nil)
		}
	}
}

func checkForcedGCNode(r *invariants.Run, f *invariants.GoFile, node ast.Node, shadowed map[string]bool) {
	f.Selectors(node, func(sel *ast.SelectorExpr, pkg, name string) {
		if id, ok := sel.X.(*ast.Ident); ok && shadowed[id.Name] {
			return
		}
		switch {
		case pkg == "runtime" && name == "GC":
			r.ReportAt(sel.Pos(), "explicit runtime.GC outside collector-behavior tests")
		case pkg == "runtime/debug" && name == "FreeOSMemory":
			r.ReportAt(sel.Pos(), "explicit runtime/debug.FreeOSMemory outside collector-behavior tests")
		}
	})
	dotRuntime := f.DotImports("runtime")
	dotDebug := f.DotImports("runtime/debug")
	if !dotRuntime && !dotDebug {
		return
	}
	f.Calls(node, func(call *ast.CallExpr, pkg, _ string) {
		if pkg != "" {
			return
		}
		id, ok := ast.Unparen(call.Fun).(*ast.Ident)
		if !ok || shadowed[id.Name] {
			return
		}
		switch {
		case dotRuntime && id.Name == "GC":
			r.ReportAt(call.Pos(), "explicit runtime.GC via dot import outside collector-behavior tests")
		case dotDebug && id.Name == "FreeOSMemory":
			r.ReportAt(call.Pos(), "explicit runtime/debug.FreeOSMemory via dot import outside collector-behavior tests")
		}
	})
}

func localBindings(fn *ast.FuncDecl) map[string]bool {
	bound := map[string]bool{}
	addFields := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, field := range fl.List {
			for _, name := range field.Names {
				bound[name.Name] = true
			}
		}
	}
	addFields(fn.Recv)
	if fn.Type != nil {
		addFields(fn.Type.Params)
		addFields(fn.Type.Results)
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch stmt := n.(type) {
		case *ast.ValueSpec:
			for _, name := range stmt.Names {
				bound[name.Name] = true
			}
		case *ast.AssignStmt:
			if stmt.Tok != token.DEFINE {
				return true
			}
			for _, lhs := range stmt.Lhs {
				if id, ok := lhs.(*ast.Ident); ok {
					bound[id.Name] = true
				}
			}
		case *ast.RangeStmt:
			if stmt.Tok != token.DEFINE {
				return true
			}
			for _, expr := range []ast.Expr{stmt.Key, stmt.Value} {
				if id, ok := expr.(*ast.Ident); ok {
					bound[id.Name] = true
				}
			}
		}
		return true
	})
	return bound
}

// onceMemoStruct describes a struct in a package that holds a sync.Once without
// a mutex lock, along with the fields initialized inside its once.Do closure.
type onceMemoStruct struct {
	hasMutex   bool
	onceFields map[string]bool
	fieldTypes map[string]string
	memoFields map[string]string
}

type pkgMemoIndex struct {
	structs map[string]*onceMemoStruct
	pkgVars map[string]string
}

func checkOnceMemoOwnership(r *invariants.Run, files []*invariants.GoFile) {
	idx := indexPkgStructs(files)
	if len(idx.structs) == 0 {
		return
	}
	discoverOnceMemoFields(idx, files)
	for _, f := range files {
		f.Funcs(func(fn *ast.FuncDecl) {
			env := idx.funcTypes(fn)
			reportMemoWritesOutsideInit(r, idx, env, fn.Body)
		})
	}
}

func indexPkgStructs(files []*invariants.GoFile) *pkgMemoIndex {
	idx := &pkgMemoIndex{
		structs: map[string]*onceMemoStruct{},
		pkgVars: map[string]string{},
	}
	for _, f := range files {
		for _, decl := range f.AST.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if st, ok := s.Type.(*ast.StructType); ok {
						idx.structs[s.Name.Name] = parseOnceMemoStruct(f, st)
					}
				case *ast.ValueSpec:
					if st, ok := s.Type.(*ast.StructType); ok {
						for _, name := range s.Names {
							anon := "<var:" + name.Name + ">"
							idx.structs[anon] = parseOnceMemoStruct(f, st)
							idx.pkgVars[name.Name] = anon
						}
					} else if s.Type != nil {
						if tname := invariants.TypeName(s.Type); tname != "" {
							for _, name := range s.Names {
								idx.pkgVars[name.Name] = tname
							}
						}
					}
				}
			}
		}
	}
	return idx
}

func parseOnceMemoStruct(f *invariants.GoFile, st *ast.StructType) *onceMemoStruct {
	sm := &onceMemoStruct{
		onceFields: map[string]bool{},
		fieldTypes: map[string]string{},
		memoFields: map[string]string{},
	}
	if st.Fields == nil {
		return sm
	}
	for _, field := range st.Fields.List {
		if isSyncType(f, field.Type, "Mutex") || isSyncType(f, field.Type, "RWMutex") {
			sm.hasMutex = true
		}
		isOnce := isSyncType(f, field.Type, "Once")
		tname := invariants.TypeName(field.Type)
		if len(field.Names) == 0 {
			if tname != "" {
				sm.fieldTypes[tname] = tname
				if isOnce {
					sm.onceFields[tname] = true
				}
			}
			continue
		}
		for _, name := range field.Names {
			if isOnce {
				sm.onceFields[name.Name] = true
			}
			if tname != "" {
				sm.fieldTypes[name.Name] = tname
			}
		}
	}
	return sm
}

func isSyncType(f *invariants.GoFile, expr ast.Expr, want string) bool {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	pkg, name, ok := f.Qualified(expr)
	return ok && pkg == "sync" && name == want
}

func (idx *pkgMemoIndex) funcTypes(fn *ast.FuncDecl) map[string]string {
	env := map[string]string{}
	addFields := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, field := range fl.List {
			if tname := invariants.TypeName(field.Type); tname != "" {
				for _, name := range field.Names {
					env[name.Name] = tname
				}
			}
		}
	}
	addFields(fn.Recv)
	if fn.Type != nil {
		addFields(fn.Type.Params)
		addFields(fn.Type.Results)
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch stmt := n.(type) {
		case *ast.ValueSpec:
			if stmt.Type != nil {
				if tname := invariants.TypeName(stmt.Type); tname != "" {
					for _, name := range stmt.Names {
						env[name.Name] = tname
					}
				}
			}
		case *ast.AssignStmt:
			if stmt.Tok != token.DEFINE {
				return true
			}
			for i, lhs := range stmt.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || i >= len(stmt.Rhs) {
					continue
				}
				if tname := idx.exprType(env, stmt.Rhs[i]); tname != "" {
					env[id.Name] = tname
				}
			}
		}
		return true
	})
	return env
}

func (idx *pkgMemoIndex) exprType(env map[string]string, expr ast.Expr) string {
	expr = ast.Unparen(expr)
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = ast.Unparen(star.X)
	}
	if unary, ok := expr.(*ast.UnaryExpr); ok && unary.Op == token.AND {
		expr = ast.Unparen(unary.X)
	}
	switch e := expr.(type) {
	case *ast.CompositeLit:
		return invariants.TypeName(e.Type)
	case *ast.TypeAssertExpr:
		return invariants.TypeName(e.Type)
	case *ast.Ident:
		if t, ok := env[e.Name]; ok {
			return t
		}
		return idx.pkgVars[e.Name]
	case *ast.SelectorExpr:
		parent := idx.exprType(env, e.X)
		if sm, ok := idx.structs[parent]; ok {
			return sm.fieldTypes[e.Sel.Name]
		}
	}
	return ""
}

func (idx *pkgMemoIndex) matchOnceDo(env map[string]string, call *ast.CallExpr) (string, string, *ast.BlockStmt) {
	doSel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok || doSel.Sel.Name != "Do" || len(call.Args) != 1 {
		return "", "", nil
	}
	onceSel, ok := ast.Unparen(doSel.X).(*ast.SelectorExpr)
	if !ok {
		return "", "", nil
	}
	lit, ok := ast.Unparen(call.Args[0]).(*ast.FuncLit)
	if !ok || lit.Body == nil {
		return "", "", nil
	}
	structName := idx.exprType(env, onceSel.X)
	sm, ok := idx.structs[structName]
	if !ok || sm.hasMutex || !sm.onceFields[onceSel.Sel.Name] {
		return "", "", nil
	}
	return structName, onceSel.Sel.Name, lit.Body
}

func discoverOnceMemoFields(idx *pkgMemoIndex, files []*invariants.GoFile) {
	for _, f := range files {
		f.Funcs(func(fn *ast.FuncDecl) {
			env := idx.funcTypes(fn)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				structName, onceField, body := idx.matchOnceDo(env, call)
				if body == nil {
					return true
				}
				sm := idx.structs[structName]
				ast.Inspect(body, func(inner ast.Node) bool {
					visitAssignedSelectors(inner, func(sel *ast.SelectorExpr) {
						if idx.exprType(env, sel.X) == structName && !sm.onceFields[sel.Sel.Name] {
							sm.memoFields[sel.Sel.Name] = onceField
						}
					})
					return true
				})
				return true
			})
		})
	}
}

type memoKey struct {
	structName string
	onceField  string
}

func reportMemoWritesOutsideInit(r *invariants.Run, idx *pkgMemoIndex, env map[string]string, body *ast.BlockStmt) {
	active := map[memoKey]int{}
	var walk func(ast.Node)
	walk = func(n ast.Node) {
		if n == nil {
			return
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if structName, onceField, doBody := idx.matchOnceDo(env, call); doBody != nil {
				key := memoKey{structName: structName, onceField: onceField}
				active[key]++
				walk(doBody)
				active[key]--
				return
			}
		}
		visitAssignedSelectors(n, func(sel *ast.SelectorExpr) {
			structName := idx.exprType(env, sel.X)
			sm, ok := idx.structs[structName]
			if !ok || sm.hasMutex {
				return
			}
			onceField, isMemo := sm.memoFields[sel.Sel.Name]
			if !isMemo {
				return
			}
			if active[memoKey{structName: structName, onceField: onceField}] > 0 {
				return
			}
			r.ReportAt(sel.Pos(), "writes once-initialized memo field %s.%s outside %s.Do",
				structName, sel.Sel.Name, onceField)
		})
		ast.Inspect(n, func(child ast.Node) bool {
			if child == n || child == nil {
				return true
			}
			walk(child)
			return false
		})
	}
	walk(body)
}

func visitAssignedSelectors(n ast.Node, visit func(*ast.SelectorExpr)) {
	switch stmt := n.(type) {
	case *ast.AssignStmt:
		for _, lhs := range stmt.Lhs {
			if sel, ok := ast.Unparen(lhs).(*ast.SelectorExpr); ok {
				visit(sel)
			}
		}
	case *ast.IncDecStmt:
		if sel, ok := ast.Unparen(stmt.X).(*ast.SelectorExpr); ok {
			visit(sel)
		}
	}
}
