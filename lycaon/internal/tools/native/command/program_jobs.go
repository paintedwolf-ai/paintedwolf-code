package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/hostprocess"
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

	mu        sync.Mutex
	results   map[string]jobOutcome
	signals   map[string]int
	processes map[string]jobProcess
	// order lists handles in start order; %N names order[N-1].
	order []string
	// programOrder tracks handles started by the active program.
	programOrder []string
}

type jobOutcome struct {
	result toolcontract.ProducerResult
	err    error
}

// jobStatus is the registry's observation; a stop request is not an exit.
type jobProcess struct {
	Identity    hostprocess.Process
	Name        string
	CommandLine string
}

type jobStatus struct {
	Process     hostprocess.Process
	Name        string
	Handle      string
	PID         int
	CommandLine string
}

type jobProcessKey struct{}

type jobProcessTracker struct {
	once     sync.Once
	ready    chan struct{}
	onRecord func(jobProcess)
}

func noteJobProcess(ctx context.Context, pid int, args []string) {
	t, _ := ctx.Value(jobProcessKey{}).(*jobProcessTracker)
	if t == nil {
		return
	}
	t.once.Do(func() {
		if t.onRecord != nil {
			p := jobProcess{CommandLine: strings.Join(args, " ")}
			if len(args) > 0 {
				p.Name = filepath.Base(args[0])
			}
			if pid > 0 {
				p.Identity, _ = hostprocess.Observe(pid)
			}
			t.onRecord(p)
		}
		close(t.ready)
	})
}

func noteJobNative(ctx context.Context, args []string) {
	noteJobProcess(ctx, 0, args)
}

// BeginProgram prunes finished jobs from prior programs before a new program
// runs so %1 names the first active or newly started job.
func (j *SessionJobs) BeginProgram() {
	if j == nil || j.Registry == nil {
		return
	}
	active := map[string]bool{}
	for _, job := range j.Registry.ActiveJobs(j.SessionID) {
		active[job.Handle] = true
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	kept := j.order[:0]
	for _, handle := range j.order {
		if active[handle] {
			kept = append(kept, handle)
		} else {
			delete(j.processes, handle)
			delete(j.results, handle)
			delete(j.signals, handle)
		}
	}
	j.order = kept
	j.programOrder = nil
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
	done := make(chan struct{})
	tracker := &jobProcessTracker{ready: make(chan struct{})}
	handle, err = j.Registry.StartProgram(ctx, bgprocess.ProgramJobSpec{
		SessionID: j.SessionID, RootSessionID: j.RootSessionID, ProjectID: j.ProjectID,
		CommandLine: commandLine, OriginTool: "command", ToolCallID: j.ToolCallID,
		Run: func(stopCtx context.Context, stdout, stderr io.Writer) (bgprocess.ProgramJobResult, error) {
			defer close(done)
			defer release()
			<-ready
			ctx, cancel := context.WithCancel(jobCtx)
			defer cancel()
			ctx = context.WithValue(ctx, jobProcessKey{}, tracker)
			stopAfter := context.AfterFunc(stopCtx, cancel)
			defer stopAfter()
			result, err := run(ctx, commandprogram.Streams{Input: commandprogram.NullInput(), Output: stdout, Error: stderr})
			if ctx.Err() != nil && (result.Terminal == "" || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				status := j.signalStatus(handle)
				result = toolcontract.ProducerResult{Status: status, Terminal: toolcontract.ProducerInterrupted, StopCode: toolrejection.ToolOwnerInterruptedCode, EffectCertainty: toolcontract.EffectUnknown}
			}
			j.record(handle, result, err)
			settled := bgprocess.ProgramJobResult{ExitCode: result.Status}
			if documents, ok := ctx.Value(programDocumentsKey{}).(*programDocuments); ok {
				settled.UnsavedDocumentPaths, settled.ReleaseNotices = documents.takePaths()
			}
			return settled, err
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
	j.programOrder = append(j.programOrder, handle)
	j.mu.Unlock()
	tracker.onRecord = func(process jobProcess) {
		j.recordProcess(handle, process)
	}
	close(ready)
	select {
	case <-tracker.ready:
	case <-done:
	case <-time.After(2 * time.Second):
	}
	if pid := j.pid(handle); pid > 0 {
		return strconv.Itoa(pid), nil
	}
	return handle, nil
}

func (j *SessionJobs) recordProcess(handle string, process jobProcess) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.processes == nil {
		j.processes = map[string]jobProcess{}
	}
	j.processes[handle] = process
}

func (j *SessionJobs) process(handle string) jobProcess {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.processes[handle]
}

func (j *SessionJobs) pid(handle string) int { return j.process(handle).Identity.PID }

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
		if pid, err := strconv.Atoi(reference); err == nil && pid > 0 {
			for handle, process := range j.processes {
				if process.Identity.PID != pid {
					continue
				}
				if hostprocess.IsCurrent(process.Identity) || !hostprocess.ProcessExists(pid) {
					return handle, nil
				}
			}
		}
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
	var status int
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
		if pid, convErr := strconv.Atoi(reference); convErr == nil && pid > 0 && (pid == os.Getpid() || hostprocess.ProcessExists(pid)) {
			return processNotOwned("kill", reference)
		}
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
		process := j.process(job.Handle)
		if process.CommandLine != "" {
			line = process.CommandLine
		}
		out = append(out, jobStatus{Handle: job.Handle, PID: process.Identity.PID,
			Process: process.Identity, Name: process.Name, CommandLine: line})
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

func processNotOwned(command, target string) error {
	return toolrejection.RejectInvalidArguments("PROCESS_NOT_OWNED", map[string]any{
		"command": command,
		"job":     target,
		"word":    target,
		"reason":  "process is not owned by this session; signal only background jobs started in this session",
	})
}
