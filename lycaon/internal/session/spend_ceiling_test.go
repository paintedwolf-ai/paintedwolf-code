package session

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/cost/costtest"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubSpendPricer struct {
	usd      float64
	unpriced bool
	noCharge bool
}

func (s stubSpendPricer) NoCharge(_, _ string) bool { return s.noCharge }

func (s stubSpendPricer) EstimateCost(providerID, model string, usage cost.TokenUsage) (cost.CostEstimate, error) {
	if s.unpriced {
		return cost.CostEstimate{Currency: "USD", Unpriced: true}, nil
	}
	return cost.CostEstimate{EstimatedUSD: s.usd, Currency: "USD"}, nil
}

func TestCheckSpendCeiling(t *testing.T) {
	ctx := context.Background()

	t.Run("disabled", func(t *testing.T) {
		tracker := costtest.NewTracker(t, stubSpendPricer{usd: 0.01})
		usd := 9.0
		testutil.FailErr(t, "RecordUsage", tracker.RecordUsage(ctx, cost.UsageEvent{
			SessionID: "s1", Caller: cost.CallerCoordinator, PromptTokens: 100, EstimatedNanoUSD: costtest.NanoUSD(t, usd),
		}))
		lim := settings.DefaultSessionLimits()
		lim.SpendCeilingEnabled = false
		lim.SessionSpendCeilingUSD = 1
		mgr := NewManagerWithLLMService(store.NewMemory(), llm.NewMockProvider(&llm.MockConfig{}), nil, tools.NewStubRegistry(), lim, tracker)
		sess := &api.Session{ID: "s1", ProjectID: "p1"}
		if err := mgr.checkSpendCeiling(ctx, "s1", sess); err != nil {
			t.Fatalf("disabled ceiling must not enforce: %v", err)
		}
	})

	t.Run("enabled_unpriced", func(t *testing.T) {
		tracker := costtest.NewTracker(t, stubSpendPricer{unpriced: true})
		testutil.FailErr(t, "RecordUsage", tracker.RecordUsage(ctx, cost.UsageEvent{
			SessionID: "s2", Caller: cost.CallerCoordinator, PromptTokens: 100, Unpriced: true,
		}))
		lim := settings.DefaultSessionLimits()
		lim.SpendCeilingEnabled = true
		lim.SessionSpendCeilingUSD = 1
		mgr := NewManagerWithLLMService(store.NewMemory(), llm.NewMockProvider(&llm.MockConfig{}), nil, tools.NewStubRegistry(), lim, tracker)
		sess := &api.Session{ID: "s2", ProjectID: "p1"}
		if err := mgr.checkSpendCeiling(ctx, "s2", sess); err != nil {
			t.Fatalf("unpriced must not enforce: %v", err)
		}
	})

	t.Run("enabled_priced_under", func(t *testing.T) {
		tracker := costtest.NewTracker(t, stubSpendPricer{usd: 0.01})
		usd := 0.5
		testutil.FailErr(t, "RecordUsage", tracker.RecordUsage(ctx, cost.UsageEvent{
			SessionID: "s3", Caller: cost.CallerCoordinator, PromptTokens: 10, EstimatedNanoUSD: costtest.NanoUSD(t, usd),
		}))
		lim := settings.DefaultSessionLimits()
		lim.SpendCeilingEnabled = true
		lim.SessionSpendCeilingUSD = 5
		mgr := NewManagerWithLLMService(store.NewMemory(), llm.NewMockProvider(&llm.MockConfig{}), nil, tools.NewStubRegistry(), lim, tracker)
		sess := &api.Session{ID: "s3", ProjectID: "p1"}
		if err := mgr.checkSpendCeiling(ctx, "s3", sess); err != nil {
			t.Fatalf("under ceiling must pass: %v", err)
		}
	})

	t.Run("enabled_priced_reached", func(t *testing.T) {
		tracker := costtest.NewTracker(t, stubSpendPricer{usd: 0.01})
		usd := 5.5
		testutil.FailErr(t, "RecordUsage", tracker.RecordUsage(ctx, cost.UsageEvent{
			SessionID: "s4", Caller: cost.CallerCoordinator, PromptTokens: 10, EstimatedNanoUSD: costtest.NanoUSD(t, usd),
		}))
		testutil.FailErr(t, "RecordUsage unpriced", tracker.RecordUsage(ctx, cost.UsageEvent{
			SessionID: "worker-1", ParentSessionID: "s4",
			Caller: cost.CallerWorker, PromptTokens: 20, Unpriced: true,
		}))
		lim := settings.DefaultSessionLimits()
		lim.SpendCeilingEnabled = true
		lim.SessionSpendCeilingUSD = 5
		mgr := NewManagerWithLLMService(store.NewMemory(), llm.NewMockProvider(&llm.MockConfig{}), nil, tools.NewStubRegistry(), lim, tracker)
		sess := &api.Session{ID: "s4", ProjectID: "p1"}
		err := mgr.checkSpendCeiling(ctx, "s4", sess)
		if !errors.Is(err, ErrSessionSpendCeiling) {
			t.Fatalf("err = %v want ErrSessionSpendCeiling", err)
		}
		var reached *SessionSpendCeilingReached
		if !errors.As(err, &reached) || reached.CeilingUSD != 5 || reached.SpentUSD != 5.5 || reached.UnpricedTokens != 20 || reached.Coverage != api.CostEstimateLowerBound {
			t.Fatalf("reached = %+v", reached)
		}
	})
}

func TestSpendCeilingRetainsUnknownChargedCalls(t *testing.T) {
	ctx := t.Context()
	tracker := costtest.NewTracker(t, stubSpendPricer{})
	usd := 5.5
	testutil.FailErr(t, "record priced call", tracker.RecordUsage(ctx, cost.UsageEvent{SessionID: "s", Caller: cost.CallerCoordinator, PromptTokens: 100, EstimatedNanoUSD: costtest.NanoUSD(t, usd)}))
	testutil.FailErr(t, "begin charged call", tracker.BeginCall(ctx, cost.UsageEvent{ID: "missing", SessionID: "s", ProviderID: "cloud", Caller: cost.CallerCoordinator}))
	testutil.FailErr(t, "mark missing usage", tracker.MarkCallUnknown(ctx, "missing"))
	lim := settings.DefaultSessionLimits()
	lim.SpendCeilingEnabled = true
	lim.SessionSpendCeilingUSD = 5
	mgr := NewManagerWithLLMService(store.NewMemory(), llm.NewMockProvider(&llm.MockConfig{}), nil, tools.NewStubRegistry(), lim, tracker)
	err := mgr.checkSpendCeiling(ctx, "s", &api.Session{ID: "s", ProjectID: "p"})
	var reached *SessionSpendCeilingReached
	if !errors.As(err, &reached) || reached.UnknownChargedCalls != 1 || reached.UnpricedTokens != 0 || reached.Coverage != api.CostEstimateLowerBound {
		t.Fatalf("ceiling error=%+v", reached)
	}
}
