package bgprocess

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/indexwatch"
	"github.com/lycaon/lycaon/pkg/api"
)

// processCloseTimeout bounds shutdown even when a leader cannot be killed.
const processCloseTimeout = exec.TerminateGrace + exec.PipelineWaitDelay + 3*time.Second

// Promote makes a job visible and publishes buffered output and any observed exit.
func (r *Registry) Promote(ctx context.Context, sessionID, handle string) error {
	proc, err := r.lookup(sessionID, handle)
	if err != nil {
		return err
	}
	r.mu.Lock()
	if !proc.silent {
		r.mu.Unlock()
		return nil
	}
	proc.silent = false
	running := proc.running
	r.mu.Unlock()
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
func (r *Registry) WatchIndex(sessionID, handle string, snapshot indexwatch.Snapshot) {
	proc, err := r.lookup(sessionID, handle)
	if err != nil || proc == nil {
		snapshot.Release()
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if proc.terminalPublished || proc.discarded {
		snapshot.Release()
		return
	}
	proc.indexWatch.Release()
	proc.indexWatch = snapshot
}

// TakeIndexWatch returns the process's index capture to an awaiting caller.
func (r *Registry) TakeIndexWatch(sessionID, handle string) indexwatch.Snapshot {
	proc, err := r.lookup(sessionID, handle)
	if err != nil || proc == nil {
		return indexwatch.Snapshot{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := proc.indexWatch
	proc.indexWatch = indexwatch.Snapshot{}
	return snapshot
}

// Discard removes a finished inline await job.
func (r *Registry) Discard(sessionID, handle string) {
	r.remove(trim(sessionID), trim(handle))
}

// Stop requests process-tree termination and returns the currently observed exit state.
func (r *Registry) Stop(sessionID, handle string) (*api.BackgroundProcessStopResult, error) {
	proc, err := r.lookup(sessionID, handle)
	if err != nil {
		return nil, err
	}
	r.killProcess(proc)
	running, hasExit, exitCode := r.exitState(proc)
	return &api.BackgroundProcessStopResult{
		ProcessID:     handle,
		StopRequested: true,
		Running:       running,
		ExitCode: func() *int {
			if hasExit {
				return &exitCode
			}
			return nil
		}(),
	}, nil
}

func (r *Registry) killProcess(proc *Process) {
	if proc == nil {
		return
	}
	r.mu.Lock()
	if proc.stopped {
		r.mu.Unlock()
		return
	}
	proc.stopped = true
	cancel := proc.cancel
	async := proc.async
	pty := proc.pty
	screen := proc.screen
	r.mu.Unlock()
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

// DisposeSession removes every handle and waits for process exit.
func (r *Registry) DisposeSession(ctx context.Context, sessionID string) error {
	if r == nil {
		return nil
	}
	sessionID = trim(sessionID)
	r.mu.Lock()
	procs := r.sessions[sessionID]
	delete(r.sessions, sessionID)
	for _, proc := range procs {
		proc.discarded = true
	}
	r.mu.Unlock()
	for _, proc := range procs {
		r.killProcess(proc)
	}
	for _, proc := range procs {
		if err := r.awaitSettled(ctx, proc); err != nil {
			return err
		}
	}
	return nil
}

// awaitSettled waits for process exit and output drain under the caller's deadline.
func (r *Registry) awaitSettled(ctx context.Context, proc *Process) error {
	select {
	case <-proc.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close rejects new work and waits for registered processes to exit.
func (r *Registry) Close(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, processCloseTimeout)
	defer cancel()
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	var procs []*Process
	for sessionID, sessionProcs := range r.sessions {
		for _, proc := range sessionProcs {
			proc.discarded = true
			procs = append(procs, proc)
		}
		delete(r.sessions, sessionID)
	}
	r.mu.Unlock()

	for _, proc := range procs {
		r.killProcess(proc)
	}
	for _, proc := range procs {
		if err := r.awaitSettled(ctx, proc); err != nil {
			return err
		}
	}
	return nil
}

// OnExit releases process-scoped resources after exit, or immediately for an absent handle.
func (r *Registry) OnExit(sessionID, handle string, fn func()) {
	if r == nil || fn == nil {
		return
	}
	proc, err := r.lookup(sessionID, handle)
	if err != nil || proc == nil {
		fn()
		return
	}
	go func() {
		<-proc.done
		fn()
	}()
}

func (r *Registry) publishTerminal(ctx context.Context, proc *Process) {
	if r == nil || proc == nil {
		return
	}
	r.mu.Lock()
	if proc.silent || proc.discarded || !proc.hasExit || proc.terminalPublished {
		r.mu.Unlock()
		return
	}
	proc.terminalPublished = true
	completion := Completion{
		IsCheck:        proc.isCheck,
		SourceRevision: proc.sourceRevision, SourceRootDigest: proc.sourceRootDigest, Cwd: proc.cwd,
		Handle: proc.Handle, SessionID: proc.SessionID, ProjectID: proc.ProjectID,
		OriginTool: proc.originTool, ToolCallID: proc.toolCallID, RunID: proc.runID,
		Mode: proc.mode, StartedAt: proc.startedAt, FinishedAt: proc.finishedAt,
		TerminationReason: proc.reason, ExitCode: proc.exitCode, Failure: proc.failure,
		Stages: append([]hostcmd.StageResult(nil), proc.Stages...),
	}
	completion.IndexWatch = proc.indexWatch
	proc.indexWatch = indexwatch.Snapshot{}
	exitCode := proc.exitCode
	boundary := proc.boundary
	facts := proc.facts
	originTool := proc.originTool
	sessionID := proc.SessionID
	publish := r.publish
	complete := r.complete
	r.mu.Unlock()
	body, _, screening := r.safeOutput(ctx, proc)
	completion.Tail = screenedOrSuppressed(CutTail(body, r.cfg.RingBufferBytes), screening)
	stamped := confine.StampRefusal(originTool, sessionID, boundary, confine.RefusalContext{
		MediatedNetwork:        facts.MediatedNetwork(),
		RemotePackageExecution: facts.Report.RemotePackageExecution,
		FailedStages:           hostcmd.FailedStages(completion.Stages, exitCode),
		Refusals:               facts.SettledRefusals(ctx),
	})
	completion.BoundaryRefusal = string(stamped.Attribution)
	completion.GuidanceCodes = append([]string(nil), stamped.GuidanceCodes...)
	completion.Observation = stamped.Observation
	if publish != nil {
		publish(ctx, proc.ProjectID, proc.SessionID, api.BackgroundProcessEvent{
			ProcessID: proc.Handle, SessionID: proc.SessionID, Stream: "exit",
			EndOffset: proc.buffer.NextCursor(), Running: false, ExitCode: &exitCode,
		})
	}
	if complete != nil {
		complete(ctx, completion)
		return
	}
	completion.IndexWatch.Release()
}

func trim(s string) string {
	return strings.TrimSpace(s)
}
