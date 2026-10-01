package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/testutil"
)

// deadlineProbeProvider records the deadline the request was actually sent with.
type deadlineProbeProvider struct {
	namedStubProvider
	remaining   time.Duration
	hadDeadline bool
}

func (p *deadlineProbeProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if deadline, ok := ctx.Deadline(); ok {
		p.hadDeadline = true
		p.remaining = time.Until(deadline)
	}
	return p.namedStubProvider.Complete(ctx, req)
}

// holdingScreen stands in for a credential card nobody has answered yet.
type holdingScreen struct {
	hold time.Duration
	err  error
}

func (s holdingScreen) Screen(ctx context.Context, _ ScreenDestination, req modelcall.CompletionRequest) (modelcall.CompletionRequest, error) {
	select {
	case <-time.After(s.hold):
	case <-ctx.Done():
		return modelcall.CompletionRequest{}, ctx.Err()
	}
	return req, s.err
}

// A hold longer than the whole budget still leaves the send its full window.
func TestCallBudgetStartsAfterScreening(t *testing.T) {
	const budget = 200 * time.Millisecond
	probe := &deadlineProbeProvider{namedStubProvider: namedStubProvider{id: "lite", content: "ok"}}
	reg := newEmptyRegistry()
	testutil.FailErr(t, "Register", reg.Register(probe))
	reg.SetOutboundSecretScreen(holdingScreen{hold: 2 * budget})

	p, err := reg.Get("lite")
	testutil.FailErr(t, "Get", err)
	budgeted := NewCallBudget(budget)
	start := time.Now()
	_, err = p.Complete(WithCallBudget(context.Background(), budgeted), modelcall.CompletionRequest{Model: "lite-model"})
	testutil.FailErr(t, "Complete", err)

	if held := time.Since(start); held < 2*budget {
		t.Fatalf("screen hold = %v, want the full %v hold", held, 2*budget)
	}
	if !probe.hadDeadline {
		t.Fatal("provider received no deadline: the budget never started")
	}
	if probe.remaining < budget/2 {
		t.Fatalf("remaining budget at send = %v, want close to %v", probe.remaining, budget)
	}
	if budgeted.Expired() {
		t.Fatal("budget reported expired for a call that completed inside it")
	}
}

// An unbudgeted caller is unbounded: the registry adds no deadline of its own.
func TestCallWithoutBudgetCarriesNoDeadline(t *testing.T) {
	probe := &deadlineProbeProvider{namedStubProvider: namedStubProvider{id: "lite", content: "ok"}}
	reg := newEmptyRegistry()
	testutil.FailErr(t, "Register", reg.Register(probe))

	p, err := reg.Get("lite")
	testutil.FailErr(t, "Get", err)
	_, err = p.Complete(context.Background(), modelcall.CompletionRequest{Model: "lite-model"})
	testutil.FailErr(t, "Complete", err)

	if probe.hadDeadline {
		t.Fatal("provider received a deadline with no budget attached")
	}
}

// A spent budget cancels the call and says so, separating running out of time
// from the caller giving up.
func TestCallBudgetExpiryIsReported(t *testing.T) {
	slow := &budgetHoldProvider{id: "lite"}
	reg := newEmptyRegistry()
	testutil.FailErr(t, "Register", reg.Register(slow))

	p, err := reg.Get("lite")
	testutil.FailErr(t, "Get", err)
	budgeted := NewCallBudget(50 * time.Millisecond)
	_, err = p.Complete(WithCallBudget(context.Background(), budgeted), modelcall.CompletionRequest{Model: "lite-model"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline", err)
	}
	if !budgeted.Expired() {
		t.Fatal("budget did not report the expiry it caused")
	}
	if !errors.Is(context.Cause(slow.callCtx), ErrCallBudgetExceeded) {
		t.Fatalf("cancel cause = %v, want the budget", context.Cause(slow.callCtx))
	}
}

// budgetHoldProvider waits for its call context to end.
type budgetHoldProvider struct {
	id      string
	callCtx context.Context //nolint:containedctx // the test asserts on the cancel cause
}

func (p *budgetHoldProvider) ID() string { return p.id }
func (p *budgetHoldProvider) Models() []modelcall.ModelInfo {
	return []modelcall.ModelInfo{{ID: p.id + "-model"}}
}
func (p *budgetHoldProvider) Profile() providerprofile.Profile { return providerprofile.Profile{} }

func (p *budgetHoldProvider) Complete(ctx context.Context, _ modelcall.CompletionRequest) (*modelcall.Completion, error) {
	p.callCtx = ctx
	<-ctx.Done()
	return nil, ctx.Err()
}

func (p *budgetHoldProvider) Stream(ctx context.Context, _ modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	p.callCtx = ctx
	<-ctx.Done()
	return nil, ctx.Err()
}
