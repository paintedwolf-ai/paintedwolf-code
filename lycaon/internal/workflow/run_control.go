package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *RunManager) rootRun(ctx context.Context, run *api.WorkflowRun) *api.WorkflowRun {
	root := run
	seen := map[string]struct{}{}
	for root != nil && root.ParentRunID != nil && strings.TrimSpace(*root.ParentRunID) != "" {
		if _, ok := seen[root.ID]; ok {
			break
		}
		seen[root.ID] = struct{}{}
		parent, err := m.Store.Get(ctx, strings.TrimSpace(*root.ParentRunID))
		if err != nil || parent == nil {
			break
		}
		root = parent
	}
	return root
}

func (m *RunManager) afterRunCanceled(ctx context.Context, run *api.WorkflowRun, reason string) {
	if run == nil {
		return
	}
	now := time.Now().UTC()
	run.Status = api.WorkflowRunStatusCanceled
	run.PauseReason = reason
	run.CompletedAt = &now
	run.UpdatedAt = now
	run.Revision++
	m.convergePendingTeardowns(ctx)
	if runHasBlueprint(run) {
		_ = m.syncBlueprintTranscriptForRun(ctx, run, false)
	}
	m.publishSession(ctx, run)
}

// Exit terminates the reviewed run lineage.
func (m *RunManager) Exit(ctx context.Context, sessionID, runID string, expectedRevision int64, reason string) (*api.WorkflowRun, error) {
	target, err := m.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if !workflowdef.RunHasParent(target) && m.SessionExit != nil {
		var exited *api.WorkflowRun
		err := m.SessionExit.WithSessionTreeStop(ctx, sessionID, reason, func(stopCtx context.Context) error {
			var exitErr error
			exited, exitErr = m.exitRun(withWorkflowStopRepair(stopCtx), sessionID, runID, expectedRevision, reason)
			return exitErr
		})
		return exited, err
	}
	return m.exitRun(ctx, sessionID, runID, expectedRevision, reason)
}

func (m *RunManager) exitRun(ctx context.Context, sessionID, runID string, expectedRevision int64, reason string) (*api.WorkflowRun, error) {
	target, err := m.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if target.SessionID != sessionID {
		return nil, ErrRunNotFound
	}
	if target.Revision != expectedRevision {
		return nil, fmt.Errorf("%w: run %s expected revision %d, actual %d", ErrRunRevisionConflict, runID, expectedRevision, target.Revision)
	}
	if reason != exitReasonSupersededByWorkflowStart {
		m.clearSessionWorkflowStartState(ctx, sessionID)
	}
	if target.ParentRunID != nil && strings.TrimSpace(*target.ParentRunID) != "" {
		run, err := m.cancelRun(ctx, target.ID, expectedRevision, reason, true)
		if err != nil {
			return nil, err
		}
		return m.resumeParentAfterChildExit(ctx, run, string(api.WorkflowRunStatusCanceled))
	}
	wasCatalog := !m.IsAmbientRun(target)
	posture := m.baselinePostureForRun(ctx, target, api.SessionPostureSpec)
	teardowns, err := m.activeTeardownIntents(ctx, sessionID, reason)
	if err != nil {
		return nil, err
	}
	runs, err := m.Store.CancelActiveTree(ctx, target, reason, posture, teardowns)
	if err != nil {
		return nil, err
	}
	for i := range runs {
		m.afterRunCanceled(ctx, &runs[i], reason)
	}
	target, err = m.loadRun(ctx, target.ID)
	if err != nil {
		return nil, err
	}
	if wasCatalog && reason != exitReasonSupersededByWorkflowStart {
		// The exit already committed, so a failed respawn is not repairable by
		// retrying it.
		if _, err := m.EnsureSessionWorkflow(ctx, sessionID); err != nil {
			slog.ErrorContext(ctx, "respawn ambient run after catalog workflow exit",
				"session_id", sessionID, "run_id", target.ID, "err", err)
		}
	}
	return target, nil
}

// Pause soft-stops a run: holds pending workers, allows advance/skip.
func (m *RunManager) Pause(ctx context.Context, runID, reason string) (*api.WorkflowRun, error) {
	unlock := m.lockRunVars(runID)
	defer unlock()
	payload := struct {
		Reason string `json:"reason"`
	}{Reason: reason}
	if replayed, ok, err := m.replayCommand(ctx, runID, "pause", payload); err != nil || ok {
		return replayed, err
	}
	run, err := m.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := verifyExpectedRevision(ctx, run); err != nil {
		return nil, err
	}
	if run.Status == api.WorkflowRunStatusPaused {
		return run, nil
	}
	if run.Status != api.WorkflowRunStatusRunning {
		return nil, &NotRunnableError{RunID: runID, Status: run.Status, Reason: string(run.Status)}
	}
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	workers := workflowWorkerMutation{HoldPending: true}
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
	boundary := newCommandBoundary(run, sourceRevision, "paused", run.CurrentPhase, reason)
	teardown := newWorkflowTeardownIntent(run.ID, sourceRevision, workerCancelNone, false, reason)
	if workers.CancelRunning {
		teardown = newWorkflowTeardownIntent(run.ID, sourceRevision, workerCancelRunning, false, reason)
	}
	if err := m.commitCommand(ctx, run, "pause", payload, nil, &boundary, "", workers, teardown); err != nil {
		return nil, err
	}
	if m.WorkerStop != nil && workers.HoldPending {
		if err := m.WorkerStop.HoldPendingWorkersByRunID(ctx, runID); err != nil {
			slog.ErrorContext(ctx, "converge held workflow workers", "run_id", runID, "err", err)
		}
	}
	if workers.CancelRunning {
		m.convergePendingTeardowns(ctx)
	}
	m.publishSession(ctx, run)
	return run, nil
}

// Resume returns a paused run to running.
func (m *RunManager) Resume(ctx context.Context, runID string) (*api.WorkflowRun, error) {
	unlock := m.lockRunVarsOnce(runID)
	defer unlock()
	if replayed, ok, err := m.replayCommand(ctx, runID, "resume", struct{}{}); err != nil || ok {
		return replayed, err
	}
	run, err := m.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := verifyExpectedRevision(ctx, run); err != nil {
		return nil, err
	}
	if run.Status == api.WorkflowRunStatusRunning {
		return run, nil
	}
	if IsTerminal(run.Status) {
		return nil, &NotRunnableError{RunID: runID, Status: run.Status, Reason: string(run.Status)}
	}
	if run.Status != api.WorkflowRunStatusPaused {
		return nil, ErrInvalidTransition
	}
	run.Status = api.WorkflowRunStatusRunning
	run.PauseReason = ""
	run.PausedAt = nil
	run.UpdatedAt = time.Now().UTC()
	sourceRevision := run.Revision
	boundary := newCommandBoundary(run, sourceRevision, "resumed", run.CurrentPhase, "")
	if err := m.commitCommand(ctx, run, "resume", struct{}{}, nil, &boundary, "", workflowWorkerMutation{ReleaseHeld: true}, nil); err != nil {
		return nil, err
	}
	m.publishSession(ctx, run)
	unlock()
	// Work may settle while paused without another completion event after resume.
	resumeCtx := withoutExpectedRevision(ctx)
	resumed, err := m.TryAutoAdvance(resumeCtx, run.ID)
	if err != nil {
		return nil, err
	}
	if resumed.Status == api.WorkflowRunStatusRunning && resumed.CurrentPhase == run.CurrentPhase && m.OnRunResumed != nil {
		m.OnRunResumed(resumeCtx, resumed)
	}
	return resumed, nil
}

// Cancel terminates a run and scopes worker/delegation teardown.
func (m *RunManager) Cancel(ctx context.Context, runID, reason string) (*api.WorkflowRun, error) {
	run, err := m.cancelRun(ctx, runID, 0, reason, true)
	if err != nil {
		return nil, err
	}
	if run.ParentRunID != nil && strings.TrimSpace(*run.ParentRunID) != "" {
		_, _ = m.resumeParentAfterChildExit(ctx, run, string(run.Status))
	}
	return run, nil
}

// Fail settles a host-held run after its topology machinery cannot continue.
func (m *RunManager) Fail(ctx context.Context, runID string, failure api.WorkflowFailure) (*api.WorkflowRun, error) {
	var didFail bool
	defer func() {
		if didFail {
			m.runVarsGuards.Delete(runID)
		}
	}()
	unlock := m.lockRunVars(runID)
	defer unlock()
	payload := struct {
		Failure api.WorkflowFailure `json:"failure"`
	}{Failure: failure}
	if replayed, ok, err := m.replayCommand(ctx, runID, "fail", payload); err != nil || ok {
		return replayed, err
	}
	run, err := m.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := verifyExpectedRevision(ctx, run); err != nil {
		return nil, err
	}
	if run.Status == api.WorkflowRunStatusFailed {
		return run, nil
	}
	if IsTerminal(run.Status) {
		return nil, &NotRunnableError{RunID: run.ID, Status: run.Status, Reason: string(run.Status)}
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
	boundary := newCommandBoundary(run, run.Revision, string(api.WorkflowBoundaryKindFailed), run.CurrentPhase, failure.Message)
	run.EndMessageID = boundary.ID

	workers := workflowWorkerMutation{CancelAll: true}
	abortDelegation := true
	if manifest, manifestErr := m.manifestForRun(ctx, run); manifestErr == nil {
		if ctrl := manifest.Controls.OnStop; ctrl != nil {
			workers.CancelAll = ctrl.CancelWorkers
			abortDelegation = ctrl.AbortDelegation
		}
	}
	scope := workerCancelNone
	if workers.CancelAll {
		scope = workerCancelAll
	}
	teardown := newWorkflowTeardownIntent(run.ID, run.Revision, scope, abortDelegation, failure.Code)
	var vars map[string]any
	if current, varsErr := m.Store.GetScaffoldVars(ctx, run.ID); varsErr != nil {
		return nil, varsErr
	} else if ask, ok := coordinatorAskPendingFromVars(current); ok {
		ask.State = coordinatorAskCanceled
		ask.ClosedReason = failure.Code
		vars = setCoordinatorAsk(current, ask)
	}
	if err := m.commitCommand(ctx, run, "fail", payload, vars, &boundary, "", workers, teardown); err != nil {
		return nil, err
	}
	didFail = true
	m.convergePendingTeardowns(ctx)
	if runHasBlueprint(run) {
		_ = m.syncBlueprintTranscriptForRun(ctx, run, false)
	}
	m.publishSession(ctx, run)
	if err := m.ReconcileTerminalRun(ctx, run); err != nil {
		return run, err
	}
	return run, nil
}

func (m *RunManager) cancelRun(ctx context.Context, runID string, expectedRevision int64, reason string, appendBoundary bool) (*api.WorkflowRun, error) {
	// Deferred cleanup runs after unlock so concurrent callers share one mutex.
	var didCancel bool
	defer func() {
		if didCancel {
			m.runVarsGuards.Delete(runID)
		}
	}()
	unlock := m.lockRunVars(runID)
	defer unlock()
	payload := struct {
		Reason   string `json:"reason"`
		Boundary bool   `json:"boundary"`
	}{Reason: reason, Boundary: appendBoundary}
	if replayed, ok, err := m.replayCommand(ctx, runID, "cancel", payload); err != nil || ok {
		return replayed, err
	}
	run, err := m.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if expectedRevision > 0 && run.Revision != expectedRevision {
		return nil, fmt.Errorf("%w: run %s expected revision %d, actual %d", ErrRunRevisionConflict, runID, expectedRevision, run.Revision)
	}
	if err := verifyExpectedRevision(ctx, run); err != nil {
		return nil, err
	}
	if run.Status == api.WorkflowRunStatusCanceled {
		return run, nil
	}
	if IsTerminal(run.Status) && run.Status != api.WorkflowRunStatusCanceled {
		return nil, &NotRunnableError{RunID: run.ID, Status: run.Status, Reason: string(run.Status)}
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
		msg := newCommandBoundary(run, run.Revision, "canceled", run.CurrentPhase, reason)
		boundary = &msg
		run.EndMessageID = msg.ID
	}
	// Persist worker state and cleanup intent together.
	workers := workflowWorkerMutation{CancelAll: true}
	abortDelegation := true
	if manifest, err := m.manifestForRun(ctx, run); err == nil {
		if ctrl := manifest.Controls.OnStop; ctrl != nil {
			workers.CancelAll = ctrl.CancelWorkers
			abortDelegation = ctrl.AbortDelegation
		}
	}
	scope := workerCancelNone
	if workers.CancelAll {
		scope = workerCancelAll
	}
	teardown := newWorkflowTeardownIntent(run.ID, run.Revision, scope, abortDelegation, reason)
	var vars map[string]any
	if current, varsErr := m.Store.GetScaffoldVars(ctx, run.ID); varsErr != nil {
		return nil, varsErr
	} else if ask, ok := coordinatorAskPendingFromVars(current); ok {
		ask.State = coordinatorAskCanceled
		ask.ClosedReason = strings.TrimSpace(reason)
		vars = setCoordinatorAsk(current, ask)
	}
	if err := m.commitCommand(ctx, run, "cancel", payload, vars, boundary, "", workers, teardown); err != nil {
		return nil, err
	}
	didCancel = true
	m.convergePendingTeardowns(ctx)
	// A held plan ask projects as its decision record.
	if runHasBlueprint(run) {
		_ = m.syncBlueprintTranscriptForRun(ctx, run, false)
	}
	m.publishSession(ctx, run)
	return run, nil
}

// IsTerminal reports whether a run has reached an absorbing status.
func IsTerminal(status api.WorkflowRunStatus) bool {
	switch status {
	case api.WorkflowRunStatusComplete, api.WorkflowRunStatusFailed, api.WorkflowRunStatusCanceled, api.WorkflowRunStatusInterrupted:
		return true
	default:
		return false
	}
}

type expectedRevisionContextKey struct{}

type approvalChannelContextKey struct{}

// Approval channels name how a person's blueprint approval arrived.
const (
	ApprovalChannelAPI  = "api"
	ApprovalChannelChat = "chat"
)

// WithExpectedRevision binds a human-reviewed run revision to one command.
func WithExpectedRevision(ctx context.Context, revision int64) context.Context {
	return context.WithValue(ctx, expectedRevisionContextKey{}, revision)
}

// WithApprovalChannel records how the approval being committed arrived.
func WithApprovalChannel(ctx context.Context, channel string) context.Context {
	return context.WithValue(ctx, approvalChannelContextKey{}, strings.TrimSpace(channel))
}

func withoutExpectedRevision(ctx context.Context) context.Context {
	return context.WithValue(ctx, expectedRevisionContextKey{}, int64(0))
}

func verifyExpectedRevision(ctx context.Context, run *api.WorkflowRun) error {
	expected, _ := ctx.Value(expectedRevisionContextKey{}).(int64)
	if expected <= 0 || run == nil || run.Revision == expected {
		return nil
	}
	return fmt.Errorf("%w: run %s expected revision %d, actual %d", ErrRunRevisionConflict, run.ID, expected, run.Revision)
}
