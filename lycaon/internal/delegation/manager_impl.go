package delegation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

// HeadSHAReader captures git HEAD at delegation creation.
type HeadSHAReader interface {
	HeadSHA(ctx context.Context, projectDir string) (string, error)
}

// Manager implements DelegationManager and worker.OutcomeProjection.
type Manager struct {
	Store             Store
	Queue             worker.WorkerQueue
	Sessions          *session.Manager
	Gate              DispatchGate
	Grounding         *GroundingCoordinator
	Plans             PlanReader
	Projects          project.Registry
	HeadSHA           HeadSHAReader
	InspectorCloseout *InspectorCloseoutGate
	OnCloseout        CloseoutHook
	ToolBudget        func(projectDir string) spawn.WorkerToolBudget

	// createMu keeps a replayed create from making a coordinator session
	// before its receipt is found.
	createMu sync.Mutex
}

// CloseoutHook runs after all legs complete or fail.
type CloseoutHook func(ctx context.Context, delegationID, sessionID, workflowRunID string)

// NewManager wires delegation dispatch to the worker queue and session prompt loop.
func NewManager(store Store, queue worker.WorkerQueue, sessions *session.Manager, gate DispatchGate) *Manager {
	if gate == nil {
		gate = AllowGate{}
	}
	gate = DependencyDispatchGate{Inner: gate, Store: store, Workers: queue}
	return &Manager{
		Store:    store,
		Queue:    queue,
		Sessions: sessions,
		Gate:     gate,
	}
}

// Create makes a delegation with a single worker leg and its own coordinator
// session. A create repeating an earlier operation_id with the same request
// answers the delegation that create made; with a different request it fails
// with ErrOperationConflict.
func (m *Manager) Create(ctx context.Context, req api.CreateDelegationRequest) (*api.Delegation, error) {
	if m.Sessions == nil {
		return nil, fmt.Errorf("session manager not configured")
	}
	req = normalizeCreateRequest(req)
	if req.ProjectID == "" {
		return nil, fmt.Errorf("project_id required")
	}
	var receipt CreateReceipt
	if req.OperationID != "" {
		var err error
		if receipt, err = createReceipt(req); err != nil {
			return nil, err
		}
		m.createMu.Lock()
		defer m.createMu.Unlock()
		if prior, found, err := m.Store.DelegationByOperation(ctx, receipt); err != nil || found {
			return prior, err
		}
	}
	sess, err := m.Sessions.CreateForProject(ctx, req.ProjectID, api.SessionPostureOrchestrate)
	if err != nil {
		return nil, err
	}
	_ = m.Sessions.SetAgentType(ctx, sess.ID, orchestration.ProfileCoordinator)
	return m.create(ctx, sess.ID, req, receipt)
}

// Init creates a delegation for an existing coordinator session, or answers
// the one the session already has.
func (m *Manager) Init(ctx context.Context, sessionID string, req api.CreateDelegationRequest) (*api.Delegation, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("session_id required")
	}
	req = normalizeCreateRequest(req)
	if req.ProjectID == "" {
		return nil, fmt.Errorf("project_id required")
	}
	if delegationID, ok := m.Store.DelegationBySessionID(sessionID); ok {
		return m.Store.Get(ctx, delegationID)
	}
	return m.create(ctx, sessionID, req, CreateReceipt{})
}

func (m *Manager) create(ctx context.Context, sessionID string, req api.CreateDelegationRequest, receipt CreateReceipt) (*api.Delegation, error) {
	projectID := req.ProjectID
	scope, err := m.scopeForSessionID(ctx, sessionID, projectID)
	if err != nil {
		return nil, err
	}
	if !scope.HasRoots {
		return nil, fmt.Errorf("project roots required")
	}

	var planDoc *api.Blueprint
	if strings.TrimSpace(req.BlueprintPath) != "" && m.Plans != nil {
		p, err := m.Plans.Get(ctx, projectID, req.BlueprintPath)
		if err != nil {
			return nil, err
		}
		planDoc = p
	}

	legs := []api.Leg{{
		ID:        uuid.NewString(),
		Title:     "Worker leg",
		AgentType: orchestration.ProfileImplementer,
		Prompt:    guidance.StripHostBlocks(req.Task),
		Files:     []string{"**/*"},
		Status:    api.LegStatusPending,
		CreatedAt: time.Now().UTC(),
	}}
	if planDoc != nil {
		legs = LegsFromPlan(planDoc, blueprint.ExtractTasks(planDoc.Content))
		for i := range legs {
			legs[i].Prompt = guidance.StripHostBlocks(legs[i].Prompt)
			if legs[i].CreatedAt.IsZero() {
				legs[i].CreatedAt = time.Now().UTC()
			}
		}
	}

	delegation := api.Delegation{
		ProjectID:       scope.ProjectID,
		WorkspaceRootID: scope.WorkspaceRootID,
		WorkspacePath:   scope.WorkspacePath,
		Task:            req.Task,
		Strategy:        req.Strategy,
		InspectMode:     req.InspectMode,
		WorkflowID:      req.WorkflowID,
		WorkflowVersion: req.WorkflowVersion,
		WorkflowRunID:   req.WorkflowRunID,
		BlueprintPath:   req.BlueprintPath,
		Status:          api.DelegationStatusActive,
		Phase:           api.DelegationPhaseSetup,
	}
	if m.HeadSHA != nil && scope.WorkspacePath != "" {
		if sha, err := m.HeadSHA.HeadSHA(ctx, scope.WorkspacePath); err == nil {
			delegation.BaseHeadSHA = sha
		}
	}

	var created *api.Delegation
	if receipt.OperationID != "" {
		created, err = m.Store.CreateOnce(ctx, delegation, sessionID, legs, receipt)
	} else {
		created, err = m.Store.Create(ctx, delegation, sessionID, legs)
	}
	if err != nil {
		return nil, err
	}
	if refreshed, err := m.Store.Get(ctx, created.ID); err == nil {
		created = refreshed
	}
	return created, nil
}

func (m *Manager) scopeForSessionID(ctx context.Context, sessionID, projectID string) (project.ProjectScope, error) {
	if m.Sessions == nil {
		return project.ProjectScope{ProjectID: projectID}, nil
	}
	sess, err := m.Sessions.Get(ctx, sessionID)
	if err != nil {
		return project.ProjectScope{}, err
	}
	if m.Projects == nil {
		return project.ProjectScope{ProjectID: strings.TrimSpace(sess.ProjectID)}, nil
	}
	return project.ScopeForSession(ctx, sess, m.Projects)
}

func taskScopeForLeg(agentType string, files []string) *api.TaskScope {
	paths := append([]string(nil), files...)
	mode := api.TaskScopeModeRead
	if mutationCapable, known := prompts.AgentMutationCapable(agentType); known && mutationCapable {
		mode = api.TaskScopeModeWrite
	}
	return &api.TaskScope{Mode: mode, Paths: paths}
}

// DispatchLeg replays retries by source tool call ID.
func (m *Manager) DispatchLeg(ctx context.Context, delegationID, legID, sourceToolCallID string) (*api.Leg, error) {
	sessionID, ok := m.Store.SessionID(delegationID)
	if !ok {
		return nil, fmt.Errorf("delegation %s missing session", delegationID)
	}
	var leg *api.Leg
	err := m.Queue.WithTaskAdmission(ctx, api.WorkerTask{ParentSessionID: sessionID}, func() error {
		var dispatchErr error
		leg, dispatchErr = m.dispatchLegAdmitted(ctx, delegationID, legID, sourceToolCallID)
		return dispatchErr
	})
	return leg, err
}

func (m *Manager) dispatchLegAdmitted(ctx context.Context, delegationID, legID, sourceToolCallID string) (*api.Leg, error) {
	allowed, reason, err := m.Gate.Check(ctx, delegationID, legID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, fmt.Errorf("dispatch blocked: %s", reason)
	}

	leg, err := m.Store.GetLeg(ctx, delegationID, legID)
	if err != nil {
		return nil, err
	}
	delegation, err := m.Store.Get(ctx, delegationID)
	if err != nil {
		return nil, err
	}
	sessionID, ok := m.Store.SessionID(delegationID)
	if !ok {
		return nil, fmt.Errorf("delegation %s missing session", delegationID)
	}
	sourceToolCallID = strings.TrimSpace(sourceToolCallID)
	if sourceToolCallID == "" {
		sourceToolCallID = session.HostTaskCallPrefix + legID
	}
	if leg.Status != api.LegStatusPending {
		if replay := m.replayDispatchedLeg(leg, sourceToolCallID); replay != nil {
			if err := m.recordDispatchCard(ctx, sessionID, replay.WorkerID, sourceToolCallID); err != nil {
				return nil, err
			}
			return replay, nil
		}
		return nil, ErrLegNotPending
	}

	now := time.Now().UTC()
	leg.Status = api.LegStatusDispatched
	leg.StartedAt = &now
	if len(leg.CompletionCriteria) == 0 {
		_ = PopulateLegCriteria(ctx, m.Plans, delegation, leg)
	}
	if len(leg.CompletionCriteria) == 0 {
		leg.CompletionCriteria = append([]string(nil), defaultCompletionCriteria...)
	}
	agentType := strings.TrimSpace(leg.AgentType)
	if agentType == "" {
		agentType = orchestration.ProfileImplementer
	}
	prompt := strings.TrimSpace(guidance.StripHostBlocks(leg.Prompt))
	if prompt == "" {
		return nil, fmt.Errorf("leg prompt required")
	}
	brief := strings.TrimSpace(leg.Title)
	if brief == "" {
		return nil, fmt.Errorf("leg title required")
	}

	scope := project.ProjectScope{
		ProjectID:       delegation.ProjectID,
		WorkspaceRootID: delegation.WorkspaceRootID,
		WorkspacePath:   delegation.WorkspacePath,
		HasRoots:        strings.TrimSpace(delegation.WorkspacePath) != "",
	}
	taskScope := taskScopeForLeg(agentType, leg.Files)
	task := api.WorkerTask{
		DelegationID:     delegationID,
		LegID:            legID,
		ParentSessionID:  sessionID,
		SourceToolCallID: sourceToolCallID,
		WorkflowRunID:    delegation.WorkflowRunID,
		AgentType:        agentType,
		Prompt:           prompt,
		Brief:            brief,
		Scope:            taskScope,
		Files:            append([]string(nil), leg.Files...),
		SpawnReason:      api.SpawnReasonInitial,
		Status:           api.WorkerStatusPending,
		ExecutionTarget:  api.ExecutionTargetLocal,
		ProjectID:        scope.ProjectID,
		WorkspaceRootID:  scope.WorkspaceRootID,
		WorkspacePath:    scope.WorkspacePath,
	}
	for _, id := range leg.DependsOn {
		upstream, err := m.Store.GetLeg(ctx, delegationID, id)
		if err != nil {
			return nil, err
		}
		if upstream.WorkerID != "" {
			task.AfterWorkers = append(task.AfterWorkers, upstream.WorkerID)
		}
	}
	if ws := strings.TrimSpace(leg.WorkspaceRoot); ws != "" {
		task.WorkspaceRoot = ws
	}
	if err := worker.ApplyEnqueueDefaults(&task, scope, worker.DefaultWorkersConfig()); err != nil {
		return nil, err
	}
	if err := m.Queue.PrepareEnqueue(ctx, "", &task); err != nil {
		return nil, err
	}
	leg.WorkerID = task.ID
	delegation.Phase = api.DelegationPhaseWorker
	if err := m.Store.DispatchLegWithJob(ctx, *leg, *delegation, task); err != nil {
		_ = m.Queue.Cancel(ctx, task.ID, nil)
		if errors.Is(err, ErrLegNotPending) {
			// Lost the pending-to-dispatched CAS to a concurrent dispatch.
			if current, legErr := m.Store.GetLeg(ctx, delegationID, legID); legErr == nil {
				if replay := m.replayDispatchedLeg(current, sourceToolCallID); replay != nil {
					if err := m.recordDispatchCard(ctx, sessionID, replay.WorkerID, sourceToolCallID); err != nil {
						return nil, err
					}
					return replay, nil
				}
			}
		}
		return nil, err
	}
	m.Queue.PublishEnqueued(ctx, task.ID)
	if err := m.recordDispatchCard(ctx, sessionID, task.ID, sourceToolCallID); err != nil {
		return nil, err
	}

	updated, err := m.Store.GetLeg(ctx, delegationID, legID)
	if err != nil {
		return nil, err
	}
	if m.Grounding != nil {
		if sessionID, ok := m.Store.SessionID(delegationID); ok {
			m.Grounding.Reset(sessionID)
		}
	}
	return updated, nil
}

func (m *Manager) recordDispatchCard(ctx context.Context, sessionID, jobID, sourceToolCallID string) error {
	if m.Sessions == nil {
		return nil
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil
	}
	task, ok := m.Queue.Get(jobID)
	if !ok || task == nil {
		return nil
	}
	return m.Sessions.EnsureWorkerCardProjection(ctx, sessionID, session.WorkerDispatchRowInput{
		JobID:          task.ID,
		AgentType:      task.AgentType,
		Brief:          task.Brief,
		ChildSessionID: task.ChildSessionID,
		ToolCallID:     sourceToolCallID,
	})
}

// replayDispatchedLeg resolves idempotent dispatch retries.
func (m *Manager) replayDispatchedLeg(leg *api.Leg, sourceToolCallID string) *api.Leg {
	if leg == nil || strings.TrimSpace(leg.WorkerID) == "" || sourceToolCallID == "" {
		return nil
	}
	job, ok := m.Queue.Get(leg.WorkerID)
	if !ok || job == nil {
		return nil
	}
	if strings.TrimSpace(job.SourceToolCallID) != sourceToolCallID {
		return nil
	}
	return leg
}

// RecordOutcome records a worker result and attempts delegation closeout.
func (m *Manager) RecordOutcome(ctx context.Context, delegationID, legID, workerID string, outcome api.WorkerResult) error {
	if strings.TrimSpace(workerID) == "" {
		return fmt.Errorf("worker_id required for leg outcome")
	}
	leg, err := m.Store.GetLeg(ctx, delegationID, legID)
	if err != nil {
		return err
	}
	leg.WorkerID = workerID
	now := time.Now().UTC()
	leg.Status = legStatusForOutcome(outcome)
	if leg.Status == api.LegStatusRetryPending {
		delegation, delegationErr := m.Store.Get(ctx, delegationID)
		if delegationErr != nil {
			return delegationErr
		}
		if strings.TrimSpace(delegation.WorkflowRunID) == "" {
			leg.Status = api.LegStatusFailed
		}
	}
	leg.Result = &outcome
	if leg.Status == api.LegStatusRetryPending {
		leg.CompletedAt = nil
	} else {
		leg.CompletedAt = &now
	}
	if _, err := m.Store.RecordLegOutcome(ctx, *leg); err != nil {
		return err
	}
	return m.tryCloseout(ctx, delegationID)
}

func legStatusForOutcome(outcome api.WorkerResult) api.LegStatus {
	switch strings.TrimSpace(outcome.Status) {
	case "complete":
		return api.LegStatusComplete
	case "canceled":
		return api.LegStatusCanceled
	case "needs_decision", "held":
		return api.LegStatusHeld
	case "partial":
		if strings.TrimSpace(outcome.HintCode) == session.WorkerBudgetExhaustedCode {
			return api.LegStatusRetryPending
		}
		return api.LegStatusFailed
	default:
		// Only complete results release dependencies.
		return api.LegStatusFailed
	}
}

// ResumeLeg continues a budget-limited leg in its existing child session.
func (m *Manager) ResumeLeg(ctx context.Context, delegationID, legID string) (*api.Leg, error) {
	leg, err := m.Store.GetLeg(ctx, delegationID, legID)
	if err != nil {
		return nil, err
	}
	if leg.Status != api.LegStatusRetryPending || leg.Result == nil || strings.TrimSpace(leg.Result.HintCode) != session.WorkerBudgetExhaustedCode {
		return nil, ErrLegNotPending
	}
	prior, ok := m.Queue.Get(strings.TrimSpace(leg.WorkerID))
	if !ok || prior == nil || strings.TrimSpace(prior.ChildSessionID) == "" {
		return nil, fmt.Errorf("retryable leg %s has no preserved child session", legID)
	}
	delegation, err := m.Store.Get(ctx, delegationID)
	if err != nil {
		return nil, err
	}
	budget := spawn.DefaultWorkerToolBudget()
	if m.ToolBudget != nil {
		budget = m.ToolBudget(delegation.WorkspacePath)
	}
	currentMax := prior.MaxToolLoops
	if currentMax <= 0 {
		currentMax = budget.Default
	}
	if currentMax >= budget.Max {
		now := time.Now().UTC()
		leg.Status = api.LegStatusFailed
		leg.CompletedAt = &now
		if err := m.Store.UpdateLeg(ctx, *leg); err != nil {
			return nil, err
		}
		if err := m.tryCloseout(ctx, delegationID); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("worker budget exhausted at host cap %d", budget.Max)
	}
	nextMax := currentMax * 2
	if nextMax > budget.Max {
		nextMax = budget.Max
	}
	sessionID, ok := m.Store.SessionID(delegationID)
	if !ok {
		return nil, fmt.Errorf("delegation %s missing session", delegationID)
	}
	task := api.WorkerTask{
		ChildSessionID: prior.ChildSessionID, DelegationID: delegationID, LegID: legID,
		ParentSessionID: sessionID, SourceToolCallID: session.HostTaskCallPrefix + legID + ":retry:" + prior.ID,
		WorkflowRunID: delegation.WorkflowRunID, AgentType: prior.AgentType, Prompt: prior.Prompt, Brief: prior.Brief,
		Scope: prior.Scope, Files: append([]string(nil), prior.Files...), WorkspaceRoot: prior.WorkspaceRoot,
		SpawnReason: api.SpawnReasonRetry, Status: api.WorkerStatusPending, ExecutionTarget: prior.ExecutionTarget,
		ProjectID: prior.ProjectID, WorkspaceRootID: prior.WorkspaceRootID, WorkspacePath: prior.WorkspacePath,
		MaxToolLoops: nextMax,
	}
	if err := worker.ApplyEnqueueDefaults(&task, project.ProjectScope{
		ProjectID: task.ProjectID, WorkspaceRootID: task.WorkspaceRootID,
		WorkspacePath: task.WorkspacePath, HasRoots: strings.TrimSpace(task.WorkspacePath) != "",
	}, worker.DefaultWorkersConfig()); err != nil {
		return nil, err
	}
	if err := m.Queue.PrepareEnqueue(ctx, "", &task); err != nil {
		return nil, err
	}
	leg.WorkerID = task.ID
	leg.Status = api.LegStatusDispatched
	leg.Result = nil
	leg.CompletedAt = nil
	delegation.Phase = api.DelegationPhaseWorker
	if err := m.Store.RedispatchLegWithJob(ctx, *leg, *delegation, task); err != nil {
		if errors.Is(err, ErrLegNotPending) {
			current, currentErr := m.Store.GetLeg(ctx, delegationID, legID)
			if currentErr == nil && current.Status == api.LegStatusDispatched && current.WorkerID == task.ID {
				return current, nil
			}
		}
		_ = m.Queue.Cancel(ctx, task.ID, nil)
		return nil, err
	}
	m.Queue.PublishEnqueued(ctx, task.ID)
	if err := m.recordDispatchCard(ctx, sessionID, task.ID, task.SourceToolCallID); err != nil {
		return nil, err
	}
	return m.Store.GetLeg(ctx, delegationID, legID)
}

// GetStatus returns the current delegation snapshot.
func (m *Manager) GetStatus(ctx context.Context, delegationID string) (*api.Delegation, error) {
	return m.Store.Get(ctx, delegationID)
}

// Abort cancels the delegation and every non-terminal worker for its legs.
func (m *Manager) Abort(ctx context.Context, delegationID, reason string) error {
	delegation, err := m.Store.Get(ctx, delegationID)
	if err != nil {
		return err
	}
	tasks, err := m.Queue.List(ctx, delegation.ProjectID)
	if err != nil {
		return fmt.Errorf("list delegation workers: %w", err)
	}
	for _, t := range tasks {
		if t.DelegationID == delegationID && !t.Status.IsTerminal() {
			if err := m.Queue.Cancel(ctx, t.ID, nil); err != nil {
				return fmt.Errorf("cancel delegation worker %s: %w", t.ID, err)
			}
		}
	}
	if err := m.Store.Abort(ctx, delegationID, time.Now().UTC(), reason); err != nil {
		return fmt.Errorf("settle aborted delegation: %w", err)
	}
	return nil
}

func (m *Manager) tryCloseout(ctx context.Context, delegationID string) error {
	delegation, err := m.Store.Get(ctx, delegationID)
	if err != nil {
		return err
	}
	if delegationSettled(delegation) {
		return nil
	}
	legs, err := m.Store.ListLegs(ctx, delegationID)
	if err != nil {
		return err
	}
	for _, leg := range legs {
		if !leg.Status.IsTerminal() {
			return nil
		}
	}
	if m.Grounding != nil {
		if err := m.Grounding.CheckDelegationCloseout(ctx, delegationID); err != nil {
			return err
		}
	}

	settled, err := m.Store.Settle(ctx, delegationID)
	if err != nil {
		return err
	}
	if !settled {
		return nil
	}

	if m.OnCloseout != nil && strings.TrimSpace(delegation.CoordinatorSessionID) != "" {
		m.OnCloseout(ctx, delegationID, delegation.CoordinatorSessionID, strings.TrimSpace(delegation.WorkflowRunID))
	}
	return nil
}

// RetryCloseout re-evaluates closeout after a worker landing.
func (m *Manager) RetryCloseout(ctx context.Context, delegationID string) error {
	return m.tryCloseout(ctx, strings.TrimSpace(delegationID))
}

// OnWorkerComplete implements worker.OutcomeProjection.
func (m *Manager) OnWorkerComplete(ctx context.Context, jobID string, result api.WorkerResult) error {
	task, ok := m.Queue.Get(jobID)
	if !ok {
		return fmt.Errorf("worker job %s not found", jobID)
	}
	if task.DelegationID == "" || task.LegID == "" {
		return nil
	}
	if err := m.RecordOutcome(ctx, task.DelegationID, task.LegID, task.ID, result); err != nil {
		return err
	}
	return nil
}

// OnWorkerFailed implements worker.OutcomeProjection.
func (m *Manager) OnWorkerFailed(ctx context.Context, jobID string, runErr error) error {
	task, ok := m.Queue.Get(jobID)
	if !ok {
		return fmt.Errorf("worker job %s not found", jobID)
	}
	if task.DelegationID == "" || task.LegID == "" {
		return nil
	}
	result := api.WorkerResult{Status: "failed"}
	if runErr != nil {
		result.Summary = runErr.Error()
	}
	return m.RecordOutcome(ctx, task.DelegationID, task.LegID, task.ID, result)
}
