package bgprocess

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	execution "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/pkg/api"
)

// ptyCloseTimeout covers termination and the final terminal read.
const ptyCloseTimeout = execution.TerminateGrace + DefaultPTYReadTimeout + 3*time.Second

// ClosePTY kills a pty handle and removes the entry. It discards residual
// output so terminal_read remains the only incremental-output channel.
func (r *Terminal) ClosePTY(sessionID, handle string) (PTYCloseResult, error) {
	proc, err := r.jobs.lookup(sessionID, handle)
	if err != nil {
		return PTYCloseResult{}, err
	}
	r.jobs.mu.Lock()
	kind := proc.kind
	r.jobs.mu.Unlock()
	if kind != processKindPTY {
		return PTYCloseResult{}, fmt.Errorf("%w: handle %s", ErrNotPTY, handle)
	}
	r.Lifecycle.killProcess(proc)
	select {
	case <-proc.done:
	case <-time.After(ptyCloseTimeout):
	}
	r.jobs.mu.Lock()
	hasExit := proc.hasExit
	exitCode := proc.exitCode
	out := PTYCloseResult{
		Boundary: proc.boundary,
		Report:   proc.facts.Report,
		Network:  proc.facts.MediatedNetwork(),
		Refusals: proc.facts.Refusals(),
	}
	r.jobs.mu.Unlock()
	r.jobs.remove(trim(sessionID), trim(handle))
	if hasExit {
		code := exitCode
		out.ExitCode = &code
	}
	return out, nil
}

func (r *Terminal) pumpPTY(ctx context.Context, proc *Process) {
	if proc == nil || proc.pty == nil {
		return
	}
	defer close(proc.ptyOutputDone)
	buf := make([]byte, 4096)
	for {
		n, err := proc.pty.Read(buf)
		if n > 0 {
			cursor := proc.buffer.Append("stdout", buf[:n])
			// Tee into the vt screen without touching terminal_read's cursor.
			if proc.screen != nil {
				proc.screen.Write(buf[:n])
			}
			r.Output.publishStream(ctx, proc, "stdout", cursor)
		}
		if err != nil {
			return
		}
	}
}

func (r *Terminal) waitPTY(ctx context.Context, proc *Process) {
	if proc == nil || proc.pty == nil {
		return
	}
	waitErr := proc.pty.Wait()
	// Process exit can precede the final terminal read.
	select {
	case <-proc.ptyOutputDone:
	case <-time.After(DefaultPTYReadTimeout):
		_ = proc.pty.Close()
		<-proc.ptyOutputDone
	}
	_ = proc.pty.Close()
	exitCode := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	r.jobs.mu.Lock()
	alreadyClosed := false
	select {
	case <-proc.done:
		alreadyClosed = true
	default:
		proc.running = false
		proc.hasExit = true
		proc.exitCode = exitCode
		proc.finishedAt = time.Now().UTC()
		proc.reason = TerminationExited
		if proc.stopped {
			proc.reason = TerminationStopped
		}
		if len(proc.Stages) == 1 {
			code := exitCode
			proc.Stages[0].ExitCode = &code
		}
	}
	r.jobs.mu.Unlock()
	if alreadyClosed {
		return
	}
	r.jobs.mu.Lock()
	r.jobs.pruneCompletedLocked(proc.SessionID)
	r.jobs.mu.Unlock()
	if proc.screen != nil {
		final := proc.screen.snapshot()
		r.jobs.mu.Lock()
		proc.finalScreen = &final
		r.jobs.mu.Unlock()
	}
	close(proc.done)
	if proc.screen != nil {
		proc.screen.Close()
	}

	if r.Output.publish != nil && !proc.silent {
		exit := exitCode
		r.Output.publish(ctx, proc.ProjectID, proc.SessionID, api.BackgroundProcessEvent{
			ProcessID: proc.Handle,
			SessionID: proc.SessionID,
			Stream:    "exit",
			EndOffset: proc.buffer.NextCursor(),
			Running:   false,
			ExitCode:  &exit,
		})
	}
}
