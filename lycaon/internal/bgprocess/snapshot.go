package bgprocess

import (
	"context"
	"sort"
	"time"

	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/pkg/api"
)

// Snapshot captures an await job's current result.
type Snapshot struct {
	TerminationReason TerminationReason
	HasExit           bool
	ExitCode          int
	// Failure is the run error the exit status does not carry.
	Failure *hostcmd.ExecFailure
	Stages  []hostcmd.StageResult
	// Tail is the last tailBytes of Output, rune-aligned.
	Tail string
	// Output is every retained byte of the screened rendering the tail was cut
	// from, so a caller can keep the whole body when the tail dropped some of it.
	Output string
	// OutputScreened is true when Output crossed the durable secret screen and
	// may therefore leave the process (be spilled, recorded, or shown).
	OutputScreened bool
	// OutputEvicted is true when the ring buffer had already dropped earlier
	// bytes, so Output is itself a suffix of what the process wrote.
	OutputEvicted bool
}

// Await blocks until exit, budget expiry, or cancellation.
func (r *ProcessLifecycle) Await(ctx context.Context, sessionID, handle string, budget time.Duration) (bool, error) {
	proc, err := r.jobs.lookup(sessionID, handle)
	if err != nil {
		return false, err
	}
	var timerC <-chan time.Time
	if budget > 0 {
		t := time.NewTimer(budget)
		defer t.Stop()
		timerC = t.C
	}
	select {
	case <-proc.done:
		return true, nil
	case <-timerC:
		return false, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// Snapshot screens the full tail before applying its byte limit.
func (r *Registry) Snapshot(ctx context.Context, sessionID, handle string, tailBytes int) (Snapshot, error) {
	proc, err := r.jobs.lookup(sessionID, handle)
	if err != nil {
		return Snapshot{}, err
	}
	r.jobs.mu.Lock()
	snap := Snapshot{
		TerminationReason: proc.reason,
		HasExit:           proc.hasExit,
		ExitCode:          proc.exitCode,
		Failure:           proc.failure,
		Stages:            append([]hostcmd.StageResult(nil), proc.Stages...),
	}
	r.jobs.mu.Unlock()
	body, evicted, screening := r.Output.safeOutput(ctx, proc)
	snap.Output = screenedOrSuppressed(body, screening)
	snap.Tail = screenedOrSuppressed(CutTail(body, tailBytes), screening)
	snap.OutputScreened = screening == tailScreened
	snap.OutputEvicted = evicted
	return snap, nil
}

// CommandLine renders the recorded pipeline; the handle remains its control identity.
func (r *Registry) CommandLine(sessionID, handle string) (string, error) {
	proc, err := r.jobs.lookup(sessionID, handle)
	if err != nil {
		return "", err
	}
	r.jobs.mu.Lock()
	stages := append([]hostcmd.StageResult(nil), proc.Stages...)
	r.jobs.mu.Unlock()
	return hostcmd.CommandLine(stages), nil
}

// RequireRunning reports nil when handle is a live process in sessionID.
func (r *Registry) RequireRunning(sessionID, handle string) error {
	proc, err := r.jobs.lookup(sessionID, handle)
	if err != nil {
		return err
	}
	r.jobs.mu.Lock()
	defer r.jobs.mu.Unlock()
	if !proc.running {
		return ErrProcessNotRunning
	}
	return nil
}

// State reports ownership and liveness without reading command output.
func (r *Registry) State(sessionID, handle string) (known, running bool) {
	if r == nil {
		return false, false
	}
	r.jobs.mu.Lock()
	defer r.jobs.mu.Unlock()
	proc := r.jobs.sessions[trim(sessionID)][trim(handle)]
	if proc == nil {
		return false, false
	}
	return true, proc.running
}

// HasRunning includes awaited jobs as well as visible background jobs.
func (r *Registry) HasRunning(sessionID string) bool {
	return r.HasRunningHandles(sessionID, nil)
}

// HasRunningHandles checks selected handles, or all handles when none are given.
func (r *Registry) HasRunningHandles(sessionID string, handles []string) bool {
	if r == nil {
		return false
	}
	wanted := make(map[string]struct{}, len(handles))
	for _, handle := range handles {
		if handle = trim(handle); handle != "" {
			wanted[handle] = struct{}{}
		}
	}
	r.jobs.mu.Lock()
	defer r.jobs.mu.Unlock()
	for handle, proc := range r.jobs.sessions[trim(sessionID)] {
		if len(wanted) > 0 {
			if _, ok := wanted[handle]; !ok {
				continue
			}
		}
		if proc.running {
			return true
		}
	}
	return false
}

// RawOutput is unscreened retained output read from a byte offset.
type RawOutput struct {
	Handle string
	// From is the requested offset; Next is the offset just past the last
	// retained byte.
	From      int64
	Next      int64
	Chunks    []OutputChunk
	Running   bool
	Truncated bool
	ExitCode  *int
}

// OutputSnapshot combines process output with its spawn boundary.
type OutputSnapshot struct {
	Output   RawOutput
	Boundary confine.Boundary
	Facts    confine.SpawnFacts
	// Failure is the run error the exit status does not carry.
	Failure *hostcmd.ExecFailure
}

// ReadRawOutput returns buffered output for internal tool observation.
func (r *Output) ReadRawOutput(sessionID, handle string, cursor int64) (OutputSnapshot, error) {
	proc, err := r.jobs.lookup(sessionID, handle)
	if err != nil {
		return OutputSnapshot{}, err
	}
	chunks, next, truncated := proc.buffer.ReadSince(cursor)
	r.jobs.mu.Lock()
	running, hasExit, exitCode, failure := proc.running, proc.hasExit, proc.exitCode, proc.failure
	r.jobs.mu.Unlock()
	out := RawOutput{
		Handle:    handle,
		From:      cursor,
		Next:      next,
		Chunks:    chunks,
		Running:   running,
		Truncated: truncated,
	}
	if hasExit {
		out.ExitCode = &exitCode
	}
	return OutputSnapshot{Output: out, Boundary: proc.boundary, Facts: proc.facts, Failure: failure}, nil
}

// ReadOutput returns the safe Den-facing projection of the whole retained window.
func (r *Output) ReadOutput(ctx context.Context, sessionID, handle string) (api.BackgroundProcessOutput, error) {
	proc, err := r.jobs.lookup(sessionID, handle)
	if err != nil {
		return api.BackgroundProcessOutput{}, err
	}
	snapshot, err := r.ReadRawOutput(sessionID, handle, 0)
	if err != nil {
		return api.BackgroundProcessOutput{}, err
	}
	raw := snapshot.Output
	out := api.BackgroundProcessOutput{
		ProcessID: raw.Handle,
		Chunks:    toAPIChunks(raw.Chunks),
		Running:   raw.Running,
		Truncated: raw.Truncated,
		ExitCode:  raw.ExitCode,
	}
	proc.publishMu.Lock()
	projection := r.projectWindow(ctx, proc)
	proc.publishMu.Unlock()
	if projection.Empty {
		return out, nil
	}
	text := projection.Text
	if !projection.Screened {
		text = captureUnavailableText
	}
	out.Chunks = []api.BackgroundProcessChunk{{
		Offset: projection.Head, Stream: "combined", Text: text,
	}}
	return out, nil
}

func toAPIStages(stages []hostcmd.StageResult) []api.BackgroundProcessStage {
	if len(stages) == 0 {
		return nil
	}
	out := make([]api.BackgroundProcessStage, len(stages))
	for i, s := range stages {
		out[i] = api.BackgroundProcessStage{
			Command:   s.Command,
			ExitCode:  s.ExitCode,
			Skipped:   s.Skipped,
			Connector: s.Connector,
		}
	}
	return out
}

// List returns running and recent handles for a session.
func (r *Output) List(ctx context.Context, sessionID string) []api.BackgroundProcess {
	if r == nil {
		return nil
	}
	sessionID = trim(sessionID)
	r.jobs.mu.Lock()
	procs := r.jobs.sessions[sessionID]
	if len(procs) == 0 {
		r.jobs.mu.Unlock()
		return nil
	}
	out := make([]api.BackgroundProcess, 0, len(procs))
	scopes := make([]captureprojection.Scope, 0, len(procs))
	for _, proc := range procs {
		if proc.silent {
			continue
		}
		item := api.BackgroundProcess{
			ProcessID: proc.Handle,
			Running:   proc.running,
			Stages:    toAPIStages(proc.Stages),
		}
		if proc.hasExit {
			code := proc.exitCode
			item.ExitCode = &code
		}
		out = append(out, item)
		scopes = append(scopes, processCaptureScope(proc))
	}
	projector := r.projector
	r.jobs.mu.Unlock()
	for i := range out {
		for j := range out[i].Stages {
			if projector == nil {
				out[i].Stages[j].Command = "[Capture screening unavailable]"
				continue
			}
			projected, err := projector.Text(
				ctx, scopes[i], "capture.process.command", out[i].Stages[j].Command,
			)
			if err != nil {
				out[i].Stages[j].Command = "[Capture screening unavailable]"
				continue
			}
			out[i].Stages[j].Command = projected.Value
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProcessID < out[j].ProcessID })
	return out
}

// HasPipelineHandles reports whether the session still holds a command-job handle.
func (r *Registry) HasPipelineHandles(sessionID string) bool {
	if r == nil {
		return false
	}
	r.jobs.mu.Lock()
	defer r.jobs.mu.Unlock()
	for _, proc := range r.jobs.sessions[trim(sessionID)] {
		if proc.kind == processKindPipeline && !proc.silent {
			return true
		}
	}
	return false
}

// ActiveJobs returns host-authoritative state for every live command job in a session.
func (r *Registry) ActiveJobs(sessionID string) []JobSnapshot {
	if r == nil {
		return nil
	}
	r.jobs.mu.Lock()
	defer r.jobs.mu.Unlock()
	var out []JobSnapshot
	for _, proc := range r.jobs.sessions[trim(sessionID)] {
		if !proc.running || proc.kind != processKindPipeline || proc.silent {
			continue
		}
		out = append(out, JobSnapshot{
			Handle: proc.Handle, Mode: proc.mode, OriginTool: proc.originTool,
			StartedAt: proc.startedAt, Timeout: proc.timeout,
			Stages: append([]hostcmd.StageResult(nil), proc.Stages...),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

// CountLivePTYs returns the number of running pty-backed handles in a session.
func (r *Registry) CountLivePTYs(sessionID string) int {
	if r == nil {
		return 0
	}
	r.jobs.mu.Lock()
	defer r.jobs.mu.Unlock()
	n := 0
	for _, proc := range r.jobs.sessions[trim(sessionID)] {
		if proc.running && proc.kind == processKindPTY {
			n++
		}
	}
	return n
}

// exitState reads the wait goroutine's exit fields under r.jobs.mu.
func (r *processTable) exitState(proc *Process) (running, hasExit bool, exitCode int) {
	if r == nil || proc == nil {
		return false, false, 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return proc.running, proc.hasExit, proc.exitCode
}
