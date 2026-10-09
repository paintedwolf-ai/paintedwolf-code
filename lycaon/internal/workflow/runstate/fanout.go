package runstate

import (
	"encoding/json"
	"fmt"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

type FanoutPlanLeg struct {
	ID        string `json:"id"`
	AgentType string `json:"agent_type"`
	// Subject names the area the leg covers for a reader who never saw the
	// plan, such as "Desktop app".
	Subject string         `json:"subject"`
	Prompt  string         `json:"prompt"`
	Scope   *api.TaskScope `json:"scope,omitempty"`
	// MaxToolLoops is the leg's planned ceiling; zero selects the host default.
	MaxToolLoops int `json:"max_tool_loops,omitempty"`
}

type FanoutPlan struct {
	Phase       string          `json:"phase"`
	MaxAttempts int             `json:"max_attempts"`
	Legs        []FanoutPlanLeg `json:"legs"`
	Rationale   string          `json:"rationale,omitempty"`
	ThreatModel string          `json:"threat_model,omitempty"`
}

func StampFanoutPlan(vars map[string]any, plan FanoutPlan) map[string]any {
	vars = CloneVars(vars)
	raw, err := json.Marshal(plan)
	if err != nil {
		return vars
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return vars
	}
	delete(vars, "fanout_coverage")
	delete(vars, "fanout_settled")
	plans, _ := vars["fanout_plans"].(map[string]any)
	plans = CloneVars(plans)
	plans[plan.Phase] = decoded
	vars["fanout_plans"] = plans
	return vars
}

func decodeFanoutPlan(raw any) (FanoutPlan, bool) {
	b, err := json.Marshal(raw)
	if err != nil {
		return FanoutPlan{}, false
	}
	var plan FanoutPlan
	if err := json.Unmarshal(b, &plan); err != nil {
		return FanoutPlan{}, false
	}
	if len(plan.Legs) == 0 {
		return FanoutPlan{}, false
	}
	return plan, true
}

func FanoutPlanForPhase(vars map[string]any, phase workflowdef.PhaseDef) (FanoutPlan, bool) {
	if !workflowdef.PhaseHasGate(phase, "worker_cycle_ready") {
		return FanoutPlan{}, false
	}
	return FanoutPlanForExecution(vars, phase.ID)
}

func FanoutPlanForExecution(vars map[string]any, phase string) (FanoutPlan, bool) {
	plans, _ := vars["fanout_plans"].(map[string]any)
	raw, ok := plans[phase]
	if !ok {
		return FanoutPlan{}, false
	}
	return decodeFanoutPlan(raw)
}

func FormatFanoutPlan(plan FanoutPlan) string {
	var b strings.Builder
	if tm := strings.TrimSpace(plan.ThreatModel); tm != "" {
		b.WriteString("Threat model: ")
		b.WriteString(tm)
		b.WriteString("\n\n")
	}
	for i, leg := range plan.Legs {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%d. %s: **%s** [workflow_work_id=%s] — %s", i+1, leg.Subject, leg.AgentType, leg.ID, leg.Prompt)
		if leg.Scope != nil && len(leg.Scope.Paths) > 0 {
			fmt.Fprintf(&b, " (focus: %s)", strings.Join(leg.Scope.Paths, ", "))
		}
		if leg.MaxToolLoops > 0 {
			fmt.Fprintf(&b, " (ceiling: %d tool rounds)", leg.MaxToolLoops)
		}
	}
	if r := strings.TrimSpace(plan.Rationale); r != "" {
		b.WriteString("\n\nRationale: ")
		b.WriteString(r)
	}
	fmt.Fprintf(&b, "\n\nAttempt allowance per leg: %d", max(1, plan.MaxAttempts))
	return strings.TrimSpace(b.String())
}
