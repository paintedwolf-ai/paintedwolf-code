package forbiddencalls

import (
	"go/ast"
	"go/token"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
)

var workflowVersion = []laws.Rule{{
	// Only the definition catalog, the anchor matcher and run creation branch
	// on a workflow version; elsewhere it is passed through, stored or shown.
	Name:    "workflow-version-is-not-branched-on",
	Anchors: []laws.Anchor{laws.Package("internal/workflow/definition"), laws.Package("internal/coordinator/anchor")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal") {
			if isVersionUseExempt(f) {
				continue
			}
			ast.Inspect(f.AST, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.BinaryExpr:
					if comparesWorkflowVersion(node) {
						r.ReportAt(node.Pos(), "compares a workflow version")
					}
				case *ast.SwitchStmt:
					if node.Tag != nil && isWorkflowVersionExpr(node.Tag) {
						r.ReportAt(node.Pos(), "switches on a workflow version")
					}
				case *ast.CallExpr:
					if isProhibitedVersionFunc(f, node.Fun) {
						for _, arg := range node.Args {
							if isWorkflowVersionExpr(arg) {
								r.ReportAt(node.Pos(), "parses or matches a workflow version")
							}
						}
					}
				}
				return true
			})
		}
	},
}}

func isVersionUseExempt(f *laws.GoFile) bool {
	if laws.Under(f, "internal/workflow/definition", "internal/coordinator/anchor", "internal/workflow/run_start.go", "internal/workflow/invoke_child.go") {
		return true
	}
	// Evaluation suites and timeline archives carry versions of their own.
	return strings.Contains("/"+f.Dir+"/", "/eval/") || strings.Contains("/"+f.Dir+"/", "/timelinearchive/")
}

func isProhibitedVersionFunc(f *laws.GoFile, fun ast.Expr) bool {
	pkg, name, ok := f.Qualified(fun)
	if !ok {
		return false
	}
	if pkg == "strings" {
		return contains([]string{"HasPrefix", "HasSuffix", "Contains", "EqualFold", "Split", "CutPrefix", "CutSuffix"}, name)
	}
	return strings.Contains(strings.ToLower(pkg), "semver")
}

// comparesWorkflowVersion reports an equality or ordering comparison on a
// workflow version. Emptiness checks (v == "" or v != "") are input validation.
func comparesWorkflowVersion(bin *ast.BinaryExpr) bool {
	switch bin.Op {
	case token.EQL, token.NEQ:
		if isEmptyStringLiteral(bin.X) || isEmptyStringLiteral(bin.Y) {
			return false
		}
	case token.LSS, token.GTR, token.LEQ, token.GEQ:
	default:
		return false
	}
	return isWorkflowVersionExpr(bin.X) || isWorkflowVersionExpr(bin.Y)
}

func isEmptyStringLiteral(expr ast.Expr) bool {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	return lit.Value == `""` || lit.Value == `''`
}

func isWorkflowVersionExpr(expr ast.Expr) bool {
	if expr == nil {
		return false
	}
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch sel.Sel.Name {
	case "WorkflowVersion":
		return true
	case "Version":
		// A Version field counts only on a manifest or workflow descriptor.
		if ident, ok := sel.X.(*ast.Ident); ok {
			lower := strings.ToLower(ident.Name)
			return strings.Contains(lower, "manifest") || lower == "m" || strings.Contains(lower, "wf")
		}
	}
	return false
}
