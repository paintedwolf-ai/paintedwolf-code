package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/workflow/toolguard"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)


// maxLegSubjectRunes keeps a leg subject to one short checklist line.
const maxLegSubjectRunes = 60

// FanoutPlanToolResult is returned by fanout_plan.
type FanoutPlanToolResult struct {
	OK      bool   `json:"ok"`
	Legs    int    `json:"legs"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// RegisterFanoutPlanTool registers fanout_plan for coordinator sessions in plan phases.
func RegisterFanoutPlanTool(reg *tools.DefaultRegistry, runs *Fanout) error {
	if reg == nil || runs == nil {
		return fmt.Errorf("registry and run manager required")
	}
	return reg.Register("fanout_plan", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if !toolguard.IsCoordinatorAgent(tctx.Agent) {
			return "", fmt.Errorf("fanout_plan requires coordinator role")
		}
		plan, err := parseFanoutPlanArgs(args)
		if err != nil {
			var reject *tools.ToolReject
			if errors.As(err, &reject) {
				return "", err
			}
			return marshalFanoutPlanResult(FanoutPlanToolResult{Error: "invalid_plan", Message: err.Error()})
		}
		result := FanoutPlanToolResult{
			OK: true, Legs: len(plan.Legs),
			Message: fmt.Sprintf("fanout plan stamped (%d leg(s)); no workers were dispatched — call workflow_advance when ready to execute", len(plan.Legs)),
		}
		if _, ok, replayErr := runs.Journal.ReplayOperation(ctx, tctx.ToolCallID, "fanout_plan", args); replayErr != nil || ok {
			if replayErr != nil {
				return "", replayErr
			}
			return marshalFanoutPlanResult(result)
		}
		active, err := runs.Runs.ActiveBySession(ctx, tctx.SessionID)
		if err != nil {
			return "", err
		}
		if active == nil {
			return marshalFanoutPlanResult(FanoutPlanToolResult{Error: "no_active_run", Message: "start a workflow run before calling fanout_plan"})
		}
		manifest, err := runs.Resolver.ForRun(ctx, active)
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
		roster := runs.Policy.RosterFor(active, manifest)
		if err := validateFanoutPlan(plan, roster, runstate.ReviewLoopFanoutExcludedAgents(manifest), maxLegs, def.Fanout.RequireThreatModel); err != nil {
			return marshalFanoutPlanResult(FanoutPlanToolResult{Error: "invalid_plan", Message: err.Error()})
		}
		if def.Fanout.RequireTaskCharter {
			for _, leg := range plan.Legs {
				if len(leg.DoneWhen) == 0 {
					return "", &tools.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"tool": "fanout_plan", "field": "legs.done_when", "reason": "completion_criteria_required"}}
				}
			}
		}
		if err := validateFanoutLegBudgets(plan, runs.workerToolBudget(tctx.ActiveRootPath())); err != nil {
			return marshalFanoutPlanResult(FanoutPlanToolResult{Error: "invalid_plan", Message: err.Error()})
		}
		plan.MaxAttempts = max(1, def.Fanout.MaxAttempts)
		plan.Phase = def.Next
		unlockVars := runs.Vars.Lock(active.ID)
		defer unlockVars()
		vars, err := runs.Runs.GetScaffoldVars(ctx, active.ID)
		if err != nil {
			return "", err
		}
		vars = runstate.StampFanoutPlan(vars, plan)
		vars = runstate.SetGateSatisfied(vars, "fanout_planned", true)
		commandCtx := runstate.WithCommandOperation(runstate.WithExpectedRevision(ctx, active.Revision), tctx.ToolCallID)
		if err := runs.Journal.Commit(commandCtx, active, "fanout_plan", args, vars, nil, "", runstate.WorkerMutation{}, nil); err != nil {
			return "", err
		}
		seedFanoutProgress(ctx, runs.Progress, tctx.SessionID, active.ID, plan)
		return marshalFanoutPlanResult(result)
	})
}

// seedFanoutProgress gives a coordinator with no checklist one pending row per
// planned leg, so the progress gate opens on the plan it just stamped.
func seedFanoutProgress(ctx context.Context, store progress.RunScopedStore, sessionID, runID string, plan runstate.FanoutPlan) {
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

func parseFanoutPlanArgs(args map[string]any) (runstate.FanoutPlan, error) {
	if args == nil {
		return runstate.FanoutPlan{}, fmt.Errorf("legs required")
	}
	rawLegs, ok := args["legs"].([]any)
	if !ok || len(rawLegs) == 0 {
		return runstate.FanoutPlan{}, fmt.Errorf("legs must be a non-empty array")
	}
	legs := make([]runstate.FanoutPlanLeg, 0, len(rawLegs))
	for i, item := range rawLegs {
		m, ok := item.(map[string]any)
		if !ok {
			return runstate.FanoutPlan{}, fmt.Errorf("legs[%d] must be an object", i)
		}
		leg := runstate.FanoutPlanLeg{
			ID:        fmt.Sprintf("leg-%d", i+1),
			AgentType: strings.TrimSpace(toolguard.StringArg(m["agent_type"])),
			Subject:   strings.Join(strings.Fields(toolguard.StringArg(m["subject"])), " "),
			Prompt:    strings.TrimSpace(toolguard.StringArg(m["prompt"])),
			DoneWhen:  fanoutDoneWhen(m["done_when"]),
		}
		if leg.AgentType == "" || leg.Subject == "" || leg.Prompt == "" {
			return runstate.FanoutPlan{}, fmt.Errorf("legs[%d] requires agent_type, subject, and prompt", i)
		}
		if len(leg.DoneWhen) > 0 && spawn.TaskCharterRunes(api.WorkerTaskCharter{Goal: leg.Prompt, DoneWhen: leg.DoneWhen}) > spawn.MaxTaskCharterRunes {
			return runstate.FanoutPlan{}, &tools.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"tool": "fanout_plan", "field": fmt.Sprintf("legs[%d]", i), "reason": "brief_too_long", "max_runes": spawn.MaxTaskCharterRunes}}
		}
		if n := len([]rune(leg.Subject)); n > maxLegSubjectRunes {
			return runstate.FanoutPlan{}, fmt.Errorf("legs[%d].subject is %d characters; name the area in at most %d", i, n, maxLegSubjectRunes)
		}
		if scopeRaw, ok := m["scope"].(map[string]any); ok && len(scopeRaw) > 0 {
			scope, err := taskScopeFromMap(scopeRaw)
			if err != nil {
				return runstate.FanoutPlan{}, fmt.Errorf("legs[%d].scope: %w", i, err)
			}
			leg.Scope = &scope
		}
		loops, err := workeradmission.ParseTaskMaxToolLoopsFromArgs(m)
		if err != nil {
			return runstate.FanoutPlan{}, fmt.Errorf("legs[%d].%w", i, err)
		}
		leg.MaxToolLoops = loops
		legs = append(legs, leg)
	}
	return runstate.FanoutPlan{
		Legs:        legs,
		Rationale:   strings.TrimSpace(toolguard.StringArg(args["rationale"])),
		ThreatModel: strings.TrimSpace(toolguard.StringArg(args["threat_model"])),
	}, nil
}

func taskScopeFromMap(m map[string]any) (api.TaskScope, error) {
	mode := strings.TrimSpace(toolguard.StringArg(m["mode"]))
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

func validateFanoutPlan(plan runstate.FanoutPlan, allowedAgents, excludedAgents []string, maxLegs int, requireThreatModel bool) error {
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
func validateFanoutLegBudgets(plan runstate.FanoutPlan, budget spawn.WorkerToolBudget) error {
	for i, leg := range plan.Legs {
		if workeradmission.ValidateTaskMaxToolLoopsCode(leg.MaxToolLoops, budget) != "" {
			return fmt.Errorf("legs[%d].max_tool_loops is %d; set it between %d and %d, or omit it for the default %d",
				i, leg.MaxToolLoops, budget.Min, budget.Max, budget.Default)
		}
	}
	return nil
}

// workerToolBudget resolves the ceiling bounds for workers under projectDir.
func (m *Fanout) workerToolBudget(projectDir string) spawn.WorkerToolBudget {
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

func marshalFanoutPlanResult(result FanoutPlanToolResult) (string, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func fanoutDoneWhen(raw any) []string {
	var out []string
	switch values := raw.(type) {
	case []any:
		for _, v := range values {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case []string:
		out = append(out, values...)
	}
	return out
}
