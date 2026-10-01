package bgprocess

import (
	"context"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
)

// PTYSnapshotResult is a non-consuming terminal snapshot.
type PTYSnapshotResult struct {
	Screen   ScreenSnapshot
	Running  bool
	ExitCode *int
	Boundary confine.Boundary
	Report   confine.Report
	Network  []confine.EgressHost
	Refusals confine.SandboxRefusals
}

// SnapshotPTY returns the settled screen without consuming output.
func (r *Registry) SnapshotPTY(ctx context.Context, sessionID, handle string, opts PTYReadOpts) (PTYSnapshotResult, error) {
	proc, err := r.lookup(sessionID, handle)
	if err != nil {
		return PTYSnapshotResult{}, err
	}
	r.mu.Lock()
	kind := proc.kind
	screen := proc.screen
	finalScreen := proc.finalScreen
	r.mu.Unlock()
	if kind != processKindPTY {
		return PTYSnapshotResult{}, fmt.Errorf("%w: handle %s", ErrNotPTY, handle)
	}
	if screen == nil {
		return PTYSnapshotResult{}, fmt.Errorf("handle %s has no vt screen", handle)
	}

	idle := opts.Idle
	if idle <= 0 {
		idle = DefaultPTYIdle
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultPTYReadTimeout
	}
	if opts.WaitForOutput {
		r.waitPTYOutput(proc, timeout)
	}
	r.waitPTYQuiescence(proc, idle, timeout)

	var snap ScreenSnapshot
	if finalScreen != nil {
		snap = *finalScreen
	} else {
		snap = screen.snapshot()
	}
	r.mu.Lock()
	out := PTYSnapshotResult{
		Screen:   snap,
		Running:  proc.running,
		Boundary: proc.boundary,
		Report:   proc.facts.Report,
		Network:  proc.facts.MediatedNetwork(),
		Refusals: proc.facts.Refusals(),
	}
	if proc.hasExit {
		code := proc.exitCode
		out.ExitCode = &code
	}
	r.mu.Unlock()
	projected, projectErr := r.ProjectScreen(ctx, processCaptureScope(proc), snap)
	if projectErr != nil {
		return PTYSnapshotResult{}, fmt.Errorf("project terminal capture: %w", projectErr)
	}
	out.Screen = projected
	return out, nil
}

// waitPTYQuiescence watches the main ring-buffer cursor; it does not drain
// or advance terminal_read's high-water mark.
func (r *Registry) waitPTYQuiescence(proc *Process, idle, timeout time.Duration) {
	if proc == nil || proc.buffer == nil {
		return
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(ptyReadPoll)
	defer ticker.Stop()
	last := proc.buffer.NextCursor()
	lastGrowth := time.Now()
	for {
		cur := proc.buffer.NextCursor()
		if cur != last {
			last = cur
			lastGrowth = time.Now()
		}
		r.mu.Lock()
		exited := proc.hasExit
		r.mu.Unlock()
		if exited || time.Since(lastGrowth) >= idle || time.Now().After(deadline) {
			return
		}
		remain := time.Until(deadline)
		select {
		case <-proc.done:
			return
		case <-ticker.C:
		case <-time.After(remain):
			return
		}
	}
}

// waitPTYOutput blocks until the main buffer grows, the process exits, or
// timeout — used for first-frame snapshots right after terminal_open.
func (r *Registry) waitPTYOutput(proc *Process, timeout time.Duration) {
	if proc == nil || proc.buffer == nil {
		return
	}
	if timeout <= 0 {
		timeout = DefaultPTYReadTimeout
	}
	deadline := time.Now().Add(timeout)
	start := proc.buffer.NextCursor()
	ticker := time.NewTicker(ptyReadPoll)
	defer ticker.Stop()
	for {
		r.mu.Lock()
		exited := proc.hasExit
		r.mu.Unlock()
		if exited || proc.buffer.NextCursor() > start || time.Now().After(deadline) {
			return
		}
		remain := time.Until(deadline)
		select {
		case <-proc.done:
			return
		case <-ticker.C:
		case <-time.After(remain):
			return
		}
	}
}
