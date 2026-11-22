package contract

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/lycaon/lycaon/internal/pongoplain"
)

type oarViolation struct {
	Source     string
	Expression string
	Action     string
	Reason     string
}

func (v oarViolation) String() string {
	return fmt.Sprintf("%s | expr=%q | action=%q: %s", v.Source, v.Expression, v.Action, v.Reason)
}

func parseOARConditionIdents(src string) (map[string]bool, error) {
	trimmed := strings.TrimSpace(src)
	if trimmed == "" {
		return nil, nil
	}
	refs, err := oarcore.ConditionReferences(trimmed)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(refs))
	for _, r := range refs {
		out[r] = true
	}
	return out, nil
}

func parseCopyTemplateExprIdents(src string) (map[string]bool, error) {
	trimmed := strings.TrimSpace(src)
	if trimmed == "" {
		return nil, nil
	}
	if refs, err := oarcore.ConditionReferences(trimmed); err == nil {
		out := make(map[string]bool, len(refs))
		for _, r := range refs {
			out[r] = true
		}
		return out, nil
	}
	expr, err := pongoplain.ParseExpression(trimmed)
	if err != nil {
		return nil, err
	}
	ids := expr.Identifiers()
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}
