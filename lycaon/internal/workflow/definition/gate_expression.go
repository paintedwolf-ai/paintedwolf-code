package definition

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/boolexpr"
	"github.com/lycaon/lycaon/internal/vocabulary"
)

// CompletionLeaves unwraps completion forms before resolving their predicates.
func CompletionLeaves(expr string) ([]string, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" || expr == vocabulary.CompleteWhenGatesSatisfied {
		return nil, nil
	}
	if leaf, ok := strings.CutPrefix(expr, vocabulary.CompleteWhenGateSatisfied); ok {
		leaf = strings.TrimSpace(leaf)
		if leaf == "" {
			return nil, fmt.Errorf("gate_satisfied requires a gate leaf")
		}
		return []string{leaf}, nil
	}
	return expressionLeaves(expr)
}

func expressionLeaves(expr string) ([]string, error) {
	node, err := boolexpr.Parse(expr)
	if err != nil {
		return nil, err
	}
	return boolexpr.CollectIdents(node), nil
}

type PredicateField struct {
	Path   string
	Leaves []string
	Err    error
}

// PredicateFields follows the parent and child execution contracts of a phase.
func (p PhaseDef) PredicateFields() []PredicateField {
	var out []PredicateField
	for _, completion := range []struct{ path, value string }{{"complete_when", p.CompleteWhen}, {"child_complete_when", p.ChildCompleteWhen}} {
		if strings.TrimSpace(completion.value) != "" {
			leaves, err := CompletionLeaves(completion.value)
			out = append(out, PredicateField{completion.path, leaves, err})
		}
	}
	if strings.TrimSpace(p.EntryWhen) != "" {
		leaves, err := expressionLeaves(p.EntryWhen)
		out = append(out, PredicateField{"entry_when", leaves, err})
	}
	if p.HumanApproval != nil && p.HumanApproval.Readiness != "" {
		leaves, err := expressionLeaves(p.HumanApproval.Readiness)
		out = append(out, PredicateField{"human_approval.readiness", leaves, err})
	}
	for _, edge := range p.Transitions {
		if edge.When != "" {
			leaves, err := expressionLeaves(edge.When)
			out = append(out, PredicateField{"transitions[" + edge.ID + "].when", leaves, err})
		}
	}
	for _, group := range []struct {
		path  string
		gates []string
	}{{"gates", p.Gates}, {"child_gates", p.ChildGates}} {
		for i, gate := range group.gates {
			out = append(out, PredicateField{Path: fmt.Sprintf("%s[%d]", group.path, i), Leaves: []string{strings.TrimSpace(gate)}})
		}
	}
	return out
}
