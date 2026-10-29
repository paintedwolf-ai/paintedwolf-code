package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// SessionJobs puts a program's & jobs in the session's process registry, so
// jobs, wait, kill, command_output and command_stop share one set of handles.
// A native job has no fabricated OS PID; its stop cancels its own context.
type SessionJobs struct {
	Registry      *bgprocess.Registry
	Supervisor    *ProgramSupervisor
	SessionID     string
	RootSessionID string
	ProjectID     string
	ToolCallID    string

	mu      sync.Mutex
	closed  atomic.Bool
	results map[string]jobOutcome
	signals map[string]int
	// order lists handles in start order; %N names order[N-1].
	order []string
	// programOrder tracks handles started by the active program.
	programOrder []string
}

// sessionJobsKey binds admission to this exact retained session owner.
type sessionJobsKey struct{}

type jobOutcome struct {
	result toolcontract.ProducerResult
	err    error
}

// jobStatus is the registry's observation; a stop request is not an exit.
type jobStatus struct {
	Handle      string
	CommandLine string
}

// BeginProgram prunes finished jobs from prior programs before a new program
// runs so %1 names the first active or newly started job.
func (j *SessionJobs) BeginProgram(ctx context.Context) (context.Context, error) {
	if j == nil || j.Registry == nil {
		return ctx, nil
	}
	active := map[string]bool{}
	for _, job := range j.Registry.ActiveJobs(j.SessionID) {
		active[job.Handle] = true
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed.Load() {
		return ctx, ErrProgramStopped
	}
	for handle := range j.results {
		if !active[handle] {
			delete(j.results, handle)
		}
	}
	for handle := range j.signals {
		if !active[handle] {
			delete(j.signals, handle)
		}
	}
	kept := j.order[:0]
	for _, handle := range j.order {
		if active[handle] {
			kept = append(kept, handle)
		}
	}
	j.order = kept
	j.programOrder = nil
	return context.WithValue(ctx, sessionJobsKey{}, j), nil
}

// start runs the job body detached from the launching statement's context.
func (j *SessionJobs) start(ctx context.Context, commandLine string, run func(context.Context, commandprogram.Streams) (toolcontract.ProducerResult, error)) (string, error) {
	if j == nil || j.Registry == nil || run == nil {
		return "", fmt.Errorf("program jobs require a session registry and body")
	}
	// Serialize registration against closing this retained session owner.
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed.Load() {
		return "", ErrProgramStopped
	}
	jobCtx, setStop, release, err := j.Supervisor.Hold(ctx)
	if err != nil {
		return "", err
	}
	var handle string
	ready := make(chan struct{})
	handle, err = j.Registry.StartProgram(ctx, bgprocess.ProgramJobSpec{
		SessionID: j.SessionID, RootSessionID: j.RootSessionID, ProjectID: j.ProjectID,
		CommandLine: commandLine, OriginTool: "command", ToolCallID: j.ToolCallID,
		Run: func(stopCtx context.Context, stdout, stderr io.Writer) (int, error) {
			defer release()
			<-ready
			ctx, cancel := context.WithCancel(jobCtx)
			defer cancel()
			stopAfter := context.AfterFunc(stopCtx, cancel)
			defer stopAfter()
			result, err := run(ctx, commandprogram.Streams{Input: commandprogram.NullInput(), Output: stdout, Error: stderr})
			if ctx.Err() != nil {
				status := j.signalStatus(handle)
				result = toolcontract.ProducerResult{Status: status, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.EffectUnknown}
			}
			j.record(handle, result, err)
			return result.Status, err
		},
	})
	if err != nil {
		release()
		close(ready)
		return "", err
	}
	setStop(func() { _, _ = j.Registry.Lifecycle.Stop(j.SessionID, handle) })
	j.order = append(j.order, handle)
	j.programOrder = append(j.programOrder, handle)
	close(ready)
	return handle, nil
}

func (j *SessionJobs) signalStatus(handle string) int {
	j.mu.Lock()
	defer j.mu.Unlock()
	if status := j.signals[handle]; status != 0 {
		return status
	}
	return 130
}

func (j *SessionJobs) record(handle string, result toolcontract.ProducerResult, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.results == nil {
		j.results = map[string]jobOutcome{}
	}
	j.results[handle] = jobOutcome{result: result, err: err}
}

// Resolve maps a job reference to its handle: %N (the Nth job this session
// started), %% or %+ (the latest), %- (the one before) or a handle as $! and
// jobs print it. Nothing outside this session's jobs is addressable.
func (j *SessionJobs) Resolve(reference string) (string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	index := -1
	switch {
	case reference == "%%" || reference == "%+" || reference == "%":
		index = len(j.order) - 1
	case reference == "%-":
		index = len(j.order) - 2
	case strings.HasPrefix(reference, "%"):
		if n, err := strconv.Atoi(reference[1:]); err == nil && n > 0 {
			index = n - 1
		}
	default:
		if slices.Contains(j.order, reference) {
			return reference, nil
		}
		if _, ok := j.results[reference]; ok {
			return reference, nil
		}
	}
	if index < 0 || index >= len(j.order) {
		return "", unknownJob(reference)
	}
	return j.order[index], nil
}

// Unwaited returns the handles bare `wait` should wait on: jobs started by the
// current program if any, otherwise all active session jobs.
func (j *SessionJobs) Unwaited(ctx context.Context) ([]string, error) {
	j.mu.Lock()
	programHandles := slices.Clone(j.programOrder)
	j.mu.Unlock()
	if len(programHandles) > 0 {
		return programHandles, nil
	}
	statuses, err := j.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(statuses))
	for _, job := range statuses {
		out = append(out, job.Handle)
	}
	return out, nil
}

// Number is a handle's %N job number in this session, or 0.
func (j *SessionJobs) Number(handle string) int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return slices.Index(j.order, handle) + 1
}

// Wait returns the job's own settled outcome; waiting does not stop it.
func (j *SessionJobs) Wait(ctx context.Context, reference string) (toolcontract.ProducerResult, error) {
	handle, err := j.Resolve(reference)
	if err != nil {
		return toolcontract.ProducerResult{}, err
	}
	if _, _, err := j.Registry.Lifecycle.Wait(ctx, j.SessionID, handle); err != nil {
		return toolcontract.ProducerResult{}, jobLookupError(err, handle)
	}
	j.mu.Lock()
	outcome, ok := j.results[handle]
	j.mu.Unlock()
	if !ok {
		return toolcontract.ProducerResult{}, fmt.Errorf("job %s settled without an outcome", handle)
	}
	if outcome.result.Terminal == toolcontract.ProducerInterrupted && errors.Is(outcome.err, context.Canceled) {
		// A stopped job's own cancellation is its status, not the waiter's failure.
		return outcome.result, nil
	}
	return outcome.result, outcome.err
}

// Signal accepts the catalog's TERM/INT/KILL; every one requests the owned
// job's stop. Acknowledgement is not proof of exit.
func (j *SessionJobs) Signal(_ context.Context, reference, signal string) error {
	status := 143
	switch signal {
	case "TERM", "15":
		status = 143
	case "INT", "2":
		status = 130
	case "KILL", "9":
		status = 137
	default:
		return toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"signal": signal, "expected": "TERM, INT or KILL"})
	}
	handle, err := j.Resolve(reference)
	if err != nil {
		return err
	}
	j.mu.Lock()
	if j.signals == nil {
		j.signals = map[string]int{}
	}
	j.signals[handle] = status
	j.mu.Unlock()
	if _, err := j.Registry.Lifecycle.Stop(j.SessionID, handle); err != nil {
		return jobLookupError(err, handle)
	}
	return nil
}

// List reports this session's live jobs, never host-global processes.
func (j *SessionJobs) List(context.Context) ([]jobStatus, error) {
	var out []jobStatus
	for _, job := range j.Registry.ActiveJobs(j.SessionID) {
		line := ""
		if len(job.Stages) > 0 {
			line = job.Stages[0].Command
		}
		out = append(out, jobStatus{Handle: job.Handle, CommandLine: line})
	}
	return out, nil
}

func jobLookupError(err error, handle string) error {
	if errors.Is(err, bgprocess.ErrProcessNotFound) {
		return unknownJob(handle)
	}
	return err
}

// unknownJob refuses a reference that names no job of this session; kill,
// wait and jobs never address host processes.
func unknownJob(reference string) error {
	return toolrejection.RejectInvalidArguments("COMMAND_JOB_UNKNOWN", map[string]any{"job": reference, "word": reference, "reason": "not a job of this session; use jobs to list this session's background jobs, then pass %N, %% or $!"})
}
