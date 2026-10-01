package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestExtensionDiagnosticRegistryClosure requires emitted diagnostics to be registered and graded.
func TestExtensionDiagnosticRegistryClosure(t *testing.T) {
	t.Parallel()
	consts, registered, graded := scanExtpackDiagnosticTables(t)

	inRegistry := map[string]bool{}
	for _, name := range registered {
		inRegistry[name] = true
	}
	inSeverity := map[string]bool{}
	for _, name := range graded {
		inSeverity[name] = true
	}

	var violations []string
	for name, code := range consts {
		if !inRegistry[name] {
			violations = append(violations, code+" ("+name+") is declared but missing from allDiagnosticCodes")
		}
		if !inSeverity[name] {
			violations = append(violations, code+" ("+name+") has no diagnosticSeverity row, so it silently defaults to error")
		}
	}
	for _, name := range registered {
		if _, ok := consts[name]; !ok {
			violations = append(violations, name+" is in allDiagnosticCodes but is not a declared diagnostic constant")
		}
	}
	contractcheck.FailViolations(t, "extension diagnostic registry drift", violations)
}

// The exported accessor is the only view codegen and the API have, so it must
// carry every declared code rather than whatever the literal happened to list.
func TestExtensionDiagnosticCodesExported(t *testing.T) {
	t.Parallel()
	consts, _, _ := scanExtpackDiagnosticTables(t)

	exported := map[string]bool{}
	for _, code := range extpacks.AllDiagnosticCodes() {
		exported[code] = true
	}

	var violations []string
	for name, code := range consts {
		if !exported[code] {
			violations = append(violations, code+" ("+name+") is not returned by AllDiagnosticCodes")
		}
	}
	contractcheck.FailViolations(t, "extension diagnostic export drift", violations)
}

// scanExtpackDiagnosticTables reads the package source rather than its runtime
// values: allDiagnosticCodes and diagnosticSeverity are unexported, and the
// point of the check is that a constant can exist without reaching either.
func scanExtpackDiagnosticTables(t *testing.T) (consts map[string]string, registered, graded []string) {
	t.Helper()
	dir := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "extpacks")
	entries, err := os.ReadDir(dir)
	contractcheck.FailErr(t, "read internal/extpacks", err)

	consts = map[string]string{}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		contractcheck.FailErr(t, "parse "+name, parseErr)

		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok || len(value.Names) != len(value.Values) {
					continue
				}
				for i, ident := range value.Names {
					switch {
					case gen.Tok == token.CONST && strings.HasPrefix(ident.Name, "Diag"):
						if code := contractcheck.AstStringLit(value.Values[i]); code != "" {
							consts[ident.Name] = code
						}
					case ident.Name == "allDiagnosticCodes":
						registered = append(registered, compositeIdentElements(value.Values[i])...)
					case ident.Name == "diagnosticSeverity":
						graded = append(graded, compositeIdentKeys(value.Values[i])...)
					}
				}
			}
		}
	}
	if len(consts) == 0 || len(registered) == 0 || len(graded) == 0 {
		t.Fatal("diagnostic constant, registry, or severity table not found in internal/extpacks")
	}
	return consts, registered, graded
}

// compositeIdentElements returns the identifier names listed in a slice literal.
func compositeIdentElements(expr ast.Expr) []string {
	literal, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil
	}
	var out []string
	for _, element := range literal.Elts {
		if ident, ok := element.(*ast.Ident); ok {
			out = append(out, ident.Name)
		}
	}
	return out
}

// compositeIdentKeys returns the identifier keys of a map literal.
func compositeIdentKeys(expr ast.Expr) []string {
	literal, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil
	}
	var out []string
	for _, element := range literal.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if ident, ok := pair.Key.(*ast.Ident); ok {
			out = append(out, ident.Name)
		}
	}
	return out
}
