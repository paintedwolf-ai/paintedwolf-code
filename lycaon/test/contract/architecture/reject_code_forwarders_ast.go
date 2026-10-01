package contract

import (
	"go/ast"
	"path"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/pkg/testcorpus"
)

// A reject carrier is an error type whose literal names a Code and carries
// Data: the shape every structured refusal takes before rendering.
func rejectCarrierTypes(files []testcorpus.GoFile) map[string]bool {
	errorTypes := map[string]bool{}
	shaped := map[string]bool{}
	for _, gf := range files {
		for _, decl := range gf.AST.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv != nil && d.Name.Name == "Error" && len(d.Recv.List) == 1 {
					errorTypes[receiverTypeName(d.Recv.List[0].Type)] = true
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					if st, ok := ts.Type.(*ast.StructType); ok && hasField(st, "Code") && hasField(st, "Data") {
						shaped[ts.Name.Name] = true
					}
				}
			}
		}
	}
	carriers := map[string]bool{}
	for name := range shaped {
		if errorTypes[name] {
			carriers[name] = true
		}
	}
	return carriers
}

func receiverTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func hasField(st *ast.StructType, name string) bool {
	for _, field := range st.Fields.List {
		for _, n := range field.Names {
			if n.Name == name {
				return true
			}
		}
	}
	return false
}

// forwarderKey names a function or method by the package directory declaring it.
type forwarderKey struct {
	dir  string
	name string
}

// rejectForwarders finds every function that passes one of its parameters on
// as a reject code: into a carrier literal's Code, into safecmd.Reject, or
// into another forwarder. The value is the parameter's position.
func rejectForwarders(files []testcorpus.GoFile, carriers map[string]bool) map[forwarderKey]int {
	forwarders := map[forwarderKey]int{}
	for changed := true; changed; {
		changed = false
		for _, gf := range files {
			dir := path.Dir(gf.Rel)
			for _, decl := range gf.AST.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				key := forwarderKey{dir: dir, name: fn.Name.Name}
				if _, known := forwarders[key]; known {
					continue
				}
				params := paramIndexes(fn.Type)
				if len(params) == 0 {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					if _, found := forwarders[key]; found {
						return false
					}
					if idx, ok := forwardedParam(n, gf, carriers, forwarders, params); ok {
						forwarders[key] = idx
						changed = true
						return false
					}
					return true
				})
			}
		}
	}
	return forwarders
}

func paramIndexes(ft *ast.FuncType) map[string]int {
	out := map[string]int{}
	i := 0
	for _, field := range ft.Params.List {
		if len(field.Names) == 0 {
			i++
			continue
		}
		for _, name := range field.Names {
			out[name.Name] = i
			i++
		}
	}
	return out
}

// forwardedParam reports a node that hands a parameter on as a reject code.
func forwardedParam(n ast.Node, gf testcorpus.GoFile, carriers map[string]bool, forwarders map[forwarderKey]int, params map[string]int) (int, bool) {
	switch node := n.(type) {
	case *ast.CompositeLit:
		if !carriers[compositeTypeName(node.Type)] {
			return 0, false
		}
		if id, ok := unwrapConversion(literalCodeValue(node)).(*ast.Ident); ok {
			idx, isParam := params[id.Name]
			return idx, isParam
		}
	case *ast.CallExpr:
		codeArg, ok := rejectCodeArg(node, gf, forwarders)
		if !ok {
			return 0, false
		}
		if id, ok := unwrapConversion(codeArg).(*ast.Ident); ok {
			idx, isParam := params[id.Name]
			return idx, isParam
		}
	}
	return 0, false
}

func compositeTypeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	default:
		return ""
	}
}

func literalCodeValue(lit *ast.CompositeLit) ast.Expr {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Code" {
			return kv.Value
		}
	}
	return nil
}

// unwrapConversion strips a single-argument type conversion such as
// wire.ApiErrorCode(code).
func unwrapConversion(expr ast.Expr) ast.Expr {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return expr
	}
	switch call.Fun.(type) {
	case *ast.Ident, *ast.SelectorExpr:
		return call.Args[0]
	default:
		return expr
	}
}

// rejectCodeArg returns the code argument of safecmd.Reject or a forwarder call.
func rejectCodeArg(call *ast.CallExpr, gf testcorpus.GoFile, forwarders map[forwarderKey]int) (ast.Expr, bool) {
	pkgName := gf.AST.Name.Name
	if isSafecmdRejectCall(call.Fun, pkgName) && len(call.Args) > 0 {
		return call.Args[0], true
	}
	key, ok := calleeKey(call.Fun, gf)
	if !ok {
		return nil, false
	}
	idx, found := forwarders[key]
	if !found || idx >= len(call.Args) {
		return nil, false
	}
	return call.Args[idx], true
}

// calleeKey resolves a call to the declaring package directory: a bare name or
// a method call stays in the caller's package, pkg.F follows the import.
func calleeKey(fun ast.Expr, gf testcorpus.GoFile) (forwarderKey, bool) {
	dir := path.Dir(gf.Rel)
	switch f := fun.(type) {
	case *ast.Ident:
		return forwarderKey{dir: dir, name: f.Name}, true
	case *ast.SelectorExpr:
		if pkg, ok := f.X.(*ast.Ident); ok {
			if importDir, imported := importDirOf(gf.AST, pkg.Name); imported {
				if !strings.HasPrefix(importDir, lycaonModulePrefix) {
					return forwarderKey{}, false
				}
				return forwarderKey{dir: strings.TrimPrefix(importDir, lycaonModulePrefix), name: f.Sel.Name}, true
			}
		}
		return forwarderKey{dir: dir, name: f.Sel.Name}, true
	default:
		return forwarderKey{}, false
	}
}

const lycaonModulePrefix = "github.com/lycaon/lycaon/"

// importDirOf returns the import path a file binds to local.
func importDirOf(file *ast.File, local string) (string, bool) {
	for _, imp := range file.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		name := path.Base(importPath)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name != local {
			continue
		}
		return importPath, true
	}
	return "", false
}
