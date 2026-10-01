package contract

import (
	"go/ast"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestMCPErrorBridgeNoProseScanAST fails when the MCP CallTool error bridge
// classifies free-text with substring/Contains heuristics instead of reading a
// declared code field.
func TestMCPErrorBridgeNoProseScanAST(t *testing.T) {
	t.Parallel()
	lycaonRoot := filepath.Join(contractcheck.RepoRoot(t), "lycaon")
	corp, err := contractcheck.LoadGoASTCorpus(lycaonRoot)
	contractcheck.FailErr(t, "load AST corpus", err)

	bridgeFiles := map[string]struct{}{
		"internal/mcp/call_envelope.go": {},
	}
	var violations []string
	for _, gf := range corp.Files() {
		if gf.IsTest {
			continue
		}
		rel := filepath.ToSlash(gf.Rel)
		if _, ok := bridgeFiles[rel]; !ok {
			continue
		}
		ast.Inspect(gf.AST, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				return true
			}
			name := sel.Sel.Name
			if name != "Contains" && name != "ContainsAny" && name != "EqualFold" {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok || id.Name != "strings" {
				return true
			}
			pos := corp.Fset.Position(call.Pos())
			violations = append(violations, rel+":"+strconv.Itoa(pos.Line)+": strings."+name+" in MCP error bridge")
			return true
		})
	}
	if len(violations) > 0 {
		t.Fatalf("MCP error bridge must not prose-scan; found:\n  %s", strings.Join(violations, "\n  "))
	}
}
