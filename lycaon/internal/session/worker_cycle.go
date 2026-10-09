package session

import (
	workflowfacts "github.com/lycaon/lycaon/internal/session/workflowfacts"

	"context"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

var workerCycleLog = observability.LazyComponent("worker_cycle")

const (
	CoordinatorWorkerInFlightCode = "COORDINATOR_WORKER_IN_FLIGHT"
)

// WorkerCycleLister loads worker jobs for coordinator cycle guards and outcome bridging.
type WorkerCycleLister interface {
	ListBySession(ctx context.Context, projectID, sessionID string, status ...api.WorkerStatus) ([]api.WorkerTask, error)
	ListPendingOutcomes(ctx context.Context) ([]api.WorkerTask, error)
	Get(jobID string) (*api.WorkerTask, bool)
	ClaimWorkerBranch(ctx context.Context, jobID string) (*api.WorkerTask, error)
}

// ParentSessionInFlightWorkers returns pending and running jobs for a coordinator parent.
func ParentSessionInFlightWorkers(
	ctx context.Context,
	list WorkerCycleLister,
	projectID, parentSessionID string,
) ([]api.WorkerTask, error) {
	if list == nil {
		return nil, nil
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	projectID = strings.TrimSpace(projectID)
	if parentSessionID == "" || projectID == "" {
		return nil, nil
	}
	return list.ListBySession(
		ctx,
		projectID,
		parentSessionID,
		api.WorkerStatusPending,
		api.WorkerStatusRunning,
	)
}

// ParentSessionPendingOverlayPromote returns completed write workers whose
// isolated overlays still await a merge decision.
func ParentSessionPendingOverlayPromote(
	ctx context.Context,
	list WorkerCycleLister,
	projectID, parentSessionID string,
) ([]api.WorkerTask, error) {
	if list == nil {
		return nil, nil
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	projectID = strings.TrimSpace(projectID)
	if parentSessionID == "" || projectID == "" {
		return nil, nil
	}
	tasks, err := list.ListBySession(ctx, projectID, parentSessionID, api.WorkerStatusComplete)
	if err != nil {
		return nil, err
	}
	var pending []api.WorkerTask
	for _, task := range tasks {
		if !task.EffectiveScope().IsWrite() || strings.TrimSpace(task.WorkspaceRoot) == "" {
			continue
		}
		if task.MergeStatus != api.WorkerMergeStatusPending {
			continue
		}
		pending = append(pending, task)
	}
	return pending, nil
}

// ParentSessionWorkerCycleIdle includes pending terminal delivery.
func ParentSessionWorkerCycleIdle(
	ctx context.Context,
	list WorkerCycleLister,
	projectID, parentSessionID, excludingJobID string,
) (bool, error) {
	if list == nil {
		return true, nil
	}
	active, err := ParentSessionInFlightWorkers(ctx, list, projectID, parentSessionID)
	if err != nil {
		return false, err
	}
	pending, err := list.ListPendingOutcomes(ctx)
	if err != nil {
		return false, err
	}
	for _, task := range pending {
		if task.ProjectID == projectID && task.ParentSessionID == parentSessionID {
			active = append(active, task)
		}
	}
	exclude := strings.TrimSpace(excludingJobID)
	for _, task := range active {
		if exclude != "" && task.ID == exclude {
			continue
		}
		return false, nil
	}
	return true, nil
}

// CoordinatorTaskConcurrencyCap returns the max pending or running task() jobs for a parent session.
func CoordinatorTaskConcurrencyCap(maxWorkers int) int {
	if maxWorkers > 0 {
		return maxWorkers
	}
	return spawn.MaxInFlightTaskWorkers
}

// SetWorkerQueue wires the worker job ledger for coordinator worker-cycle guards.
func (m *Manager) SetWorkerQueue(q WorkerCycleLister) {
	if m != nil {
		m.workerQueue = q
	}
}

func (m *Manager) workerCycleGuardDeps() WorkerCycleGuardDeps {
	if m == nil {
		return WorkerCycleGuardDeps{}
	}
	return WorkerCycleGuardDeps{
		Workers: m.workerQueue,
		MaxWorkers: func(ctx context.Context, sessionID string) int {
			if m.workflows == nil {
				return 0
			}
			return m.workflows.Ambient.ParallelTaskMaxWorkers(ctx, sessionID)
		},
		MaxReadWorkers: func(ctx context.Context, sessionID string) int {
			if m.workflows == nil {
				return 0
			}
			return m.workflows.Ambient.ParallelTaskMaxReadWorkers(ctx, sessionID)
		},
		MaxWriteWorkers: func(ctx context.Context, sessionID string) int {
			if m.workflows == nil {
				return 0
			}
			return m.workflows.Ambient.ParallelTaskMaxWriteWorkers(ctx, sessionID)
		},
		PhaseGuardState: func(ctx context.Context, sessionID string) workflowfacts.WorkflowPhaseGuardState {
			if m.workflows == nil {
				return workflowfacts.WorkflowPhaseGuardState{}
			}
			return m.workflows.Policy.ActivePhaseGuardState(ctx, sessionID)
		},
		RepoKnownEmpty: func(ctx context.Context, workspacePath string) bool {
			return sessionWorkspaceKnownEmpty(ctx, m.repoProvider, workspacePath)
		},
	}
}

// ShouldNudgeCoordinatorLoopAfterWorkerTask decides whether a terminal job wakes coordination.
func (m *Manager) ShouldNudgeCoordinatorLoopAfterWorkerTask(
	ctx context.Context,
	parentID, projectID, completingJobID string,
) bool {
	if m == nil {
		return false
	}
	completingJobID = strings.TrimSpace(completingJobID)
	if completingJobID != "" && m.workerQueue != nil {
		if task, ok := m.workerQueue.Get(completingJobID); ok && task.EffectiveScope().IsWrite() {
			idle, err := ParentSessionWorkerCycleIdle(ctx, m.workerQueue, projectID, parentID, completingJobID)
			return err == nil && idle
		}
		return true
	}
	if m.workerQueue == nil {
		return true
	}
	idle, err := ParentSessionWorkerCycleIdle(ctx, m.workerQueue, projectID, parentID, "")
	return err == nil && idle
}

// ParentSessionMergedWriteChangedPaths returns deduplicated changed paths from merged write workers.
func ParentSessionMergedWriteChangedPaths(
	ctx context.Context,
	list WorkerCycleLister,
	projectID, parentSessionID string,
) []string {
	if list == nil {
		return nil
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	projectID = strings.TrimSpace(projectID)
	if parentSessionID == "" || projectID == "" {
		return nil
	}
	tasks, err := list.ListBySession(ctx, projectID, parentSessionID, api.WorkerStatusComplete)
	if err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, task := range tasks {
		if task.MergeStatus != api.WorkerMergeStatusMerged {
			continue
		}
		if !task.EffectiveScope().IsWrite() {
			continue
		}
		var paths []string
		if task.Result != nil && task.Result.ChangeReport != nil {
			paths = task.Result.ChangeReport.ChangedPaths
		}
		for _, p := range paths {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// BuildImplementSessionState returns coordinator turn axes from the worker job ledger.
func (m *Manager) BuildImplementSessionState(ctx context.Context, sess *api.Session) surface.ImplementSessionState {
	if m == nil || sess == nil {
		return surface.ImplementSessionState{}
	}
	state := surface.ImplementSessionState{}
	if m.workerQueue != nil {
		if active, err := ParentSessionInFlightWorkers(ctx, m.workerQueue, sess.ProjectID, sess.ID); err == nil {
			state.WorkersInFlight = len(active)
		}
		pending, err := ParentSessionPendingOverlayPromote(ctx, m.workerQueue, sess.ProjectID, sess.ID)
		if err != nil {
			workerCycleLog.Warn("pending overlay list failed",
				"session_id", sess.ID, "project_id", sess.ProjectID, "err", err)
		} else {
			ids := make([]string, 0, len(pending))
			var paths []string
			for i := range pending {
				id := strings.TrimSpace(pending[i].ID)
				if id == "" {
					continue
				}
				ids = append(ids, id)
				paths = append(paths, OverlayWorkspaceChangedPaths(ctx, &pending[i], nil)...)
			}
			state.PendingOverlayIDs = ids
			state.PendingOverlayPaths = paths
		}
	}
	if m.workflows != nil {
		if vars, err := m.workflows.Policy.ScaffoldVarsForSession(ctx, sess.ID); err == nil {
			b := batch.Read(vars)
			state.BatchPhase = b.Phase
			state.BatchSeq = b.Seq
			state.PendingUserInput = scaffoldvars.HasPendingUserInput(vars)
		}
	}
	if m.store != nil {
		msgs, err := m.store.GetMessages(ctx, sess.ID)
		if err == nil {
			progressContent := ""
			if m.progress != nil {
				progressContent = m.progress.Get(ctx, RootSessionID(ctx, m.store, sess.ID))
			}
			verifyRequired, verifyPassed, verifyRepair, verifyUnverified := m.workflowVerifyGateState(ctx, sess, msgs)
			state.WrapupGatesLoaded = true
			state.VerifyUnverified = verifyUnverified
			if m.verifyConfig != nil {
				state.VerifyCommand = strings.TrimSpace(m.verifyConfig.VerifyTestCommand(m.overlayProjectDir(ctx, sess)))
			}
			state.BatchReadyForSynthesis = BatchReadyForSynthesis(state, msgs, progressContent, verifyRequired, verifyPassed || verifyUnverified)
			state.OpenRepairSinceUserIntent = OpenRepairSinceUserIntent(msgs, verifyRepair)
			state.ProgressOpenCount = progress.OpenStepCount(progressContent)
			state.ProgressMissing = progress.ProgressMissing(progressContent)
			since := api.UserIntentBoundary(msgs)
			state.ProgressGatedToolAttemptedSinceIntent = progress.ProgressGatedToolAttemptedSinceBoundary(msgs, since)
		}
	}
	return state
}
