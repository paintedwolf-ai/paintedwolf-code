package contract

// AST scan for inline approval explanation prose on the gate/card path.
// Mirrors guidance_emission_ast.go discipline for the approval explanation registry.

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

var inlineApprovalProsePatterns = []string{
	"Can't be undone",
	"remote repository",
	"Work freely in the project",
	"Always allow running",
	"Always let the agent edit",
}

var inlineApprovalProseAllowlist = map[string]bool{
	"internal/hitl/manager_impl.go": true, // default Title: Approve %s until 8.2 attaches registry copy
}

type approvalProseScan struct {
	InlineApprovalProse []string
}

func scanInlineApprovalProse(lycaonRoot string) (*approvalProseScan, error) {
	corp, err := contractcheck.LoadGoASTCorpus(lycaonRoot)
	if err != nil {
		return nil, err
	}
	out := &approvalProseScan{}
	prefixes := []string{
		"internal/hitl/",
		"internal/tools/",
		"internal/governance/",
		"internal/api/",
	}
	for _, gf := range corp.Files() {
		if gf.IsTest {
			continue
		}
		if inlineApprovalProseAllowlist[gf.Rel] {
			continue
		}
		allowed := false
		for _, p := range prefixes {
			if strings.HasPrefix(gf.Rel, p) {
				allowed = true
				break
			}
		}
		if !allowed {
			continue
		}
		ast.Inspect(gf.AST, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			val := contractcheck.AstStringLit(lit)
			for _, pattern := range inlineApprovalProsePatterns {
				if strings.Contains(val, pattern) {
					pos := corp.Fset.Position(lit.Pos())
					out.InlineApprovalProse = append(out.InlineApprovalProse, gf.Rel+":"+strconv.Itoa(pos.Line)+": "+pattern)
				}
			}
			return true
		})
	}
	return out, nil
}
