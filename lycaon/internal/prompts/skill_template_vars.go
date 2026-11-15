package prompts

import (
	"github.com/lycaon/lycaon/internal/spawn"
)

// SkillVars builds the template variables one skill read renders with from the
// reader's facts plus spawn policy and sizing.
func SkillVars(reader Reader, budget spawn.WorkerToolBudget) map[string]any {
	resolved := defaultDelegationBudget(budget)
	vars := spawn.PolicyTemplateVars(resolved)
	for k, v := range reader.Facts() {
		vars[k] = v
	}
	vars["worker_tool_budget_default"] = resolved.Default
	vars["worker_tool_budget_min"] = resolved.Min
	vars["worker_tool_budget_max"] = resolved.Max
	spawn.RefreshSizingHintVars(vars)
	return vars
}

func defaultDelegationBudget(budget spawn.WorkerToolBudget) spawn.WorkerToolBudget {
	def := spawn.DefaultWorkerToolBudget()
	if budget.Default == 0 {
		budget.Default = def.Default
	}
	if budget.Min == 0 {
		budget.Min = def.Min
	}
	if budget.Max == 0 {
		budget.Max = def.Max
	}
	return budget
}
