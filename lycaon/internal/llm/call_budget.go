package llm

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
)

// ErrCallBudgetExceeded is the cause a started budget cancels its call with.
var ErrCallBudgetExceeded = errors.New("provider call budget exceeded")

// CallBudget starts at provider dispatch, excludes screening time, and is shared across retries.
type CallBudget struct {
	limit   time.Duration
	mu      sync.Mutex
	started time.Time
	expired atomic.Bool
}

type callBudgetKey struct{}

// NewCallBudget returns the budget for one call. A limit of zero or less is
// unbounded: the call runs until its caller's context ends.
func NewCallBudget(limit time.Duration) *CallBudget {
	return &CallBudget{limit: limit}
}

// WithCallBudget attaches the budget the next provider call will start.
func WithCallBudget(ctx context.Context, budget *CallBudget) context.Context {
	if budget == nil {
		return ctx
	}
	return context.WithValue(ctx, callBudgetKey{}, budget)
}

func callBudgetFrom(ctx context.Context) *CallBudget {
	budget, _ := ctx.Value(callBudgetKey{}).(*CallBudget)
	return budget
}

// deadline starts the clock on the first send and holds that instant for every
// later attempt of the same call.
func (b *CallBudget) deadline() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.started.IsZero() {
		b.started = time.Now()
	}
	return b.started.Add(b.limit)
}

// startCallBudget converts an attached budget into a deadline on the outbound
// call. With no budget attached the context passes through unchanged.
func startCallBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	budget := callBudgetFrom(ctx)
	if budget == nil || budget.limit <= 0 {
		return ctx, func() {}
	}
	call, cancel := context.WithDeadlineCause(ctx, budget.deadline(), ErrCallBudgetExceeded)
	context.AfterFunc(call, func() {
		if errors.Is(context.Cause(call), ErrCallBudgetExceeded) {
			budget.expired.Store(true)
		}
	})
	return call, cancel
}

// Expired separates running out of budget from the caller's own cancellation.
func (b *CallBudget) Expired() bool {
	if b == nil {
		return false
	}
	if b.expired.Load() {
		return true
	}
	b.mu.Lock()
	started := b.started
	limit := b.limit
	b.mu.Unlock()
	return limit > 0 && !started.IsZero() && !time.Now().Before(started.Add(limit))
}

// budgetedProvider starts the call budget at the last host step before the
// request leaves, so every decorator above it runs on the caller's clock.
type budgetedProvider struct {
	inner modelcall.Provider
}

func (p *budgetedProvider) ID() string                       { return p.inner.ID() }
func (p *budgetedProvider) Models() []modelcall.ModelInfo    { return p.inner.Models() }
func (p *budgetedProvider) Profile() providerprofile.Profile { return p.inner.Profile() }

func (p *budgetedProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	req.CaptureSources()
	call, cancel := startCallBudget(ctx)
	defer cancel()
	return p.inner.Complete(call, req)
}

func (p *budgetedProvider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	req.CaptureSources()
	call, cancel := startCallBudget(ctx)
	ch, err := p.inner.Stream(call, req)
	if err != nil {
		cancel()
		return nil, err
	}
	// The stream outlives this call, so the deadline is released when the
	// caller's context ends rather than on return.
	context.AfterFunc(ctx, cancel)
	return ch, nil
}
