package validation

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/spawn"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflowdiag"
	"github.com/lycaon/lycaon/pkg/api"
)

// Tool IDs required by host leaveability rules.
const (
	ToolWorkflowAdvance = "workflow_advance"
	ToolAskUser         = "ask_user"
	ToolWait            = "wait"
	ToolSubmitVerdict   = "submit_verdict"
)

var blueprintMutationTools = []string{"write", "edit", "replace_lines"}

// ValidateBlueprintWritePhases checks that authoring phases can write.
func ValidateBlueprintWritePhases(m workflowdef.Manifest) []api.ComposeValidationError {
	writers := make([]workflowdef.PhaseDef, 0, len(m.PhaseDefs))
	for _, phase := range m.PhaseDefs {
		if phase.BlueprintWrite {
			writers = append(writers, phase)
		}
	}
	if m.Blueprint == nil {
		var out []api.ComposeValidationError
		for _, phase := range writers {
			out = append(out, workflowdiag.EmitDefault(
				workflowdiag.MustCode("blueprint_writer_without_blueprint"),
				fmt.Sprintf("phases[%s].blueprint_write", phase.ID),
				map[string]any{"workflow": m.ID, "phase": phase.ID},
			))
		}
		return out
	}
	if blueprintConsumed(m) && len(writers) == 0 {
		return []api.ComposeValidationError{workflowdiag.EmitDefault(
			workflowdiag.MustCode("blueprint_writer_required"),
			"phases",
			map[string]any{"workflow": m.ID},
		)}
	}
	plans, err := surface.CompileToolPlans(1)
	if err != nil {
		return nil
	}
	var out []api.ComposeValidationError
	for _, phase := range writers {
		binding, bindErr := workflowdef.ResolveSurfaceBinding(m, phase.ID, m.ID, "")
		if bindErr != nil {
			continue
		}
		surfaceID := strings.TrimSpace(binding.CoordinatorSurface)
		if surfaceID == "" {
			out = append(out, workflowdiag.EmitDefault(
				workflowdiag.MustCode("blueprint_writer_missing_surface"),
				fmt.Sprintf("phases[%s].coordinator_surface", phase.ID),
				map[string]any{"workflow": m.ID, "phase": phase.ID},
			))
			continue
		}
		toolPlan, ok := plans[surfaceID]
		if !ok {
			continue
		}
		tools := toolPlan.AddressableNames()
		writable := false
		for _, tool := range blueprintMutationTools {
			if containsToolID(tools, tool) {
				writable = true
				break
			}
		}
		if writable {
			continue
		}
		out = append(out, workflowdiag.EmitDefault(
			workflowdiag.MustCode("blueprint_writer_surface_read_only"),
			fmt.Sprintf("phases[%s].coordinator_surface", phase.ID),
			map[string]any{"workflow": m.ID, "phase": phase.ID, "surface": surfaceID},
		))
	}
	return out
}

func blueprintConsumed(m workflowdef.Manifest) bool {
	for _, phase := range m.PhaseDefs {
		if phase.HumanApproval != nil {
			return true
		}
		for _, conditionID := range collectPhaseConditionIDs(phase) {
			switch conditionID {
			case "blueprint_materialized", "options_selection_valid", "plan_stub_valid":
				return true
			}
		}
	}
	return false
}

// PhaseImpliesHITLTools derives human-input tools from phase state.
func PhaseImpliesHITLTools(p workflowdef.PhaseDef) bool {
	if p.OnEnter.RequestUserFeedback != nil {
		return true
	}
	if len(p.Intake) > 0 {
		return true
	}
	for _, id := range collectPhaseConditionIDs(p) {
		if strings.HasPrefix(id, "hitl_consulted:") {
			return true
		}
	}
	return false
}

// RequiredSurfaceToolsForPhase returns tools needed to leave the phase.
func RequiredSurfaceToolsForPhase(m workflowdef.Manifest, p workflowdef.PhaseDef) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(tool string) {
		tool = strings.TrimSpace(tool)
		if tool == "" {
			return
		}
		if _, ok := seen[tool]; ok {
			return
		}
		seen[tool] = struct{}{}
		out = append(out, tool)
	}
	if workflowdef.EffectiveAdvancePolicy(m, p) == workflowdef.AdvanceWhenGateMetCoordinator {
		add(ToolWorkflowAdvance)
	}
	if PhaseImpliesHITLTools(p) {
		add(ToolAskUser)
		add(ToolWait)
	}
	// Review loops leave through a recorded verdict.
	if p.ReviewLoop != nil {
		add(ToolSubmitVerdict)
	}
	return out
}

// TerminalStampValid reports whether a terminal phase uses the host stamp pair.
func TerminalStampValid(p workflowdef.PhaseDef) bool {
	if !p.Terminal {
		return true
	}
	return strings.TrimSpace(p.CompleteWhen) == "orchestration_complete"
}

// ValidateResolvedSurfaceTools checks the tools needed to leave a surface.
func ValidateResolvedSurfaceTools(m workflowdef.Manifest, p workflowdef.PhaseDef, surfaceID string, surfaceTools []string) []api.ComposeValidationError {
	surfaceID = strings.TrimSpace(surfaceID)
	if surfaceID == "" {
		return nil
	}
	var out []api.ComposeValidationError
	for _, tool := range RequiredSurfaceToolsForPhase(m, p) {
		if containsToolID(surfaceTools, tool) {
			continue
		}
		field := fmt.Sprintf("phases[%s]", p.ID)
		switch tool {
		case ToolWorkflowAdvance:
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("missing_tool_for_advance_policy"),
				field+".advance",
				map[string]any{"phase": p.ID, "tool": tool, "surface": surfaceID}))
		case ToolAskUser, ToolWait:
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("missing_hitl_tools"),
				field+".gates",
				map[string]any{"phase": p.ID, "tool": tool, "surface": surfaceID}))
		case ToolSubmitVerdict:
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("missing_review_verdict_tool"),
				field+".review_loop",
				map[string]any{"phase": p.ID, "tool": tool, "surface": surfaceID}))
		}
	}
	out = append(out, ValidateReviewSpawnRoster(m, p, surfaceID)...)
	out = append(out, ValidateGatedCloseoutAsk(p, surfaceID, surfaceTools)...)
	return out
}

// ValidateGatedCloseoutAsk requires a human-input path for gated closeout.
func ValidateGatedCloseoutAsk(p workflowdef.PhaseDef, surfaceID string, surfaceTools []string) []api.ComposeValidationError {
	surfaceID = strings.TrimSpace(surfaceID)
	if !p.Closeout.Gated() || surfaceID == "" || containsToolID(surfaceTools, ToolAskUser) {
		return nil
	}
	return []api.ComposeValidationError{workflowdiag.EmitDefault(
		workflowdiag.MustCode("missing_gated_closeout_ask"),
		fmt.Sprintf("phases[%s].controls.closeout", p.ID),
		map[string]any{"phase": p.ID, "surface": surfaceID},
	)}
}

// PhaseRequiresCoordinatorSurface identifies phases with coordinator output.
func PhaseRequiresCoordinatorSurface(p workflowdef.PhaseDef) bool {
	if p.OnEnter.PromptCoordinator {
		return true
	}
	return workflowdef.PhaseHasGate(p, "topology_report_delivered")
}

// ValidateRequiredCoordinatorSurface checks coordinator surface bindings.
func ValidateRequiredCoordinatorSurface(m workflowdef.Manifest, p workflowdef.PhaseDef) []api.ComposeValidationError {
	if !PhaseRequiresCoordinatorSurface(p) {
		return nil
	}
	if strings.TrimSpace(p.CoordinatorSurface) != "" {
		return nil
	}
	// surface_profile bindings leave CoordinatorSurface empty until resolve.
	if binding, err := workflowdef.ResolveSurfaceBinding(m, p.ID, m.ID, ""); err == nil && strings.TrimSpace(binding.CoordinatorSurface) != "" {
		return nil
	}
	return []api.ComposeValidationError{workflowdiag.EmitDefault(
		workflowdiag.MustCode("missing_coordinator_surface"),
		fmt.Sprintf("phases[%s].coordinator_surface", p.ID),
		map[string]any{"phase": p.ID},
	)}
}

// ValidateReportPhaseSurfaceExit requires report exits for report phases.
func ValidateReportPhaseSurfaceExit(m workflowdef.Manifest, p workflowdef.PhaseDef) []api.ComposeValidationError {
	if !workflowdef.PhaseHasGate(p, "topology_report_delivered") {
		return nil
	}
	binding, err := workflowdef.ResolveSurfaceBinding(m, p.ID, m.ID, "")
	if err != nil {
		return nil
	}
	surfID := strings.TrimSpace(binding.CoordinatorSurface)
	if surfID == "" {
		// missing_coordinator_surface represents absence.
		return nil
	}
	exit, err := surface.SurfaceExit(surfID)
	if err != nil {
		// Unknown surfaces and catalog load failures have their own diagnostics.
		return nil
	}
	if exit == surface.ExitReport {
		return nil
	}
	return []api.ComposeValidationError{workflowdiag.EmitDefault(
		workflowdiag.MustCode("report_phase_exit_mismatch"),
		fmt.Sprintf("phases[%s].coordinator_surface", p.ID),
		map[string]any{"phase": p.ID, "surface": surfID, "exit": string(exit)},
	)}
}

// ValidateReportControlHasReportPhase requires a reachable report phase.
func ValidateReportControlHasReportPhase(m workflowdef.Manifest) []api.ComposeValidationError {
	if !m.ReportEnabled() {
		return nil
	}
	for _, p := range m.PhaseDefs {
		if workflowdef.PhaseHasGate(p, "topology_report_delivered") {
			return nil
		}
	}
	return []api.ComposeValidationError{workflowdiag.EmitDefault(
		workflowdiag.MustCode("report_control_without_report_phase"),
		"controls.report.enabled",
		map[string]any{"workflow": m.ID},
	)}
}

// ValidateReviewAgentsDeclared checks review agents against the workflow roster.
func ValidateReviewAgentsDeclared(m workflowdef.Manifest, p workflowdef.PhaseDef) []api.ComposeValidationError {
	if p.ReviewLoop == nil {
		return nil
	}
	allowed := map[string]struct{}{}
	for _, id := range m.AllowedAgents {
		id = strings.TrimSpace(id)
		if id != "" {
			allowed[id] = struct{}{}
		}
	}
	var out []api.ComposeValidationError
	required := map[string]struct{}{}
	for _, agent := range p.ReviewLoop.RequiredAgents {
		agent = strings.TrimSpace(agent)
		if agent == "" {
			continue
		}
		required[agent] = struct{}{}
		if _, ok := allowed[agent]; ok {
			continue
		}
		out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("review_agent_not_declared"),
			fmt.Sprintf("phases[%s].review_loop.required_agents", p.ID),
			map[string]any{"phase": p.ID, "agent": agent}))
	}
	for _, agent := range p.ReviewLoop.IfSpawnable {
		agent = strings.TrimSpace(agent)
		if agent == "" {
			continue
		}
		if _, ok := required[agent]; ok {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("review_agent_role_conflict"),
				fmt.Sprintf("phases[%s].review_loop.if_spawnable", p.ID),
				map[string]any{"phase": p.ID, "agent": agent}))
		}
		if _, ok := allowed[agent]; ok {
			continue
		}
		out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("review_agent_not_declared"),
			fmt.Sprintf("phases[%s].review_loop.if_spawnable", p.ID),
			map[string]any{"phase": p.ID, "agent": agent}))
	}
	return out
}

// dispatchableOnSurface applies the surface's compile-time lane gate.
func dispatchableOnSurface(declared []string, surfaceID string) map[string]struct{} {
	lane, gated := spawn.LaneForSurface(surfaceID)
	present := make(map[string]struct{}, len(declared))
	for _, id := range declared {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if gated && !agentdef.ServesLane(id, lane) {
			continue
		}
		present[id] = struct{}{}
	}
	return present
}

// ValidatePhaseSpawnRoster checks the topology worker against the surface lane.
func ValidatePhaseSpawnRoster(m workflowdef.Manifest, p workflowdef.PhaseDef, surfaceID string, topologyProfile string) []api.ComposeValidationError {
	surfaceID = strings.TrimSpace(surfaceID)
	topologyProfile = strings.TrimSpace(topologyProfile)
	if surfaceID == "" || topologyProfile == "" || strings.TrimSpace(p.BindTopologyStage) == "" {
		return nil
	}
	lane, gated := spawn.LaneForSurface(surfaceID)
	if !gated || agentdef.ServesLane(topologyProfile, lane) {
		return nil
	}
	return []api.ComposeValidationError{workflowdiag.EmitDefault(
		workflowdiag.MustCode("phase_surface_cannot_dispatch"),
		fmt.Sprintf("phases[%s].coordinator_surface", p.ID),
		map[string]any{
			"phase": p.ID, "agent": topologyProfile, "surface": surfaceID, "lane": lane,
			"declared_lanes": strings.Join(agentdef.LanesFor(topologyProfile), ", "),
		})}
}

// ValidateReviewSpawnRoster checks review agents against the surface lane.
func ValidateReviewSpawnRoster(m workflowdef.Manifest, p workflowdef.PhaseDef, surfaceID string) []api.ComposeValidationError {
	surfaceID = strings.TrimSpace(surfaceID)
	if p.ReviewLoop == nil || surfaceID == "" {
		return nil
	}
	present := dispatchableOnSurface(m.AllowedAgents, surfaceID)
	var out []api.ComposeValidationError
	emitMissing := func(field, agent string) {
		if _, ok := present[agent]; ok {
			return
		}
		out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("missing_review_spawn_agent"),
			fmt.Sprintf("phases[%s].review_loop.%s", p.ID, field),
			map[string]any{"phase": p.ID, "agent": agent, "surface": surfaceID}))
	}
	for _, agent := range p.ReviewLoop.RequiredAgents {
		agent = strings.TrimSpace(agent)
		if agent == "" {
			continue
		}
		emitMissing("required_agents", agent)
	}
	for _, agent := range p.ReviewLoop.IfSpawnable {
		agent = strings.TrimSpace(agent)
		if agent == "" {
			continue
		}
		emitMissing("if_spawnable", agent)
	}
	return out
}

// ValidateTerminalStamp emits a diagnostic when terminal stamp is wrong.
func ValidateTerminalStamp(p workflowdef.PhaseDef) []api.ComposeValidationError {
	if TerminalStampValid(p) {
		return nil
	}
	return []api.ComposeValidationError{workflowdiag.EmitDefault(
		workflowdiag.MustCode("terminal_requires_orchestration_complete"),
		fmt.Sprintf("phases[%s].complete_when", p.ID),
		map[string]any{"phase": p.ID},
	)}
}

func containsToolID(tools []string, want string) bool {
	for _, t := range tools {
		if t == want {
			return true
		}
	}
	return false
}
