package rules

import "github.com/lycaon/lycaon/internal/conditions"

// StubValidator checks plan stub sections from run-scoped plan content.
type StubValidator struct {
	Content string
}

// Valid reports whether the plan content carries every section plan_stub_valid requires.
func (v StubValidator) Valid() bool {
	return conditions.PlanStubValidText(v.Content)
}

func stubFromEval(eval EvalContext) StubValidator {
	return StubValidator{Content: eval.PlanContent}
}
