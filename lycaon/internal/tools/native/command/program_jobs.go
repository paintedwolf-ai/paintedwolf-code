package command

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	SessionID     string
	RootSessionID string
	ProjectID     string
	ToolCallID    string

	mu      sync.Mutex
	results map[string]jobOutcome
}

type jobOutcome struct {
	result toolcontract.ProducerResult
	err    error
}

var _ commandprogram.ProgramJobs = (*SessionJobs)(nil)

// Start runs the job body detached from the launching statement's context.
func (j *SessionJobs) Start(ctx context.Context, launch commandprogram.JobLaunch) (string, error) {
	if j == nil || j.Registry == nil || launch.Run == nil {
		return "", fmt.Errorf("program jobs require a session registry and body")
	}
	var handle string
	ready := make(chan struct{})
	handle, err := j.Registry.StartProgram(ctx, bgprocess.ProgramJobSpec{
		SessionID: j.SessionID, RootSessionID: j.RootSessionID, ProjectID: j.ProjectID,
		CommandLine: launch.CommandLine, OriginTool: "command", ToolCallID: j.ToolCallID,
		Run: func(ctx context.Context, stdout, stderr io.Writer) (int, error) {
			<-ready
			result, err := launch.Run(ctx, commandprogram.Streams{Input: commandprogram.NullInput(), Output: stdout, Error: stderr})
			if ctx.Err() != nil && result.Terminal == "" {
				result = toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.EffectUnknown}
			}
			j.record(handle, result, err)
			return result.Status, err
		},
	})
	if err != nil {
		close(ready)
		return "", err
	}
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

// Wait returns the job's own settled outcome; waiting does not stop it.
func (j *SessionJobs) Wait(ctx context.Context, handle string) (toolcontract.ProducerResult, error) {
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
func (j *SessionJobs) Signal(_ context.Context, handle, signal string) error {
	switch signal {
	case "TERM", "INT", "KILL", "15", "2", "9":
	default:
		return toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"signal": signal, "expected": "TERM, INT or KILL"})
	}
	if _, err := j.Registry.Lifecycle.Stop(j.SessionID, handle); err != nil {
		return jobLookupError(err, handle)
	}
	return nil
}

// List reports this session's live jobs, never host-global processes.
func (j *SessionJobs) List(context.Context) ([]commandprogram.JobStatus, error) {
	var out []commandprogram.JobStatus
	for _, job := range j.Registry.ActiveJobs(j.SessionID) {
		line := ""
		if len(job.Stages) > 0 {
			line = job.Stages[0].Command
		}
		out = append(out, commandprogram.JobStatus{Handle: job.Handle, CommandLine: line, Running: true})
	}
	return out, nil
}

func jobLookupError(err error, handle string) error {
	if errors.Is(err, bgprocess.ErrProcessNotFound) {
		return toolrejection.RejectInvalidArguments("PROCESS_REFERENCE_STALE", map[string]any{"job": handle, "reason": "not a job of this session"})
	}
	return err
}
