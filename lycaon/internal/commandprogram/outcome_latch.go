package commandprogram

import (
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// HostStopKind distinguishes terminal host facts from ordinary utility status.
type HostStopKind string

const (
	StopInvalidResult HostStopKind = "invalid_result"
	StopAuthority     HostStopKind = "authority_refused"
	StopInterrupted   HostStopKind = "interrupted"
	StopRequiredInput HostStopKind = "required_input_incomplete"
	StopOwner         HostStopKind = "owner_stop"
)

// HostStop retains the first stopping fact. It is neither a new effect owner nor
// a replacement for the original domain's rejection and recovery evidence.
type HostStop struct {
	Kind        HostStopKind
	ExecutionID string
	Result      toolcontract.ProducerResult
}

// OutcomeLatch guards further admission, independently of conditionals, negation
// or explicit exit. Existing operations still settle through their own domains.
type OutcomeLatch struct {
	mu   sync.Mutex
	stop *HostStop
}

func (l *OutcomeLatch) Record(executionID string, result toolcontract.ProducerResult, requiredInputComplete bool) error {
	err := result.Validate()
	var kind HostStopKind
	switch {
	case err != nil:
		kind = StopInvalidResult
	case result.Terminal == toolcontract.ProducerRefused:
		kind = StopAuthority
	case result.Terminal == toolcontract.ProducerInterrupted:
		kind = StopInterrupted
	case result.StopCode != "":
		kind = StopOwner
	case !requiredInputComplete:
		kind = StopRequiredInput
	}
	if kind != "" {
		l.mu.Lock()
		if l.stop == nil {
			l.stop = &HostStop{Kind: kind, ExecutionID: executionID, Result: result}
		}
		l.mu.Unlock()
	}
	return err
}

func (l *OutcomeLatch) Stop() (HostStop, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stop == nil {
		return HostStop{}, false
	}
	return *l.stop, true
}

func (l *OutcomeLatch) AdmitNext() error {
	if stop, ok := l.Stop(); ok {
		return &AdmissionStoppedError{Stop: stop}
	}
	return nil
}

type AdmissionStoppedError struct{ Stop HostStop }

func (e *AdmissionStoppedError) Error() string {
	return fmt.Sprintf("program admission stopped at execution %s: %s", e.Stop.ExecutionID, e.Stop.Kind)
}
