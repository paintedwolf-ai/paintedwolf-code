package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func scanWorkerExecuteFailureCodes(t *testing.T, path string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	contractcheck.FailErr(t, "parse failure.go", err)

	var inMapper bool
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if ok {
			inMapper = fn.Name != nil && fn.Name.Name == "ExecuteFailureCode"
			return true
		}
		if !inMapper {
			return true
		}
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return true
		}
		if v := contractcheck.AstStringLit(ret.Results[0]); v != "" && contractcheck.IsErrorCodeShape(v) {
			out[v] = true
		}
		return true
	})
	return out
}

func scanAPIHostErrorCodes(t *testing.T, dir string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	constants := loadNoticeCodeConstants(t, dir)
	corpus, err := contractcheck.LoadGoASTCorpusMode(dir, parser.SkipObjectResolution)
	contractcheck.FailErr(t, "load API source corpus", err)
	for _, source := range corpus.Production() {
		file := source.AST
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil || fn.Name.Name != "promptHostErrorCode" {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				ret, ok := n.(*ast.ReturnStmt)
				if ok && len(ret.Results) == 1 {
					if v := astNoticeCode(ret.Results[0], constants); v != "" && contractcheck.IsErrorCodeShape(v) {
						out[v] = true
					}
				}
				return true
			})
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				name, _ := astErrorEmitCallee(call.Fun)
				if name == "NewSessionHostError" && len(call.Args) == 1 {
					if v := astNoticeCode(call.Args[0], constants); v != "" && contractcheck.IsErrorCodeShape(v) {
						out[v] = true
					}
				}
			}
			literal, ok := n.(*ast.CompositeLit)
			if !ok || !isSessionHostErrorType(literal.Type) {
				return true
			}
			for _, element := range literal.Elts {
				field, ok := element.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, isKey := field.Key.(*ast.Ident)
				if isKey && key.Name == "Code" {
					if v := astNoticeCode(field.Value, constants); v != "" && contractcheck.IsErrorCodeShape(v) {
						out[v] = true
					}
				}
			}
			return true
		})
	}
	for code := range scanCodedNoticeErrors(t, filepath.Dir(dir), constants) {
		out[code] = true
	}
	return out
}

// scanCodedNoticeErrors follows the typed error boundary used by
// promptHostErrorCode. A NoticeCode method or coded sentinel can reach that
// boundary from any internal package without a central type switch.
func scanCodedNoticeErrors(t *testing.T, internalDir string, constants map[string]string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.Walk(internalDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.FuncDecl:
				if node.Name == nil || node.Name.Name != "NoticeCode" || node.Body == nil {
					return true
				}
				ast.Inspect(node.Body, func(bodyNode ast.Node) bool {
					ret, ok := bodyNode.(*ast.ReturnStmt)
					if ok && len(ret.Results) == 1 {
						if code := astNoticeCode(ret.Results[0], constants); code != "" && contractcheck.IsErrorCodeShape(code) {
							out[code] = true
						}
					}
					return true
				})
				return false
			case *ast.CallExpr:
				name, _ := astErrorEmitCallee(node.Fun)
				if name == "NewSentinel" && len(node.Args) == 2 {
					if code := astNoticeCode(node.Args[1], constants); code != "" && contractcheck.IsErrorCodeShape(code) {
						out[code] = true
					}
				}
			}
			return true
		})
		return nil
	})
	contractcheck.FailErr(t, "scan coded notice errors", err)
	return out
}

func loadNoticeCodeConstants(t *testing.T, apiDir string) map[string]string {
	t.Helper()
	path := filepath.Join(apiDir, "..", "..", "pkg", "api", "notice_codes.generated.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	contractcheck.FailErr(t, "parse generated notice codes", err)
	out := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != len(value.Values) {
				continue
			}
			for i, name := range value.Names {
				if code := contractcheck.AstStringLit(value.Values[i]); code != "" {
					out[name.Name] = code
				}
			}
		}
	}
	return out
}

func astNoticeCode(expr ast.Expr, constants map[string]string) string {
	if code := contractcheck.AstStringLit(expr); code != "" {
		return code
	}
	switch value := expr.(type) {
	case *ast.Ident:
		return constants[value.Name]
	case *ast.SelectorExpr:
		return constants[value.Sel.Name]
	default:
		return ""
	}
}

func isSessionHostErrorType(expr ast.Expr) bool {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name == "SessionHostError"
	case *ast.SelectorExpr:
		return value.Sel.Name == "SessionHostError"
	default:
		return false
	}
}
