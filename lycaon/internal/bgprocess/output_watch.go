package bgprocess

import (
	"context"
	"errors"
)

// ProcessOutputState summarizes screened retained-output cursors for a process_output watch.
type ProcessOutputState struct {
	Lost             bool
	Running          bool
	HasExit          bool
	ExitCode         int
	Head             int64
	NextCursor       int64
	ProcessEndCursor int64
	HasProcessBytes  bool
	Truncated        bool
}

// SetWatchOutputHook registers a callback invoked whenever screened output advances,
// a handle exits, or a handle is evicted/disposed.
func (r *Output) SetWatchOutputHook(fn func(ctx context.Context, sessionID, handle string)) {
	if r == nil {
		return
	}
	r.jobs.mu.Lock()
	r.watchOutput = fn
	r.jobs.onHandleEvicted = fn
	r.jobs.mu.Unlock()
}

func (r *Output) notifyWatchOutput(ctx context.Context, sessionID, handle string) {
	if r == nil || sessionID == "" || handle == "" {
		return
	}
	r.jobs.mu.Lock()
	fn := r.watchOutput
	r.jobs.mu.Unlock()
	if fn != nil {
		fn(ctx, sessionID, handle)
	}
}

// InspectProcessOutput screens retained output for handle and reports process-origin cursor progress.
func (r *Output) InspectProcessOutput(ctx context.Context, sessionID, handle string, since int64) (ProcessOutputState, error) {
	if r == nil {
		return ProcessOutputState{Lost: true}, nil
	}
	proc, err := r.jobs.lookup(sessionID, handle)
	if err != nil {
		if errors.Is(err, ErrProcessNotFound) {
			return ProcessOutputState{Lost: true}, nil
		}
		return ProcessOutputState{}, err
	}
	running, hasExit, exitCode, _ := r.jobs.exitState(proc)

	proc.publishMu.Lock()
	projection := r.projectWindow(ctx, proc, 0)
	proc.publishMu.Unlock()

	state := ProcessOutputState{
		Running:    running,
		HasExit:    hasExit,
		ExitCode:   exitCode,
		Head:       projection.Head,
		NextCursor: projection.Next,
		Truncated:  projection.Truncated || since < projection.Head,
	}
	if !projection.Screened || projection.Empty {
		return state, nil
	}
	floor := max(since, projection.Head)
	for _, chunk := range projection.Chunks {
		if chunk.HostOrigin || chunk.Text == "" {
			continue
		}
		end := chunk.Cursor + int64(len(chunk.Text))
		if end <= floor {
			continue
		}
		state.HasProcessBytes = true
		if end > state.ProcessEndCursor {
			state.ProcessEndCursor = end
		}
	}
	return state, nil
}
