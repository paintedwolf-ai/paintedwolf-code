package bgprocess

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/hostcmd"
)

// processKindProgram is a host-evaluated background statement. It owns no OS
// process of its own; external stages it launches keep their own supervision.
const processKindProgram = "program"

// ProgramJobSpec is one statement-trailing & job. Run receives the job's
// context, which Stop cancels, and the registry's stdout/stderr capture.
type ProgramJobSpec struct {
	SessionID     string
	RootSessionID string
	ProjectID     string
	Cwd           string
	CommandLine   string
	OriginTool    string
	ToolCallID    string
	Run           func(ctx context.Context, stdout, stderr io.Writer) (exitCode int, err error)
}

// StartProgram registers a native job beside pipeline jobs under the same
// session handles, capacity, Stop and output services.
func (r *Registry) StartProgram(ctx context.Context, spec ProgramJobSpec) (string, error) {
	if r == nil {
		return "", fmt.Errorf("background registry not configured")
	}
	sessionID := trim(spec.SessionID)
	if sessionID == "" || spec.Run == nil {
		return "", fmt.Errorf("program job requires session and body")
	}
	handle := uuid.NewString()
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	proc := &Process{
		cwd: spec.Cwd, Handle: handle, SessionID: sessionID, ProjectID: trim(spec.ProjectID), RootSessionID: trim(spec.RootSessionID),
		buffer: NewRingBuffer(r.ringBufferBytes), running: true, done: make(chan struct{}), cancel: cancel,
		mode: JobModeBackground, originTool: trim(spec.OriginTool), toolCallID: trim(spec.ToolCallID),
		startedAt: time.Now().UTC(), kind: processKindProgram,
		Stages: []hostcmd.StageResult{{Command: spec.CommandLine}},
	}
	if proc.RootSessionID == "" {
		proc.RootSessionID = sessionID
	}
	r.jobs.mu.Lock()
	if r.jobs.closed {
		r.jobs.mu.Unlock()
		cancel()
		return "", ErrRegistryClosed
	}
	r.jobs.pruneCompletedLocked(sessionID)
	if r.jobs.countRunningBackgroundLocked(sessionID) >= r.jobs.maxBackground {
		err := r.jobs.backgroundCapacityErrorLocked(sessionID)
		r.jobs.mu.Unlock()
		cancel()
		return "", err
	}
	if r.jobs.sessions[sessionID] == nil {
		r.jobs.sessions[sessionID] = make(map[string]*Process)
	}
	r.jobs.sessions[sessionID][handle] = proc
	r.jobs.mu.Unlock()
	stdout := streamWriter{proc: proc, stream: "stdout", publish: r.Output.publishStream}
	stderr := streamWriter{proc: proc, stream: "stderr", publish: r.Output.publishStream}
	go func() {
		exitCode, err := spec.Run(runCtx, stdout, stderr)
		cancel()
		r.jobs.mu.Lock()
		proc.running, proc.hasExit, proc.exitCode = false, true, exitCode
		proc.Stages[0].ExitCode, proc.Stages[0].Failed = &exitCode, exitCode != 0
		proc.failure = hostcmd.ExecFailureOf(err)
		proc.finishedAt = time.Now().UTC()
		proc.reason = TerminationExited
		if proc.stopped {
			proc.reason = TerminationStopped
		}
		r.jobs.pruneCompletedLocked(proc.SessionID)
		r.jobs.mu.Unlock()
		close(proc.done)
		r.Output.publishTerminal(context.WithoutCancel(ctx), proc)
	}()
	return handle, nil
}

// Wait blocks until the handle settles or ctx ends, then returns its exit state.
func (r *ProcessLifecycle) Wait(ctx context.Context, sessionID, handle string) (exitCode int, stopped bool, err error) {
	proc, err := r.jobs.lookup(sessionID, handle)
	if err != nil {
		return 0, false, err
	}
	if err := r.awaitSettled(ctx, proc); err != nil {
		return 0, false, err
	}
	r.jobs.mu.Lock()
	defer r.jobs.mu.Unlock()
	return proc.exitCode, proc.stopped, nil
}
