package loopwake

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
)

// loopDepsForTest returns idle coordinator defaults.
func loopDepsForTest() LoopDeps {
	return LoopDeps{
		IsCoordinatorSession: func(context.Context, *api.Session) bool { return true },
		Limits:               func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		WorkerCycleIdle:      func(context.Context, *api.Session, string) (bool, error) { return true, nil },
		QueueInform:          func(context.Context, string, anchor.ID, anchor.Envelope) {},
	}
}

type StubLoopWF struct {
	run                *api.WorkflowRun
	vars               map[string]any
	hostObligationHeld bool
	hostObligationKind string
}

func (s StubLoopWF) ActiveRun(context.Context, string) (*api.WorkflowRun, error) {
	return s.run, nil
}

func (s StubLoopWF) ScaffoldVars(context.Context, string) (map[string]any, error) {
	if s.vars != nil {
		return s.vars, nil
	}
	return map[string]any{}, nil
}

func (s StubLoopWF) HumanApprovalAwaiting(context.Context, string) (bool, error) {
	return scaffoldvars.HumanApprovalAwaiting(s.vars), nil
}

func (s StubLoopWF) HostObligationHeld(context.Context, string) (bool, error) {
	return s.hostObligationHeld, nil
}

func (s StubLoopWF) HostObligationHoldKinds(context.Context, string) []string {
	if !s.hostObligationHeld || s.hostObligationKind == "" {
		return nil
	}
	return []string{s.hostObligationKind}
}
