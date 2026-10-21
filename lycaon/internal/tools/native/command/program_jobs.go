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
	results map[string]jobOutcome
	// order lists handles in start order; %N names order[N-1].
	order []string
}

type jobOutcome struct {
	result toolcontract.ProducerResult
	err    error
}

// jobStatus is the registry's observation; a stop request is not an exit.
type jobStatus struct {
	Handle      string
	CommandLine string
}

// start runs the job body detached from the launching statement's context.
func (j *SessionJobs) start(ctx context.Context, commandLine string, run func(context.Context, commandprogram.Streams) (toolcontract.ProducerResult, error)) (string, error) {
	if j == nil || j.Registry == nil || run == nil {
		return "", fmt.Errorf("program jobs require a session registry and body")
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
			if ctx.Err() != nil && result.Terminal == "" {
				result = toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.EffectUnknown}
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
	j.mu.Lock()
	j.order = append(j.order, handle)
	j.mu.Unlock()
	close(ready)
	return handle, nil
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
	}
	if index < 0 || index >= len(j.order) {
		return "", unknownJob(reference)
	}
	return j.order[index], nil
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
	return outcome.result, outcome.err
}

// Signal accepts the catalog's TERM/INT/KILL; every one requests the owned
// job's stop. Acknowledgement is not proof of exit.
func (j *SessionJobs) Signal(_ context.Context, reference, signal string) error {
	switch signal {
	case "TERM", "INT", "KILL", "15", "2", "9":
	default:
		return toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"signal": signal, "expected": "TERM, INT or KILL"})
	}
	handle, err := j.Resolve(reference)
	if err != nil {
		return err
	}
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
	return toolrejection.RejectInvalidArguments("COMMAND_JOB_UNKNOWN", map[string]any{"job": reference, "reason": "not a job of this session"})
}
