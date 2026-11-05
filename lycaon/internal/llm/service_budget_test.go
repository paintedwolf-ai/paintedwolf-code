package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
)

type admittedBudgetProvider struct {
	cancelableCapacityProvider
	background context.Context
	measured   int
}

func (p *admittedBudgetProvider) RequestBudget(context.Context, modelcall.CompletionRequest) modelcall.RequestBudget {
	p.measured++
	if p.background.Err() == nil {
		return modelcall.RequestBudget{Err: errors.New("measurement reached occupied runtime")}
	}
	return modelcall.RequestBudget{Input: 17, Exact: true}
}

func TestRequestMeasurementPreemptsAliasedBackground(t *testing.T) {
	registry := &Registry{}
	plane := NewUtilityPlane()
	group := modelcall.NewCapacityGroup()
	provider := &admittedBudgetProvider{cancelableCapacityProvider: cancelableCapacityProvider{id: "coordinator", group: group}}
	alias := &cancelableCapacityProvider{id: "utility", group: group}
	background, release, err := plane.lanes.acquireBackground(t.Context(), modelcall.CapacityKey(alias), true)
	testutil.FailErr(t, "admit title", err)
	defer release()
	provider.background = background
	testutil.FailErr(t, "register measurement provider", registry.Register(provider))
	service := &Service{Registry: registry, Utility: plane}
	budget := service.RequestBudget(t.Context(), provider.ID(), modelcall.CompletionRequest{})
	testutil.FailErr(t, "measure foreground", budget.Err)
	if !budget.Exact || budget.Input != 17 || provider.measured != 1 {
		t.Fatalf("measurement = %+v, calls = %d", budget, provider.measured)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	budget = service.RequestBudget(ctx, provider.ID(), modelcall.CompletionRequest{})
	if !errors.Is(budget.Err, context.Canceled) || provider.measured != 1 {
		t.Fatalf("canceled measurement = %+v, calls = %d", budget, provider.measured)
	}
}

func TestRequestMeasurementWithoutRegistryUsesEstimate(t *testing.T) {
	var service *Service
	got := service.RequestBudget(t.Context(), "missing", modelcall.CompletionRequest{})
	if got.Err != nil || got.Exact {
		t.Fatalf("fallback measurement = %+v", got)
	}
}
