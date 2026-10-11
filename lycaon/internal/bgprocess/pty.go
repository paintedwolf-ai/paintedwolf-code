package bgprocess

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/confine"
	lycexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
)

// Default PTY read settle values balance idle quiescence and per-call timeout.
const (
	DefaultPTYIdle        = 200 * time.Millisecond
	DefaultPTYReadTimeout = 5 * time.Second
	ptyReadPoll           = 20 * time.Millisecond
)

// ErrNotPTY reports a process without a terminal.
var ErrNotPTY = errors.New("process is not a pty")

// PTYReadOpts bounds an incremental terminal_read.
type PTYReadOpts struct {
	Idle          time.Duration
	Timeout       time.Duration
	MaxBytes      int
	WaitForOutput bool // wait for first main byte (or exit) before idle settle
}

// PTYReadResult is incremental main output since the last read cursor.
type PTYReadResult struct {
	Text                   string
	BytesReturned          int
	Truncated              bool
	PageContinuation       bool
	EvictedBytes           int64
	StartCursor            int64
	NextCursor             int64
	AvailableThroughCursor int64
	Running                bool
	ExitCode               *int
	Boundary               confine.Boundary
	Report                 confine.Report
	Network                []confine.EgressHost
	Refusals               confine.SandboxRefusals
}

// PTYCloseResult reports termination without observing output.
type PTYCloseResult struct {
	ExitCode *int
	Boundary confine.Boundary
	Report   confine.Report
	Network  []confine.EgressHost
	Refusals confine.SandboxRefusals
}

// StartPTY registers a terminal and its spawn facts.
func (r *Terminal) StartPTY(
	ctx context.Context,
	sessionID, rootSessionID, projectID string,
	req hostcmd.Request,
	runner *hostcmd.Runner,
	winsize lycexec.WinSize,
	facts confine.SpawnFacts,
) (string, error) {
	return r.startPTY(ctx, sessionID, rootSessionID, projectID, req, runner, winsize, facts, false)
}

func (r *Terminal) startPTY(
	ctx context.Context,
	sessionID, rootSessionID, projectID string,
	req hostcmd.Request,
	runner *hostcmd.Runner,
	winsize lycexec.WinSize,
	facts confine.SpawnFacts,
	silent bool,
) (string, error) {
	if r == nil {
		return "", fmt.Errorf("background registry not configured")
	}
	sessionID = trim(sessionID)
	if sessionID == "" {
		return "", fmt.Errorf("session id required")
	}
	if runner == nil {
		return "", fmt.Errorf("host command runner not configured")
	}
	if len(req.Stages) != 1 {
		return "", fmt.Errorf("pty requires exactly one command stage")
	}
	if err := runner.ValidateStages(ctx, req.Stages); err != nil {
		return "", err
	}
	if trim(req.ProjectDir) == "" {
		return "", fmt.Errorf("project dir is required")
	}
	projectDir, err := filepath.Abs(req.ProjectDir)
	if err != nil {
		return "", err
	}

	handle := uuid.NewString()
	// The terminal keeps request values without request cancellation.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))

	size := winsize
	if size.Cols == 0 {
		size.Cols = lycexec.DefaultPTYCols
	}
	if size.Rows == 0 {
		size.Rows = lycexec.DefaultPTYRows
	}

	mode := JobModeBackground
	if silent {
		mode = JobModeAwaited
	}
	proc := &Process{
		Handle:        handle,
		SessionID:     sessionID,
		ProjectID:     trim(projectID),
		RootSessionID: trim(rootSessionID),
		buffer:        NewRingBuffer(r.ringBufferBytes),
		running:       true,
		done:          make(chan struct{}),
		cancel:        cancel,
		kind:          processKindPTY,
		ptyOutputDone: make(chan struct{}),
		screen:        newPTYScreen(int(size.Cols), int(size.Rows)),
		boundary:      confine.BoundaryOf(req.Launch.Confinement),
		facts:         facts,
		mode:          mode,
		silent:        silent,
		startedAt:     time.Now().UTC(),
	}
	if proc.RootSessionID == "" {
		proc.RootSessionID = sessionID
	}
	proc.Stages = hostcmd.StagePlaceholders(req.Stages)

	r.jobs.mu.Lock()
	if r.jobs.closed {
		r.jobs.mu.Unlock()
		cancel()
		proc.screen.Close()
		return "", ErrRegistryClosed
	}
	r.jobs.pruneCompletedLocked(sessionID)
	if silent && r.jobs.countRunningAwaitedLocked(sessionID) >= r.jobs.maxAwaited {
		err := r.jobs.awaitedCapacityErrorLocked(sessionID)
		r.jobs.mu.Unlock()
		cancel()
		proc.screen.Close()
		return "", err
	}
	if !silent && r.jobs.countRunningBackgroundLocked(sessionID) >= r.jobs.maxBackground {
		err := r.jobs.backgroundCapacityErrorLocked(sessionID)
		r.jobs.mu.Unlock()
		cancel()
		proc.screen.Close()
		return "", err
	}
	if r.jobs.sessions[sessionID] == nil {
		r.jobs.sessions[sessionID] = make(map[string]*Process)
	}
	r.jobs.sessions[sessionID][handle] = proc
	r.jobs.mu.Unlock()

	stage := req.Stages[0]
	inlineEnv := req.InlineEnv
	if len(stage.Env) > 0 {
		merged := make(map[string]string, len(inlineEnv)+len(stage.Env))
		for k, v := range inlineEnv {
			merged[k] = v
		}
		for k, v := range stage.Env {
			merged[k] = v
		}
		inlineEnv = merged
	}
	session, err := lycexec.StartPTY(runCtx, stage.Name, stage.Args, lycexec.PTYOpts{
		Dir:            projectDir,
		NoTimeout:      true,
		InlineEnv:      inlineEnv,
		PathExtra:      req.PathExtra,
		Launch:         req.Launch,
		WinSize:        size,
		MaxOutputBytes: r.ringBufferBytes,
	})
	if err != nil {
		cancel()
		r.jobs.remove(sessionID, handle)
		return "", err
	}
	r.jobs.mu.Lock()
	proc.pty = session
	stopped := proc.stopped
	r.jobs.mu.Unlock()
	if stopped {
		_ = session.Close()
	}
	// Forward terminal replies so interactive commands do not stall.
	if proc.screen != nil {
		pty := session
		proc.screen.setOnReply(func(b []byte) {
			_, _ = pty.Write(b)
		})
	}

	detached := context.WithoutCancel(ctx)
	go r.pumpPTY(detached, proc)
	go r.waitPTY(detached, proc)
	return handle, nil
}

// PTYReport returns the terminal's spawn report.
func (r *Terminal) PTYReport(sessionID, handle string) (confine.Report, error) {
	proc, err := r.jobs.lookup(sessionID, handle)
	if err != nil {
		return confine.Report{}, err
	}
	r.jobs.mu.Lock()
	defer r.jobs.mu.Unlock()
	if proc.kind != processKindPTY {
		return confine.Report{}, fmt.Errorf("%w: handle %s", ErrNotPTY, handle)
	}
	return proc.facts.Report, nil
}

// LookupPTY accepts registered terminal handles, including exited processes.
func (r *Terminal) LookupPTY(sessionID, handle string) error {
	proc, err := r.jobs.lookup(sessionID, handle)
	if err != nil {
		return err
	}
	r.jobs.mu.Lock()
	defer r.jobs.mu.Unlock()
	if proc.kind != processKindPTY || proc.pty == nil {
		return fmt.Errorf("%w: handle %s", ErrNotPTY, handle)
	}
	return nil
}
