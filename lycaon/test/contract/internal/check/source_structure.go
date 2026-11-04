package contractcheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"
)

// ParseRepoGoFile parses one repository Go file, so a check reads its structure and not its text:
// a comment or an unrelated string can no longer satisfy it.
func ParseRepoGoFile(t *testing.T, root, rel string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, filepath.FromSlash(rel)), nil, 0)
	FailErr(t, "parse "+rel, err)
	return file
}

// StringLiterals collects the string literal values below node.
func StringLiterals(node ast.Node) map[string]bool {
	values := map[string]bool{}
	ast.Inspect(node, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if value, err := strconv.Unquote(lit.Value); err == nil {
				values[value] = true
			}
		}
		return true
	})
	return values
}

// PackageVar returns the initializer of a package-level variable, or fails.
func PackageVar(t *testing.T, file *ast.File, name string) ast.Expr {
	t.Helper()
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.VAR {
			continue
		}
		for _, spec := range general.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, ident := range value.Names {
				if ident.Name == name && i < len(value.Values) {
					return value.Values[i]
				}
			}
		}
	}
	t.Fatalf("package variable %s not found", name)
	return nil
}

// TopLevelFunc returns a function without a receiver, or fails.
func TopLevelFunc(t *testing.T, file *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, declaration := range file.Decls {
		if fn, ok := declaration.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == name {
			return fn
		}
	}
	t.Fatalf("function %s not found", name)
	return nil
}

// KeyedStrings collects the string values given to key in composite literals below node.
func KeyedStrings(node ast.Node, key string) map[string]bool {
	values := map[string]bool{}
	ast.Inspect(node, func(n ast.Node) bool {
		pair, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		if name, ok := pair.Key.(*ast.Ident); ok && name.Name == key {
			for value := range StringLiterals(pair.Value) {
				values[value] = true
			}
		}
		return true
	})
	return values
}

// CallsSelector reports whether node calls qualifier.name.
func CallsSelector(node ast.Node, qualifier, name string) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == name {
			if base, ok := selector.X.(*ast.Ident); ok && base.Name == qualifier {
				found = true
			}
		}
		return !found
	})
	return found
}
