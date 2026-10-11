package workeroutcomes

import (
	"context"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/pkg/api"
)

type CycleLedger interface {
	ListBySession(context.Context, string, string, ...api.WorkerStatus) ([]api.WorkerTask, error)
	ListPendingOutcomes(context.Context) ([]api.WorkerTask, error)
	Get(string) (*api.WorkerTask, bool)
}

type SessionMessages interface {
	Get(context.Context, string) (*api.Session, error)
	GetMessages(context.Context, string) ([]api.Message, error)
}

type WorkflowVars interface {
	ScaffoldVarsForSession(context.Context, string) (map[string]any, error)
}
type ProgressReader interface {
	Get(context.Context, string) string
}
type VerificationFacts interface {
	WorkflowGateState(context.Context, *api.Session, []api.Message) (bool, bool, bool, bool)
	SourceVerifyCommand(context.Context, string) string
}
type WorkspacePath interface {
	SettingsPath(context.Context, *api.Session) string
}

type State struct {
	workers        CycleLedger
	sessions       SessionMessages
	workflows      WorkflowVars
	progress       ProgressReader
	verification   VerificationFacts
	workspace      WorkspacePath
	overlayChanges func(context.Context, *api.WorkerTask, []projectroot.RootRef) []string
}

type StatePorts struct {
	Workers        CycleLedger
	Sessions       SessionMessages
	Workflows      WorkflowVars
	Progress       ProgressReader
	Verification   VerificationFacts
	Workspace      WorkspacePath
	OverlayChanges func(context.Context, *api.WorkerTask, []projectroot.RootRef) []string
}

func NewState(ports StatePorts) *State {
	return &State{workers: ports.Workers, sessions: ports.Sessions, workflows: ports.Workflows, progress: ports.Progress, verification: ports.Verification, workspace: ports.Workspace, overlayChanges: ports.OverlayChanges}
}
func (m *State) SetWorkers(workers CycleLedger)      { m.workers = workers }
func (m *State) SetWorkflows(workflows WorkflowVars) { m.workflows = workflows }
func (m *State) SetProgress(progress ProgressReader) { m.progress = progress }

var workerCycleLog = observability.LazyComponent("worker_cycle")

// ParentSessionInFlightWorkers returns pending and running jobs for a coordinator parent.
func ParentSessionInFlightWorkers(
	ctx context.Context,
	list CycleLedger,
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
	list CycleLedger,
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
	list CycleLedger,
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

// ShouldNudge decides whether a terminal job wakes coordination.
func (m *State) ShouldNudge(
	ctx context.Context,
	parentID, projectID, completingJobID string,
) bool {
	if m == nil {
		return false
	}
	completingJobID = strings.TrimSpace(completingJobID)
	if completingJobID != "" && m.workers != nil {
		if task, ok := m.workers.Get(completingJobID); ok && task.EffectiveScope().IsWrite() {
			idle, err := ParentSessionWorkerCycleIdle(ctx, m.workers, projectID, parentID, completingJobID)
			return err == nil && idle
		}
		return true
	}
	if m.workers == nil {
		return true
	}
	idle, err := ParentSessionWorkerCycleIdle(ctx, m.workers, projectID, parentID, "")
	return err == nil && idle
}

// ParentSessionMergedWriteChangedPaths returns deduplicated changed paths from merged write workers.
func ParentSessionMergedWriteChangedPaths(
	ctx context.Context,
	list CycleLedger,
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

// ForSession returns coordinator turn axes from the worker job ledger.
func (m *State) ForSession(ctx context.Context, sess *api.Session) surface.ImplementSessionState {
	if m == nil || sess == nil {
		return surface.ImplementSessionState{}
	}
	state := surface.ImplementSessionState{}
	if m.workers != nil {
		if active, err := ParentSessionInFlightWorkers(ctx, m.workers, sess.ProjectID, sess.ID); err == nil {
			state.WorkersInFlight = len(active)
		}
		pending, err := ParentSessionPendingOverlayPromote(ctx, m.workers, sess.ProjectID, sess.ID)
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
				if m.overlayChanges != nil {
					paths = append(paths, m.overlayChanges(ctx, &pending[i], nil)...)
				}
			}
			state.PendingOverlayIDs = ids
			state.PendingOverlayPaths = paths
		}
	}
	if m.workflows != nil {
		if vars, err := m.workflows.ScaffoldVarsForSession(ctx, sess.ID); err == nil {
			b := batch.Read(vars)
			state.BatchPhase = b.Phase
			state.BatchSeq = b.Seq
			state.PendingUserInput = scaffoldvars.HasPendingUserInput(vars)
		}
	}
	if m.sessions != nil {
		msgs, err := m.sessions.GetMessages(ctx, sess.ID)
		if err == nil {
			progressContent := ""
			if m.progress != nil {
				progressContent = m.progress.Get(ctx, sessiontree.RootID(ctx, m.sessions, sess.ID))
			}
			verifyRequired, verifyPassed, verifyRepair, verifyUnverified := false, false, false, false
			if m.verification != nil {
				verifyRequired, verifyPassed, verifyRepair, verifyUnverified = m.verification.WorkflowGateState(ctx, sess, msgs)
			}
			state.WrapupGatesLoaded = true
			state.VerifyUnverified = verifyUnverified
			if m.verification != nil && m.workspace != nil {
				state.VerifyCommand = strings.TrimSpace(m.verification.SourceVerifyCommand(ctx, m.workspace.SettingsPath(ctx, sess)))
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
