package forbiddencalls

import (
	"go/ast"
	"go/types"
	"slices"

	"github.com/lycaon/lycaon/test/contract/laws"
)

// Host decisions read declared codes and typed fields, never prose.
var prose = []laws.Rule{{
	// The MCP CallTool error bridge reads the declared code field.
	Name:    "mcp-error-bridge-reads-codes",
	Anchors: []laws.Anchor{laws.GoPath("internal/mcp/call_envelope.go")},
	Check: func(r *laws.Run) {
		f := r.GoFile("internal/mcp/call_envelope.go")
		f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
			if pkg == "strings" && (name == "Contains" || name == "ContainsAny" || name == "EqualFold") {
				r.ReportAt(call.Pos(), "strings.%s classifies error prose", name)
			}
		})
	},
}}

// coordinatorScanRoots run turns; their decisions branch on typed state.
var coordinatorScanRoots = []string{"internal/session", "internal/coordinator", "internal/workflow"}

var userTextProbes = []string{"Contains", "HasPrefix", "HasSuffix", "EqualFold", "MatchString"}

var userTextParams = []string{"userPrompt", "userMessage", "message", "text"}

var userText = []laws.Rule{{
	// Errors are classified by sentinel and errors.Is, never by their text.
	Name:    "no-error-text-parsing-in-coordinator-packages",
	Anchors: []laws.Anchor{laws.Package("internal/session"), laws.Package("internal/coordinator/loopwake"), laws.Package("internal/workflow")},
	Check: func(r *laws.Run) {
		errType := types.Universe.Lookup("error").Type().Underlying().(*types.Interface)
		for _, pkg := range r.TypedPackages(coordinatorScanRoots...) {
			for _, f := range pkg.Files {
				f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
					name := laws.CalleeName(call)
					if !slices.Contains([]string{"Contains", "HasPrefix", "HasSuffix", "MatchString"}, name) {
						return
					}
					for _, arg := range call.Args {
						inner, ok := ast.Unparen(arg).(*ast.CallExpr)
						if !ok || laws.CalleeName(inner) != "Error" || len(inner.Args) != 0 {
							continue
						}
						sel, ok := inner.Fun.(*ast.SelectorExpr)
						if !ok {
							continue
						}
						if t := pkg.Info.TypeOf(sel.X); t != nil && types.Implements(t, errType) {
							r.ReportAt(call.Pos(), "%s over an error's text; use typed sentinels and errors.Is", name)
						}
					}
				})
			}
		}
	},
}, {
	// A string probe of the user's text never feeds a behavior sink.
	Name:    "no-user-text-probe-to-behavior-sink",
	Anchors: []laws.Anchor{laws.Func("internal/coordinator/surface", "SessionUserTask")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, coordinatorScanRoots...) {
			f.Funcs(func(fn *ast.FuncDecl) {
				params := map[string]bool{}
				for _, field := range fn.Type.Params.List {
					for _, name := range field.Names {
						if slices.Contains(userTextParams, name.Name) {
							params[name.Name] = true
						}
					}
				}
				probes, sinks := false, false
				f.Calls(fn.Body, func(call *ast.CallExpr, _, _ string) {
					name := laws.CalleeName(call)
					probes = probes || slices.Contains(userTextProbes, name) && len(call.Args) > 0 &&
						isUserText(call.Args[0], params) && !probesNewline(name, call)
					sinks = sinks || name == "Emit"
				})
				if probes && sinks {
					r.ReportAt(fn.Pos(), "%s probes user text and reaches a behavior sink", fn.Name.Name)
				}
			})
		}
	},
}}

func probesNewline(name string, call *ast.CallExpr) bool {
	if name != "Contains" || len(call.Args) < 2 {
		return false
	}
	value, _ := laws.StringLit(call.Args[1])
	return value == "\n"
}

func isUserText(expr ast.Expr, params map[string]bool) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return params[e.Name] || slices.Contains(userTextParams, e.Name)
	case *ast.CallExpr:
		switch laws.CalleeName(e) {
		case "SessionUserTask":
			return true
		case "TrimSpace", "ToLower", "ToUpper":
			return len(e.Args) > 0 && isUserText(e.Args[0], params)
		}
	case *ast.SelectorExpr:
		if id, ok := e.X.(*ast.Ident); ok {
			return params[id.Name] || slices.Contains(userTextParams, id.Name)
		}
	}
	return false
}
