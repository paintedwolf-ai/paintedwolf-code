package nativegen

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/modelstore"
)

type ReadinessState string
type RecoveryAction string

type Readiness struct {
	State         ReadinessState
	Action        RecoveryAction
	Detail        string
	Revision      string
	ContextTokens int
	exited        <-chan struct{}
}

// Readiness does not wait for inference or read model payloads.
func (p *Runtime) Readiness() Readiness {
	if p == nil {
		return Readiness{State: "unavailable", Action: "reinstall"}
	}
	if p.lifetime.Err() != nil {
		return Readiness{State: "stopped", Action: "none"}
	}
	current := p.readiness.Load()
	if current == nil {
		return Readiness{State: "idle", Action: "retry"}
	}
	result := *current
	if result.exited != nil {
		select {
		case <-result.exited:
			return Readiness{State: "idle", Action: "retry"}
		default:
		}
	}
	return result
}

func (p *Runtime) publishReadiness(state ReadinessState, action RecoveryAction) {
	p.readiness.Store(&Readiness{State: state, Action: action})
}

func (p *Runtime) settleReadiness(err error) {
	if err == nil && p.loaded != nil {
		p.readiness.Store(&Readiness{
			State: "ready", Action: "none", Revision: p.loaded.process.lease.Manifest.Revision,
			ContextTokens: p.ContextWindow(), exited: p.loaded.process.done,
		})
		return
	}
	result := Readiness{State: "unavailable", Action: "retry"}
	if err != nil {
		result.Detail = err.Error()
	}
	switch {
	case errors.Is(err, inference.ErrCapacity):
		result.State = "insufficient_memory"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		result.State = "idle"
	case p.config.ModelStore != nil:
		status := p.config.ModelStore.Status(DefaultModelID, p.config.Backend)
		if status.State != modelstore.StateReady {
			result.State, result.Action = "repair_required", "repair"
			if p.config.SeedDirectory == "" {
				result.Action = "reinstall"
			}
		}
	}
	p.readiness.Store(&result)
}
