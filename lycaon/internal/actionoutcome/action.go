package actionoutcome

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
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

var actionSequence atomic.Uint64

type Action struct {
	Order     uint64
	mu        sync.Mutex
	record    api.ActionContinuation
	paths     []*Path
	pending   int
	producers int
	replay    bool
	sealed    bool
	history   *History[api.ActionContinuation]
	publish   func(api.ActionContinuation)
}

type actionKey struct{}

func Capture(ctx context.Context, kind api.ContinuationAction) (context.Context, *Action) {
	a := &Action{Order: actionSequence.Add(1), record: api.ActionContinuation{ContinuationID: uuid.NewString(), Action: kind, State: api.ContinuationStatePending, OpenedAt: time.Now().UTC()}}
	return context.WithValue(ctx, actionKey{}, a), a
}

// CaptureOperation gives retries the original continuation handle.
func CaptureOperation(ctx context.Context, kind api.ContinuationAction, key ...string) (context.Context, *Action) {
	ctx, a := Capture(ctx, kind)
	parts := append([]string{string(kind)}, key...)
	a.record.ContinuationID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(strings.Join(parts, "\x00"))).String()
	return ctx, a
}

func MarkReplay(ctx context.Context) {
	if a, _ := ctx.Value(actionKey{}).(*Action); a != nil {
		a.mu.Lock()
		a.replay = true
		a.mu.Unlock()
	}
}

func MarkAskAnswer(ctx context.Context) {
	if a, _ := ctx.Value(actionKey{}).(*Action); a != nil {
		a.mu.Lock()
		if !a.sealed && a.record.Action == api.ContinuationActionFeedback {
			a.record.Action = api.ContinuationActionAskAnswer
		}
		a.mu.Unlock()
	}
}

func (a *Action) ID() string { return a.record.ContinuationID }

type Path struct {
	sessionID string
	action    *Action
	result    Result
}

type producerKey struct{}
type producer struct {
	action   *Action
	released bool
}

// Producer reserves path registration before a mutation hands off work.
func Producer(ctx context.Context) (context.Context, func()) {
	a, _ := ctx.Value(actionKey{}).(*Action)
	if a == nil {
		return ctx, func() {}
	}
	a.mu.Lock()
	if !a.canRegister(ctx) {
		a.mu.Unlock()
		return ctx, func() {}
	}
	a.producers++
	p := &producer{action: a}
	a.mu.Unlock()
	return context.WithValue(ctx, producerKey{}, p), func() {
		a.mu.Lock()
		if p.released {
			a.mu.Unlock()
			return
		}
		p.released = true
		a.producers--
		var ev *api.ActionContinuation
		if a.history != nil {
			ev = a.resolveLocked(Result{})
		}
		a.mu.Unlock()
		if ev != nil && a.publish != nil {
			a.publish(*ev)
		}
	}
}
func (a *Action) canRegister(ctx context.Context) bool {
	p, _ := ctx.Value(producerKey{}).(*producer)
	if p != nil && p.action == a {
		return !p.released
	}
	return !a.sealed
}

func Register(ctx context.Context, sessionID string) *Path {
	a, _ := ctx.Value(actionKey{}).(*Action)
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.canRegister(ctx) || (a.sealed && a.record.SessionID != sessionID) {
		return nil
	}
	p := &Path{action: a, sessionID: sessionID}
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
	if !a.sealed || p.sessionID != a.record.SessionID {
		a.mu.Unlock()
		return
	}
	a.pending--
	ev := a.resolveLocked(r)
	a.mu.Unlock()
	if ev != nil && a.publish != nil {
		a.publish(*ev)
	}
}

// Commit publishes only actions accepted by the mutation.
func (a *Action) Commit(s *Session, sessionID string, publish func(api.ActionContinuation)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sealed = true
	if a.replay {
		return
	}
	a.publish = publish
	a.history = s.Actions
	a.record.SessionID = sessionID
	if !a.history.Open(a.ID(), a.record) {
		return
	}
	if publish != nil {
		publish(a.record)
	}
	var success Result
	a.pending = 0
	for _, p := range a.paths {
		if p.sessionID != sessionID {
			continue
		}
		if p.result.State == "" {
			a.pending++
		}
		if p.result.State == api.ContinuationStateContinued && success.State == "" {
			success = p.result
		}
	}
	if ev := a.resolveLocked(success); ev != nil && publish != nil {
		publish(*ev)
	}
}
func (a *Action) resolveLocked(r Result) *api.ActionContinuation {
	if a.record.State != api.ContinuationStatePending {
		return nil
	}
	if r.State != api.ContinuationStateContinued {
		if !a.sealed || a.producers > 0 || a.pending > 0 {
			return nil
		}
		r = Result{State: api.ContinuationStateFailed, FailureCode: string(api.HostContinuationFailureHostInvariantFailed)}
		best := 0
		for _, p := range a.paths {
			if p.sessionID != a.record.SessionID {
				continue
			}
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
	if !a.history.Change(a.ID(), func(api.ActionContinuation) (api.ActionContinuation, bool) { return value, true }) {
		return nil
	}
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
