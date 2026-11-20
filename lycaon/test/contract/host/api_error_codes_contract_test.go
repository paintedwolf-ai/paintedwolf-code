package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// scanAPIErrorCodes finds literal codes at handler emission sites.
func scanAPIErrorCodes(t *testing.T, apiDir string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	constants := apiErrorCodeConstants(t, filepath.Join(apiDir, "..", "..", "pkg", "api", "api_error_code_ids.generated.go"))

	corpus, err := contractcheck.LoadGoASTCorpusMode(apiDir, parser.SkipObjectResolution)
	contractcheck.FailErr(t, "load API source corpus", err)
	for _, source := range corpus.Production() {
		f := source.AST
		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CompositeLit:
				// View failures are resource state, not HTTP error codes.
				if typ, ok := node.Type.(*ast.SelectorExpr); ok && typ.Sel.Name == "SourceViewFailure" {
					return false
				}
			case *ast.CallExpr:
				// Fail* carry the code second; the positional forms carry it third.
				name, ok := astErrorEmitCallee(node.Fun)
				if !ok {
					return true
				}
				index := -1
				switch name {
				case "Fail", "FailReason", "FailDetails":
					index = 1
				case "Error", "ErrorContext", "ReasonError":
					index = 2
				}
				if index >= 0 && index < len(node.Args) {
					if v := astErrorCode(node.Args[index], constants); v != "" && contractcheck.IsErrorCodeShape(v) {
						out[v] = true
					}
				}
			case *ast.SelectorExpr:
				// A generated constant names a code wherever a handler passes it on.
				if code, ok := constants[node.Sel.Name]; ok {
					out[code] = true
				}
			case *ast.KeyValueExpr:
				// Code: "<code>" inside any composite literal (typically
				// wire.ErrorResponse, ComposeValidationError).
				key, ok := node.Key.(*ast.Ident)
				if !ok || key.Name != "Code" {
					return true
				}
				if v := astErrorCode(node.Value, constants); v != "" && contractcheck.IsErrorCodeShape(v) {
					out[v] = true
				}
			}
			return true
		})
		scanAPIReturnedErrorCodes(f, out, constants)
	}
	return out
}

func scanAPIReturnedErrorCodes(file *ast.File, out map[string]bool, constants map[string]string) {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		indexes := apiErrorCodeResultIndexes(fn.Type.Results)
		if len(indexes) == 0 {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			ret, ok := n.(*ast.ReturnStmt)
			if !ok {
				return true
			}
			for _, index := range indexes {
				if index >= len(ret.Results) {
					continue
				}
				if code := astErrorCode(ret.Results[index], constants); code != "" && contractcheck.IsErrorCodeShape(code) {
					out[code] = true
				}
			}
			return true
		})
	}
}

func apiErrorCodeResultIndexes(results *ast.FieldList) []int {
	if results == nil {
		return nil
	}
	var indexes []int
	resultIndex := 0
	for _, field := range results.List {
		count := len(field.Names)
		if count == 0 {
			count = 1
		}
		selector, ok := field.Type.(*ast.SelectorExpr)
		if ok && selector.Sel != nil && selector.Sel.Name == "ApiErrorCode" {
			for offset := range count {
				indexes = append(indexes, resultIndex+offset)
			}
		}
		resultIndex += count
	}
	return indexes
}

func apiErrorCodeConstants(t *testing.T, path string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	contractcheck.FailErr(t, "parse generated API error codes", err)
	out := map[string]string{}
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || len(value.Values) != 1 {
				continue
			}
			if code := contractcheck.AstStringLit(value.Values[0]); code != "" {
				out[value.Names[0].Name] = code
			}
		}
	}
	return out
}

func astErrorCode(expr ast.Expr, constants map[string]string) string {
	if code := contractcheck.AstStringLit(expr); code != "" {
		return code
	}
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || selector.Sel == nil {
		return ""
	}
	return constants[selector.Sel.Name]
}

func astErrorEmitCallee(fun ast.Expr) (string, bool) {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name, true
	case *ast.SelectorExpr:
		// slog shares the Error/ErrorContext names; its third argument is a log key.
		if f.Sel != nil && !astLoggerReceiver(f.X) {
			return f.Sel.Name, true
		}
	}
	return "", false
}

// astLoggerReceiver reports slog package calls and Logger fields.
func astLoggerReceiver(x ast.Expr) bool {
	switch r := x.(type) {
	case *ast.Ident:
		return r.Name == "slog"
	case *ast.SelectorExpr:
		return r.Sel != nil && (r.Sel.Name == "Logger" || r.Sel.Name == "logger")
	case *ast.CallExpr:
		return astLoggerReceiver(r.Fun)
	}
	return false
}
