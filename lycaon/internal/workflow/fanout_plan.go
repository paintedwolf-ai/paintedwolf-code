package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// FanoutPlanLeg is one coordinator-planned parallel worker leg.
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

// maxLegSubjectRunes keeps a leg subject to one short checklist line.
const maxLegSubjectRunes = 60

// FanoutPlan describes one phase's planned worker legs.
type FanoutPlan struct {
	Phase       string          `json:"phase"`
	MaxAttempts int             `json:"max_attempts"`
	Legs        []FanoutPlanLeg `json:"legs"`
	Rationale   string          `json:"rationale,omitempty"`
	ThreatModel string          `json:"threat_model,omitempty"`
}

// FanoutPlanToolResult is returned by fanout_plan.
type FanoutPlanToolResult struct {
	OK      bool   `json:"ok"`
	Legs    int    `json:"legs"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// RegisterFanoutPlanTool registers fanout_plan for coordinator sessions in plan phases.
func RegisterFanoutPlanTool(reg *tools.DefaultRegistry, runs *RunManager) error {
	if reg == nil || runs == nil {
		return fmt.Errorf("registry and run manager required")
	}
	return reg.Register("fanout_plan", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if !isCoordinatorAgent(tctx.Identity.Agent) {
			return "", fmt.Errorf("fanout_plan requires coordinator role")
		}
		plan, err := parseFanoutPlanArgs(args)
		if err != nil {
			return marshalFanoutPlanResult(FanoutPlanToolResult{Error: "invalid_plan", Message: err.Error()})
		}
		result := FanoutPlanToolResult{
			OK: true, Legs: len(plan.Legs),
			Message: fmt.Sprintf("fanout plan stamped (%d leg(s)); no workers were dispatched — call workflow_advance when ready to execute", len(plan.Legs)),
		}
		if _, ok, replayErr := runs.replayCommandOperation(ctx, tctx.Identity.ToolCallID, "fanout_plan", args); replayErr != nil || ok {
			if replayErr != nil {
				return "", replayErr
			}
			return marshalFanoutPlanResult(result)
		}
		active, err := runs.Store.ActiveBySession(ctx, tctx.Identity.SessionID)
		if err != nil {
			return "", err
		}
		if active == nil {
			return marshalFanoutPlanResult(FanoutPlanToolResult{Error: "no_active_run", Message: "start a workflow run before calling fanout_plan"})
		}
		manifest, err := runs.manifestForRun(ctx, active)
		if err != nil {
			return "", err
		}
		def, ok := manifest.PhaseByID(active.CurrentPhase)
		if !ok || !workflowdef.PhaseHasGate(def, "fanout_planned") {
			return marshalFanoutPlanResult(FanoutPlanToolResult{
				Error:   "fanout_plan_not_available",
				Message: fmt.Sprintf("fanout_plan is only available on phases with fanout_planned gate (current phase %q)", active.CurrentPhase),
			})
		}
		maxLegs := FanoutPlanMaxLegsForPhase(manifest, def)
		roster := runs.RosterFor(active, manifest)
		if err := validateFanoutPlan(plan, roster, ReviewLoopFanoutExcludedAgents(manifest), maxLegs, def.Fanout.RequireThreatModel); err != nil {
			return marshalFanoutPlanResult(FanoutPlanToolResult{Error: "invalid_plan", Message: err.Error()})
		}
		if err := validateFanoutLegBudgets(plan, runs.workerToolBudget(tctx.ActiveRootPath())); err != nil {
			return marshalFanoutPlanResult(FanoutPlanToolResult{Error: "invalid_plan", Message: err.Error()})
		}
		plan.MaxAttempts = max(1, def.Fanout.MaxAttempts)
		plan.Phase = def.Next
		unlockVars := runs.lockRunVars(active.ID)
		defer unlockVars()
		vars, err := runs.Store.GetScaffoldVars(ctx, active.ID)
		if err != nil {
			return "", err
		}
		vars = stampFanoutPlan(vars, plan)
		vars = SetGateSatisfied(vars, "fanout_planned", true)
		commandCtx := withWorkflowCommandOperation(WithExpectedRevision(ctx, active.Revision), tctx.Identity.ToolCallID)
		if err := runs.commitCommand(commandCtx, active, "fanout_plan", args, vars, nil, "", workflowWorkerMutation{}, nil); err != nil {
			return "", err
		}
		seedFanoutProgress(ctx, runs.Progress, tctx.Identity.SessionID, active.ID, plan)
		return marshalFanoutPlanResult(result)
	})
}

// seedFanoutProgress gives a coordinator with no checklist one pending row per
// planned leg, so the progress gate opens on the plan it just stamped.
func seedFanoutProgress(ctx context.Context, store progress.RunScopedStore, sessionID, runID string, plan FanoutPlan) {
	if store == nil {
		return
	}
	switch store.BoundRunID(sessionID) {
	case "":
		progress.AdoptActiveRun(ctx, store, sessionID, runID, "")
	case runID:
	default:
		return
	}
	prev := store.Get(ctx, sessionID)
	if !progress.ProgressMissing(prev) {
		return
	}
	labels := make([]string, 0, len(plan.Legs))
	for _, leg := range plan.Legs {
		labels = append(labels, leg.ID+" "+leg.Subject)
	}
	if err := store.Set(sessionID, progress.SeedChecklist(prev, labels)); err != nil {
		slog.WarnContext(ctx, "seed fan-out progress", "session", sessionID, "error", err)
		return
	}
	progress.NotifyWriteObservers(ctx, progress.WriteEvent{SessionID: sessionID, Prev: prev})
}

func parseFanoutPlanArgs(args map[string]any) (FanoutPlan, error) {
	if args == nil {
		return FanoutPlan{}, fmt.Errorf("legs required")
	}
	rawLegs, ok := args["legs"].([]any)
	if !ok || len(rawLegs) == 0 {
		return FanoutPlan{}, fmt.Errorf("legs must be a non-empty array")
	}
	legs := make([]FanoutPlanLeg, 0, len(rawLegs))
	for i, item := range rawLegs {
		m, ok := item.(map[string]any)
		if !ok {
			return FanoutPlan{}, fmt.Errorf("legs[%d] must be an object", i)
		}
		leg := FanoutPlanLeg{
			ID:        fmt.Sprintf("leg-%d", i+1),
			AgentType: strings.TrimSpace(stringArg(m["agent_type"])),
			Subject:   strings.Join(strings.Fields(stringArg(m["subject"])), " "),
			Prompt:    strings.TrimSpace(stringArg(m["prompt"])),
		}
		if leg.AgentType == "" || leg.Subject == "" || leg.Prompt == "" {
			return FanoutPlan{}, fmt.Errorf("legs[%d] requires agent_type, subject, and prompt", i)
		}
		if n := len([]rune(leg.Subject)); n > maxLegSubjectRunes {
			return FanoutPlan{}, fmt.Errorf("legs[%d].subject is %d characters; name the area in at most %d", i, n, maxLegSubjectRunes)
		}
		if scopeRaw, ok := m["scope"].(map[string]any); ok && len(scopeRaw) > 0 {
			scope, err := taskScopeFromMap(scopeRaw)
			if err != nil {
				return FanoutPlan{}, fmt.Errorf("legs[%d].scope: %w", i, err)
			}
			leg.Scope = &scope
		}
		loops, err := session.ParseTaskMaxToolLoopsFromArgs(m)
		if err != nil {
			return FanoutPlan{}, fmt.Errorf("legs[%d].%w", i, err)
		}
		leg.MaxToolLoops = loops
		legs = append(legs, leg)
	}
	return FanoutPlan{
		Legs:        legs,
		Rationale:   strings.TrimSpace(stringArg(args["rationale"])),
		ThreatModel: strings.TrimSpace(stringArg(args["threat_model"])),
	}, nil
}

func taskScopeFromMap(m map[string]any) (api.TaskScope, error) {
	mode := strings.TrimSpace(stringArg(m["mode"]))
	if mode == "" {
		mode = string(api.TaskScopeModeRead)
	}
	scope := api.TaskScope{Mode: api.TaskScopeMode(mode)}
	if raw, ok := m["paths"].([]any); ok {
		for _, p := range raw {
			if s, ok := p.(string); ok && strings.TrimSpace(s) != "" {
				scope.Paths = append(scope.Paths, strings.TrimSpace(s))
			}
		}
	}
	return scope, nil
}

// FanoutPlanMaxLegsForPhase returns the max leg count for fanout_plan on a plan phase.
func FanoutPlanMaxLegsForPhase(m workflowdef.Manifest, planPhase workflowdef.PhaseDef) int {
	const defaultMax = 5
	nextID := strings.TrimSpace(planPhase.Next)
	if nextID == "" {
		return defaultMax
	}
	exec, ok := m.PhaseByID(nextID)
	if !ok || exec.ParallelTask == nil || exec.ParallelTask.MaxWorkers <= 0 {
		return defaultMax
	}
	return exec.ParallelTask.MaxWorkers
}

func validateFanoutPlan(plan FanoutPlan, allowedAgents, excludedAgents []string, maxLegs int, requireThreatModel bool) error {
	if maxLegs <= 0 {
		maxLegs = 5
	}
	if len(plan.Legs) == 0 {
		return fmt.Errorf("plan must include at least one leg")
	}
	if len(plan.Legs) > maxLegs {
		return fmt.Errorf("plan has %d legs; maximum is %d", len(plan.Legs), maxLegs)
	}
	if requireThreatModel && strings.TrimSpace(plan.ThreatModel) == "" {
		return fmt.Errorf("threat_model required: state the target system's kind, who can reach it, and how authentication works before planning legs")
	}
	allow := agentAllowSet(allowedAgents)
	excluded := agentAllowSet(excludedAgents)
	for i, leg := range plan.Legs {
		if leg.AgentType == "coordinator" {
			return fmt.Errorf("legs[%d]: coordinator cannot be a worker leg", i)
		}
		if _, ok := excluded[leg.AgentType]; ok {
			return fmt.Errorf("legs[%d]: agent_type %q is a review_loop reviewer — plan survey legs only; a later review phase delegates that agent against claims",
				i, leg.AgentType)
		}
		if _, ok := allow[leg.AgentType]; !ok {
			return fmt.Errorf("legs[%d]: agent_type %q is not in workflow allowed_agents (%s)",
				i, leg.AgentType, formatWorkerAgentAllowlist(allowedAgents))
		}
		if leg.Scope != nil && leg.Scope.Mode == api.TaskScopeModeWrite {
			return fmt.Errorf("legs[%d]: explore fanout legs must use read scope", i)
		}
	}
	return nil
}

// validateFanoutLegBudgets keeps each planned ceiling inside the host range.
func validateFanoutLegBudgets(plan FanoutPlan, budget spawn.WorkerToolBudget) error {
	for i, leg := range plan.Legs {
		if session.ValidateTaskMaxToolLoopsCode(leg.MaxToolLoops, budget) != "" {
			return fmt.Errorf("legs[%d].max_tool_loops is %d; set it between %d and %d, or omit it for the default %d",
				i, leg.MaxToolLoops, budget.Min, budget.Max, budget.Default)
		}
	}
	return nil
}

// workerToolBudget resolves the ceiling bounds for workers under projectDir.
func (m *RunManager) workerToolBudget(projectDir string) spawn.WorkerToolBudget {
	if m != nil && m.WorkerToolBudget != nil {
		return m.WorkerToolBudget(projectDir)
	}
	return spawn.DefaultWorkerToolBudget()
}

// formatWorkerAgentAllowlist includes valid worker ids in rejection text.
func formatWorkerAgentAllowlist(ids []string) string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || id == "coordinator" {
			continue
		}
		out = append(out, id)
	}
	if len(out) == 0 {
		return "no worker agents allowlisted"
	}
	return "allowed: " + strings.Join(out, ", ")
}

func agentAllowSet(ids []string) map[string]struct{} {
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" {
			out[id] = struct{}{}
		}
	}
	return out
}

func stampFanoutPlan(vars map[string]any, plan FanoutPlan) map[string]any {
	vars = cloneVars(vars)
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
	plans = cloneVars(plans)
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

// FanoutPlanForPhase returns a stamped plan only on the phase that dispatches it.
func FanoutPlanForPhase(vars map[string]any, phase workflowdef.PhaseDef) (FanoutPlan, bool) {
	if !workflowdef.PhaseHasGate(phase, "worker_cycle_ready") {
		return FanoutPlan{}, false
	}
	return fanoutPlanForExecution(vars, phase.ID)
}

func fanoutPlanForExecution(vars map[string]any, phase string) (FanoutPlan, bool) {
	plans, _ := vars["fanout_plans"].(map[string]any)
	raw, ok := plans[phase]
	if !ok {
		return FanoutPlan{}, false
	}
	return decodeFanoutPlan(raw)
}

// FormatFanoutPlan renders a stamped plan.
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

func marshalFanoutPlanResult(result FanoutPlanToolResult) (string, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
