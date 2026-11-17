// Package analyzers holds arch-lint's per-package checks.
package analyzers

import (
	"go/ast"
	"go/types"

	"golang.org/x/tools/go/analysis"
)

// Reexport reports names that only forward another package's: type aliases,
// function variables, and exported functions whose body passes their
// parameters straight to another package's function.
var Reexport = &analysis.Analyzer{
	Name: "reexport",
	Doc:  "report aliases, function variables and pass-through functions that forward another package",
	Run:  runReexport,
}

func runReexport(pass *analysis.Pass) (any, error) {
	for _, file := range productionFiles(pass) {
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						reportAlias(pass, s)
					case *ast.ValueSpec:
						reportFuncVar(pass, s)
					}
				}
			case *ast.FuncDecl:
				reportPassThrough(pass, d)
			}
		}
	}
	return nil, nil
}

func reportAlias(pass *analysis.Pass, s *ast.TypeSpec) {
	if !s.Assign.IsValid() {
		return
	}
	if obj := foreignObject(pass, s.Type); obj != nil {
		pass.Reportf(s.Pos(), "type %s re-exports %s.%s", s.Name.Name, obj.Pkg().Path(), obj.Name())
	}
}

func reportFuncVar(pass *analysis.Pass, s *ast.ValueSpec) {
	for i, value := range s.Values {
		if i >= len(s.Names) {
			return
		}
		if fn, ok := foreignObject(pass, value).(*types.Func); ok {
			pass.Reportf(s.Names[i].Pos(), "%s re-exports %s.%s", s.Names[i].Name, fn.Pkg().Path(), fn.Name())
		}
	}
}

// reportPassThrough reports an exported package function whose only statement
// calls another package's function with its own parameters in order.
func reportPassThrough(pass *analysis.Pass, d *ast.FuncDecl) {
	if d.Recv != nil || !d.Name.IsExported() || d.Body == nil || len(d.Body.List) != 1 {
		return
	}
	var call *ast.CallExpr
	switch s := d.Body.List[0].(type) {
	case *ast.ReturnStmt:
		if len(s.Results) == 1 {
			call, _ = s.Results[0].(*ast.CallExpr)
		}
	case *ast.ExprStmt:
		call, _ = s.X.(*ast.CallExpr)
	}
	if call == nil {
		return
	}
	fn, ok := foreignObject(pass, call.Fun).(*types.Func)
	if !ok || fn.Type().(*types.Signature).Recv() != nil {
		return
	}
	var params []string
	for _, field := range d.Type.Params.List {
		for _, name := range field.Names {
			params = append(params, name.Name)
		}
	}
	if len(params) != len(call.Args) {
		return
	}
	for i, arg := range call.Args {
		id, ok := arg.(*ast.Ident)
		if !ok || id.Name != params[i] {
			return
		}
	}
	pass.Reportf(d.Name.Pos(), "%s forwards to %s.%s", d.Name.Name, fn.Pkg().Path(), fn.Name())
}

// foreignObject resolves a qualified identifier p.X to its object when p is
// another package.
func foreignObject(pass *analysis.Pass, expr ast.Expr) types.Object {
	if idx, ok := expr.(*ast.IndexExpr); ok {
		expr = idx.X
	}
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	pkgID, ok := sel.X.(*ast.Ident)
	if !ok {
		return nil
	}
	if _, ok := pass.TypesInfo.Uses[pkgID].(*types.PkgName); !ok {
		return nil
	}
	obj := pass.TypesInfo.Uses[sel.Sel]
	if obj == nil || obj.Pkg() == nil || obj.Pkg() == pass.Pkg {
		return nil
	}
	return obj
}
