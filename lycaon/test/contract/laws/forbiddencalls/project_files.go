package forbiddencalls

import (
	"go/ast"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
)

var projectFiles = []laws.Rule{{
	// AGENTS.md changes go through the reviewed file-change path.
	Name:    "no-hardcoded-agents-md-writes",
	Anchors: []laws.Anchor{laws.Package("internal/fseffect")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal") {
			f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
				if laws.CalleeName(call) != "WriteFile" || len(call.Args) == 0 {
					return
				}
				if target, ok := laws.StringLit(call.Args[0]); ok && strings.Contains(target, "AGENTS.md") {
					r.ReportAt(call.Pos(), "writes AGENTS.md outside the reviewed file-change path")
				}
			})
		}
	},
}, {
	// Source recording receives its history, command and checkpoint ports
	// explicitly; it never recovers them from the recorder by assertion.
	Name: "source-recording-port-recovers-no-peer-capabilities",
	Anchors: []laws.Anchor{laws.AnchorFunc("a SourceLedger field", func(c *laws.Corpus) error {
		return anyProductionSelector(c, "internal", "SourceLedger")
	})},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal") {
			f.Funcs(func(fn *ast.FuncDecl) {
				for _, assertion := range sourceRecorderAssertions(fn.Body) {
					r.ReportAt(assertion.Pos(), "type-asserts the source recorder; take the port explicitly")
				}
			})
		}
	},
}}

func sourceRecorderAssertions(body *ast.BlockStmt) []*ast.TypeAssertExpr {
	aliases := map[string]bool{}
	var isRecorder func(ast.Expr) bool
	isRecorder = func(expression ast.Expr) bool {
		switch expression := expression.(type) {
		case *ast.Ident:
			return expression.Name == "sourceLedger" || aliases[expression.Name]
		case *ast.SelectorExpr:
			return expression.Sel.Name == "SourceLedger" || expression.Sel.Name == "sourceLedger"
		case *ast.ParenExpr:
			return isRecorder(expression.X)
		}
		return false
	}
	for changed := true; changed; {
		changed = false
		ast.Inspect(body, func(node ast.Node) bool {
			assignment, ok := node.(*ast.AssignStmt)
			if !ok || len(assignment.Lhs) != len(assignment.Rhs) {
				return true
			}
			for index, expression := range assignment.Rhs {
				name, ok := assignment.Lhs[index].(*ast.Ident)
				if ok && isRecorder(expression) && !aliases[name.Name] {
					aliases[name.Name] = true
					changed = true
				}
			}
			return true
		})
	}
	var assertions []*ast.TypeAssertExpr
	ast.Inspect(body, func(node ast.Node) bool {
		assertion, ok := node.(*ast.TypeAssertExpr)
		if ok && isRecorder(assertion.X) {
			assertions = append(assertions, assertion)
		}
		return true
	})
	return assertions
}
