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
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/hostprocess"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// SessionJobs puts a program's & jobs in the session's process registry, so
// jobs, wait, kill, command_output and command_stop share one set of handles.
// A native job has a session numeric identifier; its stop cancels its context.
type SessionJobs struct {
	Registry      *bgprocess.Registry
	Supervisor    *ProgramSupervisor
	SessionID     string
	RootSessionID string
	ProjectID     string
	ToolCallID    string

	mu        sync.Mutex
	closed    atomic.Bool
	results   map[string]jobOutcome
	waiters   map[string]map[*jobWaiter]bool
	signals   map[string]int
	processes map[string]jobProcess
	// order is the job table in start order. A job takes one more than the
	// highest number in the table and leaves it once waited, so numbers are
	// reused as a shell reuses them.
	order   []string
	numbers map[string]int
	// programOrder tracks handles started by the active program.
	programOrder []string
	publicIDs    map[string]int
	nextNativeID int
}

type jobWaiter struct{ unblock func() }

// sessionJobsKey binds admission to this retained session owner.
type sessionJobsKey struct{}

type jobOutcome struct {
	result toolcontract.ProducerResult
	err    error
}

// jobStatus is the registry's observation; a stop request is not an exit.
type jobProcess struct {
	Identity    hostprocess.Process
	PID         int
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
			p := jobProcess{CommandLine: strings.Join(args, " "), PID: pid}
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
	// Completed raw handles remain waitable until the registry evicts them
	// under its bounded MaxRecent history policy. Only %N order is live-only.
	for handle := range j.results {
		if _, err := j.Registry.CommandLine(j.SessionID, handle); err != nil {
			delete(j.results, handle)
		}
	}
	for handle := range j.signals {
		if _, err := j.Registry.CommandLine(j.SessionID, handle); err != nil {
			delete(j.signals, handle)
		}
	}
	kept := j.order[:0]
	for _, handle := range j.order {
		if active[handle] {
			kept = append(kept, handle)
		} else {
			delete(j.numbers, handle)
			delete(j.processes, handle)
			delete(j.publicIDs, handle)
		}
	}
	j.order = kept
	j.programOrder = nil
	return context.WithValue(ctx, sessionJobsKey{}, j), nil
}

// start runs the job body detached from the launching statement's context.
func (j *SessionJobs) start(ctx context.Context, commandLine string, run func(context.Context, string, commandprogram.Streams) (toolcontract.ProducerResult, error)) (string, error) {
	if j == nil || j.Registry == nil || run == nil {
		return "", fmt.Errorf("program jobs require a session registry and body")
	}
	// Serialize registration against closing this retained session owner.
	j.mu.Lock()
	if j.closed.Load() {
		j.mu.Unlock()
		return "", ErrProgramStopped
	}
	jobCtx, setStop, release, err := j.Supervisor.Hold(ctx)
	if err != nil {
		j.mu.Unlock()
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
			result, err := run(ctx, handle, commandprogram.Streams{Input: commandprogram.NullInput(), Output: stdout, Error: stderr})
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
		j.mu.Unlock()
		release()
		close(ready)
		return "", err
	}
	setStop(func() { _, _ = j.Registry.Lifecycle.Stop(j.SessionID, handle) })
	if j.numbers == nil {
		j.numbers = map[string]int{}
	}
	number := 1
	for _, current := range j.order {
		number = max(number, j.numbers[current]+1)
	}
	j.numbers[handle] = number
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
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return strconv.Itoa(j.publicID(handle)), nil
}

// publicID exposes the real process PID when one started. Native jobs use a
// session-unique positive integer above the platform process ID range.
func (j *SessionJobs) publicID(handle string) int {
	j.mu.Lock()
	defer j.mu.Unlock()
	if id := j.publicIDs[handle]; id > 0 {
		return id
	}
	if pid := j.processes[handle].PID; pid > 0 {
		if j.publicIDs == nil {
			j.publicIDs = map[string]int{}
		}
		j.publicIDs[handle] = pid
		return pid
	}
	if j.nextNativeID == 0 {
		j.nextNativeID = 1 << 22
	}
	j.nextNativeID++
	if j.publicIDs == nil {
		j.publicIDs = map[string]int{}
	}
	j.publicIDs[handle] = j.nextNativeID
	return j.nextNativeID
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

func (j *SessionJobs) pid(handle string) int { return j.process(handle).PID }

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
	for waiter := range j.waiters[handle] {
		waiter.unblock()
		delete(j.waiters[handle], waiter)
	}
	delete(j.waiters, handle)
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
			index = slices.IndexFunc(j.order, func(handle string) bool { return j.numbers[handle] == n })
		}
	default:
		if pid, err := strconv.Atoi(reference); err == nil && pid > 0 {
			for handle, id := range j.publicIDs {
				if id == pid {
					return handle, nil
				}
			}
			for handle, process := range j.processes {
				if process.PID != pid {
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
	return j.numbers[handle]
}

// reap removes a waited job from the job table; its outcome stays for a
// later wait on its PID or handle.
func (j *SessionJobs) reap(handle string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.order = slices.DeleteFunc(j.order, func(current string) bool { return current == handle })
	delete(j.numbers, handle)
}

// Wait returns the job's own settled outcome; waiting does not stop it.
func (j *SessionJobs) Wait(ctx context.Context, reference string, activity *commandprogram.ExecutionWait, statement string) (toolcontract.ProducerResult, error) {
	handle, err := j.Resolve(reference)
	if err != nil {
		return toolcontract.ProducerResult{}, err
	}
	j.mu.Lock()
	var waiter *jobWaiter
	if _, complete := j.results[handle]; !complete && activity != nil {
		unblock, err := activity.Block(ctx, commandprogram.WaitState{Statement: statement, Jobs: []string{handle}})
		if err != nil {
			j.mu.Unlock()
			return toolcontract.ProducerResult{}, err
		}
		waiter = &jobWaiter{unblock: unblock}
		if j.waiters == nil {
			j.waiters = map[string]map[*jobWaiter]bool{}
		}
		if j.waiters[handle] == nil {
			j.waiters[handle] = map[*jobWaiter]bool{}
		}
		j.waiters[handle][waiter] = true
	}
	j.mu.Unlock()
	if waiter != nil {
		defer func() {
			j.mu.Lock()
			defer j.mu.Unlock()
			if j.waiters[handle][waiter] {
				waiter.unblock()
				delete(j.waiters[handle], waiter)
				if len(j.waiters[handle]) == 0 {
					delete(j.waiters, handle)
				}
			}
		}()
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
	j.reap(handle)
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
		out = append(out, jobStatus{Handle: job.Handle, PID: process.PID,
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
