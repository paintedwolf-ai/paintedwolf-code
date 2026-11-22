package invariants

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"
)

// Under reports whether f lies at or below any module-relative dir or is
// one of the named files.
func Under(f *GoFile, dirs ...string) bool {
	for _, dir := range dirs {
		dir = strings.Trim(dir, "/")
		if f.Rel == dir || f.Dir == dir || strings.HasPrefix(f.Dir, dir+"/") {
			return true
		}
	}
	return false
}

// Calls visits every call below node. pkg and name are set when the callee
// is a package-qualified function, such as os.Getenv.
func (f *GoFile) Calls(node ast.Node, visit func(call *ast.CallExpr, pkg, name string)) {
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		pkg, name, _ := f.Qualified(ast.Unparen(call.Fun))
		visit(call, pkg, name)
		return true
	})
}

// Selectors visits every package-qualified reference below node, called or
// not.
func (f *GoFile) Selectors(node ast.Node, visit func(sel *ast.SelectorExpr, pkg, name string)) {
	ast.Inspect(node, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, name, ok := f.Qualified(sel); ok {
			visit(sel, pkg, name)
		}
		return true
	})
}

// Funcs visits every function declaration with a body.
func (f *GoFile) Funcs(visit func(fn *ast.FuncDecl)) {
	for _, decl := range f.AST.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
			visit(fn)
		}
	}
}

// FuncName is Name for a function and Type.Name for a method.
func FuncName(fn *ast.FuncDecl) string {
	if recv := ReceiverType(fn); recv != "" {
		return recv + "." + fn.Name.Name
	}
	return fn.Name.Name
}

// EnclosingFunc returns the function declaration holding pos, or nil.
func (f *GoFile) EnclosingFunc(pos token.Pos) *ast.FuncDecl {
	for _, decl := range f.AST.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Pos() <= pos && pos < fn.End() {
			return fn
		}
	}
	return nil
}

// Site keys pos as "file:Func" by the bare name of the function holding it,
// or "file:<package>" for a package-level declaration.
func (f *GoFile) Site(pos token.Pos) string {
	if fn := f.EnclosingFunc(pos); fn != nil {
		return f.Rel + ":" + fn.Name.Name
	}
	return f.Rel + ":<package>"
}

// StringLit returns the value of a string literal expression.
func StringLit(expr ast.Expr) (string, bool) {
	lit, ok := ast.Unparen(expr).(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	return value, err == nil
}

// StringLits visits every string literal below node, skipping import specs.
func StringLits(node ast.Node, visit func(lit *ast.BasicLit, value string)) {
	ast.Inspect(node, func(n ast.Node) bool {
		if _, ok := n.(*ast.ImportSpec); ok {
			return false
		}
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if value, err := strconv.Unquote(lit.Value); err == nil {
			visit(lit, value)
		}
		return true
	})
}

// SelectorName returns "X.Sel" for a selector on an identifier, or "".
func SelectorName(expr ast.Expr) string {
	sel, ok := ast.Unparen(expr).(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	if id, ok := sel.X.(*ast.Ident); ok {
		return id.Name + "." + sel.Sel.Name
	}
	return ""
}

// CalleeName returns the called function's bare name: Name for Name(...),
// pkg.Name(...) and x.y.Name(...).
func CalleeName(call *ast.CallExpr) string {
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		return fun.Sel.Name
	case *ast.IndexExpr:
		return CalleeName(&ast.CallExpr{Fun: fun.X})
	case *ast.IndexListExpr:
		return CalleeName(&ast.CallExpr{Fun: fun.X})
	}
	return ""
}

// ConstString resolves a string literal, or a string constant declared in
// the module and named by an identifier in f's package or a qualified
// selector. It reads declarations, so it sees every platform's files.
func (g *GoCorpus) ConstString(f *GoFile, expr ast.Expr) (string, bool) {
	if value, ok := StringLit(expr); ok {
		return value, true
	}
	var pkg, name string
	switch e := ast.Unparen(expr).(type) {
	case *ast.Ident:
		pkg, name = f.Pkg, e.Name
	case *ast.SelectorExpr:
		p, n, ok := f.Qualified(e)
		if !ok {
			return "", false
		}
		pkg, name = p, n
	default:
		return "", false
	}
	d, ok := g.Decl(pkg, name)
	if !ok {
		return "", false
	}
	spec, ok := d.Node.(*ast.ValueSpec)
	if !ok {
		return "", false
	}
	for i, id := range spec.Names {
		if id.Name == name && i < len(spec.Values) {
			return StringLit(spec.Values[i])
		}
	}
	return "", false
}
