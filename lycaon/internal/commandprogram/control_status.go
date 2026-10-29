package commandprogram

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// ProgramLimitError reports host resource refusal independently of utility status.
type ProgramLimitError struct {
	Limit             string
	Maximum, Observed int64
}

func (e *ProgramLimitError) Error() string {
	return fmt.Sprintf("program %s ceiling exceeded: %d > %d", e.Limit, e.Observed, e.Maximum)
}

// StatusConnector selects the next reached statement without expanding it.
type StatusConnector uint8

const (
	StatusSequence StatusConnector = iota
	StatusAnd
	StatusOr
)

// StatusControl is one program or isolated subprogram's status and explicit exit.
// The shared latch prevents host stops being erased by branches or child frames.
// Functions share their enclosing program exit control. Parallel jobs retain
// their own status control; they never race this sequential state.
type StatusControl struct {
	latch  *OutcomeLatch
	status int
	exited bool
}

func NewStatusControl(latch *OutcomeLatch) (*StatusControl, error) {
	if latch == nil {
		return nil, fmt.Errorf("status control requires a host-stop latch")
	}
	return &StatusControl{latch: latch}, nil
}
func (s *StatusControl) Status() int  { return s.status }
func (s *StatusControl) Exited() bool { return s.exited }

// ShouldRun is queried before word expansion, substitution or effect admission.
func (s *StatusControl) ShouldRun(connector StatusConnector) (bool, error) {
	if err := s.latch.AdmitNext(); err != nil {
		return false, err
	}
	if s.exited {
		return false, nil
	}
	switch connector {
	case StatusSequence:
		return true, nil
	case StatusAnd:
		return s.status == 0, nil
	case StatusOr:
		return s.status != 0, nil
	default:
		return false, fmt.Errorf("invalid status connector")
	}
}

// Record keeps original ordinary status except for explicit shell negation.
// Host stops retain their original status and cannot be converted to a branch.
func (s *StatusControl) Record(executionID string, result toolcontract.ProducerResult, requiredInputComplete, negated bool) error {
	err := s.latch.Record(executionID, result, requiredInputComplete)
	if stop, stopped := s.latch.Stop(); stopped {
		s.status = stop.Result.Status
		if s.status < 0 || s.status > 255 {
			s.status = 2
		}
		return err
	}
	if err != nil {
		return err
	}
	if s.exited {
		return nil
	}
	s.status = result.Status
	if _, stopped := s.latch.Stop(); !stopped && negated {
		if s.status == 0 {
			s.status = 1
		} else {
			s.status = 0
		}
	}
	return nil
}

// Exit changes this frame only after ordinary status validation and admission.
// Its caller records the admitted builtin through the existing child receipt.
func (s *StatusControl) Exit(status int) error {
	if err := s.latch.AdmitNext(); err != nil {
		return err
	}
	if s.exited {
		return fmt.Errorf("program already exited")
	}
	if status < 0 || status > 255 {
		return &StatusOperandError{Value: status}
	}
	s.status = status
	s.exited = true
	return nil
}

// StatusOperandError is a builtin usage error (status2), never modulo wrapping.
type StatusOperandError struct{ Value int }

func (e *StatusOperandError) Error() string {
	return fmt.Sprintf("utility status outside 0..255: %d", e.Value)
}

// SubstitutionFailure prevents the containing launch without reparsing output.
// Ordinary nonzero status remains handleable by the following outer sequence.
type SubstitutionFailure struct {
	Status   int
	Complete bool
}

func (e *SubstitutionFailure) Error() string {
	return fmt.Sprintf("substitution failed at status %d (complete=%t)", e.Status, e.Complete)
}

// AdmitSubstitution checks the settled subprogram before admitting its parent.
// Missing required complete output latches; ordinary nonzero remains ordinary.
func (s *StatusControl) AdmitSubstitution(executionID string, result PipelineOutcome) error {
	if err := s.latch.AdmitNext(); err != nil {
		return err
	}
	if executionID == "" || result.Status < 0 || result.Status > 255 {
		invalid := toolcontract.ProducerResult{Status: result.Status, Terminal: toolcontract.ProducerCompleted, EffectCertainty: toolcontract.EffectUnknown}
		return s.latch.Record(executionID, invalid, true)
	}
	if !result.Complete {
		_ = s.latch.Record(executionID, toolcontract.ProducerResult{Status: result.Status, Terminal: toolcontract.ProducerCompleted, EffectCertainty: toolcontract.EffectUnknown}, false)
		return s.latch.AdmitNext()
	}
	if result.Status != 0 {
		return &SubstitutionFailure{Status: result.Status, Complete: true}
	}
	return nil
}
