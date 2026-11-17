package forbiddencalls

import (
	"go/ast"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
)

var projectRoots = []laws.Rule{{
	// Tools address files through the resolver hub, which joins every
	// project root with a validated relative path.
	Name:    "tools-join-no-single-root",
	Anchors: []laws.Anchor{laws.GoPath("internal/tools/projectpaths/projectpaths.go"), laws.Func("internal/tools", "ToolContext.ActiveRootPath")},
	Exempt: []laws.Exemption{
		{Key: "internal/tools/native/gitcmd/file_entries.go:entryAbs", Reason: "git index entries are relative to the repository resolved from the reached directory, -C included"},
	},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal/tools") {
			f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Join" || len(call.Args) < 2 {
					return
				}
				base := exprString(call.Args[0])
				switch {
				case strings.Contains(base, "projectDir") || strings.Contains(base, "ProjectDir") || base == "root":
					if singleReturnOf(f, call, "entryAbs") && r.Exempt(f.Site(call.Pos())) {
						return
					}
					r.ReportAt(call.Pos(), "joins the single root %s; resolve through projectpaths", base)
				case strings.Contains(base, "ActiveRootPath"):
					r.ReportAt(call.Pos(), "joins ActiveRootPath; resolve through projectpaths")
				}
			})
		}
	},
}}

// exprString spells an identifier, selector or method-call chain.
func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.CallExpr:
		if sel, ok := v.Fun.(*ast.SelectorExpr); ok {
			return exprString(sel.X) + "." + sel.Sel.Name + "()"
		}
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	}
	return ""
}

// singleReturnOf reports call as the whole body of function name in f:
// `return call`.
func singleReturnOf(f *laws.GoFile, call *ast.CallExpr, name string) bool {
	fn := f.EnclosingFunc(call.Pos())
	if fn == nil || fn.Name.Name != name || len(fn.Body.List) != 1 {
		return false
	}
	ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
	return ok && len(ret.Results) == 1 && ret.Results[0] == call
}
