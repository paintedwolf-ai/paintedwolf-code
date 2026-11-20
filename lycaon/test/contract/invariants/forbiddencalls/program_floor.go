package forbiddencalls

import (
	"go/ast"
	"go/token"
	"go/types"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

var programFloor = []invariants.Invariant{{
	// Settings hold no command-name permission floor; the floor is
	// confinement facts, never a list of program names.
	Name:    "no-program-name-floor-under-settings",
	Anchors: []invariants.Anchor{invariants.Package("internal/settings")},
	Check:   func(r *invariants.Run) { checkProgramNameFloor(r, "internal/settings") },
}, {
	// Confinement and project code hold no command-name permission floor.
	Name:    "no-program-name-floor-under-confine-and-project",
	Anchors: []invariants.Anchor{invariants.Package("internal/confine"), invariants.Package("internal/project")},
	Check:   func(r *invariants.Run) { checkProgramNameFloor(r, "internal/confine", "internal/project") },
}}

func checkProgramNameFloor(r *invariants.Run, dirs ...string) {
	for _, f := range r.GoFiles(invariants.Production, dirs...) {
		locals := localSingleAssignments(f.AST)
		ast.Inspect(f.AST, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CompositeLit:
				if isProgramNameBoolMap(node) {
					r.ReportAt(node.Pos(), "a map[string]bool of program names is a command-name floor")
				}
			case *ast.SwitchStmt:
				if switchOnFilepathBase(node.Tag, locals) && switchHasProgramNameCases(node) {
					r.ReportAt(node.Pos(), "switches on a program name from filepath.Base")
				}
			}
			return true
		})
	}
}

func isProgramNameBoolMap(expr ast.Expr) bool {
	cl, ok := expr.(*ast.CompositeLit)
	if !ok {
		return false
	}
	mt, ok := cl.Type.(*ast.MapType)
	if !ok {
		return false
	}
	key, ok := mt.Key.(*ast.Ident)
	if !ok || key.Name != "string" {
		return false
	}
	val, ok := mt.Value.(*ast.Ident)
	if !ok || val.Name != "bool" {
		return false
	}
	if len(cl.Elts) == 0 {
		return false
	}
	programish := 0
	for _, elt := range cl.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		lit, ok := kv.Key.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			continue
		}
		if looksLikeProgramName(s) {
			programish++
		}
	}
	return programish >= 2
}

// localSingleAssignments maps an identifier to the expression assigned to it,
// for identifiers assigned exactly once in the file. `base := filepath.Base(p)`
// then `switch base` is the same floor as switching on the call. A name assigned
// more than once is dropped rather than guessed at.
func localSingleAssignments(f *ast.File) map[string]ast.Expr {
	out := map[string]ast.Expr{}
	shadowed := map[string]bool{}
	record := func(lhs []ast.Expr, rhs []ast.Expr) {
		if len(lhs) != 1 || len(rhs) != 1 {
			return
		}
		ident, ok := lhs[0].(*ast.Ident)
		if !ok || ident.Name == "_" {
			return
		}
		if _, seen := out[ident.Name]; seen {
			shadowed[ident.Name] = true
			return
		}
		out[ident.Name] = rhs[0]
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			record(node.Lhs, node.Rhs)
		case *ast.ValueSpec:
			lhs := make([]ast.Expr, 0, len(node.Names))
			for _, name := range node.Names {
				lhs = append(lhs, name)
			}
			record(lhs, node.Values)
		}
		return true
	})
	for name := range shadowed {
		delete(out, name)
	}
	return out
}

// switchOnFilepathBase reports whether the switch tag is a filepath.Base result,
// directly or through one local assignment, and through one wrapping call
// (`strings.ToLower(filepath.Base(p))` is the same tag).
func switchOnFilepathBase(tag ast.Expr, locals map[string]ast.Expr) bool {
	if tag == nil {
		return false
	}
	if ident, ok := tag.(*ast.Ident); ok {
		assigned, found := locals[ident.Name]
		if !found {
			return false
		}
		// One hop only: locals carries no scope, so a chain would guess across
		// functions.
		return isFilepathBaseCall(assigned)
	}
	return isFilepathBaseCall(tag)
}

func isFilepathBaseCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if pkg, isIdent := sel.X.(*ast.Ident); isIdent && pkg.Name == "filepath" && sel.Sel.Name == "Base" {
		return true
	}
	// A wrapper — strings.ToLower, strings.TrimSpace — leaves the tag a program name.
	for _, arg := range call.Args {
		if isFilepathBaseCall(arg) {
			return true
		}
	}
	return false
}

func switchHasProgramNameCases(sw *ast.SwitchStmt) bool {
	count := 0
	for _, stmt := range sw.Body.List {
		cc, ok := stmt.(*ast.CaseClause)
		if !ok {
			continue
		}
		for _, expr := range cc.List {
			lit, ok := expr.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				continue
			}
			if looksLikeProgramName(s) {
				count++
			}
		}
	}
	return count >= 1
}

func looksLikeProgramName(s string) bool {
	if s == "" || strings.ContainsAny(s, "/\\ ") {
		return false
	}
	if strings.Contains(s, ".") && !strings.HasPrefix(s, "mkfs") {
		// Allow dotted tool ids (mcp.foo) — not argv[0] floor entries.
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' && r != '.' && r != '*' {
			return false
		}
	}
	return true
}

const (
	pongoPkg      = "github.com/flosch/pongo2/v6"
	pongoplainPkg = "github.com/lycaon/lycaon/internal/pongoplain"
)

// pongoSetCeilingMutators re-open a template set after pongoplain configured
// it; the ban list is the ceiling, so it is built in one place.
var pongoSetCeilingMutators = []string{"AddLoader", "BanFilter", "BanTag", "CleanCache"}

var templates = []invariants.Invariant{{
	// Pongo compilation and execution stay inside internal/pongoplain, which
	// carries the tag, resource and context ceilings. The watched render
	// methods are the exported method set of *pongo2.Template.
	Name:    "pongo-templates-carry-the-host-ceiling",
	Anchors: []invariants.Anchor{invariants.Func("internal/pongoplain", "Compile"), invariants.Func("internal/pongoplain", "Execute")},
	Check: func(r *invariants.Run) {
		render := pongoTemplateMethods(r)
		for _, f := range r.GoFiles(invariants.AnyGo, "internal", "cmd", "pkg", "config", "test") {
			inside := invariants.Under(f, "internal/pongoplain")
			hasPongo := f.Imports(pongoPkg)
			if f.DotImports(pongoPkg) && !inside {
				r.Report("%s dot-imports pongo2", f.Rel)
			}
			if f.DotImports(pongoplainPkg) {
				r.Report("%s dot-imports pongoplain", f.Rel)
			}
			f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return
				}
				switch {
				case pkg == pongoplainPkg:
					if name == "Compile" && len(call.Args) == 1 && isAssembledSource(f, call.Args[0]) {
						r.ReportAt(call.Pos(), "pongoplain.Compile is handed assembled template source; pass the authored body and branch in Go")
					}
				case inside || !hasPongo:
				case pkg == pongoPkg:
					r.ReportAt(call.Pos(), "pongo2.%s outside internal/pongoplain carries no tag ceiling", name)
				case render[sel.Sel.Name]:
					r.ReportAt(call.Pos(), ".%s renders outside pongoplain.Execute", sel.Sel.Name)
				case contains(pongoSetCeilingMutators, sel.Sel.Name):
					r.ReportAt(call.Pos(), ".%s re-opens a template set after pongoplain built its ceiling", sel.Sel.Name)
				}
			})
		}
	},
}}

// pongoTemplateMethods derives the exported methods of *pongo2.Template.
func pongoTemplateMethods(r *invariants.Run) map[string]bool {
	var pongo *types.Package
	for _, imp := range r.TypedPackage("internal/pongoplain").Types.Imports() {
		if imp.Path() == pongoPkg {
			pongo = imp
		}
	}
	if pongo == nil {
		r.Fatalf("internal/pongoplain does not import %s", pongoPkg)
	}
	tmpl, ok := pongo.Scope().Lookup("Template").(*types.TypeName)
	if !ok {
		r.Fatalf("pongo2 declares no Template type")
	}
	methods := map[string]bool{}
	set := types.NewMethodSet(types.NewPointer(tmpl.Type()))
	for i := range set.Len() {
		if name := set.At(i).Obj().Name(); token.IsExported(name) {
			methods[name] = true
		}
	}
	r.Expect(len(methods), "exported *pongo2.Template methods")
	return methods
}

// isAssembledSource reports template source built by concatenation or
// fmt.Sprint*, rather than passed through.
func isAssembledSource(f *invariants.GoFile, arg ast.Expr) bool {
	switch v := arg.(type) {
	case *ast.BinaryExpr:
		return v.Op == token.ADD
	case *ast.CallExpr:
		pkg, name, ok := f.Qualified(v.Fun)
		return ok && pkg == "fmt" && strings.HasPrefix(name, "Sprint")
	}
	return false
}

func contains(items []string, item string) bool {
	for _, x := range items {
		if x == item {
			return true
		}
	}
	return false
}
