package bgprocess

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
)

// StartPipeline admits and launches one command job.
func (r *Registry) StartPipeline(ctx context.Context, spec PipelineSpec) (string, error) {
	if r == nil {
		return "", fmt.Errorf("background registry not configured")
	}
	sessionID := trim(spec.SessionID)
	if sessionID == "" {
		return "", fmt.Errorf("session id required")
	}
	if spec.Runner == nil {
		return "", fmt.Errorf("host command runner not configured")
	}
	if spec.Mode != JobModeAwaited && spec.Mode != JobModeBackground {
		return "", fmt.Errorf("command job mode must be %q or %q", JobModeAwaited, JobModeBackground)
	}
	req := spec.Request
	if err := spec.Runner.ValidateStages(ctx, req.Stages); err != nil {
		return "", err
	}
	if trim(req.ProjectDir) == "" {
		return "", fmt.Errorf("project dir is required")
	}
	projectDir, err := filepath.Abs(req.ProjectDir)
	if err != nil {
		return "", err
	}
	req.ProjectDir = projectDir

	runKey := hostcmd.ExecutionKey(req)
	handle := uuid.NewString()
	// Detached jobs keep context values without request cancellation.
	baseCtx := context.WithoutCancel(ctx)
	var runCtx context.Context
	var cancel context.CancelFunc
	if spec.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(baseCtx, spec.Timeout)
	} else {
		runCtx, cancel = context.WithCancel(baseCtx)
	}

	proc := &Process{
		isCheck:        spec.IsCheck,
		sourceRevision: spec.SourceRevision, sourceRootDigest: spec.SourceRootDigest, cwd: spec.Cwd,
		Handle:        handle,
		SessionID:     sessionID,
		ProjectID:     trim(spec.ProjectID),
		RootSessionID: trim(spec.RootSessionID),
		buffer:        NewRingBuffer(r.cfg.RingBufferBytes),
		running:       true,
		silent:        spec.Mode == JobModeAwaited,
		done:          make(chan struct{}),
		cancel:        cancel,
		mode:          spec.Mode,
		originTool:    trim(spec.OriginTool),
		toolCallID:    trim(spec.ToolCallID),
		runID:         trim(spec.RunID),
		runKey:        runKey,
		startedAt:     time.Now().UTC(),
		timeout:       spec.Timeout,
		boundary:      confine.BoundaryOf(req.Launch.Confinement),
		facts:         spec.Facts,
	}
	if proc.RootSessionID == "" {
		proc.RootSessionID = sessionID
	}
	proc.Stages = hostcmd.StagePlaceholders(req.Stages)

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		cancel()
		return "", ErrRegistryClosed
	}
	r.pruneCompletedLocked(sessionID)
	duplicates, awaited := r.runningConflictsLocked(sessionID, runKey)
	if len(duplicates) > 0 {
		r.mu.Unlock()
		cancel()
		return "", &StartConflict{Kind: ErrDuplicateRunning, Handles: duplicates}
	}
	if spec.Mode == JobModeAwaited && !spec.AllowConcurrent && len(awaited) > 0 {
		r.mu.Unlock()
		cancel()
		return "", &StartConflict{Kind: ErrCommandInFlight, Handles: awaited}
	}
	// Awaited jobs begin invisible so a quick command does not flash a process card.
	if proc.silent {
		if r.countRunningAwaitedLocked(sessionID) >= r.cfg.MaxAwaited {
			err := r.awaitedCapacityErrorLocked(sessionID)
			r.mu.Unlock()
			cancel()
			return "", err
		}
	} else if r.countRunningBackgroundLocked(sessionID) >= r.cfg.MaxBackground {
		err := r.backgroundCapacityErrorLocked(sessionID)
		r.mu.Unlock()
		cancel()
		return "", err
	}
	if r.sessions[sessionID] == nil {
		r.sessions[sessionID] = make(map[string]*Process)
	}
	r.sessions[sessionID][handle] = proc
	r.mu.Unlock()

	stdout := streamWriter{proc: proc, stream: "stdout", publish: r.publishStream}
	stderr := streamWriter{proc: proc, stream: "stderr", publish: r.publishStream}

	async, err := exec.StartPipelineAsync(runCtx, req.Stages, exec.ExecOpts{
		Launch:         req.Launch,
		Dir:            req.ProjectDir,
		Timeout:        spec.Timeout,
		NoTimeout:      spec.Timeout <= 0,
		MaxOutputBytes: r.cfg.RingBufferBytes,
		InlineEnv:      req.InlineEnv,
		PathExtra:      req.PathExtra,
		Stdin:          req.Stdin,
		Redirect:       req.Redirect,
	}, stdout, stderr)
	if err != nil {
		cancel()
		r.remove(sessionID, handle)
		return "", err
	}
	r.mu.Lock()
	proc.async = async
	stopped := proc.stopped
	r.mu.Unlock()
	if stopped {
		async.Kill()
	}

	r.watchRefusals(baseCtx, proc)
	go r.waitProcess(context.WithoutCancel(ctx), proc, req.Stages)
	return handle, nil
}

type streamWriter struct {
	proc    *Process
	stream  string
	publish func(context.Context, *Process, string, int64)
}

func (w streamWriter) Write(p []byte) (int, error) {
	cursor := w.proc.buffer.Append(w.stream, p)
	if w.publish != nil && len(p) > 0 {
		w.publish(context.Background(), w.proc, w.stream, cursor)
	}
	return len(p), nil
}

func (r *Registry) waitProcess(ctx context.Context, proc *Process, stages []exec.Stage) {
	if proc == nil || proc.async == nil {
		return
	}
	res, runErr := proc.async.Wait()
	failure := hostcmd.ExecFailureOf(runErr)
	exitCode := -1
	finalStages := hostcmd.StagePlaceholders(stages)
	if res != nil {
		exitCode = res.ExitCode
		finalStages = hostcmd.StageResultsFromRun(res.Stages)
	}

	// Publish exit fields atomically for readers.
	finishedAt := time.Now().UTC()
	r.mu.Lock()
	proc.Stages = finalStages
	proc.running = false
	proc.hasExit = true
	proc.exitCode = exitCode
	proc.failure = failure
	proc.finishedAt = finishedAt
	proc.reason = TerminationExited
	if res != nil && res.TimedOut {
		proc.reason = TerminationTimedOut
	} else if proc.stopped {
		proc.reason = TerminationStopped
	}
	r.pruneCompletedLocked(proc.SessionID)
	r.mu.Unlock()
	close(proc.done)
	r.publishTerminal(ctx, proc)
}

var _ io.Writer = streamWriter{}
