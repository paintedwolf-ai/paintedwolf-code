package runtime

import (
	"context"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	workflowfacts "github.com/lycaon/lycaon/internal/session/workflowfacts"
	"github.com/lycaon/lycaon/internal/spawn"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowgates "github.com/lycaon/lycaon/internal/workflow/gates"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

// AgentToolAccess returns the active workflow's explicit breadth for an agent.
func (m *SessionPolicy) AgentToolAccess(ctx context.Context, sessionID, agentType string) sandbox.ToolAccess {
	if m == nil || sessionID == "" || strings.TrimSpace(agentType) == "" {
		return sandbox.ToolAccessProfile
	}
	active, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return sandbox.ToolAccessProfile
	}
	manifest, err := m.Resolver.ForRun(ctx, active)
	if err != nil {
		return sandbox.ToolAccessProfile
	}
	if manifest.AgentToolAccess[strings.TrimSpace(agentType)] == sandbox.ToolAccessAll {
		return sandbox.ToolAccessAll
	}
	return sandbox.ToolAccessProfile
}

// AssertSessionRunnable requires a running workflow for coordinator execution.
func (m *SessionPolicy) AssertSessionRunnable(ctx context.Context, sessionID string) error {
	if m == nil || sessionID == "" {
		return nil
	}
	active, err := m.Runs.ActiveBySession(ctx, sessionID)
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
		return runstate.ErrNoActiveRun
	}
	return m.AssertRunnable(ctx, active.ID)
}

// AllowedAgents returns the workflow roster, including contributed agents for ambient runs.
func (m *SessionPolicy) AllowedAgents(ctx context.Context, sessionID string) []string {
	if m == nil || sessionID == "" {
		return nil
	}
	active, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return spawn.AmbientAllowedAgents()
	}
	manifest, err := m.Resolver.ForRun(ctx, active)
	if err != nil || len(manifest.AllowedAgents) == 0 {
		return nil
	}
	return m.RosterFor(active, manifest)
}

// RosterFor supplies the task roster shared by dispatch and prompt assembly.
func (m *SessionPolicy) RosterFor(active *api.WorkflowRun, manifest workflowdef.Manifest) []string {
	declared := append([]string(nil), manifest.AllowedAgents...)
	if !runstate.IsAmbientRun(active) {
		return declared
	}
	return spawn.WithContributedAgents(declared)
}

// CurrentPhase returns the active workflow phase for a session, or empty when none.
func (m *SessionPolicy) CurrentPhase(ctx context.Context, sessionID string) string {
	if m == nil || sessionID == "" {
		return ""
	}
	active, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return ""
	}
	return active.CurrentPhase
}

// ActivePhaseHasReviewLoop reports whether the active phase declares a review_loop.
func (m *SessionPolicy) ActivePhaseHasReviewLoop(ctx context.Context, sessionID string) bool {
	if m == nil || sessionID == "" {
		return false
	}
	active, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return false
	}
	manifest, err := m.Resolver.ForRun(ctx, active)
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
func (m *SessionPolicy) ActivePhaseGuardState(ctx context.Context, sessionID string) workflowfacts.WorkflowPhaseGuardState {
	var state workflowfacts.WorkflowPhaseGuardState
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return state
	}
	active, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return state
	}
	manifest, err := m.Resolver.ForRun(ctx, active)
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
	vars, err := m.Runs.GetScaffoldVars(ctx, active.ID)
	if err != nil {
		state.PhaseObligationPending = true
		state.PendingObligationKinds = kinds
		return state
	}
	// Check only obligation gates.
	ec := conditions.EvalContextFromRun(ctx, m.sessionForRun(ctx, active), active, vars)
	var pending []string
	for _, kind := range kinds {
		settled, evalErr := m.Registry.Evaluate(workflowdef.ObligationGateLeaf(kind), ec)
		if evalErr != nil || !settled {
			pending = append(pending, kind)
		}
	}
	state.PhaseObligationPending = len(pending) > 0
	state.PendingObligationKinds = pending
	return state
}

func (m *SessionPolicy) sessionForRun(ctx context.Context, run *api.WorkflowRun) *api.Session {
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
func (m *SessionPolicy) ActiveReviewVerdictPending(ctx context.Context, sessionID string) bool {
	if m == nil || sessionID == "" {
		return false
	}
	active, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return false
	}
	manifest, err := m.Resolver.ForRun(ctx, active)
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
	vars, err := m.Runs.GetScaffoldVars(ctx, active.ID)
	if err != nil {
		return false
	}
	return !workflowgates.SatisfiedInVars(vars, "evidence_passed:"+key)
}

// ActiveCloseoutGateState reports open completion gates, excluding pending human input.
func (m *SessionPolicy) ActiveCloseoutGateState(ctx context.Context, sessionID string) workflowfacts.WorkflowCloseoutGateState {
	var state workflowfacts.WorkflowCloseoutGateState
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return state
	}
	active, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return state
	}
	manifest, err := m.Resolver.ForRun(ctx, active)
	if err != nil {
		return state
	}
	def, ok := manifest.PhaseByID(active.CurrentPhase)
	if !ok || !def.Closeout.Gated() {
		return state
	}
	vars, err := m.Runs.GetScaffoldVars(ctx, active.ID)
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
func (m *SessionPolicy) ActiveManifest(ctx context.Context, sessionID string) (workflowfacts.ActiveWorkflowManifest, bool) {
	if m == nil || sessionID == "" {
		return workflowfacts.ActiveWorkflowManifest{}, false
	}
	active, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return workflowfacts.ActiveWorkflowManifest{}, false
	}
	manifest, err := m.Resolver.ForRun(ctx, active)
	if err != nil {
		return workflowfacts.ActiveWorkflowManifest{}, false
	}
	return workflowfacts.ActiveWorkflowManifest{
		CoordinatorProfile: strings.TrimSpace(manifest.CoordinatorProfile),
		Rules:              append([]string(nil), manifest.Rules...),
		HostPhaseAdvance:   workflowdef.PhaseHostPhaseAdvance(manifest, active.CurrentPhase),
	}, true
}

// ResolvedRequest returns the active run's resolved request.
func (m *SessionPolicy) ResolvedRequest(ctx context.Context, sessionID string) workflowfacts.ResolvedWorkflowRequest {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return workflowfacts.ResolvedWorkflowRequest{}
	}
	active, vars, err := m.Runs.ActiveStateBySession(ctx, sessionID)
	if err != nil || active == nil {
		return workflowfacts.ResolvedWorkflowRequest{}
	}
	if state, ok := runstate.RequestStateFromVars(vars); ok && state.Status == runstate.RequestStatusResolved {
		return workflowfacts.ResolvedWorkflowRequest{RunID: active.ID, OpeningMessageID: active.StartMessageID, Text: strings.TrimSpace(state.Text)}
	}
	return workflowfacts.ResolvedWorkflowRequest{}
}

func (m *SessionPolicy) AssertRunnable(ctx context.Context, runID string) error {
	if runID == "" {
		return nil
	}
	run, err := m.Runs.Get(ctx, runID)
	if err != nil {
		return err
	}
	switch run.Status {
	case api.WorkflowRunStatusRunning:
		return nil
	case api.WorkflowRunStatusPaused:
		return &runstate.NotRunnableError{RunID: runID, Status: run.Status, Reason: "paused"}
	case api.WorkflowRunStatusPausedOnChild:
		return &runstate.NotRunnableError{RunID: runID, Status: run.Status, Reason: "paused_on_child"}
	case api.WorkflowRunStatusCanceled:
		return &runstate.NotRunnableError{RunID: runID, Status: run.Status, Reason: "canceled"}
	case api.WorkflowRunStatusComplete:
		return &runstate.NotRunnableError{RunID: runID, Status: run.Status, Reason: "complete"}
	default:
		return &runstate.NotRunnableError{RunID: runID, Status: run.Status, Reason: string(run.Status)}
	}
}

type SessionPolicy struct {
	Runs      runstate.RunsRepository
	Resolver  *workflowcatalog.Resolver
	Sessions  Sessions
	Registry  *conditions.ConditionRegistry
	Gates     workflowgates.GateEvaluator
	Approvals ApprovalState
}

func (m *SessionPolicy) gateEvaluator() workflowgates.GateEvaluator {
	if m != nil && m.Gates != nil {
		return m.Gates
	}
	return workflowgates.FailClosedGateEvaluator{}
}
func (m *SessionPolicy) HumanApprovalAwaiting(ctx context.Context, sessionID string) (bool, error) {
	if m == nil || m.Runs == nil {
		return false, nil
	}
	run, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || run == nil {
		return false, err
	}
	vars, err := m.Runs.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return false, err
	}
	return m.Approvals.AwaitsHumanApproval(ctx, run, vars)
}
