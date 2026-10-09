package definition

import (
	"strings"

	"github.com/lycaon/lycaon/internal/boolexpr"
)

func DecomposeGateExpression(expr string) []string {
	expr = strings.TrimSpace(expr)
	if expr == "" || expr == CompleteWhenGatesSatisfied {
		return nil
	}
	if strings.HasPrefix(expr, CompleteWhenGateSatisfied) {
		leaf := strings.TrimSpace(strings.TrimPrefix(expr, CompleteWhenGateSatisfied))
		if leaf != "" {
			return []string{leaf}
		}
		return nil
	}
	if NeedsCompoundCompleteWhen(expr) {
		node, err := boolexpr.Parse(expr)
		if err != nil {
			return []string{expr}
		}
		return boolexpr.CollectIdents(node)
	}
	if IsKnownCompleteWhen(expr) || IsKnownGateLeaf(expr) {
		return []string{expr}
	}
	return nil
}
