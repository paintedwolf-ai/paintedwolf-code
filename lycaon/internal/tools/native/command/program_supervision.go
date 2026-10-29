package command

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// ProgramInterrupter is the receipt owner's host-loss reconciliation.
type ProgramInterrupter interface {
	InterruptPrograms(ctx context.Context, sessionID, detailReference string) (int64, error)
}

// ErrProgramStopped is the cancellation cause of an explicit stop.
var ErrProgramStopped = errors.New("command program stopped")

// errSupervisorLost is the cancellation cause when supervision is lost.
var errSupervisorLost = errors.New("command program supervisor lost")

// ProgramSupervisor keeps live programs in memory under host supervision. It
// holds no interpreter checkpoint: stop closes admission, then cancels owned
// work; loss interrupts the program and reconciles receipts without replay.
type ProgramSupervisor struct {
	Interrupter ProgramInterrupter
	// Background is the session process registry that holds & jobs.
	Background *bgprocess.Registry

	mu       sync.Mutex
	programs map[string]*supervisedProgram
	sessions map[string]*SessionJobs
}

// Jobs returns the session's job set; wait and kill read the same outcomes
// StartJob records.
func (s *ProgramSupervisor) Jobs(sessionID, projectID string) *SessionJobs {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, program := range s.programs {
		if program.sessionID == sessionID && program.closed && program.sessionJobs != nil {
			return program.sessionJobs
		}
	}
	if s.sessions == nil {
		s.sessions = map[string]*SessionJobs{}
	}
	jobs, ok := s.sessions[sessionID]
	if !ok {
		jobs = &SessionJobs{Registry: s.Background, Supervisor: s, SessionID: sessionID, RootSessionID: sessionID, ProjectID: projectID}
		s.sessions[sessionID] = jobs
	}
	return jobs
}

// StartJob supervises one statement-trailing & job in the session registry.
func (s *ProgramSupervisor) StartJob(ctx context.Context, invocation toolcontract.OperationInvocation, run func(context.Context, commandprogram.Streams) (toolcontract.ProducerResult, error)) (string, error) {
	if s == nil || s.Background == nil {
		return "", fmt.Errorf("program jobs require the session process registry")
	}
	id, _ := ctx.Value(supervisedKey{}).(string)
	s.mu.Lock()
	program := s.programs[id]
	var jobs *SessionJobs
	if program != nil && !program.closed && program.sessionID == invocation.SessionID {
		jobs = program.sessionJobs
	}
	s.mu.Unlock()
	if jobs == nil {
		return "", ErrProgramStopped
	}
	return jobs.start(ctx, "background job "+invocation.Identity.ExecutionID, run)
}

type supervisedProgram struct {
	sessionID   string
	sessionJobs *SessionJobs
	closed      bool
	cancel      context.CancelCauseFunc
	jobs        map[int]*supervisedJob
	nextJob     int
	foreground  bool
	settle      func(stopped bool)
}

type supervisedKey struct{}

var _ commandprogram.ProgramSupervision = (*ProgramSupervisor)(nil)

// RunProgram supervises one program to its aggregate outcome. A stop or loss
// overrides the evaluator's status with the typed host stop; children keep the
// outcomes their owners already settled.
func (s *ProgramSupervisor) RunProgram(ctx context.Context, root toolcontract.OperationInvocation, run func(context.Context) (toolcontract.ProducerResult, error)) (toolcontract.ProducerResult, error) {
	if s == nil || run == nil || root.Identity.ProgramID == "" {
		return toolcontract.ProducerResult{}, fmt.Errorf("program supervision requires a supervisor, program and body")
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	program := &supervisedProgram{sessionID: root.SessionID, cancel: cancel}
	s.mu.Lock()
	if s.programs == nil {
		s.programs = map[string]*supervisedProgram{}
	}
	if _, exists := s.programs[root.Identity.ProgramID]; exists {
		s.mu.Unlock()
		return toolcontract.ProducerResult{}, fmt.Errorf("program %s is already supervised", root.Identity.ProgramID)
	}
	program.sessionJobs = s.sessions[root.SessionID]
	if admitted, ok := ctx.Value(sessionJobsKey{}).(*SessionJobs); ok && admitted != program.sessionJobs {
		s.mu.Unlock()
		return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, StopCode: toolrejection.ToolOwnerInterruptedCode, EffectCertainty: toolcontract.EffectUnknown}, ErrProgramStopped
	}
	if program.sessionJobs != nil {
		closed := program.sessionJobs.closed.Load()
		if closed {
			s.mu.Unlock()
			return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, StopCode: toolrejection.ToolOwnerInterruptedCode, EffectCertainty: toolcontract.EffectUnknown}, ErrProgramStopped
		}
	}
	s.programs[root.Identity.ProgramID] = program
	s.mu.Unlock()
	defer s.foregroundDone(root.Identity.ProgramID)
	result, err := run(context.WithValue(ctx, supervisedKey{}, root.Identity.ProgramID))
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, ErrProgramStopped), errors.Is(cause, errSupervisorLost):
		stopped := toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, StopCode: toolrejection.ToolOwnerInterruptedCode, EffectCertainty: result.EffectCertainty}
		if stopped.EffectCertainty == "" {
			stopped.EffectCertainty = toolcontract.EffectUnknown
		}
		return stopped, errors.Join(err, cause)
	}
	return result, err
}

// AdmitChild refuses new children once their program's admission is closed.
// Dispatch calls it before every child, before any owner is reached.
func (s *ProgramSupervisor) AdmitChild(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id, _ := ctx.Value(supervisedKey{}).(string)
	if s == nil || id == "" {
		return fmt.Errorf("program child is outside supervision")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	program, ok := s.programs[id]
	if !ok || program.closed {
		return toolrejection.RejectInvalidArguments(toolrejection.ToolOwnerInterruptedCode, map[string]any{"program_id": id, "reason": "program admission closed"})
	}
	return nil
}

// Stop closes admission for the session's programs and then cancels them. The
// returned IDs acknowledge the request; settlement reports the actual exit.
func (s *ProgramSupervisor) Stop(sessionID string) []string {
	return s.end(sessionID, ErrProgramStopped)
}

// Lost handles supervisor loss for a live host: programs are interrupted,
// receipts reconciled through the owner, and nothing is replayed.
func (s *ProgramSupervisor) Lost(ctx context.Context, sessionID, detailReference string) (int64, error) {
	s.end(sessionID, errSupervisorLost)
	if s.Interrupter == nil {
		return 0, fmt.Errorf("program supervisor has no receipt reconciliation")
	}
	return s.Interrupter.InterruptPrograms(ctx, sessionID, detailReference)
}

// StopAll stops every session's programs, for the host disable control.
func (s *ProgramSupervisor) StopAll() []string {
	return s.end("", ErrProgramStopped)
}

func (s *ProgramSupervisor) end(sessionID string, cause error) []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	var ids []string
	var cancels []context.CancelCauseFunc
	var jobs []*supervisedJob
	for id, program := range s.programs {
		if (sessionID != "" && program.sessionID != sessionID) || program.closed {
			continue
		}
		program.closed = true
		ids = append(ids, id)
		cancels = append(cancels, program.cancel)
		for _, job := range program.jobs {
			jobs = append(jobs, job)
		}
	}
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel(cause)
	}
	for _, job := range jobs {
		job.end(cause)
	}
	return ids
}

// supervisedJob is one held & job: its context and the registry stop that
// marks the job's handle stopped when supervision ends it.
type supervisedJob struct {
	mu     sync.Mutex
	cancel context.CancelCauseFunc
	stop   func()
}

func (j *supervisedJob) end(cause error) {
	j.mu.Lock()
	stop := j.stop
	j.mu.Unlock()
	if stop != nil {
		stop()
	}
	j.cancel(cause)
}

// Hold keeps a program supervised for one & job beyond its foreground. The
// job context keeps the program's identity, survives the launching call and
// ends on Stop or loss, which also calls the stop registered with setStop.
// release ends the hold exactly once.
func (s *ProgramSupervisor) Hold(ctx context.Context) (jobCtx context.Context, setStop func(func()), release func(), err error) {
	id, _ := ctx.Value(supervisedKey{}).(string)
	if s == nil || id == "" {
		return nil, nil, nil, fmt.Errorf("program job is outside supervision")
	}
	jobCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	job := &supervisedJob{cancel: cancel}
	s.mu.Lock()
	program, ok := s.programs[id]
	if !ok || program.closed {
		s.mu.Unlock()
		cancel(ErrProgramStopped)
		return nil, nil, nil, toolrejection.RejectInvalidArguments(toolrejection.ToolOwnerInterruptedCode, map[string]any{"program_id": id, "reason": "program admission closed"})
	}
	if program.jobs == nil {
		program.jobs = map[int]*supervisedJob{}
	}
	program.nextJob++
	key := program.nextJob
	program.jobs[key] = job
	s.mu.Unlock()
	var once sync.Once
	setStop = func(stop func()) {
		job.mu.Lock()
		job.stop = stop
		job.mu.Unlock()
	}
	return jobCtx, setStop, func() {
		once.Do(func() {
			cancel(nil)
			s.mu.Lock()
			delete(program.jobs, key)
			s.mu.Unlock()
			s.finishIfIdle(id)
		})
	}, nil
}

// AfterJobs runs settle once the program's foreground and every held job end;
// stopped reports whether a stop or loss closed the program first.
func (s *ProgramSupervisor) AfterJobs(programID string, settle func(stopped bool)) {
	s.mu.Lock()
	program, ok := s.programs[programID]
	if ok {
		program.settle = settle
	}
	s.mu.Unlock()
	if !ok {
		settle(false)
		return
	}
	s.finishIfIdle(programID)
}

func (s *ProgramSupervisor) foregroundDone(programID string) {
	s.mu.Lock()
	if program, ok := s.programs[programID]; ok {
		program.foreground = true
	}
	s.mu.Unlock()
	s.finishIfIdle(programID)
}

func (s *ProgramSupervisor) finishIfIdle(programID string) {
	s.mu.Lock()
	program, ok := s.programs[programID]
	if !ok || !program.foreground || len(program.jobs) > 0 || program.settle == nil {
		s.mu.Unlock()
		return
	}
	delete(s.programs, programID)
	settle, stopped := program.settle, program.closed
	s.mu.Unlock()
	settle(stopped)
}

// DisposeSession closes the retained owner before draining registered jobs. A
// failed drain keeps that owner closed for retry; no tombstone outlives it.
func (s *ProgramSupervisor) DisposeSession(ctx context.Context, sessionID string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	jobs := s.sessions[sessionID]
	s.mu.Unlock()
	if jobs != nil {
		jobs.mu.Lock()
		jobs.closed.Store(true)
		jobs.mu.Unlock()
	}
	s.Stop(sessionID)
	if s.Background != nil {
		if err := s.Background.Lifecycle.DisposeSession(ctx, sessionID); err != nil {
			return err
		}
	}
	s.mu.Lock()
	if s.sessions[sessionID] == jobs {
		delete(s.sessions, sessionID)
	}
	s.mu.Unlock()
	return nil
}
