package conditions

import (
	"encoding/json"
	"strings"
)

func planAwaitingApproval(ctx EvalContext, deps RegistryDeps) (bool, error) {
	_ = deps
	phase := strings.TrimSpace(strings.ToLower(ctx.Phase))
	if phase != "" && phase != "approve" {
		return false, nil
	}
	if !DotPathTruthy(ctx.Vars, "human_approval.active") {
		return false, nil
	}
	if DotPathTruthy(ctx.Vars, "human_approval.issued") && DotPathTruthy(ctx.Vars, "human_approval.ready") {
		hash, _ := dotPathString(ctx.Vars, "human_approval.blueprint_hash")
		if strings.TrimSpace(hash) != "" {
			return false, nil
		}
	}
	if phase == "" {
		return false, nil
	}
	return true, nil
}

const (
	planTasksMarkerStart = "<!-- lycaon:tasks"
	planTasksMarkerEnd   = "-->"
)

type planTask struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

func extractPlanTasks(content string) []planTask {
	start := strings.Index(content, planTasksMarkerStart)
	if start < 0 {
		return nil
	}
	rest := content[start+len(planTasksMarkerStart):]
	end := strings.Index(rest, planTasksMarkerEnd)
	if end < 0 {
		return nil
	}
	raw := strings.TrimSpace(rest[:end])
	var tasks []planTask
	if err := json.Unmarshal([]byte(raw), &tasks); err != nil {
		return nil
	}
	return tasks
}

// ShippedPlanDomainIDs returns registered plan predicates.
func ShippedPlanDomainIDs() []string {
	return append([]string(nil), shippedPlanDomainIDs...)
}

var shippedPlanDomainIDs = []string{
	"plan_stub_valid",
	"plan_awaiting_approval",
	"research_required",
	"research_satisfied",
	"review_loop_active",
	"scope_missing",
	"breaking_missing",
	"review_depth_missing",
	"implement_workflow_ready",
}

// RegisterPlanDomain registers plan-workflow-only vocabulary evaluators.
func RegisterPlanDomain(reg *ConditionRegistry, deps RegistryDeps) error {
	if reg == nil {
		return nil
	}
	entries := []struct {
		name string
		fn   ConditionFunc
	}{
		{"plan_stub_valid", func(ctx EvalContext) (bool, error) {
			text, ok := planContent(ctx, deps)
			if !ok {
				return false, nil
			}
			return PlanStubValidText(text), nil
		}},
		{"plan_awaiting_approval", func(ctx EvalContext) (bool, error) {
			return planAwaitingApproval(ctx, deps)
		}},
		{"research_required", func(ctx EvalContext) (bool, error) {
			text, ok := planContent(ctx, deps)
			if !ok {
				return false, nil
			}
			return ResearchRequiredText(text), nil
		}},
		{"research_satisfied", func(ctx EvalContext) (bool, error) {
			return researchSatisfied(ctx, deps)
		}},
		{"review_loop_active", func(ctx EvalContext) (bool, error) {
			return ctx.ReviewLoopActive, nil
		}},
		{"scope_missing", func(ctx EvalContext) (bool, error) {
			return planSectionMissingEval(ctx, deps, "## Scope", "## Plan implementation scope"), nil
		}},
		{"breaking_missing", func(ctx EvalContext) (bool, error) {
			return planSectionMissingEval(ctx, deps, "## Breaking changes", "## Breaking", "## Plan breaking changes"), nil
		}},
		{"review_depth_missing", func(ctx EvalContext) (bool, error) {
			return planSectionMissingEval(ctx, deps, "## Plan review depth"), nil
		}},
		{"implement_workflow_ready", func(ctx EvalContext) (bool, error) {
			return implementWorkflowReady(ctx, deps)
		}},
	}
	for _, e := range entries {
		if reg.Has(e.name) {
			continue
		}
		if err := reg.Register(e.name, e.fn); err != nil {
			return err
		}
	}
	return nil
}

func planSectionMissingEval(ctx EvalContext, deps RegistryDeps, headings ...string) bool {
	text, ok := planContent(ctx, deps)
	if !ok {
		return true
	}
	return PlanSectionMissing(text, headings...)
}

func planContent(ctx EvalContext, deps RegistryDeps) (string, bool) {
	if strings.TrimSpace(ctx.PlanContent) != "" {
		return ctx.PlanContent, true
	}
	path := strings.TrimSpace(ctx.BlueprintPath)
	if path != "" && deps.BlueprintContent != nil && strings.TrimSpace(ctx.ProjectDir) != "" {
		c, err := deps.BlueprintContent(ctx.Ctx, ctx.ProjectDir, path)
		if err == nil && strings.TrimSpace(c) != "" {
			return c, true
		}
	}
	if deps.BlueprintGet != nil && path != "" {
		p, err := deps.BlueprintGet(ctx.Ctx, path)
		if err == nil && p != nil && strings.TrimSpace(p.Content) != "" {
			return p.Content, true
		}
	}
	return "", false
}

func validatePlanTasks(content string) bool {
	tasks := extractPlanTasks(content)
	if len(tasks) == 0 {
		return false
	}
	for _, task := range tasks {
		if strings.TrimSpace(task.ID) == "" || strings.TrimSpace(task.Title) == "" {
			return false
		}
	}
	return true
}

func planTechnicallyReady(ctx EvalContext, deps RegistryDeps) (bool, error) {
	if BoolVar(ctx.Vars, "decomposition_validated") {
		return true, nil
	}
	text, ok := planContent(ctx, deps)
	if !ok {
		return false, nil
	}
	if !PlanStubValidText(text) {
		return false, nil
	}
	return validatePlanTasks(text), nil
}

func implementWorkflowReady(ctx EvalContext, deps RegistryDeps) (bool, error) {
	if !gatePassedInVars(ctx.Vars, "human_approval") {
		return false, nil
	}
	return planTechnicallyReady(ctx, deps)
}

func gatePassedInVars(vars map[string]any, gate string) bool {
	gates, _ := vars["gates"].(map[string]any)
	if gates == nil {
		return false
	}
	ok, _ := gates[gate].(bool)
	return ok
}

func researchSatisfied(ctx EvalContext, deps RegistryDeps) (bool, error) {
	if phaseSkipped(ctx.Vars, "research") {
		return true, nil
	}
	if BoolVar(ctx.Vars, "research_satisfied") {
		return true, nil
	}
	if gatePassedInVars(ctx.Vars, "research_satisfied") {
		return true, nil
	}
	text, ok := planContent(ctx, deps)
	// A missing blueprint declares no research requirement.
	if !ok || !ResearchRequiredText(text) {
		return true, nil
	}
	return false, nil
}
