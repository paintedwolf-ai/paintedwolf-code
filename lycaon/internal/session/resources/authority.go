package resources

import (
	"context"

	"github.com/lycaon/lycaon/internal/resourcelifecycle"
)

type Grants interface {
	NoteUserIntentBoundary(string)
	ReleaseRun(string)
	ForgetSession(string)
}

// Authority keeps approved chat grants across Stop and discards them on disposal.
type Authority struct {
	Writes   Grants
	Listen   Grants
	Loopback Grants
}

func (a *Authority) ReleaseRun(id string) {
	if a.Writes != nil {
		a.Writes.ReleaseRun(id)
	}
	if a.Listen != nil {
		a.Listen.ReleaseRun(id)
	}
	if a.Loopback != nil {
		a.Loopback.ReleaseRun(id)
	}
}

func (a *Authority) dispose(_ context.Context, scope resourcelifecycle.Scope) error {
	if a.Writes != nil {
		a.Writes.ForgetSession(scope.ID)
	}
	if a.Listen != nil {
		a.Listen.ForgetSession(scope.ID)
	}
	if a.Loopback != nil {
		a.Loopback.ForgetSession(scope.ID)
	}
	return nil
}

// ToolState forgets approval coalescing and feedback counters from a released run.
type ToolState struct {
	Approvals IntentMemory
	Repeat    IntentMemory
	Counters  SessionForgetter
	Rejects   SessionForgetter
	Outputs   SessionForgetter
}

func (s *ToolState) Forget(id string) {
	if s.Approvals != nil {
		s.Approvals.ForgetSession(id)
	}
	if s.Repeat != nil {
		s.Repeat.ForgetSession(id)
	}
	if s.Counters != nil {
		s.Counters.ForgetSession(id)
	}
	if s.Rejects != nil {
		s.Rejects.ForgetSession(id)
	}
	if s.Outputs != nil {
		s.Outputs.ForgetSession(id)
	}
}

type IntentMemory interface {
	SessionForgetter
	NoteUserIntentBoundary(string)
}
