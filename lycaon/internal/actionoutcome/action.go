package actionoutcome

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/pkg/api"
)

type Result struct {
	State       api.ContinuationState
	Reason      api.ContinuationReason
	TurnID      string
	FailureCode string
}

type Action struct {
	mu      sync.Mutex
	record  api.ActionContinuation
	paths   []*Path
	pending int
	sealed  bool
	history *History[api.ActionContinuation]
	publish func(api.ActionContinuation)
}

type actionKey struct{}

func (s *Session) BeginAction(ctx context.Context, sessionID string, kind api.ContinuationAction, publish func(api.ActionContinuation)) (context.Context, *Action) {
	a := &Action{history: s.Actions, publish: publish, record: api.ActionContinuation{
		ContinuationID: uuid.NewString(), SessionID: sessionID, Action: kind, State: api.ContinuationStatePending, OpenedAt: time.Now().UTC(),
	}}
	s.Actions.Open(a.ID(), a.record)
	if publish != nil {
		publish(a.record)
	}
	return context.WithValue(ctx, actionKey{}, a), a
}

func (a *Action) ID() string { return a.record.ContinuationID }

type Path struct {
	action *Action
	result Result
}

func Register(ctx context.Context, sessionID string) *Path {
	a, _ := ctx.Value(actionKey{}).(*Action)
	if a == nil || a.record.SessionID != sessionID {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sealed || a.record.State != api.ContinuationStatePending {
		return nil
	}
	p := &Path{action: a}
	a.paths = append(a.paths, p)
	a.pending++
	return p
}

func (p *Path) Resolve(r Result) {
	if p == nil {
		return
	}
	a := p.action
	a.mu.Lock()
	if p.result.State != "" {
		a.mu.Unlock()
		return
	}
	p.result = r
	a.pending--
	ev := a.resolveLocked(r)
	a.mu.Unlock()
	if ev != nil && a.publish != nil {
		a.publish(*ev)
	}
}

func (a *Action) Seal() {
	a.mu.Lock()
	a.sealed = true
	ev := a.resolveLocked(Result{})
	a.mu.Unlock()
	if ev != nil && a.publish != nil {
		a.publish(*ev)
	}
}

func (a *Action) resolveLocked(r Result) *api.ActionContinuation {
	if a.record.State != api.ContinuationStatePending {
		return nil
	}
	if r.State != api.ContinuationStateContinued {
		if !a.sealed || a.pending > 0 {
			return nil
		}
		r = Result{State: api.ContinuationStateFailed, FailureCode: string(api.HostContinuationFailureHostInvariantFailed)}
		best := 0
		for _, p := range a.paths {
			if priority := resultPriority(p.result.State); priority > best {
				best = priority
				r = p.result
			}
		}
	}
	now := time.Now().UTC()
	a.record.State = r.State
	a.record.Reason = r.Reason
	a.record.FailureCode = r.FailureCode
	a.record.TurnID = r.TurnID
	a.record.ResolvedAt = &now
	value := a.record
	a.history.Change(a.ID(), func(api.ActionContinuation) (api.ActionContinuation, bool) { return value, true })
	return &value
}

func resultPriority(state api.ContinuationState) int {
	switch state {
	case api.ContinuationStateFailed:
		return 4
	case api.ContinuationStateHeld:
		return 3
	case api.ContinuationStateFinished:
		return 2
	case api.ContinuationStateNotContinued:
		return 1
	default:
		return 0
	}
}
