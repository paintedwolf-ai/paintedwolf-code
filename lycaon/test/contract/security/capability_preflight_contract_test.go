package contract

import (
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Tool-specific refusals cannot bypass capability authorization.
func TestCapabilityPreflightNeverBypassesAuthorizationByToolName(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, "lycaon", "internal")
	fset, files := contractcheck.ParseNonTestGoTree(t, dir)

	var findings []string
	for _, file := range files {
		// Capability preflight lives in the capability and socket grant files.
		path := fset.File(file.Pos()).Name()
		if base := filepath.Base(path); !strings.Contains(base, "capability") && !strings.HasPrefix(base, "socket_") {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if branch, ok := n.(*ast.IfStmt); ok && branch.Else == nil && rejectsCapabilityBranch(branch.Body) {
				return false
			}
			cmp, ok := n.(*ast.BinaryExpr)
			if !ok || (cmp.Op != token.EQL && cmp.Op != token.NEQ) {
				return true
			}
			ident, ok := cmp.X.(*ast.Ident)
			if !ok || ident.Name != "tool" {
				return true
			}
			lit, ok := cmp.Y.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			pos := fset.Position(cmp.Pos())
			findings = append(findings, fmt.Sprintf("%s:%d gates capability preflight on tool name %s",
				filepath.Base(path), pos.Line, lit.Value))
			return true
		})
	}
	if len(findings) > 0 {
		t.Fatalf("capability preflight must gate on Contract.Supports, not a tool name:\n  %s",
			strings.Join(findings, "\n  "))
	}
}

func rejectsCapabilityBranch(body *ast.BlockStmt) bool {
	if len(body.List) != 1 {
		return false
	}
	ret, ok := body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return false
	}
	call, ok := ret.Results[0].(*ast.CallExpr)
	if !ok || len(call.Args) != 5 {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "rejectBeforeInvoke" {
		return false
	}
	receiver, ok := selector.X.(*ast.Ident)
	if !ok || receiver.Name != "e" {
		return false
	}
	rejection, ok := call.Args[4].(*ast.UnaryExpr)
	if !ok || rejection.Op != token.AND {
		return false
	}
	literal, ok := rejection.X.(*ast.CompositeLit)
	if !ok {
		return false
	}
	kind, ok := literal.Type.(*ast.Ident)
	return ok && kind.Name == "ToolReject"
}
