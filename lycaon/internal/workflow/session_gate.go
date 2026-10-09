package workflow

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/spawn"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// AgentToolAccess returns the active workflow's explicit breadth for an agent.
func (m *RunManager) AgentToolAccess(ctx context.Context, sessionID, agentType string) sandbox.ToolAccess {
	if m == nil || sessionID == "" || strings.TrimSpace(agentType) == "" {
		return sandbox.ToolAccessProfile
	}
	active, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return sandbox.ToolAccessProfile
	}
	manifest, err := m.manifestForRun(ctx, active)
	if err != nil {
		return sandbox.ToolAccessProfile
	}
	if manifest.AgentToolAccess[strings.TrimSpace(agentType)] == sandbox.ToolAccessAll {
		return sandbox.ToolAccessAll
	}
	return sandbox.ToolAccessProfile
}

// AssertSessionRunnable requires a running workflow for coordinator execution.
func (m *RunManager) AssertSessionRunnable(ctx context.Context, sessionID string) error {
	if m == nil || sessionID == "" {
		return nil
	}
	active, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil {
		return err
	}
	if active == nil {
		sess, err := m.Sessions.Get(ctx, sessionID)
		if err != nil {
			return err
		}
		if sess != nil && sess.IsWorkerChild() {
			return nil
		}
		return ErrNoActiveRun
	}
	return m.AssertRunnable(ctx, active.ID)
}

// AllowedAgents returns the workflow roster, including contributed agents for ambient runs.
func (m *RunManager) AllowedAgents(ctx context.Context, sessionID string) []string {
	if m == nil || sessionID == "" {
		return nil
	}
	active, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return spawn.AmbientAllowedAgents()
	}
	manifest, err := m.manifestForRun(ctx, active)
	if err != nil || len(manifest.AllowedAgents) == 0 {
		return nil
	}
	return m.RosterFor(active, manifest)
}

// RosterFor supplies the task roster shared by dispatch and prompt assembly.
func (m *RunManager) RosterFor(active *api.WorkflowRun, manifest workflowdef.Manifest) []string {
	declared := append([]string(nil), manifest.AllowedAgents...)
	if !m.IsAmbientRun(active) {
		return declared
	}
	return spawn.WithContributedAgents(declared)
}

// CurrentPhase returns the active workflow phase for a session, or empty when none.
func (m *RunManager) CurrentPhase(ctx context.Context, sessionID string) string {
	if m == nil || sessionID == "" {
		return ""
	}
	active, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return ""
	}
	return active.CurrentPhase
}

// ActivePhaseHasReviewLoop reports whether the active phase declares a review_loop.
func (m *RunManager) ActivePhaseHasReviewLoop(ctx context.Context, sessionID string) bool {
	if m == nil || sessionID == "" {
		return false
	}
	active, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return false
	}
	manifest, err := m.manifestForRun(ctx, active)
	if err != nil {
		return false
	}
	def, ok := manifest.PhaseByID(active.CurrentPhase)
	if !ok || def.ReviewLoop == nil {
		return false
	}
	return true
}

// ActivePhaseGuardState derives closeout and dispatch facts for the active phase.
func (m *RunManager) ActivePhaseGuardState(ctx context.Context, sessionID string) session.WorkflowPhaseGuardState {
	var state session.WorkflowPhaseGuardState
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return state
	}
	active, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return state
	}
	manifest, err := m.manifestForRun(ctx, active)
	if err != nil {
		return state
	}
	state.Phase = strings.TrimSpace(active.CurrentPhase)
	current, currentOK := manifest.PhaseByID(active.CurrentPhase)
	for _, phase := range manifest.PhaseDefs {
		if workflowdef.PhaseHasGate(phase, "topology_report_delivered") {
			state.ReportPhaseDeclared = true
			state.ReportCloseoutPending = !currentOK || !workflowdef.PhaseHasGate(current, "topology_report_delivered")
			break
		}
	}
	if !currentOK || !current.HasOnEnterObligations() {
		return state
	}
	kinds := make([]string, 0, len(current.OnEnter.Obligations))
	for _, ob := range current.OnEnter.Obligations {
		kinds = append(kinds, ob.Kind)
	}
	vars, err := m.Store.GetScaffoldVars(ctx, active.ID)
	if err != nil {
		state.PhaseObligationPending = true
		state.PendingObligationKinds = kinds
		return state
	}
	// Check only obligation gates.
	ec := conditions.EvalContextFromRun(ctx, m.sessionForRun(ctx, active), active, vars)
	var pending []string
	for _, kind := range kinds {
		settled, evalErr := m.Registry.Evaluate(ObligationGateLeaf(kind), ec)
		if evalErr != nil || !settled {
			pending = append(pending, kind)
		}
	}
	state.PhaseObligationPending = len(pending) > 0
	state.PendingObligationKinds = pending
	return state
}

func (m *RunManager) sessionForRun(ctx context.Context, run *api.WorkflowRun) *api.Session {
	if m == nil || m.Sessions == nil || run == nil {
		return nil
	}
	sess, err := m.Sessions.Get(ctx, run.SessionID)
	if err != nil {
		return nil
	}
	return sess
}

// ActiveReviewVerdictPending reports an unsatisfied review-loop evidence gate.
func (m *RunManager) ActiveReviewVerdictPending(ctx context.Context, sessionID string) bool {
	if m == nil || sessionID == "" {
		return false
	}
	active, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil || active.Status != api.WorkflowRunStatusRunning {
		return false
	}
	manifest, err := m.manifestForRun(ctx, active)
	if err != nil {
		return false
	}
	def, ok := manifest.PhaseByID(active.CurrentPhase)
	if !ok || def.ReviewLoop == nil {
		return false
	}
	key := strings.TrimSpace(def.ReviewLoop.EvidenceKey)
	if key == "" {
		return false
	}
	vars, err := m.Store.GetScaffoldVars(ctx, active.ID)
	if err != nil {
		return false
	}
	return !gateSatisfiedInVars(vars, "evidence_passed:"+key)
}

// ActiveCloseoutGateState reports open completion gates, excluding pending human input.
func (m *RunManager) ActiveCloseoutGateState(ctx context.Context, sessionID string) session.WorkflowCloseoutGateState {
	var state session.WorkflowCloseoutGateState
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return state
	}
	active, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil || active.Status != api.WorkflowRunStatusRunning {
		return state
	}
	manifest, err := m.manifestForRun(ctx, active)
	if err != nil {
		return state
	}
	def, ok := manifest.PhaseByID(active.CurrentPhase)
	if !ok || !def.Closeout.Gated() {
		return state
	}
	vars, err := m.Store.GetScaffoldVars(ctx, active.ID)
	if err != nil || scaffoldvars.HasPendingUserInput(vars) {
		return state
	}
	met, result, err := m.gateEvaluator().PhaseGateMet(ctx, manifest, active, vars)
	if err != nil || met {
		return state
	}
	state.Gated = true
	state.Phase = strings.TrimSpace(active.CurrentPhase)
	state.OpenLeaves = append([]string(nil), result.FailedLeaves...)
	if len(state.OpenLeaves) == 0 {
		state.OpenLeaves = []string{strings.TrimSpace(def.CompleteWhen)}
	}
	return state
}

// ActiveManifest returns coordinator_profile and rules from the active workflow manifest.
func (m *RunManager) ActiveManifest(ctx context.Context, sessionID string) (session.ActiveWorkflowManifest, bool) {
	if m == nil || sessionID == "" {
		return session.ActiveWorkflowManifest{}, false
	}
	active, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return session.ActiveWorkflowManifest{}, false
	}
	manifest, err := m.manifestForRun(ctx, active)
	if err != nil {
		return session.ActiveWorkflowManifest{}, false
	}
	return session.ActiveWorkflowManifest{
		CoordinatorProfile: strings.TrimSpace(manifest.CoordinatorProfile),
		Rules:              append([]string(nil), manifest.Rules...),
		HostPhaseAdvance:   workflowdef.PhaseHostPhaseAdvance(manifest, active.CurrentPhase),
		Archive:            runArchive(manifest),
	}, true
}

// ResolvedRequest returns the active run's resolved request.
func (m *RunManager) ResolvedRequest(ctx context.Context, sessionID string) session.ResolvedWorkflowRequest {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return session.ResolvedWorkflowRequest{}
	}
	active, vars, err := m.Store.ActiveStateBySession(ctx, sessionID)
	if err != nil || active == nil {
		return session.ResolvedWorkflowRequest{}
	}
	if state, ok := requestStateFromVars(vars); ok && state.Status == requestStatusResolved {
		return session.ResolvedWorkflowRequest{RunID: active.ID, OpeningMessageID: active.StartMessageID, Text: strings.TrimSpace(state.Text)}
	}
	return session.ResolvedWorkflowRequest{}
}
