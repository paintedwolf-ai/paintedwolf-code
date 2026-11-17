package forbiddencalls

import (
	"go/ast"
	"go/types"

	"github.com/lycaon/lycaon/test/contract/laws"
)

const extpacksPkg = "github.com/lycaon/lycaon/internal/extpacks"

// deviceCatalogBoundaries read the device catalog: the ambient task roster and
// the workflow manifest registry are device-scoped and have no session to
// resolve a turn view from.
var deviceCatalogBoundaries = []laws.Exemption{
	{Key: "internal/app/configuration/catalog.go", Reason: "device catalog composition"},
	{Key: "internal/app/build_server.go", Reason: "server composition"},
	{Key: "internal/app/workflows/build.go", Reason: "the device-scoped workflow registry"},
	{Key: "internal/app/execution/runtime.go", Reason: "the device-scoped execution runtime"},
	{Key: "internal/session/catalog/effective_catalog.go", Reason: "resolves a turn view from the device catalog"},
	{Key: "internal/session/profiles/effective_skills.go", Reason: "the ambient task roster"},
	{Key: "internal/session/catalog/catalog_view.go", Reason: "builds the turn view"},
	{Key: "internal/workflow/definition/manifest.go", Reason: "the workflow manifest registry"},
}

var catalogReach = []laws.Rule{{
	// Below the device boundaries, code reaches the catalog through a turn
	// View or WithCatalog.
	Name:    "extpacks-active-only-at-device-boundaries",
	Anchors: []laws.Anchor{laws.Func("internal/extpacks", "Active")},
	Exempt:  deviceCatalogBoundaries,
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal", "cmd", "pkg", "config", "test") {
			f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
				if pkg == extpacksPkg && name == "Active" && !r.Exempt(f.Rel) {
					r.ReportAt(call.Pos(), "extpacks.Active() below the device boundaries; take a turn View")
				}
			})
		}
	},
}, {
	// A catalog or view is an explicit parameter, never a context value.
	Name:    "no-context-carried-catalog-or-view",
	Anchors: []laws.Anchor{laws.Decl("internal/extpacks", "EffectiveCatalog"), laws.Decl("internal/catalogview", "View")},
	Check: func(r *laws.Run) {
		for _, pkg := range r.TypedPackages(laws.Source...) {
			for _, f := range pkg.Files {
				f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
					fn := pkg.Callee(call)
					if fn == nil || fn.Pkg() == nil || fn.Pkg().Path() != "context" || fn.Name() != "WithValue" || len(call.Args) != 3 {
						return
					}
					if carriesCatalog(pkg.Info.TypeOf(call.Args[2])) {
						r.ReportAt(call.Pos(), "context carries a catalog or view; pass it as a parameter")
					}
				})
			}
		}
	},
}}

func carriesCatalog(t types.Type) bool {
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	path, name := named.Obj().Pkg().Path(), named.Obj().Name()
	return path == extpacksPkg && name == "EffectiveCatalog" || path == "github.com/lycaon/lycaon/internal/catalogview" && name == "View"
}
