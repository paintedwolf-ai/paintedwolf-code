package workeradmission

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/session/workflowfacts"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkerCycleGuardDeps wires in-flight worker checks for coordinator task().
type WorkerCycleGuardDeps struct {
	Workers         workeroutcomes.CycleLedger
	MaxWorkers      func(ctx context.Context, sessionID string) int
	MaxReadWorkers  func(ctx context.Context, sessionID string) int
	MaxWriteWorkers func(ctx context.Context, sessionID string) int
	PhaseGuardState func(ctx context.Context, sessionID string) workflowfacts.WorkflowPhaseGuardState
	// RepoKnownEmpty reports whether the session workspace is empty.
	RepoKnownEmpty func(ctx context.Context, workspacePath string) bool
}

// ObserveCoordinatorTaskInFlight publishes coordinator task guard facts.
func ObserveCoordinatorTaskInFlight(
	ctx context.Context,
	deps WorkerCycleGuardDeps,
	sess *api.Session,
	toolName string,
	args map[string]any,
	gc *oar.GuardContext,
) error {
	if gc == nil || strings.TrimSpace(strings.ToLower(toolName)) != "task" || sess == nil || !surface.IsCoordinatorParent(sess) {
		return nil
	}
	gc.Tool = "task"
	gc.DeriveToolClassFacts()
	if deps.PhaseGuardState != nil {
		state := deps.PhaseGuardState(ctx, sess.ID)
		if state.PhaseObligationPending {
			gc.Phase = state.Phase
			gc.PhaseObligationPending = true
			gc.PhaseObligationKinds = strings.Join(state.PendingObligationKinds, ", ")
			gc.PutRejectData("WORKFLOW_OBLIGATION_PENDING", map[string]any{
				"phase":                  state.Phase,
				"phase_obligation_kinds": gc.PhaseObligationKinds,
			})
			return nil
		}
	}
	agentType := taskAgentType(args)
	scope, err := api.TaskScopeFromArgs(args)
	if err != nil {
		return err
	}
	caps := parallelTaskCaps(ctx, deps, sess)
	publishTaskScopeFacts(gc, scope, caps, agentType)
	publishRepoEmptyReadOnlyFacts(ctx, deps, sess, agentType, gc)
	if code := ValidateTaskScopeForAgent(agentType, scope); code != "" {
		observeCoordinatorTaskScope(gc, code, scope, nil, caps, agentType, "")
		return nil
	}
	if gc.RepoKnownEmpty && !gc.ProfileMutationCapable && gc.ProfileSurveysProjectTree {
		observeCoordinatorTaskScope(gc, RepoEmptyReadOnlyWorkerCode, scope, nil, caps, agentType, "")
		return nil
	}
	if rejection, vErr := ValidateStackedBase(ctx, deps.Workers, sess.ID, sess.WorkspacePath, scope); vErr != nil {
		return vErr
	} else if rejection.Code != "" {
		data := map[string]any{
			"scope_mode":  string(scope.Normalized().Mode),
			"scope_paths": scope.Normalized().Paths,
		}
		if agentType != "" {
			data["agent_type"] = agentType
		}
		for k, v := range rejection.Data {
			data[k] = v
		}
		publishOverlayBaseFacts(gc, scope, rejection.Code)
		gc.PutRejectData(rejection.Code, data)
		return nil
	}
	publishOverlayBaseFacts(gc, scope, "")
	if _, err := ParseTaskMaxToolLoopsFromArgs(args); err != nil {
		return err
	}
	active, err := workeroutcomes.ParentSessionInFlightWorkers(ctx, deps.Workers, sess.ProjectID, sess.ID)
	if err != nil {
		return err
	}
	gc.ActiveWorkerCount = int64(len(active))
	gc.WorkersIdle = len(active) == 0
	reads, writes := CountInFlightByMode(active)
	gc.ActiveReadCount, gc.ActiveWriteCount = int64(reads), int64(writes)
	gc.WorkerSpawnBlocked = len(active) >= caps.MaxTotal || TaskScopeCapExceeded(active, scope, caps)
	if !gc.WorkerSpawnBlocked {
		return nil
	}
	observeCoordinatorTaskScope(gc, CoordinatorWorkerInFlightCode, scope, active, caps, agentType, "")
	return nil
}

func publishRepoEmptyReadOnlyFacts(
	ctx context.Context,
	deps WorkerCycleGuardDeps,
	sess *api.Session,
	agentType string,
	gc *oar.GuardContext,
) {
	if surveys, ok := prompts.AgentSurveysProjectTree(agentType); ok {
		gc.ProfileSurveysProjectTree = surveys
	}
	if deps.RepoKnownEmpty == nil || sess == nil {
		return
	}
	gc.RepoKnownEmpty = deps.RepoKnownEmpty(ctx, strings.TrimSpace(sess.WorkspacePath))
}

func publishTaskScopeFacts(gc *oar.GuardContext, scope api.TaskScope, caps ParallelTaskCaps, agentType string) {
	n := scope.Normalized()
	gc.ScopeMode = string(n.Mode)
	if capable, ok := prompts.AgentMutationCapable(agentType); ok {
		gc.ProfileMutationCapable = capable
	}
	gc.MaxWorkers = int64(caps.MaxTotal)
	gc.MaxReadWorkers = int64(caps.MaxRead)
	gc.MaxWriteWorkers = int64(caps.MaxWrite)
	gc.BaseOverlayID = strings.TrimSpace(n.BaseOverlayID)
}

func publishOverlayBaseFacts(gc *oar.GuardContext, scope api.TaskScope, code string) {
	gc.BaseOverlayChecked = true
	gc.BaseOverlayID = strings.TrimSpace(scope.Normalized().BaseOverlayID)
	switch code {
	case "":
		gc.BaseOverlayResolves = gc.BaseOverlayID != ""
		gc.BaseOverlayPending = gc.BaseOverlayID != ""
	case OverlayBaseMissingCode:
		gc.BaseOverlayResolves = false
		gc.BaseOverlayPending = false
	case OverlayBaseNotPendingCode:
		gc.BaseOverlayResolves = true
		gc.BaseOverlayPending = false
	}
}

// observeCoordinatorTaskScope publishes one task rejection.
func observeCoordinatorTaskScope(
	gc *oar.GuardContext,
	code string,
	scope api.TaskScope,
	active []api.WorkerTask,
	caps ParallelTaskCaps,
	agentType string,
	reason string,
) {
	publishTaskScopeFacts(gc, scope, caps, agentType)
	data := map[string]any{
		"scope_mode":  string(scope.Normalized().Mode),
		"scope_paths": scope.Normalized().Paths,
	}
	if agentType != "" {
		data["agent_type"] = agentType
	}
	if active != nil {
		data["active_count"] = len(active)
		gc.ActiveWorkerCount = int64(len(active))
		gc.WorkersIdle = len(active) == 0
	}
	if code == CoordinatorWorkerInFlightCode {
		data["max_workers"] = caps.MaxTotal
		data["active_read_count"] = gc.ActiveReadCount
		data["active_write_count"] = gc.ActiveWriteCount
		data["max_read_workers"] = caps.MaxRead
		data["max_write_workers"] = caps.MaxWrite
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		data["reason"] = reason
	}
	gc.PutRejectData(code, data)
}

func parallelTaskCaps(ctx context.Context, deps WorkerCycleGuardDeps, sess *api.Session) ParallelTaskCaps {
	maxTotal := 0
	maxRead := 0
	maxWrite := 0
	if deps.MaxWorkers != nil && sess != nil {
		maxTotal = deps.MaxWorkers(ctx, sess.ID)
	}
	if deps.MaxReadWorkers != nil && sess != nil {
		maxRead = deps.MaxReadWorkers(ctx, sess.ID)
	}
	if deps.MaxWriteWorkers != nil && sess != nil {
		maxWrite = deps.MaxWriteWorkers(ctx, sess.ID)
	}
	return ResolveParallelTaskCaps(maxTotal, maxRead, maxWrite)
}

func taskAgentType(args map[string]any) string {
	if args == nil {
		return ""
	}
	agentType, _ := args["agent_type"].(string)
	return strings.TrimSpace(agentType)
}
