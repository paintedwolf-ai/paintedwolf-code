package contract

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestWorkflowVersionUsageGuardrail enforces that production code outside the 3 approved
// catalog, matcher, and run-creation packages never branches on or parses workflow versions.
// Pass-through assignments, persistence, logging, and UI displays are permitted.
func TestWorkflowVersionUsageGuardrail(t *testing.T) {
	t.Parallel()
	repoRoot := contractcheck.RepoRoot(t)
	internalDir := filepath.Join(repoRoot, "lycaon", "internal")

	corpus, err := contractcheck.LoadGoASTCorpus(internalDir)
	contractcheck.FailErr(t, "LoadGoASTCorpus", err)

	for _, gf := range corpus.Production() {
		relPath := filepath.ToSlash(gf.Rel)
		if isVersionUseExempt(relPath) {
			continue
		}

		ast.Inspect(gf.AST, func(n ast.Node) bool {
			if n == nil {
				return true
			}

			// 1. Binary comparison operators (==, !=, <, >, <=, >=)
			if bin, ok := n.(*ast.BinaryExpr); ok {
				if comparesWorkflowVersion(bin) {
					pos := corpus.Fset.Position(bin.Pos())
					t.Errorf("%s:%d: illegal binary comparison on workflow version; branching on workflow version is prohibited", relPath, pos.Line)
				}
				return true
			}

			// 2. Switch statement switching on workflow version
			if sw, ok := n.(*ast.SwitchStmt); ok {
				if sw.Tag != nil && isWorkflowVersionExpr(sw.Tag) {
					pos := corpus.Fset.Position(sw.Pos())
					t.Errorf("%s:%d: illegal switch on workflow version; branching on workflow version is prohibited", relPath, pos.Line)
				}
				return true
			}

			// 3. Passing workflow version to strings or semver parsing functions
			if call, ok := n.(*ast.CallExpr); ok {
				if isProhibitedVersionFunc(call.Fun) {
					for _, arg := range call.Args {
						if isWorkflowVersionExpr(arg) {
							pos := corpus.Fset.Position(call.Pos())
							t.Errorf("%s:%d: illegal version parsing/matching call with workflow version argument; branching on workflow version is prohibited", relPath, pos.Line)
						}
					}
				}
				return true
			}

			return true
		})
	}
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

func isVersionUseExempt(relPath string) bool {
	clean := filepath.ToSlash(relPath)
	// definition catalog loader
	if strings.HasPrefix(clean, "workflow/definition/") || strings.Contains(clean, "/workflow/definition/") {
		return true
	}
	// anchor matcher
	if strings.HasPrefix(clean, "coordinator/anchor/") || strings.Contains(clean, "/coordinator/anchor/") {
		return true
	}
	// run creation
	if strings.HasSuffix(clean, "workflow/run_start.go") || strings.HasSuffix(clean, "workflow/invoke_child.go") {
		return true
	}
	// eval / test suites
	if strings.HasPrefix(clean, "eval/") || strings.Contains(clean, "/eval/") {
		return true
	}
	// unrelated archive packages
	if strings.HasPrefix(clean, "timelinearchive/") || strings.Contains(clean, "/timelinearchive/") {
		return true
	}
	return false
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

func isProhibitedVersionFunc(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	pkg := pkgIdent.Name
	fn := sel.Sel.Name

	if pkg == "strings" {
		switch fn {
		case "HasPrefix", "HasSuffix", "Contains", "EqualFold", "Split", "CutPrefix", "CutSuffix":
			return true
		}
	}
	if pkg == "semver" || strings.Contains(strings.ToLower(pkg), "semver") {
		return true
	}
	return false
}
