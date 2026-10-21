package command

import (
	"context"
	"errors"
	"fmt"
	"sync"

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

	mu       sync.Mutex
	programs map[string]*supervisedProgram
}

type supervisedProgram struct {
	sessionID string
	closed    bool
	cancel    context.CancelCauseFunc
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
	s.programs[root.Identity.ProgramID] = program
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.programs, root.Identity.ProgramID)
		s.mu.Unlock()
	}()
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

func (s *ProgramSupervisor) end(sessionID string, cause error) []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	var ids []string
	var cancels []context.CancelCauseFunc
	for id, program := range s.programs {
		if program.sessionID != sessionID || program.closed {
			continue
		}
		program.closed = true
		ids = append(ids, id)
		cancels = append(cancels, program.cancel)
	}
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel(cause)
	}
	return ids
}
