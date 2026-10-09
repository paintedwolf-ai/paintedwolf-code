package lifecycle

import (
	"context"
	"fmt"
	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"log/slog"
	"strings"
	"time"
)

// Commands owns pause, resume, cancellation, failure, and lineage exit.
type Commands struct {
	Runs         RunReader
	Trees        TreeCancellation
	Vars         *runstate.Variables
	Journal      *runstate.Journal
	Resolver     *workflowcatalog.Resolver
	Cleanup      *Cleanup
	Admission    *Admission
	Publication  Publication
	Plans        Plans
	Settlement   Settlement
	Scaffold     StartScaffold
	Asks         AskCancellation
	Phases       Phases
	Ambient      Ambient
	SessionExit  ExitBarrier
	OnRunResumed func(context.Context, *api.WorkflowRun)
}

func (m *Commands) RootRun(ctx context.Context, run *api.WorkflowRun) *api.WorkflowRun {
	root := run
	seen := map[string]struct{}{}
	for root != nil && root.ParentRunID != nil && strings.TrimSpace(*root.ParentRunID) != "" {
		if _, ok := seen[root.ID]; ok {
			break
		}
		seen[root.ID] = struct{}{}
		parent, err := m.Runs.Get(ctx, strings.TrimSpace(*root.ParentRunID))
		if err != nil || parent == nil {
			break
		}
		root = parent
	}
	return root
}

func (m *Commands) AfterCanceled(ctx context.Context, run *api.WorkflowRun, reason string) {
	if run == nil {
		return
	}
	now := time.Now().UTC()
	run.Status = api.WorkflowRunStatusCanceled
	run.PauseReason = reason
	run.CompletedAt = &now
	run.UpdatedAt = now
	run.Revision++
	m.Cleanup.Converge(ctx)
	if runstate.RunHasBlueprint(run) {
		_ = m.Plans.SyncTranscriptForRun(ctx, run, false)
	}
	m.Publication.PublishSession(ctx, run)
}

// Exit terminates the reviewed run lineage.
func (m *Commands) Exit(ctx context.Context, sessionID, runID string, expectedRevision int64, reason string) (*api.WorkflowRun, error) {
	target, err := m.Runs.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if !workflowdef.RunHasParent(target) && m.SessionExit != nil {
		var exited *api.WorkflowRun
		err := m.SessionExit.WithSessionTreeStop(ctx, sessionID, reason, func(stopCtx context.Context) error {
			var exitErr error
			exited, exitErr = m.exitRun(WithStopRepair(stopCtx), sessionID, runID, expectedRevision, reason)
			return exitErr
		})
		return exited, err
	}
	return m.exitRun(ctx, sessionID, runID, expectedRevision, reason)
}

func (m *Commands) exitRun(ctx context.Context, sessionID, runID string, expectedRevision int64, reason string) (*api.WorkflowRun, error) {
	target, err := m.Runs.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if target.SessionID != sessionID {
		return nil, runstate.ErrNotFound
	}
	if target.Revision != expectedRevision {
		return nil, fmt.Errorf("%w: run %s expected revision %d, actual %d", runstate.ErrRevisionConflict, runID, expectedRevision, target.Revision)
	}
	if reason != runstate.ExitReasonSupersededByWorkflowStart {
		m.Scaffold.ClearStartState(ctx, sessionID)
	}
	if target.ParentRunID != nil && strings.TrimSpace(*target.ParentRunID) != "" {
		run, err := m.CancelRun(ctx, target.ID, expectedRevision, reason, true)
		if err != nil {
			return nil, err
		}
		return m.Settlement.ResumeParentAfterChildExit(ctx, run, string(api.WorkflowRunStatusCanceled))
	}
	wasCatalog := !runstate.IsAmbientRun(target)
	posture := m.BaselinePosture(ctx, target, api.SessionPostureSpec)
	teardowns, err := m.Cleanup.ForSession(ctx, sessionID, reason)
	if err != nil {
		return nil, err
	}
	runs, err := m.Trees.CancelActiveTree(ctx, target, reason, posture, teardowns)
	if err != nil {
		return nil, err
	}
	for i := range runs {
		m.AfterCanceled(ctx, &runs[i], reason)
	}
	target, err = m.Runs.Get(ctx, target.ID)
	if err != nil {
		return nil, err
	}
	if wasCatalog && reason != runstate.ExitReasonSupersededByWorkflowStart {
		// The exit already committed, so a failed respawn is not repairable by
		// retrying it.
		if _, err := m.Ambient.EnsureSessionWorkflow(ctx, sessionID); err != nil {
			slog.ErrorContext(ctx, "respawn ambient run after catalog workflow exit",
				"session_id", sessionID, "run_id", target.ID, "err", err)
		}
	}
	return target, nil
}

// Pause soft-stops a run: holds pending workers, allows advance/skip.
func (m *Commands) Pause(ctx context.Context, runID, reason string) (*api.WorkflowRun, error) {
	unlock := m.Vars.Lock(runID)
	defer unlock()
	payload := struct {
		Reason string `json:"reason"`
	}{Reason: reason}
	if replayed, ok, err := m.Journal.Replay(ctx, runID, "pause", payload); err != nil || ok {
		return replayed, err
	}
	run, err := m.Runs.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := runstate.VerifyExpectedRevision(ctx, run); err != nil {
		return nil, err
	}
	if run.Status == api.WorkflowRunStatusPaused {
		return run, nil
	}
	if run.Status != api.WorkflowRunStatusRunning {
		return nil, &runstate.NotRunnableError{RunID: runID, Status: run.Status, Reason: string(run.Status)}
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	workers := runstate.WorkerMutation{HoldPending: true}
	if ctrl := manifest.Controls.OnPause; ctrl != nil {
		workers.HoldPending = ctrl.HoldPending
		workers.CancelRunning = ctrl.CancelRunning
	}
	now := time.Now().UTC()
	run.Status = api.WorkflowRunStatusPaused
	run.PauseReason = reason
	run.PausedAt = &now
	run.UpdatedAt = now
	sourceRevision := run.Revision
	boundary := runstate.NewCommandBoundary(run, sourceRevision, "paused", run.CurrentPhase, reason)
	teardown := runstate.NewTeardownIntent(run.ID, sourceRevision, runstate.WorkerCancelNone, false, reason)
	if workers.CancelRunning {
		teardown = runstate.NewTeardownIntent(run.ID, sourceRevision, runstate.WorkerCancelRunning, false, reason)
	}
	if err := m.Journal.Commit(ctx, run, "pause", payload, nil, &boundary, "", workers, teardown); err != nil {
		return nil, err
	}
	if m.Cleanup.Workers != nil && workers.HoldPending {
		if err := m.Cleanup.Workers.HoldPendingWorkersByRunID(ctx, runID); err != nil {
			slog.ErrorContext(ctx, "converge held workflow workers", "run_id", runID, "err", err)
		}
	}
	if workers.CancelRunning {
		m.Cleanup.Converge(ctx)
	}
	m.Publication.PublishSession(ctx, run)
	return run, nil
}

// Resume returns a paused run to running.
func (m *Commands) Resume(ctx context.Context, runID string) (*api.WorkflowRun, error) {
	unlock := m.Vars.LockOnce(runID)
	defer unlock()
	if replayed, ok, err := m.Journal.Replay(ctx, runID, "resume", struct{}{}); err != nil || ok {
		return replayed, err
	}
	run, err := m.Runs.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := runstate.VerifyExpectedRevision(ctx, run); err != nil {
		return nil, err
	}
	if run.Status == api.WorkflowRunStatusRunning {
		return run, nil
	}
	if runstate.IsTerminal(run.Status) {
		return nil, &runstate.NotRunnableError{RunID: runID, Status: run.Status, Reason: string(run.Status)}
	}
	if run.Status != api.WorkflowRunStatusPaused {
		return nil, runstate.ErrInvalidTransition
	}
	if m.Resolver != nil {
		if _, err := m.Resolver.ForRun(ctx, run); err != nil {
			return nil, err
		}
	}
	run.Status = api.WorkflowRunStatusRunning
	run.PauseReason = ""
	run.PausedAt = nil
	run.UpdatedAt = time.Now().UTC()
	sourceRevision := run.Revision
	boundary := runstate.NewCommandBoundary(run, sourceRevision, "resumed", run.CurrentPhase, "")
	if err := m.Journal.Commit(ctx, run, "resume", struct{}{}, nil, &boundary, "", runstate.WorkerMutation{ReleaseHeld: true}, nil); err != nil {
		return nil, err
	}
	m.Publication.PublishSession(ctx, run)
	unlock()
	// Work may settle while paused without another completion event after resume.
	resumeCtx := runstate.WithoutExpectedRevision(ctx)
	resumed, err := m.Phases.TryAutoAdvance(resumeCtx, run.ID)
	if err != nil {
		return nil, err
	}
	if resumed.Status == api.WorkflowRunStatusRunning && resumed.CurrentPhase == run.CurrentPhase && m.OnRunResumed != nil {
		m.OnRunResumed(resumeCtx, resumed)
	}
	return resumed, nil
}

// Cancel terminates a run and scopes worker/delegation teardown.
func (m *Commands) Cancel(ctx context.Context, runID, reason string) (*api.WorkflowRun, error) {
	run, err := m.CancelRun(ctx, runID, 0, reason, true)
	if err != nil {
		return nil, err
	}
	if run.ParentRunID != nil && strings.TrimSpace(*run.ParentRunID) != "" {
		_, _ = m.Settlement.ResumeParentAfterChildExit(ctx, run, string(run.Status))
	}
	return run, nil
}

// Fail settles a host-held run after its topology machinery cannot continue.
func (m *Commands) Fail(ctx context.Context, runID string, failure api.WorkflowFailure) (*api.WorkflowRun, error) {
	var didFail bool
	defer func() {
		if didFail {
			m.Vars.Forget(runID)
		}
	}()
	unlock := m.Vars.Lock(runID)
	defer unlock()
	payload := struct {
		Failure api.WorkflowFailure `json:"failure"`
	}{Failure: failure}
	if replayed, ok, err := m.Journal.Replay(ctx, runID, "fail", payload); err != nil || ok {
		return replayed, err
	}
	run, err := m.Runs.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := runstate.VerifyExpectedRevision(ctx, run); err != nil {
		return nil, err
	}
	if run.Status == api.WorkflowRunStatusFailed {
		return run, nil
	}
	if runstate.IsTerminal(run.Status) {
		return nil, &runstate.NotRunnableError{RunID: run.ID, Status: run.Status, Reason: string(run.Status)}
	}
	if strings.TrimSpace(failure.Code) == "" || strings.TrimSpace(failure.Message) == "" {
		return nil, fmt.Errorf("workflow failure requires code and message")
	}
	now := time.Now().UTC()
	run.Status = api.WorkflowRunStatusFailed
	run.Failure = &failure
	run.PauseReason = ""
	run.PausedAt = nil
	run.CompletedAt = &now
	run.UpdatedAt = now
	boundary := runstate.NewCommandBoundary(run, run.Revision, string(api.WorkflowBoundaryKindFailed), run.CurrentPhase, failure.Message)
	run.EndMessageID = boundary.ID

	workers := runstate.WorkerMutation{CancelAll: true}
	abortDelegation := true
	if manifest, manifestErr := m.Resolver.ForRun(ctx, run); manifestErr == nil {
		if ctrl := manifest.Controls.OnStop; ctrl != nil {
			workers.CancelAll = ctrl.CancelWorkers
			abortDelegation = ctrl.AbortDelegation
		}
	}
	scope := runstate.WorkerCancelNone
	if workers.CancelAll {
		scope = runstate.WorkerCancelAll
	}
	teardown := runstate.NewTeardownIntent(run.ID, run.Revision, scope, abortDelegation, failure.Code)
	var vars map[string]any
	if current, varsErr := m.Runs.GetScaffoldVars(ctx, run.ID); varsErr != nil {
		return nil, varsErr
	} else {
		vars = m.Asks.CancelPendingAsk(current, failure.Code)
	}
	if err := m.Journal.Commit(ctx, run, "fail", payload, vars, &boundary, "", workers, teardown); err != nil {
		return nil, err
	}
	didFail = true
	m.Cleanup.Converge(ctx)
	if runstate.RunHasBlueprint(run) {
		_ = m.Plans.SyncTranscriptForRun(ctx, run, false)
	}
	m.Publication.PublishSession(ctx, run)
	if err := m.Settlement.ReconcileTerminalRun(ctx, run); err != nil {
		return run, err
	}
	return run, nil
}

func (m *Commands) CancelRun(ctx context.Context, runID string, expectedRevision int64, reason string, appendBoundary bool) (*api.WorkflowRun, error) {
	// Deferred cleanup runs after unlock so concurrent callers share one mutex.
	var didCancel bool
	defer func() {
		if didCancel {
			m.Vars.Forget(runID)
		}
	}()
	unlock := m.Vars.Lock(runID)
	defer unlock()
	payload := struct {
		Reason   string `json:"reason"`
		Boundary bool   `json:"boundary"`
	}{Reason: reason, Boundary: appendBoundary}
	if replayed, ok, err := m.Journal.Replay(ctx, runID, "cancel", payload); err != nil || ok {
		return replayed, err
	}
	run, err := m.Runs.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if expectedRevision > 0 && run.Revision != expectedRevision {
		return nil, fmt.Errorf("%w: run %s expected revision %d, actual %d", runstate.ErrRevisionConflict, runID, expectedRevision, run.Revision)
	}
	if err := runstate.VerifyExpectedRevision(ctx, run); err != nil {
		return nil, err
	}
	if run.Status == api.WorkflowRunStatusCanceled {
		return run, nil
	}
	if runstate.IsTerminal(run.Status) && run.Status != api.WorkflowRunStatusCanceled {
		return nil, &runstate.NotRunnableError{RunID: run.ID, Status: run.Status, Reason: string(run.Status)}
	}
	now := time.Now().UTC()
	run.Status = api.WorkflowRunStatusCanceled
	if reason != "" {
		run.PauseReason = reason
	}
	run.CompletedAt = &now
	run.UpdatedAt = now
	var boundary *api.Message
	if appendBoundary {
		msg := runstate.NewCommandBoundary(run, run.Revision, "canceled", run.CurrentPhase, reason)
		boundary = &msg
		run.EndMessageID = msg.ID
	}
	// Persist worker state and cleanup intent together.
	workers := runstate.WorkerMutation{CancelAll: true}
	abortDelegation := true
	if manifest, err := m.Resolver.ForRun(ctx, run); err == nil {
		if ctrl := manifest.Controls.OnStop; ctrl != nil {
			workers.CancelAll = ctrl.CancelWorkers
			abortDelegation = ctrl.AbortDelegation
		}
	}
	scope := runstate.WorkerCancelNone
	if workers.CancelAll {
		scope = runstate.WorkerCancelAll
	}
	teardown := runstate.NewTeardownIntent(run.ID, run.Revision, scope, abortDelegation, reason)
	var vars map[string]any
	if current, varsErr := m.Runs.GetScaffoldVars(ctx, run.ID); varsErr != nil {
		return nil, varsErr
	} else {
		vars = m.Asks.CancelPendingAsk(current, strings.TrimSpace(reason))
	}
	if err := m.Journal.Commit(ctx, run, "cancel", payload, vars, boundary, "", workers, teardown); err != nil {
		return nil, err
	}
	didCancel = true
	m.Cleanup.Converge(ctx)
	// A held plan ask projects as its decision record.
	if runstate.RunHasBlueprint(run) {
		_ = m.Plans.SyncTranscriptForRun(ctx, run, false)
	}
	m.Publication.PublishSession(ctx, run)
	return run, nil
}

func (m *Commands) BaselinePosture(ctx context.Context, active *api.WorkflowRun, fallback api.SessionPosture) api.SessionPosture {
	if active == nil {
		return fallback
	}
	vars, err := m.Runs.GetScaffoldVars(ctx, active.ID)
	if err != nil {
		return fallback
	}
	raw, _ := vars[runstate.BaselinePostureKey].(string)
	if sessionposture.ValidSessionPosture(raw) {
		return api.SessionPosture(raw)
	}
	return fallback
}
