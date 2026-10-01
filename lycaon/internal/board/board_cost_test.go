package board

import (
	"context"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"testing"

	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/cost/costtest"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBoardCostFilledWhenPriced(t *testing.T) {
	tracker := costtest.NewTracker(t, cost.NoopPricer{})
	nano := int64(1_250_000_000)
	testutil.FailErr(t, "RecordUsage", tracker.RecordUsage(context.Background(), cost.UsageEvent{
		SessionID:        "sess-priced",
		Caller:           cost.CallerCoordinator,
		PromptTokens:     100,
		EstimatedNanoUSD: &nano,
		PricingSource:    "live",
	}))

	b := &SnapshotBuilder{Repo: repotest.NewProvider(t), Cost: tracker}
	snap, err := b.Build(context.Background(), testdbseed.DefaultProjectID, t.TempDir(), "sess-priced", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "Build", err)
	if snap.Cost == nil || snap.Cost.EstimateCoverage != api.CostEstimateComplete || snap.Cost.EstimatedNanoUsd != nano {
		t.Fatalf("cost = %+v", snap.Cost)
	}

	empty, err := b.Build(context.Background(), testdbseed.DefaultProjectID, t.TempDir(), "sess-other", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "Build empty", err)
	// A session with no usage events is complete at $0 (nothing to price yet).
	if empty.Cost == nil || empty.Cost.EstimateCoverage != api.CostEstimateComplete || empty.Cost.EstimatedNanoUsd != 0 {
		t.Fatalf("empty session must report complete $0, got %+v", empty.Cost)
	}
}

func TestBoardCostIsAbsentWhenTrackingIsDisabled(t *testing.T) {
	tracker := costtest.NewTracker(t, cost.NoopPricer{})
	nano := int64(1_250_000_000)
	testutil.FailErr(t, "RecordUsage", tracker.RecordUsage(context.Background(), cost.UsageEvent{
		SessionID: "sess-priced", Caller: cost.CallerCoordinator, PromptTokens: 100, EstimatedNanoUSD: &nano,
	}))
	b := &SnapshotBuilder{
		Repo: repotest.NewProvider(t), Cost: tracker,
		CostTrackingEnabled: func() bool { return false },
	}
	snap, err := b.Build(context.Background(), testdbseed.DefaultProjectID, t.TempDir(), "sess-priced", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "Build", err)
	if snap.Cost != nil {
		t.Fatalf("disabled cost tracking exposed cost slice: %+v", snap.Cost)
	}
}

func TestBoardCostPreservesIncompleteCoverage(t *testing.T) {
	ctx := t.Context()
	tracker := costtest.NewTracker(t, cost.NoopPricer{})
	testutil.FailErr(t, "record unpriced usage", tracker.RecordUsage(ctx, cost.UsageEvent{SessionID: "s", Caller: cost.CallerCoordinator, PromptTokens: 100, Unpriced: true}))
	b := &SnapshotBuilder{Cost: tracker}
	unpriced := b.costSlice(ctx, "s")
	if unpriced == nil || unpriced.EstimateCoverage != api.CostEstimateUnpriced || unpriced.UnpricedTokens != 100 {
		t.Fatalf("unpriced summary=%+v", unpriced)
	}
	nano := int64(1_250_000_000)
	testutil.FailErr(t, "record priced usage", tracker.RecordUsage(ctx, cost.UsageEvent{SessionID: "s", Caller: cost.CallerCoordinator, PromptTokens: 50, EstimatedNanoUSD: &nano}))
	testutil.FailErr(t, "begin charged call", tracker.BeginCall(ctx, cost.UsageEvent{ID: "unknown", SessionID: "s", ProviderID: "cloud", Caller: cost.CallerCoordinator}))
	testutil.FailErr(t, "mark missing usage", tracker.MarkCallUnknown(ctx, "unknown"))
	partial := b.costSlice(ctx, "s")
	if partial == nil || partial.EstimateCoverage != api.CostEstimateLowerBound || partial.UnpricedTokens != 100 || partial.UnknownChargedCalls != 1 || partial.EstimatedNanoUsd != nano {
		t.Fatalf("partial summary=%+v", partial)
	}
}
