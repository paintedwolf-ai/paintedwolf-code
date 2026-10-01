package conditions

import (
	"context"
	"github.com/lycaon/lycaon/internal/evidence"
	"strings"

	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

// DelegationReader loads delegation state for core predicates.
type DelegationReader interface {
	DelegationBySessionID(sessionID string) (string, bool)
	Get(ctx context.Context, delegationID string) (*api.Delegation, error)
	ListLegs(ctx context.Context, delegationID string) ([]api.Leg, error)
}

// EvidenceReader loads anchored evidence for gate predicates.
type EvidenceReader interface {
	LatestEvidence(ctx context.Context, projectDir, delegationID, taskID string, gateType evidence.GateType, scope inspector.EvidenceScope) (*evidence.Record, error)
}

// SourceSnapshotReader resolves the immutable source identity used by scan gates.
type SourceSnapshotReader interface {
	SourceSnapshotID(ctx context.Context, projectDir string) (string, error)
}

// CoreDeps supplies host services for reusable core vocabulary evaluators.
type CoreDeps struct {
	DelegationStore         DelegationReader
	Evidence                EvidenceReader
	DelegationCloseout      func(ctx context.Context, sessionID string) (bool, error)
	SourceVerifyPassed      func(ctx context.Context, sessionID string) (bool, error)
	DeliveryReported        func(ctx context.Context, sessionID, runID, phase string) (bool, error)
	ScanLedger              ScanLedger
	SourceSnapshots         SourceSnapshotReader
	ScanProactiveCategories []api.ScanCategory
	SecurityScannersEnabled func() bool
	GroundingBlocked        func(ctx context.Context, sessionID string) (bool, error)
	DoomLoopExceeded        func(ctx context.Context, sessionID string) (bool, error)
	ApprovalDenied          func(ctx context.Context, sessionID string) (bool, error)
}

// RegisterCoreConditions registers reusable core workflow vocabulary evaluators.
func RegisterCoreConditions(reg *ConditionRegistry, deps CoreDeps) error {
	if reg == nil {
		return nil
	}
	if err := registerCoreWorkflowRun(reg); err != nil {
		return err
	}
	if err := registerCoreUserInteraction(reg); err != nil {
		return err
	}
	if err := registerCorePosture(reg); err != nil {
		return err
	}
	if err := registerCoreTools(reg); err != nil {
		return err
	}
	if err := registerCoreSpawnGuards(reg, deps); err != nil {
		return err
	}
	if err := registerCoreDelegation(reg, deps); err != nil {
		return err
	}
	if err := registerCoreEvidence(reg, deps); err != nil {
		return err
	}
	if err := registerCoreHostGuards(reg, deps); err != nil {
		return err
	}
	if err := registerCoreCompleteWhen(reg, deps); err != nil {
		return err
	}
	if err := registerCoreRuleFacts(reg); err != nil {
		return err
	}
	return registerCoreVars(reg)
}

func registerCoreWorkflowRun(reg *ConditionRegistry) error {
	if err := reg.Register("choice_transition_required", func(EvalContext) (bool, error) {
		return false, nil
	}); err != nil {
		return err
	}
	if err := reg.Register("workflow_active", func(ec EvalContext) (bool, error) {
		return ec.RunStatus == api.WorkflowRunStatusRunning, nil
	}); err != nil {
		return err
	}
	if err := reg.Register("workflow_paused", func(ec EvalContext) (bool, error) {
		return ec.RunStatus == api.WorkflowRunStatusPaused, nil
	}); err != nil {
		return err
	}
	if err := reg.Register("workflow_not_runnable", func(ec EvalContext) (bool, error) {
		switch ec.RunStatus {
		case api.WorkflowRunStatusRunning:
			return false, nil
		case "":
			return false, nil
		default:
			return true, nil
		}
	}); err != nil {
		return err
	}
	if err := reg.RegisterParameterized("phase_is:", func(ec EvalContext) (bool, error) {
		want := suffixAfter(ec.ConditionID, "phase_is:")
		return want != "" && ec.Phase == want, nil
	}); err != nil {
		return err
	}
	return reg.RegisterParameterized("phase_skipped:", func(ec EvalContext) (bool, error) {
		id := suffixAfter(ec.ConditionID, "phase_skipped:")
		return id != "" && phaseSkipped(ec.Vars, id), nil
	})
}

func registerCoreUserInteraction(reg *ConditionRegistry) error {
	prefixes := []struct {
		prefix string
		fn     func(EvalContext) (bool, error)
	}{
		{"user_feedback_pending:", func(ec EvalContext) (bool, error) {
			id := suffixAfter(ec.ConditionID, "user_feedback_pending:")
			return id != "" && phaseBucketPending(ec.Vars, "user_feedback", id), nil
		}},
		{"user_feedback_received:", func(ec EvalContext) (bool, error) {
			id := suffixAfter(ec.ConditionID, "user_feedback_received:")
			return id != "" && phaseBucketReceived(ec.Vars, "user_feedback", id), nil
		}},
		{"user_decision_pending:", func(ec EvalContext) (bool, error) {
			id := suffixAfter(ec.ConditionID, "user_decision_pending:")
			return id != "" && phaseBucketPending(ec.Vars, "user_decision", id), nil
		}},
		{"user_decision_received:", func(ec EvalContext) (bool, error) {
			id := suffixAfter(ec.ConditionID, "user_decision_received:")
			return id != "" && phaseBucketReceived(ec.Vars, "user_decision", id), nil
		}},
		{"user_decision:", func(ec EvalContext) (bool, error) {
			rest := suffixAfter(ec.ConditionID, "user_decision:")
			parts := strings.SplitN(rest, ",", 2)
			if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
				return false, nil
			}
			return phaseBucketChoice(ec.Vars, strings.TrimSpace(parts[0]), parts[1]), nil
		}},
		{"hitl_consulted:", func(ec EvalContext) (bool, error) {
			id := suffixAfter(ec.ConditionID, "hitl_consulted:")
			return id != "" && BoolVar(ec.Vars, "hitl_consulted:"+id), nil
		}},
	}
	for _, p := range prefixes {
		if err := reg.RegisterParameterized(p.prefix, p.fn); err != nil {
			return err
		}
	}
	return nil
}

func registerCorePosture(reg *ConditionRegistry) error {
	return reg.RegisterParameterized("posture_is:", func(ec EvalContext) (bool, error) {
		want := suffixAfter(ec.ConditionID, "posture_is:")
		return want != "" && string(ec.SessionPosture) == want, nil
	})
}

func registerCoreTools(reg *ConditionRegistry) error {
	entries := []struct {
		name string
		fn   ConditionFunc
	}{
		{"high_risk_tool", func(ec EvalContext) (bool, error) {
			return isHighRiskTool(ec.ToolName, ec.ToolArgs), nil
		}},
		{"tool_is_state", func(ec EvalContext) (bool, error) {
			return strings.HasPrefix(ec.ToolName, "state_"), nil
		}},
		{"tool_is_delegation", func(ec EvalContext) (bool, error) {
			return strings.HasPrefix(ec.ToolName, "delegate_"), nil
		}},
		{"tool_is_handoff", func(ec EvalContext) (bool, error) {
			return strings.HasPrefix(ec.ToolName, "handoff_"), nil
		}},
		{"tool_is_task", func(ec EvalContext) (bool, error) {
			return ec.ToolName == "task", nil
		}},
		// The compiled axis avoids a tools → guidance → conditions import cycle.
		{"tool_is_write", func(ec EvalContext) (bool, error) {
			return toolcontract.MutatesContent(ec.ToolName), nil
		}},
		{"tool_is_command", func(ec EvalContext) (bool, error) {
			return ec.ToolName == "command", nil
		}},
		{"tool_is_scan_pack", func(ec EvalContext) (bool, error) {
			return ec.ToolName == "scan_pack", nil
		}},
	}
	for _, e := range entries {
		if err := reg.Register(e.name, e.fn); err != nil {
			return err
		}
	}
	return reg.RegisterParameterized("agent_is:", func(ec EvalContext) (bool, error) {
		want := suffixAfter(ec.ConditionID, "agent_is:")
		if want == "" || ec.ToolName != "task" {
			return false, nil
		}
		agent, _ := ec.ToolArgs["agent_type"].(string)
		return strings.EqualFold(strings.TrimSpace(agent), want), nil
	})
}

func registerCoreSpawnGuards(reg *ConditionRegistry, deps CoreDeps) error {
	if err := reg.Register("disallowed_agent", func(ec EvalContext) (bool, error) {
		return matchDisallowedAgent(ec), nil
	}); err != nil {
		return err
	}
	return reg.Register("doom_loop_exceeded", func(ec EvalContext) (bool, error) {
		if deps.DoomLoopExceeded == nil || ec.SessionID == "" {
			return false, nil
		}
		return deps.DoomLoopExceeded(ec.Ctx, ec.SessionID)
	})
}

func registerCoreDelegation(reg *ConditionRegistry, deps CoreDeps) error {
	entries := []struct {
		name string
		fn   func(EvalContext, CoreDeps) (bool, error)
	}{
		{"delegation_active", func(ec EvalContext, d CoreDeps) (bool, error) {
			if d.DelegationStore == nil || ec.SessionID == "" {
				return false, nil
			}
			_, ok := d.DelegationStore.DelegationBySessionID(ec.SessionID)
			return ok, nil
		}},
		{"delegation_phase_setup", delegationPhaseFn(api.DelegationPhaseSetup)},
		{"delegation_phase_worker", delegationPhaseFn(api.DelegationPhaseWorker)},
		{"delegation_phase_done", delegationPhaseFn(api.DelegationPhaseDone)},
		{"leg_pending", func(ec EvalContext, d CoreDeps) (bool, error) {
			dep, err := loadDelegation(d, ec)
			if err != nil || dep == nil {
				return false, err
			}
			for _, leg := range dep.Legs {
				if leg.Status == api.LegStatusPending || leg.Status == api.LegStatusRunning {
					return true, nil
				}
			}
			return false, nil
		}},
		{"all_legs_complete", func(ec EvalContext, d CoreDeps) (bool, error) {
			dep, err := loadDelegation(d, ec)
			if err != nil || dep == nil || len(dep.Legs) == 0 {
				return false, err
			}
			for _, leg := range dep.Legs {
				if leg.Status != api.LegStatusComplete {
					return false, nil
				}
			}
			return true, nil
		}},
		{"leg_failed", func(ec EvalContext, d CoreDeps) (bool, error) {
			dep, err := loadDelegation(d, ec)
			if err != nil || dep == nil {
				return false, err
			}
			for _, leg := range dep.Legs {
				if leg.Status == api.LegStatusFailed {
					return true, nil
				}
			}
			return false, nil
		}},
		{"worker_jobs_pending", func(ec EvalContext, d CoreDeps) (bool, error) {
			dep, err := loadDelegation(d, ec)
			if err != nil || dep == nil {
				return false, err
			}
			for _, leg := range dep.Legs {
				if leg.Status == api.LegStatusRunning && strings.TrimSpace(leg.WorkerID) != "" {
					return true, nil
				}
			}
			return false, nil
		}},
		{"grounding_blocked", func(ec EvalContext, d CoreDeps) (bool, error) {
			if d.GroundingBlocked == nil || ec.SessionID == "" {
				return false, nil
			}
			return d.GroundingBlocked(ec.Ctx, ec.SessionID)
		}},
		{"completion_criteria_met", func(ec EvalContext, d CoreDeps) (bool, error) {
			dep, err := loadDelegation(d, ec)
			if err != nil || dep == nil {
				return false, err
			}
			for _, leg := range dep.Legs {
				if leg.Status == api.LegStatusComplete && len(leg.CompletionCriteria) > 0 {
					return true, nil
				}
			}
			return false, nil
		}},
	}
	for _, e := range entries {
		name := e.name
		evalFn := e.fn
		if err := reg.Register(name, func(ec EvalContext) (bool, error) {
			return evalFn(ec, deps)
		}); err != nil {
			return err
		}
	}
	return nil
}

func delegationPhaseFn(want api.DelegationPhase) func(EvalContext, CoreDeps) (bool, error) {
	return func(ec EvalContext, d CoreDeps) (bool, error) {
		dep, err := loadDelegation(d, ec)
		if err != nil || dep == nil {
			return false, err
		}
		return dep.Phase == want, nil
	}
}

func loadDelegation(d CoreDeps, ec EvalContext) (*api.Delegation, error) {
	if d.DelegationStore == nil || ec.SessionID == "" {
		return nil, nil
	}
	depID, ok := d.DelegationStore.DelegationBySessionID(ec.SessionID)
	if !ok {
		return nil, nil
	}
	return d.DelegationStore.Get(ec.Ctx, depID)
}

func registerCoreEvidence(reg *ConditionRegistry, deps CoreDeps) error {
	if err := reg.RegisterParameterized("evidence_passed:", func(ec EvalContext) (bool, error) {
		if gateSatisfiedInVars(ec.Vars, ec.ConditionID) {
			return true, nil
		}
		evType := suffixAfter(ec.ConditionID, "evidence_passed:")
		return evidenceSatisfied(deps, ec, evidence.GateType(evType), true)
	}); err != nil {
		return err
	}
	if err := reg.RegisterParameterized("evidence_missing:", func(ec EvalContext) (bool, error) {
		evType := suffixAfter(ec.ConditionID, "evidence_missing:")
		ok, err := evidenceSatisfied(deps, ec, evidence.GateType(evType), true)
		return !ok && err == nil, err
	}); err != nil {
		return err
	}
	if err := reg.Register("file_modified_since_base", func(ec EvalContext) (bool, error) {
		dep, err := loadDelegation(deps, ec)
		if err != nil || dep == nil {
			return false, err
		}
		return strings.TrimSpace(dep.BaseHeadSHA) != "", nil
	}); err != nil {
		return err
	}
	return reg.RegisterParameterized("gate_passed:", func(ec EvalContext) (bool, error) {
		gate := suffixAfter(ec.ConditionID, "gate_passed:")
		if gate == "closeout" {
			ok, err := closeoutGatesPassed(deps, ec)
			return ok, err
		}
		return gateSatisfiedInVars(ec.Vars, gate), nil
	})
}

func evidenceSatisfied(deps CoreDeps, ec EvalContext, evType evidence.GateType, requireAnchored bool) (bool, error) {
	// Integrated checks describe current source; leg-scoped evidence describes that leg.
	if (evType == evidence.GateTypeVerify || evType == evidence.GateTypeTest) &&
		strings.TrimSpace(ec.EvidenceLegID) == "" && strings.TrimSpace(ec.EvidenceWorkspaceID) == "" &&
		deps.SourceVerifyPassed != nil {
		return deps.SourceVerifyPassed(ec.Ctx, ec.SessionID)
	}
	if deps.Evidence == nil || evType == "" {
		return false, nil
	}
	if evType == evidence.GateTypeSecurity {
		return securityEvidenceSatisfied(deps, ec, requireAnchored)
	}
	dep, err := loadDelegation(deps, ec)
	if err != nil || dep == nil {
		return false, err
	}
	taskIDs := delegationTaskIDs(dep)
	if legScope := strings.TrimSpace(ec.EvidenceLegID); legScope != "" {
		taskIDs = []string{legScope}
	}
	if len(taskIDs) == 0 {
		return false, nil
	}
	scope := inspector.EvidenceScope{
		LegID:       strings.TrimSpace(ec.EvidenceLegID),
		WorkspaceID: strings.TrimSpace(ec.EvidenceWorkspaceID),
	}
	for _, taskID := range taskIDs {
		rec, err := deps.Evidence.LatestEvidence(ec.Ctx, ec.ProjectDir, dep.ID, taskID, evType, scope)
		if err != nil {
			return false, err
		}
		if rec == nil {
			return false, nil
		}
		if requireAnchored {
			ok, _ := inspector.EvidenceAnchored(*rec)
			if !ok {
				return false, nil
			}
		}
	}
	return true, nil
}

func delegationTaskIDs(dep *api.Delegation) []string {
	var out []string
	for _, leg := range dep.Legs {
		if leg.Status == api.LegStatusComplete || leg.Status == api.LegStatusRunning {
			out = append(out, leg.ID)
		}
	}
	return out
}

func gateSatisfiedInVars(vars map[string]any, gate string) bool {
	gates := nestedMap(vars, "gates")
	if gates == nil {
		return false
	}
	return BoolVar(gates, gate)
}

func registerCoreHostGuards(reg *ConditionRegistry, deps CoreDeps) error {
	if err := reg.Register("approval_denied", func(ec EvalContext) (bool, error) {
		if deps.ApprovalDenied == nil || ec.SessionID == "" {
			return false, nil
		}
		return deps.ApprovalDenied(ec.Ctx, ec.SessionID)
	}); err != nil {
		return err
	}
	return reg.Register("iteration_cap_near", func(ec EvalContext) (bool, error) {
		return BoolVar(ec.Vars, "iteration_cap_near"), nil
	})
}

func registerCoreCompleteWhen(reg *ConditionRegistry, deps CoreDeps) error {
	entries := []struct {
		name string
		fn   ConditionFunc
	}{
		{"topology_stage_complete", func(ec EvalContext) (bool, error) {
			return topologyStageComplete(ec.Vars, ec.BindTopologyStage), nil
		}},
		{"parallel_stages_complete", func(ec EvalContext) (bool, error) {
			return parallelStagesComplete(ec.Vars, ec.BindParallelGroup), nil
		}},
		{"delegation_closeout_complete", func(ec EvalContext) (bool, error) {
			if deps.DelegationCloseout == nil {
				return false, nil
			}
			return deps.DelegationCloseout(ec.Ctx, ec.SessionID)
		}},
		{"delivery_gates_passed", func(ec EvalContext) (bool, error) {
			if deps.DeliveryReported == nil {
				return false, nil
			}
			reported, err := deps.DeliveryReported(ec.Ctx, ec.SessionID, ec.WorkflowRunID, ec.Phase)
			if err != nil || !reported {
				return false, err
			}
			return deliveryGatesPassed(deps, ec)
		}},
		{"closeout_gates_passed", func(ec EvalContext) (bool, error) {
			return closeoutGatesPassed(deps, ec)
		}},
		{"orchestration_complete", func(ec EvalContext) (bool, error) {
			return BoolVar(ec.Vars, "orchestration_complete"), nil
		}},
	}
	for _, e := range entries {
		if err := reg.Register(e.name, e.fn); err != nil {
			return err
		}
	}
	return nil
}

func closeoutGatesPassed(deps CoreDeps, ec EvalContext) (bool, error) {
	ok, err := deliveryGatesPassed(deps, ec)
	if err != nil || !ok {
		return false, err
	}
	return evidenceSatisfied(deps, ec, evidence.GateTypeVerify, true)
}

func deliveryGatesPassed(deps CoreDeps, ec EvalContext) (bool, error) {
	if deps.DelegationCloseout == nil {
		return false, nil
	}
	ok, err := deps.DelegationCloseout(ec.Ctx, ec.SessionID)
	if err != nil || !ok {
		return false, err
	}
	if deps.ScanLedger != nil {
		if deps.DelegationStore != nil && ec.SessionID != "" {
			if _, assigned := deps.DelegationStore.DelegationBySessionID(ec.SessionID); !assigned {
				return true, nil
			}
		}
		securityOK, err := securityEvidenceSatisfied(deps, ec, true)
		if err != nil || !securityOK {
			return false, err
		}
	}
	return true, nil
}

func registerCoreRuleFacts(reg *ConditionRegistry) error {
	facts := []struct {
		name string
		fn   ConditionFunc
	}{
		{"posture_is_spec", func(ec EvalContext) (bool, error) {
			return ec.SessionPosture == api.SessionPostureSpec, nil
		}},
		{"posture_is_build", func(ec EvalContext) (bool, error) {
			return ec.SessionPosture == api.SessionPostureBuild, nil
		}},
		{"posture_is_orchestrate", func(ec EvalContext) (bool, error) {
			return ec.SessionPosture == api.SessionPostureOrchestrate, nil
		}},
		{"posture_is_vet", func(ec EvalContext) (bool, error) {
			return ec.SessionPosture == api.SessionPostureVet, nil
		}},
		{"posture_unresolved", func(ec EvalContext) (bool, error) {
			return ec.SessionPosture == "", nil
		}},
		{"agent_is_plan_writer", func(ec EvalContext) (bool, error) {
			agent, _ := ec.ToolArgs["agent_type"].(string)
			return strings.EqualFold(strings.TrimSpace(agent), "plan-writer"), nil
		}},
	}
	for _, fact := range facts {
		if reg.Has(fact.name) {
			continue
		}
		if err := reg.Register(fact.name, fact.fn); err != nil {
			return err
		}
	}
	return nil
}

// isHighRiskTool classifies state transitions, handoffs, and implementation delegation.
func isHighRiskTool(name string, args map[string]any) bool {
	if strings.HasPrefix(name, "state_") || strings.HasPrefix(name, "handoff_") || strings.HasPrefix(name, "delegate_") {
		return true
	}
	if name != "task" {
		return false
	}
	agent, _ := args["agent_type"].(string)
	return strings.EqualFold(strings.TrimSpace(agent), "implementer")
}

// matchDisallowedAgent treats a nil roster as unrestricted and an empty roster as closed.
func matchDisallowedAgent(ec EvalContext) bool {
	if ec.ToolName != "task" || ec.AllowedAgents == nil {
		return false
	}
	agent, _ := ec.ToolArgs["agent_type"].(string)
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return len(ec.AllowedAgents) == 0
	}
	for _, allowed := range ec.AllowedAgents {
		if strings.EqualFold(allowed, agent) {
			return false
		}
	}
	return true
}
