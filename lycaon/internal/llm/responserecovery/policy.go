// Package responserecovery decides whether an uncommitted response can be replaced.
package responserecovery

import (
	"errors"

	"github.com/lycaon/lycaon/internal/llm/failure"
)

const MaxConsecutive = 2

type CommitState uint8

const (
	CommitUnknown CommitState = iota
	ResponseUncommitted
	ResponseCommitted
)

// Facts describe the failed response, not earlier operations in the turn.
type Facts struct {
	Cause            error
	Commit           CommitState
	Canceled         bool
	RemainingCalls   int
	NativeTools      bool
	StructuredOutput bool
}

type Decision struct {
	Code       string
	ProviderID string
	Model      string
	Diagnostic string
}

// State shares the allowance across malformed response categories.
type State struct{ consecutive int }

func (s *State) Decide(f Facts) Decision {
	if f.Canceled || f.Commit != ResponseUncommitted || f.RemainingCalls <= 0 || s.consecutive >= MaxConsecutive {
		return Decision{}
	}
	var protocol *failure.ProviderOutputProtocolError
	if errors.As(f.Cause, &protocol) && protocol.Kind == failure.MalformedEnvelope {
		return Decision{Code: "MODEL_RESPONSE_INVALID", ProviderID: protocol.ProviderID, Model: protocol.Model}
	}
	if dropped, ok := failure.AsProviderToolCallUnparsed(f.Cause); ok && f.NativeTools && !f.StructuredOutput {
		return Decision{Code: "TOOL_CALL_UNPARSED", ProviderID: dropped.ProviderID, Model: dropped.Model, Diagnostic: dropped.Diagnostic}
	}
	return Decision{}
}

// Replaced records an admitted replacement after feedback policy accepts it.
func (s *State) Replaced()        { s.consecutive++ }
func (s *State) Accepted()        { s.consecutive = 0 }
func (s *State) Consecutive() int { return s.consecutive }
