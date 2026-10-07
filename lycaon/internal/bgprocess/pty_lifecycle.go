package bgprocess

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// ClosePTY kills a pty handle and removes the entry. It discards residual
// output so terminal_read remains the only incremental-output channel.
func (r *Registry) ClosePTY(sessionID, handle string) (PTYCloseResult, error) {
	proc, err := r.lookup(sessionID, handle)
	if err != nil {
		return PTYCloseResult{}, err
	}
	r.mu.Lock()
	kind := proc.kind
	r.mu.Unlock()
	if kind != processKindPTY {
		return PTYCloseResult{}, fmt.Errorf("%w: handle %s", ErrNotPTY, handle)
	}
	r.killProcess(proc)
	select {
	case <-proc.done:
	case <-time.After(stopSettleTimeout):
	}
	r.mu.Lock()
	hasExit := proc.hasExit
	exitCode := proc.exitCode
	out := PTYCloseResult{
		Boundary: proc.boundary,
		Report:   proc.facts.Report,
		Network:  proc.facts.MediatedNetwork(),
		Refusals: proc.facts.Refusals(),
	}
	r.mu.Unlock()
	r.remove(trim(sessionID), trim(handle))
	if hasExit {
		code := exitCode
		out.ExitCode = &code
	}
	return out, nil
}

func (r *Registry) pumpPTY(ctx context.Context, proc *Process) {
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
			r.publishStream(ctx, proc, "stdout", cursor)
		}
		if err != nil {
			return
		}
	}
}

func (r *Registry) waitPTY(ctx context.Context, proc *Process) {
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
	r.mu.Lock()
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
	r.mu.Unlock()
	if alreadyClosed {
		return
	}
	r.mu.Lock()
	r.pruneCompletedLocked(proc.SessionID)
	r.mu.Unlock()
	if proc.screen != nil {
		final := proc.screen.snapshot()
		r.mu.Lock()
		proc.finalScreen = &final
		r.mu.Unlock()
	}
	close(proc.done)
	if proc.screen != nil {
		proc.screen.Close()
	}

	if r.publish != nil && !proc.silent {
		exit := exitCode
		r.publish(ctx, proc.ProjectID, proc.SessionID, api.BackgroundProcessEvent{
			ProcessID: proc.Handle,
			SessionID: proc.SessionID,
			Stream:    "exit",
			EndOffset: proc.buffer.NextCursor(),
			Running:   false,
			ExitCode:  &exit,
		})
	}
}
