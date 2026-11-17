package forbiddencalls

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
)

const (
	pongoPkg      = "github.com/flosch/pongo2/v6"
	pongoplainPkg = "github.com/lycaon/lycaon/internal/pongoplain"
)

// pongoSetCeilingMutators re-open a template set after pongoplain configured
// it; the ban list is the ceiling, so it is built in one place.
var pongoSetCeilingMutators = []string{"AddLoader", "BanFilter", "BanTag", "CleanCache"}

var templates = []laws.Rule{{
	// Pongo compilation and execution stay inside internal/pongoplain, which
	// carries the tag, resource and context ceilings. The watched render
	// methods are the exported method set of *pongo2.Template.
	Name:    "pongo-templates-carry-the-host-ceiling",
	Anchors: []laws.Anchor{laws.Func("internal/pongoplain", "Compile"), laws.Func("internal/pongoplain", "Execute")},
	Check: func(r *laws.Run) {
		render := pongoTemplateMethods(r)
		for _, f := range r.GoFiles(laws.AnyGo, "internal", "cmd", "pkg", "config", "test") {
			inside := laws.Under(f, "internal/pongoplain")
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
func pongoTemplateMethods(r *laws.Run) map[string]bool {
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
func isAssembledSource(f *laws.GoFile, arg ast.Expr) bool {
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
