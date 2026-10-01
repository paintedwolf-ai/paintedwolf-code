package cost

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// usd is the recorded estimate for a constant dollar amount.
func usd(v float64) *int64 {
	nano, err := USDToNano(v)
	if err != nil {
		panic(err)
	}
	return &nano
}

func TestCostSummarySplitsCoordinatorAndWorkers(t *testing.T) {
	tracker, _ := newTestTracker(t, nil)
	ctx := context.Background()
	sessionID := "parent-sess"
	childID := "child-sess"

	for _, tc := range []struct {
		sessionID string
		parentID  string
		caller    string
		usd       float64
	}{
		{sessionID, "", CallerCoordinator, 0.40},
		{sessionID, "", CallerCoordinator, 0.00},
		{childID, sessionID, CallerWorker, 0.18},
		{childID, sessionID, CallerWorker, 0.02},
	} {
		v := tc.usd
		if err := tracker.RecordUsage(ctx, UsageEvent{
			SessionID:        tc.sessionID,
			ParentSessionID:  tc.parentID,
			Caller:           tc.caller,
			PromptTokens:     100,
			EstimatedNanoUSD: usd(v),
		}); err != nil {
			t.Fatal(err)
		}
	}

	summary, err := tracker.Summary(ctx, api.CostScopeSession, sessionID, "")
	testutil.FailErr(t, "tracker.Summary failed", err)
	if summary.Coordinator.EstimatedNanoUsd != 400_000_000 {
		t.Fatalf("coordinator nano = %v want 400_000_000", summary.Coordinator.EstimatedNanoUsd)
	}
	if summary.Workers.EstimatedNanoUsd != 200_000_000 {
		t.Fatalf("workers nano = %v want 200_000_000", summary.Workers.EstimatedNanoUsd)
	}
	if summary.EstimatedNanoUsd != 600_000_000 {
		t.Fatalf("total nano = %v want 600_000_000", summary.EstimatedNanoUsd)
	}
	if summary.Workers.TaskCount != 1 {
		t.Fatalf("task_count = %d want 1", summary.Workers.TaskCount)
	}
}

func TestCostSummaryMixedPricingReportsUnpricedTokens(t *testing.T) {
	tracker, _ := newTestTracker(t, nil)
	ctx := context.Background()
	sessionID := "mixed-sess"

	testutil.FailErr(t, "record priced", tracker.RecordUsage(ctx, UsageEvent{
		SessionID: sessionID, Caller: CallerCoordinator,
		PromptTokens: 100, CompletionTokens: 50, EstimatedNanoUSD: usd(0.25),
	}))
	testutil.FailErr(t, "record unpriced", tracker.RecordUsage(ctx, UsageEvent{
		SessionID: sessionID, Caller: CallerCoordinator,
		PromptTokens: 700, CompletionTokens: 300, Unpriced: true,
	}))

	summary, err := tracker.Summary(ctx, api.CostScopeSession, sessionID, "")
	testutil.FailErr(t, "tracker.Summary failed", err)
	if summary.EstimateCoverage != api.CostEstimateLowerBound {
		t.Fatalf("mixed scope must be a lower bound: %+v", summary)
	}
	if summary.UnpricedTokens != 1000 {
		t.Fatalf("unpriced_tokens = %d want 1000", summary.UnpricedTokens)
	}
	if summary.EstimatedNanoUsd != 250_000_000 {
		t.Fatalf("total nano = %v want 250_000_000", summary.EstimatedNanoUsd)
	}
}

func TestCostSummaryLocalZeroDollarSessionIsPriced(t *testing.T) {
	tracker, _ := newTestTracker(t, nil)
	ctx := context.Background()
	testutil.FailErr(t, "record local", tracker.RecordUsage(ctx, UsageEvent{
		SessionID: "local-sess", Caller: CallerCoordinator,
		PromptTokens: 5000, CompletionTokens: 2000,
		EstimatedNanoUSD: usd(0), PricingSource: "local",
	}))
	summary, err := tracker.Summary(ctx, api.CostScopeSession, "local-sess", "")
	testutil.FailErr(t, "tracker.Summary failed", err)
	if summary.EstimateCoverage != api.CostEstimateComplete || summary.EstimatedNanoUsd != 0 || summary.UnpricedTokens != 0 {
		t.Fatalf("local usage must be complete at $0, not unpriced: %+v", summary)
	}
	if len(summary.PricingProvenance) != 1 || summary.PricingProvenance[0].Source != "local" {
		t.Fatalf("pricing provenance = %+v want local", summary.PricingProvenance)
	}
}

func TestCostSummaryFullyPricedHasZeroUnpricedTokens(t *testing.T) {
	tracker, _ := newTestTracker(t, nil)
	ctx := context.Background()
	testutil.FailErr(t, "record priced", tracker.RecordUsage(ctx, UsageEvent{
		SessionID: "s1", Caller: CallerCoordinator,
		PromptTokens: 100, CompletionTokens: 50, EstimatedNanoUSD: usd(0.25),
	}))
	summary, err := tracker.Summary(ctx, api.CostScopeSession, "s1", "")
	testutil.FailErr(t, "tracker.Summary failed", err)
	if summary.UnpricedTokens != 0 {
		t.Fatalf("unpriced_tokens = %d want 0", summary.UnpricedTokens)
	}
}

func TestCostSummaryTotalsInvariant(t *testing.T) {
	tracker, _ := newTestTracker(t, nil)
	ctx := context.Background()
	parent := "sess-parent"
	child := "sess-child"
	if err := tracker.RecordUsage(ctx, UsageEvent{SessionID: parent, Caller: CallerCoordinator, EstimatedNanoUSD: usd(0.11)}); err != nil {
		testutil.FailErr(t, "tracker.RecordUsage failed", err)
	}
	if err := tracker.RecordUsage(ctx, UsageEvent{SessionID: child, ParentSessionID: parent, Caller: CallerWorker, EstimatedNanoUSD: usd(0.09)}); err != nil {
		testutil.FailErr(t, "tracker.RecordUsage failed", err)
	}
	if err := tracker.RecordUsage(ctx, UsageEvent{SessionID: parent, Caller: CallerSummarizer, EstimatedNanoUSD: usd(0.02)}); err != nil {
		testutil.FailErr(t, "tracker.RecordUsage summarizer", err)
	}
	summary, err := tracker.Summary(ctx, api.CostScopeSession, parent, "")
	testutil.FailErr(t, "tracker.Summary failed", err)
	sum := summary.Coordinator.EstimatedNanoUsd + summary.Workers.EstimatedNanoUsd + summary.Summarizer.EstimatedNanoUsd
	if summary.EstimatedNanoUsd != sum {
		t.Fatalf("total %v != coordinator+workers+summarizer %v", summary.EstimatedNanoUsd, sum)
	}
	if summary.Summarizer.EstimatedNanoUsd != 20_000_000 {
		t.Fatalf("summarizer = %v want 20_000_000", summary.Summarizer.EstimatedNanoUsd)
	}
}

func TestCostSummarySplitsSummarizer(t *testing.T) {
	tracker, _ := newTestTracker(t, nil)
	ctx := context.Background()
	sessionID := "parent-sess"
	if err := tracker.RecordUsage(ctx, UsageEvent{
		SessionID: sessionID, Caller: CallerCoordinator, PromptTokens: 10, EstimatedNanoUSD: usd(0.40),
	}); err != nil {
		t.Fatal(err)
	}
	if err := tracker.RecordUsage(ctx, UsageEvent{
		SessionID: sessionID, Caller: CallerSummarizer, PromptTokens: 50, CompletionTokens: 5, EstimatedNanoUSD: usd(0.01),
	}); err != nil {
		t.Fatal(err)
	}
	if err := tracker.RecordUsage(ctx, UsageEvent{
		SessionID: sessionID, Caller: CallerSummarizer, PromptTokens: 20, EstimatedNanoUSD: usd(0.01),
	}); err != nil {
		t.Fatal(err)
	}
	summary, err := tracker.Summary(ctx, api.CostScopeSession, sessionID, "")
	testutil.FailErr(t, "tracker.Summary failed", err)
	if summary.Summarizer.EstimatedNanoUsd != 20_000_000 {
		t.Fatalf("summarizer nano = %v want 20_000_000", summary.Summarizer.EstimatedNanoUsd)
	}
	if summary.Summarizer.TokenTotals.Prompt != 70 {
		t.Fatalf("summarizer prompt = %d want 70", summary.Summarizer.TokenTotals.Prompt)
	}
	if summary.Coordinator.EstimatedNanoUsd != 400_000_000 {
		t.Fatalf("coordinator should stay separate: %v", summary.Coordinator.EstimatedNanoUsd)
	}
}

func TestCostSummaryEmptyWorkers(t *testing.T) {
	tracker, _ := newTestTracker(t, nil)
	v := 0.01
	if err := tracker.RecordUsage(context.Background(), UsageEvent{
		SessionID:        "sess-only",
		Caller:           CallerCoordinator,
		EstimatedNanoUSD: usd(v),
	}); err != nil {
		t.Fatal(err)
	}
	summary, err := tracker.Summary(context.Background(), api.CostScopeSession, "sess-only", "")
	testutil.FailErr(t, "tracker.Summary failed", err)
	if summary.Workers.EstimatedNanoUsd != 0 || summary.Workers.TokenTotals.Prompt != 0 {
		t.Fatalf("workers = %+v want zero values", summary.Workers)
	}
}

func TestCostSummaryProjectScope(t *testing.T) {
	tracker, _ := newTestTracker(t, nil)
	ctx := context.Background()
	projectID := "project-a"
	if err := tracker.RecordUsage(ctx, UsageEvent{
		SessionID: "s1", ProjectID: projectID, Caller: CallerCoordinator, EstimatedNanoUSD: usd(0.10),
	}); err != nil {
		t.Fatal(err)
	}
	if err := tracker.RecordUsage(ctx, UsageEvent{
		SessionID: "s2", ProjectID: projectID, ParentSessionID: "s1", Caller: CallerWorker, EstimatedNanoUSD: usd(0.05),
	}); err != nil {
		t.Fatal(err)
	}
	if err := tracker.RecordUsage(ctx, UsageEvent{
		SessionID: "other", ProjectID: "project-b", Caller: CallerCoordinator, EstimatedNanoUSD: usd(99),
	}); err != nil {
		t.Fatal(err)
	}

	summary, err := tracker.Summary(ctx, api.CostScopeProject, "", projectID)
	testutil.FailErr(t, "tracker.Summary failed", err)
	if summary.Coordinator.EstimatedNanoUsd != 100_000_000 {
		t.Fatalf("coordinator = %v", summary.Coordinator.EstimatedNanoUsd)
	}
	if summary.Workers.EstimatedNanoUsd != 50_000_000 {
		t.Fatalf("workers = %v", summary.Workers.EstimatedNanoUsd)
	}
}

func TestProjectUtilitiesExcludesSessionUsage(t *testing.T) {
	tracker, _ := newTestTracker(t, nil)
	ctx := context.Background()
	projectID := "project-a"
	for _, evt := range []UsageEvent{
		{ProjectID: projectID, Caller: CallerSummarizer, PromptTokens: 30, CompletionTokens: 5},
		{SessionID: "session-a", ProjectID: projectID, Caller: CallerSummarizer, PromptTokens: 100, CompletionTokens: 20},
		{ProjectID: "project-b", Caller: CallerSummarizer, PromptTokens: 900, CompletionTokens: 90},
	} {
		testutil.FailErr(t, "RecordUsage", tracker.RecordUsage(ctx, evt))
	}

	rollups, err := tracker.ProjectReport(ctx, projectID, ReportQuery{})
	testutil.FailErr(t, "ProjectReport", err)
	summary := rollups.ProjectUtilities
	if summary.Summarizer.TokenTotals.Prompt != 30 || summary.Summarizer.TokenTotals.Completion != 5 {
		t.Fatalf("project utility tokens = %+v", summary.Summarizer.TokenTotals)
	}
}

func TestCostSummaryPreservesPricingProvenancePerSource(t *testing.T) {
	liveAsOf := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	feedAsOf := time.Date(2025, 6, 7, 8, 9, 10, 0, time.UTC)
	summary := buildCostSummary([]UsageEvent{
		{SessionID: "session-a", Caller: CallerCoordinator, EstimatedNanoUSD: usd(0.1), PricingSource: "live", PricedAsOf: liveAsOf},
		{SessionID: "session-a", Caller: CallerCoordinator, EstimatedNanoUSD: usd(0.2), PricingSource: "models-dev", PricedAsOf: feedAsOf},
	}, UnknownCalls{})

	if len(summary.PricingProvenance) != 2 {
		t.Fatalf("pricing provenance = %+v want two sources", summary.PricingProvenance)
	}
	if got := summary.PricingProvenance[0]; got.Source != "live" || got.PricedAt == nil || !got.PricedAt.Equal(liveAsOf) {
		t.Fatalf("first pricing provenance = %+v", got)
	}
	if got := summary.PricingProvenance[1]; got.Source != "models-dev" || got.PricedAt == nil || !got.PricedAt.Equal(feedAsOf) {
		t.Fatalf("second pricing provenance = %+v", got)
	}
}

func TestProjectReportPartitionsCurrentUtilitiesAndRetiredSessions(t *testing.T) {
	tracker, sqlDB := newTestTracker(t, nil)
	ctx := context.Background()
	projectID := "project-a"
	testdbseed.InsertSession(t, sqlDB, "current", projectID)
	for _, evt := range []UsageEvent{
		{SessionID: "current", ProjectID: projectID, Caller: CallerCoordinator, EstimatedNanoUSD: usd(0.4)},
		{SessionID: "worker", ParentSessionID: "current", ProjectID: projectID, Caller: CallerWorker, EstimatedNanoUSD: usd(0.1)},
		{ProjectID: projectID, Caller: CallerSummarizer, EstimatedNanoUSD: usd(0.05)},
		{SessionID: "deleted", ProjectID: projectID, Caller: CallerCoordinator, EstimatedNanoUSD: usd(0.2)},
		{SessionID: "deleted-worker", ParentSessionID: "deleted", ProjectID: projectID, Caller: CallerWorker, EstimatedNanoUSD: usd(0.03)},
	} {
		testutil.FailErr(t, "RecordUsage", tracker.RecordUsage(ctx, evt))
	}

	report, err := tracker.ProjectReport(ctx, projectID, ReportQuery{})
	testutil.FailErr(t, "ProjectReport", err)
	if report.Summary.EstimatedNanoUsd != 780_000_000 {
		t.Fatalf("project total = %v want 780_000_000", report.Summary.EstimatedNanoUsd)
	}
	if report.Sessions[0].Cost.EstimatedNanoUsd != 500_000_000 {
		t.Fatalf("current session = %+v want 500_000_000", report.Sessions[0].Cost)
	}
	if report.ProjectUtilities.EstimatedNanoUsd != 50_000_000 {
		t.Fatalf("project utilities = %+v want 50_000_000", report.ProjectUtilities)
	}
	if report.RetiredSessions.EstimatedNanoUsd != 230_000_000 {
		t.Fatalf("retired sessions = %+v want 230_000_000", report.RetiredSessions)
	}
	parts := report.Sessions[0].Cost.EstimatedNanoUsd + report.ProjectUtilities.EstimatedNanoUsd + report.RetiredSessions.EstimatedNanoUsd
	if report.Summary.EstimatedNanoUsd != parts {
		t.Fatalf("project total %v does not reconcile with lanes %v", report.Summary.EstimatedNanoUsd, parts)
	}
}
