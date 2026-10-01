package cost

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/pricing"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRetentionPreservesPartialPricingCacheAndProvenance(t *testing.T) {
	tracker, database := newTestTracker(t, fixedPricer{})
	testdbseed.InsertProject(t, database, testdbseed.DefaultProjectID)
	rate := pricing.Rate{Currency: "USD", InputPer1K: new(float64(1)), OutputPer1K: new(float64(2)), CacheReadPer1K: new(float64(0))}
	usage := TokenUsage{PromptTokens: 700, CompletionTokens: 300, CacheReadInputTokens: 100, CacheCreationInputTokens: 100}
	est := ApplyRate(rate, usage)
	estimated, cacheSavings, err := est.NanoUSD()
	testutil.FailErr(t, "convert estimate", err)
	asOf := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, evt := range []UsageEvent{
		{ID: "partial", PromptTokens: 700, CompletionTokens: 300, CacheReadInputTokens: 100, CacheCreationInputTokens: 100,
			EstimatedNanoUSD: estimated, Unpriced: est.Unpriced, UnpricedTokens: est.UnpricedTokens, RateSnapshot: est.RateSnapshot,
			CacheSavingsNanoUSD: cacheSavings, UnpricedCacheTokens: est.UnpricedCacheTokens,
			PricingSource: "fixture", PricedAsOf: asOf, UsageSource: UsageFromProviderPartial},
		{ID: "unknown-price", PromptTokens: 123, CompletionTokens: 45, Unpriced: true, UsageSource: UsageFromHost},
		{ID: "free", PromptTokens: 50, CacheReadInputTokens: 50, EstimatedNanoUSD: new(int64(0)), PricingSource: "fixture", PricedAsOf: asOf},
	} {
		evt.SessionID, evt.ProjectID, evt.Caller = "session", testdbseed.DefaultProjectID, CallerCoordinator
		testutil.FailErr(t, "record usage", tracker.RecordUsage(t.Context(), evt))
	}
	before, err := tracker.Summary(t.Context(), api.CostScopeProject, "", testdbseed.DefaultProjectID)
	testutil.FailErr(t, "summary before retention", err)
	if before.UnknownChargedCalls != 1 || before.UnknownCalls != 1 || before.UnpricedTokens != 268 || before.HostMeasuredTokens != 168 || before.TokenTotals.CacheRead != 150 {
		t.Fatalf("coverage before retention = %+v", before)
	}
	rolledUp, err := db.RollupLLMCallIDs(t.Context(), database, []string{"partial", "unknown-price", "free"})
	testutil.FailErr(t, "roll up selected receipts", err)
	if rolledUp != 3 {
		t.Fatalf("rolled up %d calls", rolledUp)
	}
	after, err := tracker.Summary(t.Context(), api.CostScopeProject, "", testdbseed.DefaultProjectID)
	testutil.FailErr(t, "summary after retention", err)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("retention changed cost coverage:\nbefore=%+v\nafter=%+v", before, after)
	}
	rows, err := database.QueryContext(t.Context(), `SELECT rate_snapshot FROM llm_call_rollups WHERE project_id = ?`, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "read retained pricing snapshots", err)
	defer func() { _ = rows.Close() }()
	found := false
	for rows.Next() {
		var raw string
		testutil.FailErr(t, "read rate snapshot", rows.Scan(&raw))
		var rate pricing.Rate
		testutil.FailErr(t, "decode rate snapshot", json.Unmarshal([]byte(raw), &rate))
		if reflect.DeepEqual(&rate, est.RateSnapshot) {
			found = true
		}
	}
	testutil.FailErr(t, "finish rate snapshots", rows.Err())
	if !found {
		t.Fatal("retention discarded the historical rate snapshot")
	}
}
