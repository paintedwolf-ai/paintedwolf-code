package catalogload

import (
	"go/ast"
	"go/token"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/laws"
)

// errorCodes is where production code raises each API error code.
type errorCodes struct {
	// handlers are codes API handlers emit.
	handlers map[string]bool
	// hostErrors are codes promptHostErrorCode, session host errors and
	// coded notice errors raise.
	hostErrors map[string]bool
	// workerFailures are codes worker.ExecuteFailureCode maps to.
	workerFailures map[string]bool
	// typed are codes other host packages hand to handlers as values.
	typed map[string]bool
}

func (e *errorCodes) emitted() map[string]bool {
	out := map[string]bool{}
	for _, set := range []map[string]bool{e.handlers, e.hostErrors, e.workerFailures, e.typed} {
		for code := range set {
			out[code] = true
		}
	}
	return out
}

// errorCodeInventory scans production Go once for raised error codes.
func errorCodeInventory(r *laws.Run) *errorCodes {
	return laws.Part(r.Corpus, "error code inventory", func() (*errorCodes, error) {
		apiCodes := generatedConstants(r, "pkg/api/api_error_code_ids.generated.go")
		noticeCodes := generatedConstants(r, "pkg/api/notice_codes.generated.go")
		inv := &errorCodes{handlers: map[string]bool{}, hostErrors: map[string]bool{}, workerFailures: map[string]bool{}, typed: map[string]bool{}}
		code := func(expr ast.Expr, constants map[string]string) string {
			if v, ok := laws.StringLit(expr); ok {
				return v
			}
			switch e := expr.(type) {
			case *ast.Ident:
				return constants[e.Name]
			case *ast.SelectorExpr:
				return constants[e.Sel.Name]
			}
			return ""
		}
		add := func(set map[string]bool, v string) {
			if v != "" && contractcheck.IsErrorCodeShape(v) {
				set[v] = true
			}
		}
		for _, f := range r.GoFiles(laws.Production, "internal/api") {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.CompositeLit:
					// View failures are resource state, not HTTP error codes.
					if laws.TypeName(node.Type) == "SourceViewFailure" {
						return false
					}
					if laws.TypeName(node.Type) == "SessionHostError" {
						for _, elt := range node.Elts {
							if kv, ok := elt.(*ast.KeyValueExpr); ok && laws.SelectorName(kv.Key) == "" {
								if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Code" {
									add(inv.hostErrors, code(kv.Value, noticeCodes))
								}
							}
						}
					}
				case *ast.CallExpr:
					name, ok := errorEmitCallee(node.Fun)
					if !ok {
						return true
					}
					// Fail* carry the code second; the positional forms carry it third.
					at := map[string]int{"Fail": 1, "FailReason": 1, "FailDetails": 1, "Error": 2, "ErrorContext": 2, "ReasonError": 2}
					if i, ok := at[name]; ok && i < len(node.Args) {
						add(inv.handlers, code(node.Args[i], apiCodes))
					}
					if name == "NewSessionHostError" && len(node.Args) == 1 {
						add(inv.hostErrors, code(node.Args[0], noticeCodes))
					}
				case *ast.SelectorExpr:
					if v, ok := apiCodes[node.Sel.Name]; ok {
						inv.handlers[v] = true
					}
				case *ast.KeyValueExpr:
					if key, ok := node.Key.(*ast.Ident); ok && key.Name == "Code" {
						add(inv.handlers, code(node.Value, apiCodes))
					}
				}
				return true
			})
			f.Funcs(func(fn *ast.FuncDecl) {
				if fn.Name.Name == "promptHostErrorCode" {
					returns(fn, func(v ast.Expr) { add(inv.hostErrors, code(v, noticeCodes)) })
				}
				indexes := errorCodeResults(fn.Type.Results)
				if len(indexes) == 0 {
					return
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					if ret, ok := n.(*ast.ReturnStmt); ok {
						for _, i := range indexes {
							if i < len(ret.Results) {
								add(inv.handlers, code(ret.Results[i], apiCodes))
							}
						}
					}
					return true
				})
			})
		}
		mappers := 0
		for _, f := range r.GoFiles(laws.Production, "internal") {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.FuncDecl:
					if node.Body == nil {
						return false
					}
					switch {
					case node.Name.Name == "NoticeCode":
						returns(node, func(v ast.Expr) { add(inv.hostErrors, code(v, noticeCodes)) })
					case node.Name.Name == "ExecuteFailureCode" && node.Recv == nil && f.Dir == "internal/worker":
						mappers++
						returns(node, func(v ast.Expr) { add(inv.workerFailures, code(v, nil)) })
					}
				case *ast.CallExpr:
					if name, ok := errorEmitCallee(node.Fun); ok && name == "NewSentinel" && len(node.Args) == 2 {
						add(inv.hostErrors, code(node.Args[1], noticeCodes))
					}
				case *ast.SelectorExpr:
					if v, ok := apiCodes[node.Sel.Name]; ok {
						inv.typed[v] = true
					}
				case *ast.ValueSpec:
					if laws.TypeName(node.Type) == "ApiErrorCode" {
						for _, value := range node.Values {
							add(inv.typed, code(value, nil))
						}
					}
				}
				return true
			})
		}
		if mappers != 1 {
			r.Fatalf("internal/worker declares %d ExecuteFailureCode mappers, want one", mappers)
		}
		return inv, nil
	})
}

// returns visits the single-value return results of fn.
func returns(fn *ast.FuncDecl, visit func(ast.Expr)) {
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if ret, ok := n.(*ast.ReturnStmt); ok && len(ret.Results) == 1 {
			visit(ret.Results[0])
		}
		return true
	})
}

// generatedConstants maps the string constants of a generated file.
func generatedConstants(r *laws.Run, rel string) map[string]string {
	out := map[string]string{}
	for _, decl := range r.GoFile(rel).AST.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			for i, name := range value.Names {
				if i < len(value.Values) {
					if v, ok := laws.StringLit(value.Values[i]); ok {
						out[name.Name] = v
					}
				}
			}
		}
	}
	r.Expect(len(out), "constants in "+rel)
	return out
}

// errorCodeResults returns the result positions typed ApiErrorCode.
func errorCodeResults(results *ast.FieldList) []int {
	if results == nil {
		return nil
	}
	var indexes []int
	at := 0
	for _, field := range results.List {
		count := max(len(field.Names), 1)
		if laws.TypeName(field.Type) == "ApiErrorCode" {
			for i := range count {
				indexes = append(indexes, at+i)
			}
		}
		at += count
	}
	return indexes
}

// errorEmitCallee names an emitting call; slog's Error and ErrorContext
// carry a log key, not a code.
func errorEmitCallee(fun ast.Expr) (string, bool) {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name, true
	case *ast.SelectorExpr:
		if !loggerReceiver(f.X) {
			return f.Sel.Name, true
		}
	}
	return "", false
}

func loggerReceiver(x ast.Expr) bool {
	switch r := x.(type) {
	case *ast.Ident:
		return r.Name == "slog"
	case *ast.SelectorExpr:
		return r.Sel.Name == "Logger" || r.Sel.Name == "logger"
	case *ast.CallExpr:
		return loggerReceiver(r.Fun)
	}
	return false
}
