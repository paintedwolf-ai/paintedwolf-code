package bgprocess

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/indexwatch"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/pkg/api"
)

// processCloseTimeout bounds shutdown even when a leader cannot be killed.
const processCloseTimeout = exec.TerminateGrace + exec.PipelineWaitDelay + 3*time.Second

// Promote makes a job visible and publishes buffered output and any observed exit.
func (r *Output) Promote(ctx context.Context, sessionID, handle string) error {
	proc, err := r.jobs.lookup(sessionID, handle)
	if err != nil {
		return err
	}
	r.jobs.mu.Lock()
	if !proc.silent {
		r.jobs.mu.Unlock()
		return nil
	}
	proc.silent = false
	workDir := r.jobs.workDirs[proc.Handle]
	teardownReason, admitted := r.jobs.checkAdmissionLocked(proc.ProjectID, proc.SessionID, proc.RootSessionID, workDir)
	var (
		killAsync  *exec.AsyncPipeline
		killPTY    exec.PTY
		killScreen *ptyScreen
		killCancel context.CancelFunc
	)
	if !admitted && proc.running && !proc.hasExit && !proc.stopped {
		proc.stopped = true
		proc.reason = teardownReason
		killAsync = proc.async
		killPTY = proc.pty
		killScreen = proc.screen
		killCancel = proc.cancel
	}
	running := proc.running && admitted
	startEv, emitStart := r.jobs.indexProjectProcessLocked(proc)
	publishProject := r.publishProject
	r.jobs.mu.Unlock()
	if !admitted {
		if killAsync != nil {
			killAsync.Kill()
		}
		if killPTY != nil {
			_ = killPTY.Close()
		}
		if killScreen != nil {
			killScreen.Close()
		}
		if killCancel != nil {
			killCancel()
		}
	}
	if emitStart && publishProject != nil {
		publishProject(ctx, startEv)
	}
	if running {
		r.sweeper.onJobStarted() //nolint:contextcheck // The sweeper loop outlives this call and holds its own context.
	}
	if r.publish != nil {
		proc.publishMu.Lock()
		projection := r.projectWindow(ctx, proc)
		tail := projection.Text
		if !projection.Screened && !projection.Empty {
			tail = captureUnavailableText
		}
		if tail != "" {
			proc.safePublished = tail
		}
		proc.publishMu.Unlock()
		if tail != "" {
			r.publish(ctx, proc.ProjectID, proc.SessionID, api.BackgroundProcessEvent{
				ProcessID: proc.Handle,
				SessionID: proc.SessionID,
				Stream:    "stdout",
				Text:      tail,
				EndOffset: proc.buffer.NextCursor(),
				Running:   running,
				Reset:     true,
			})
		}
	}
	r.publishTerminal(ctx, proc)
	if running && len(proc.facts.Refusals().Refusals) > 0 {
		r.refusalObserved(context.WithoutCancel(ctx), proc)
	}
	return nil
}

// WatchIndex hands the process the Git index captured before it spawned. A
// process that already published its exit releases the capture at once.
func (r *ProcessLifecycle) WatchIndex(sessionID, handle string, snapshot indexwatch.Snapshot) {
	proc, err := r.jobs.lookup(sessionID, handle)
	if err != nil || proc == nil {
		snapshot.Release()
		return
	}
	r.jobs.mu.Lock()
	defer r.jobs.mu.Unlock()
	if proc.terminalPublished || proc.discarded {
		snapshot.Release()
		return
	}
	proc.resources.indexWatch.Release()
	proc.resources.indexWatch = snapshot
}

// TakeIndexWatch returns the process's index capture to an awaiting caller.
func (r *ProcessLifecycle) TakeIndexWatch(sessionID, handle string) indexwatch.Snapshot {
	proc, err := r.jobs.lookup(sessionID, handle)
	if err != nil || proc == nil {
		return indexwatch.Snapshot{}
	}
	r.jobs.mu.Lock()
	defer r.jobs.mu.Unlock()
	snapshot := proc.resources.indexWatch
	proc.resources.indexWatch = indexwatch.Snapshot{}
	return snapshot
}

// Discard removes a finished inline await job.
func (r *ProcessLifecycle) Discard(sessionID, handle string) {
	r.jobs.remove(trim(sessionID), trim(handle))
}

// Stop requests process-tree termination and returns the currently observed exit state.
func (r *ProcessLifecycle) Stop(sessionID, handle string, reason TerminationReason) (*api.BackgroundProcessStopResult, error) {
	proc, err := r.jobs.lookup(sessionID, handle)
	if err != nil {
		return nil, err
	}
	r.killProcess(proc, reason)
	r.sweeper.onJobEnded()
	running, hasExit, exitCode, settledReason := r.jobs.exitState(proc)
	out := &api.BackgroundProcessStopResult{
		ProcessID:         handle,
		StopRequested:     true,
		Running:           running,
		TerminationReason: api.BackgroundProcessTerminationReason(settledReason),
	}
	if hasExit {
		out.ExitCode = &exitCode
	}
	return out, nil
}

func (r *ProcessLifecycle) killProcess(proc *Process, reason TerminationReason) {
	if proc == nil {
		return
	}
	r.jobs.mu.Lock()
	if !proc.running || proc.hasExit || proc.stopped {
		r.jobs.mu.Unlock()
		return
	}
	if proc.timeout > 0 && !proc.startedAt.IsZero() && time.Since(proc.startedAt) >= proc.timeout {
		r.jobs.mu.Unlock()
		return
	}
	proc.stopped = true
	proc.reason = reason
	cancel := proc.cancel
	async := proc.async
	pty := proc.pty
	screen := proc.screen
	r.jobs.mu.Unlock()
	if async != nil {
		async.Kill()
	}
	if pty != nil {
		_ = pty.Close()
	}
	if screen != nil {
		screen.Close()
	}
	if cancel != nil {
		cancel()
	}
}

// DisposeAwaitedSession stops and removes unpromoted awaited commands for a
// released turn while leaving background jobs running.
func (r *ProcessLifecycle) DisposeAwaitedSession(ctx context.Context, sessionID string, reason TerminationReason) error {
	if r == nil {
		return nil
	}
	if reason == "" {
		reason = TerminationReasonFromContext(ctx, TerminationStoppedByUser)
	}
	sessionID = trim(sessionID)
	r.jobs.mu.Lock()
	sessionProcs := r.jobs.sessions[sessionID]
	var awaited []*Process
	for _, proc := range sessionProcs {
		if proc == nil || !proc.silent {
			continue
		}
		proc.discarded = true
		awaited = append(awaited, proc)

	}
	if len(sessionProcs) == 0 {
		delete(r.jobs.sessions, sessionID)
	}
	r.jobs.mu.Unlock()
	for _, proc := range awaited {
		r.killProcess(proc, reason)
	}
	for _, proc := range awaited {
		if err := r.awaitSettled(ctx, proc); err != nil {
			return err
		}
		r.jobs.remove(sessionID, proc.Handle)
	}
	return nil
}

// StopUnderRoot stops and awaits live background jobs matching handles or running under rootPath.
func (r *ProcessLifecycle) StopUnderRoot(
	ctx context.Context,
	projectID string,
	sessionIDs map[string]struct{},
	rootPath string,
	handles []RootProcess,
	reason TerminationReason,
) error {
	if r == nil || r.jobs == nil {
		return nil
	}
	if reason == "" {
		reason = TerminationReasonFromContext(ctx, TerminationRootDetached)
	}
	projectID = trim(projectID)
	rootPath = trim(rootPath)
	seen := make(map[string]*Process)
	r.jobs.mu.Lock()
	if projectID != "" {
		r.jobs.detachingRoots[projectID] = append(r.jobs.detachingRoots[projectID], rootPath)
		defer func() {
			r.jobs.mu.Lock()
			roots := r.jobs.detachingRoots[projectID]
			for i, candidate := range roots {
				if candidate == rootPath {
					r.jobs.detachingRoots[projectID] = append(roots[:i], roots[i+1:]...)
					break
				}
			}
			if len(r.jobs.detachingRoots[projectID]) == 0 {
				delete(r.jobs.detachingRoots, projectID)
			}
			r.jobs.mu.Unlock()
		}()
	}
	for _, item := range handles {
		sid := trim(item.SessionID)
		hid := trim(item.Handle)
		if procs := r.jobs.sessions[sid]; procs != nil {
			if proc := procs[hid]; proc != nil && proc.running {
				seen[sid+"/"+hid] = proc
			}
		}
	}
	for sessionID, procs := range r.jobs.sessions {
		for handle, proc := range procs {
			if proc == nil || !proc.running {
				continue
			}
			if !matchesProjectOrSessions(proc, sessionID, projectID, sessionIDs) {
				continue
			}
			workDir := r.jobs.workDirs[handle]
			if rootPath != "" && !projectroot.PathWithinRoot(rootPath, workDir) {
				continue
			}
			seen[sessionID+"/"+handle] = proc
		}
	}
	r.jobs.mu.Unlock()
	for _, proc := range seen {
		r.killProcess(proc, reason)
	}
	for _, proc := range seen {
		if err := r.awaitSettled(ctx, proc); err != nil {
			return err
		}
	}
	return nil
}

// awaitSettled waits for process exit and output drain under the caller's deadline.
func (r *ProcessLifecycle) awaitSettled(ctx context.Context, proc *Process) error {
	select {
	case <-proc.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// OnExit releases process-scoped resources after exit, or immediately for an absent handle.
func (r *ProcessLifecycle) OnExit(sessionID, handle string, fn func()) {
	if r == nil || fn == nil {
		return
	}
	r.jobs.mu.Lock()
	proc := r.jobs.ownedProcessesLocked(trim(sessionID))[trim(handle)]
	r.jobs.mu.Unlock()
	if proc == nil {
		fn()
		return
	}
	go func() {
		<-proc.done
		fn()
	}()
}

func (r *Output) publishTerminal(ctx context.Context, proc *Process) {
	if r == nil || proc == nil {
		return
	}
	r.jobs.mu.Lock()
	if proc.discarded {
		// The retained physical drain owns release and closes only after it
		// finishes; terminal publication must not take that custody away.
		r.jobs.mu.Unlock()
		return
	}
	if proc.silent || !proc.hasExit || proc.terminalPublished {
		r.jobs.mu.Unlock()
		return
	}
	proc.terminalPublished = true
	endEv, emitEnd := r.jobs.finishProjectProcessLocked(proc)
	publishProject := r.publishProject
	completion := Completion{
		IsCheck:        proc.isCheck,
		SourceRevision: proc.sourceRevision, SourceRootDigest: proc.sourceRootDigest, Cwd: proc.cwd,
		Handle: proc.Handle, SessionID: proc.SessionID, ProjectID: proc.ProjectID,
		OriginTool: proc.originTool, ToolCallID: proc.toolCallID, RunID: proc.runID,
		Mode: proc.mode, StartedAt: proc.startedAt, FinishedAt: proc.finishedAt,
		TerminationReason: proc.reason, ExitCode: proc.exitCode, Failure: proc.failure,
		Stages: append([]hostcmd.StageResult(nil), proc.Stages...),
	}
	owned := proc.resources.take()
	completion.UnsavedDocumentPaths, completion.ReleaseNotices = owned.unsavedDocumentPaths, owned.releaseNotices
	completion.IndexWatch = owned.indexWatch
	exitCode := proc.exitCode
	boundary := proc.boundary
	facts := proc.facts
	originTool := proc.originTool
	sessionID := proc.SessionID
	publish := r.publish
	complete := r.complete
	r.jobs.mu.Unlock()
	if emitEnd && publishProject != nil {
		publishProject(ctx, endEv)
	}
	body, _, screening := r.safeOutput(ctx, proc)
	completion.Tail = screenedOrSuppressed(CutTail(body, r.ringBufferBytes), screening)
	stamped := confine.StampRefusal(originTool, sessionID, boundary, confine.RefusalContext{
		MediatedNetwork:        facts.MediatedNetwork(),
		RemotePackageExecution: facts.Report.RemotePackageExecution,
		FailedStages:           hostcmd.FailedStages(completion.Stages, exitCode),
		TerminationReason:      string(completion.TerminationReason),
		Refusals:               facts.SettledRefusals(ctx),
	})
	completion.BoundaryRefusal = string(stamped.Attribution)
	completion.GuidanceCodes = append([]string(nil), stamped.GuidanceCodes...)
	completion.Observation = stamped.Observation
	if publish != nil {
		publish(ctx, proc.ProjectID, proc.SessionID, api.BackgroundProcessEvent{
			ProcessID:         proc.Handle,
			SessionID:         proc.SessionID,
			Stream:            "exit",
			EndOffset:         proc.buffer.NextCursor(),
			Running:           false,
			ExitCode:          &exitCode,
			TerminationReason: api.BackgroundProcessTerminationReason(completion.TerminationReason),
		})
	}
	if complete != nil {
		complete(ctx, completion)
		return
	}
	owned.release()
}

func trim(s string) string {
	return strings.TrimSpace(s)
}
