package cost

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// newTestTracker returns a tracker and its database.
func newTestTracker(t *testing.T, pricer Pricer) (*SQLTracker, db.Handle) {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")
	return NewSQLTracker(sqlDB, pricer), sqlDB
}

type fixedPricer struct{}

func (fixedPricer) EstimateCost(_, _ string, usage TokenUsage) (CostEstimate, error) {
	usd := (float64(usage.PromptTokens)/1000)*0.005 + (float64(usage.CompletionTokens)/1000)*0.015
	return CostEstimate{EstimatedUSD: usd, Currency: "USD"}, nil
}

func (fixedPricer) NoCharge(_, _ string) bool { return false }

// localPricer prices usage at zero and rules out charges.
type localPricer struct{}

func (localPricer) EstimateCost(_, _ string, _ TokenUsage) (CostEstimate, error) {
	return CostEstimate{Currency: "USD", PricingSource: "local"}, nil
}

func (localPricer) NoCharge(_, _ string) bool { return true }

func TestSQLTrackerPersistsUnknownAndReportedReceipts(t *testing.T) {
	tracker, sqlDB := newTestTracker(t, nil)
	event := UsageEvent{ID: "call-1", SessionID: "session-1", ProjectID: "project-1", ProviderID: "mock", Model: "m", Caller: CallerCoordinator}
	testutil.FailErr(t, "begin call", tracker.BeginCall(t.Context(), event))
	testutil.FailErr(t, "mark unknown", tracker.MarkCallUnknown(t.Context(), event.ID))
	var status string
	testutil.FailErr(t, "read unknown", sqlDB.QueryRowContext(t.Context(), `SELECT status FROM llm_calls WHERE id = ?`, event.ID).Scan(&status))
	if status != "unknown" {
		t.Fatalf("status = %q", status)
	}
	summary, err := tracker.Summary(t.Context(), api.CostScopeSession, event.SessionID, "")
	testutil.FailErr(t, "unknown summary", err)
	if summary.UnknownCalls != 1 {
		t.Fatalf("unknown calls = %d", summary.UnknownCalls)
	}
	event.PromptTokens = 11
	event.CompletionTokens = 7
	testutil.FailErr(t, "report late usage", tracker.RecordUsage(t.Context(), event))

	reopened := NewSQLTracker(sqlDB, nil)
	summary, err = reopened.Summary(t.Context(), api.CostScopeSession, event.SessionID, "")
	testutil.FailErr(t, "summary", err)
	if summary.TokenTotals.Prompt != 11 || summary.TokenTotals.Completion != 7 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestSQLTrackerRecoversInterruptedStartedReceipt(t *testing.T) {
	tracker, sqlDB := newTestTracker(t, nil)
	event := UsageEvent{ID: "call-interrupted", SessionID: "session-1", Caller: CallerCoordinator}
	testutil.FailErr(t, "begin call", tracker.BeginCall(t.Context(), event))
	testutil.FailErr(t, "recover calls", tracker.RecoverStartedCalls(t.Context()))
	var status string
	testutil.FailErr(t, "read recovered call", sqlDB.QueryRowContext(t.Context(), `SELECT status FROM llm_calls WHERE id = ?`, event.ID).Scan(&status))
	if status != "unknown" {
		t.Fatalf("status = %q", status)
	}
}

func TestSQLTrackerVoidCallDiscardsUnbilledReceipt(t *testing.T) {
	tracker, sqlDB := newTestTracker(t, nil)
	event := UsageEvent{ID: "call-void", SessionID: "session-1", ProjectID: "project-1", Caller: CallerSummarizer}
	testutil.FailErr(t, "begin call", tracker.BeginCall(t.Context(), event))
	testutil.FailErr(t, "void call", tracker.VoidCall(t.Context(), event.ID))
	var count int
	testutil.FailErr(t, "count rows", sqlDB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM llm_calls WHERE id = ?`, event.ID).Scan(&count))
	if count != 0 {
		t.Fatalf("voided receipt still stored: %d rows", count)
	}
	summary, err := tracker.Summary(t.Context(), api.CostScopeSession, event.SessionID, "")
	testutil.FailErr(t, "summary", err)
	if summary.UnknownCalls != 0 {
		t.Fatalf("voided call counted unknown: %d", summary.UnknownCalls)
	}
	project, err := tracker.Summary(t.Context(), api.CostScopeProject, "", event.ProjectID)
	testutil.FailErr(t, "project summary", err)
	if project.UnknownCalls != 0 {
		t.Fatalf("voided call counted unknown in project scope: %d", project.UnknownCalls)
	}
}

func TestSQLTrackerVoidCallNeverTouchesBilledState(t *testing.T) {
	tracker, sqlDB := newTestTracker(t, nil)
	reported := UsageEvent{ID: "call-reported", SessionID: "s1", Caller: CallerCoordinator, PromptTokens: 3}
	testutil.FailErr(t, "begin reported", tracker.BeginCall(t.Context(), reported))
	testutil.FailErr(t, "record reported", tracker.RecordUsage(t.Context(), reported))
	testutil.FailErr(t, "void reported", tracker.VoidCall(t.Context(), reported.ID))

	unknown := UsageEvent{ID: "call-unknown", SessionID: "s1", Caller: CallerCoordinator}
	testutil.FailErr(t, "begin unknown", tracker.BeginCall(t.Context(), unknown))
	testutil.FailErr(t, "mark unknown", tracker.MarkCallUnknown(t.Context(), unknown.ID))
	testutil.FailErr(t, "void unknown", tracker.VoidCall(t.Context(), unknown.ID))

	var count int
	testutil.FailErr(t, "count rows", sqlDB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM llm_calls WHERE id IN ('call-reported', 'call-unknown')`).Scan(&count))
	if count != 2 {
		t.Fatalf("void deleted billed state: %d rows want 2", count)
	}
}

func TestSQLTrackerSplitsUnknownCallsByWhetherTheyCouldCharge(t *testing.T) {
	local, _ := newTestTracker(t, localPricer{})
	localCall := UsageEvent{ID: "call-local", SessionID: "s1", ProviderID: "ollama-1", Model: "gemma", Caller: CallerSummarizer}
	testutil.FailErr(t, "begin local", local.BeginCall(t.Context(), localCall))
	testutil.FailErr(t, "abandon local", local.MarkCallUnknown(t.Context(), localCall.ID))

	summary, err := local.Summary(t.Context(), api.CostScopeSession, "s1", "")
	testutil.FailErr(t, "local summary", err)
	if summary.UnknownCalls != 1 {
		t.Fatalf("unknown calls = %d want 1 (the call is still unreported)", summary.UnknownCalls)
	}
	if summary.UnknownChargedCalls != 0 {
		t.Fatalf("unknown charged calls = %d want 0 (local inference cannot bill)", summary.UnknownChargedCalls)
	}

	hosted, _ := newTestTracker(t, fixedPricer{})
	hostedCall := UsageEvent{ID: "call-hosted", SessionID: "s1", ProviderID: "fireworks-1", Model: "m", Caller: CallerCoordinator}
	testutil.FailErr(t, "begin hosted", hosted.BeginCall(t.Context(), hostedCall))
	testutil.FailErr(t, "abandon hosted", hosted.MarkCallUnknown(t.Context(), hostedCall.ID))

	summary, err = hosted.Summary(t.Context(), api.CostScopeSession, "s1", "")
	testutil.FailErr(t, "hosted summary", err)
	if summary.UnknownCalls != 1 || summary.UnknownChargedCalls != 1 {
		t.Fatalf("unknown = %d charged = %d want 1/1", summary.UnknownCalls, summary.UnknownChargedCalls)
	}
}

// Chargeability is fixed when the receipt opens.
func TestSQLTrackerStampsNoChargeWhenTheReceiptOpens(t *testing.T) {
	tracker, sqlDB := newTestTracker(t, localPricer{})
	event := UsageEvent{ID: "call-1", SessionID: "s1", ProviderID: "ollama-1", Model: "gemma", Caller: CallerSummarizer}
	testutil.FailErr(t, "begin call", tracker.BeginCall(t.Context(), event))
	tracker.SetPricer(fixedPricer{})
	testutil.FailErr(t, "abandon call", tracker.MarkCallUnknown(t.Context(), event.ID))

	var noCharge int
	testutil.FailErr(t, "read no_charge", sqlDB.QueryRowContext(t.Context(),
		`SELECT no_charge FROM llm_calls WHERE id = ?`, event.ID).Scan(&noCharge))
	if noCharge != 1 {
		t.Fatal("no_charge was not stamped from the pricer that was live when the call ran")
	}
}

func TestSQLTrackerKeepsChargeabilityWhenAStartedReceiptSettles(t *testing.T) {
	tracker, sqlDB := newTestTracker(t, localPricer{})
	event := UsageEvent{ID: "call-1", SessionID: "s1", ProviderID: "ollama-1", Model: "gemma", Caller: CallerSummarizer}
	testutil.FailErr(t, "begin call", tracker.BeginCall(t.Context(), event))
	tracker.SetPricer(fixedPricer{})
	event.PromptTokens = 12
	testutil.FailErr(t, "record usage", tracker.RecordUsage(t.Context(), event))

	var noCharge int
	testutil.FailErr(t, "read no_charge", sqlDB.QueryRowContext(t.Context(),
		`SELECT no_charge FROM llm_calls WHERE id = ?`, event.ID).Scan(&noCharge))
	if noCharge != 1 {
		t.Fatal("settling a receipt changed its chargeability")
	}
}

func TestSQLTrackerSpendWarningLatchRearmsOnlyForANewCeiling(t *testing.T) {
	tracker, _ := newTestTracker(t, fixedPricer{})
	fired, err := tracker.ClaimSpendWarning(t.Context(), "session-1", 5)
	testutil.FailErr(t, "claim initial warning", err)
	if !fired {
		t.Fatal("initial warning was not claimed")
	}
	fired, err = tracker.ClaimSpendWarning(t.Context(), "session-1", 5)
	testutil.FailErr(t, "claim same warning", err)
	if fired {
		t.Fatal("same ceiling re-fired")
	}
	fired, err = tracker.ClaimSpendWarning(t.Context(), "session-1", 10)
	testutil.FailErr(t, "claim raised warning", err)
	if !fired {
		t.Fatal("raised ceiling did not re-arm warning")
	}
	testutil.FailErr(t, "clear warning", tracker.ClearSpendWarning(t.Context(), "session-1"))
	fired, err = tracker.ClaimSpendWarning(t.Context(), "session-1", 10)
	testutil.FailErr(t, "claim after clear", err)
	if !fired {
		t.Fatal("cleared warning did not re-arm")
	}
}

// Host-measured usage contributes to token totals.
func TestSQLTrackerReportsHostMeasuredTokens(t *testing.T) {
	tracker, _ := newTestTracker(t, fixedPricer{})
	testutil.FailErr(t, "record provider usage", tracker.RecordUsage(t.Context(), UsageEvent{
		ID: "call-provider", SessionID: "s1", ProviderID: "p", Model: "m", Caller: CallerCoordinator,
		PromptTokens: 100, CompletionTokens: 10,
	}))
	measuredNano := int64(250_000_000)
	testutil.FailErr(t, "record measured usage", tracker.RecordUsage(t.Context(), UsageEvent{
		ID: "call-measured", SessionID: "s1", ProviderID: "p", Model: "m", Caller: CallerCoordinator,
		PromptTokens: 40, CompletionTokens: 6, UsageSource: UsageFromHost, EstimatedNanoUSD: &measuredNano,
	}))

	summary, err := tracker.Summary(t.Context(), api.CostScopeSession, "s1", "")
	testutil.FailErr(t, "summary", err)
	if summary.TokenTotals.Prompt != 140 || summary.TokenTotals.Completion != 16 {
		t.Fatalf("token totals = %+v want measured tokens included", summary.TokenTotals)
	}
	if summary.HostMeasuredTokens != 46 {
		t.Fatalf("host measured tokens = %d want 46", summary.HostMeasuredTokens)
	}
	if summary.EstimatedNanoUsd < measuredNano {
		t.Fatalf("estimated nano = %v want the measured call's %v included", summary.EstimatedNanoUsd, measuredNano)
	}
}

// Receipt writes survive caller cancellation.
func TestSQLTrackerWritesSurviveCanceledCallers(t *testing.T) {
	tracker, sqlDB := newTestTracker(t, fixedPricer{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	event := UsageEvent{ID: "call-1", SessionID: "s1", ProviderID: "p", Model: "m", Caller: CallerCoordinator}
	testutil.FailErr(t, "begin call", tracker.BeginCall(ctx, event))
	event.PromptTokens = 5
	testutil.FailErr(t, "record usage", tracker.RecordUsage(ctx, event))

	var status string
	testutil.FailErr(t, "read status", sqlDB.QueryRowContext(t.Context(),
		`SELECT status FROM llm_calls WHERE id = ?`, event.ID).Scan(&status))
	if status != "reported" {
		t.Fatalf("status = %q want reported (a canceled caller must not lose the receipt)", status)
	}
}

func TestSQLTrackerSessionRollup(t *testing.T) {
	tracker, _ := newTestTracker(t, fixedPricer{})
	for i := 0; i < 5; i++ {
		nano := int64(1_000_000 * (i + 1))
		testutil.FailErr(t, "record usage", tracker.RecordUsage(t.Context(), UsageEvent{
			SessionID:        "session-1",
			ProviderID:       "openai",
			Model:            "gpt-4o",
			PromptTokens:     100,
			CompletionTokens: 50,
			EstimatedNanoUSD: &nano,
		}))
	}

	summary, err := tracker.Summary(t.Context(), api.CostScopeSession, "session-1", "")
	testutil.FailErr(t, "tracker.Summary failed", err)
	if got := summary.TokenTotals.Prompt + summary.TokenTotals.Completion; got != 750 {
		t.Fatalf("total tokens = %d, want 750", got)
	}
	if summary.EstimatedNanoUsd < 14_000_000 || summary.EstimatedNanoUsd > 16_000_000 {
		t.Fatalf("total nano = %v, want ~15_000_000", summary.EstimatedNanoUsd)
	}
	if summary.Coordinator.EstimatedNanoUsd != summary.EstimatedNanoUsd {
		t.Fatalf("coordinator nano = %v total = %v", summary.Coordinator.EstimatedNanoUsd, summary.EstimatedNanoUsd)
	}
	if summary.Workers.EstimatedNanoUsd != 0 {
		t.Fatalf("workers nano = %v want 0", summary.Workers.EstimatedNanoUsd)
	}
}

func TestSQLTrackerRetainsGlobalUtilityUsage(t *testing.T) {
	tracker, sqlDB := newTestTracker(t, nil)
	testutil.FailErr(t, "RecordUsage", tracker.RecordUsage(t.Context(), UsageEvent{
		Caller: CallerSummarizer, PromptTokens: 12, CompletionTokens: 3,
	}))
	var count int
	testutil.FailErr(t, "count global rows", sqlDB.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM llm_calls WHERE session_id = '' AND project_id = '' AND status = 'reported'`).Scan(&count))
	if count != 1 {
		t.Fatalf("global events = %d, want 1", count)
	}
}

// seedRollupRow inserts a priced retention aggregate.
func seedRollupRow(t *testing.T, sqlDB db.Handle, projectID, sessionID, parentSessionID, caller string, promptTokens, completionTokens, estimatedNanoUSD int64) {
	t.Helper()
	_, err := sqlDB.ExecContext(t.Context(), `
INSERT INTO llm_call_rollups (
	project_id, session_id, parent_session_id, provider_id, model, caller, day,
	call_count, prompt_tokens, completion_tokens, estimated_nano_usd, priced_count
) VALUES (?, ?, ?, 'openai', 'gpt-4o', ?, '2026-01-01', 3, ?, ?, ?, 3)`,
		projectID, sessionID, parentSessionID, caller, promptTokens, completionTokens, estimatedNanoUSD)
	testutil.FailErr(t, "seed llm_call_rollups", err)
}

func TestSQLTrackerSessionSummaryIncludesRolledUpSpend(t *testing.T) {
	tracker, sqlDB := newTestTracker(t, fixedPricer{})
	nano := int64(20_000_000)
	testutil.FailErr(t, "RecordUsage", tracker.RecordUsage(t.Context(), UsageEvent{
		SessionID: "session-1", ProviderID: "openai", Model: "gpt-4o",
		PromptTokens: 100, CompletionTokens: 50, EstimatedNanoUSD: &nano,
	}))
	// A worker call attributed to session-1 as its parent, folded by retention.go.
	seedRollupRow(t, sqlDB, "", "worker-child", "session-1", CallerWorker, 200, 80, 3_000_000)

	summary, err := tracker.Summary(t.Context(), api.CostScopeSession, "session-1", "")
	testutil.FailErr(t, "tracker.Summary", err)
	if got := summary.TokenTotals.Prompt + summary.TokenTotals.Completion; got != 430 {
		t.Fatalf("total tokens = %d, want 430 (150 live + 280 rolled up)", got)
	}
	if summary.Workers.TokenTotals.Prompt != 200 || summary.Workers.TokenTotals.Completion != 80 {
		t.Fatalf("workers breakdown = %+v, want rolled-up worker tokens attributed to workers", summary.Workers)
	}
	if summary.Workers.TaskCount != 1 {
		t.Fatalf("workers task count = %d, want 1 (rolled-up worker session counted)", summary.Workers.TaskCount)
	}
	wantNano := int64(23_000_000)
	if summary.EstimatedNanoUsd != wantNano {
		t.Fatalf("total nano = %v, want %v", summary.EstimatedNanoUsd, wantNano)
	}
}

func TestSQLTrackerProjectReportIncludesRolledUpSpend(t *testing.T) {
	tracker, sqlDB := newTestTracker(t, nil)
	testdbseed.InsertSession(t, sqlDB, "session-1", "project-1")
	seedRollupRow(t, sqlDB, "project-1", "session-1", "", CallerCoordinator, 40, 10, 1_000_000)

	report, err := tracker.ProjectReport(t.Context(), "project-1", ReportQuery{})
	testutil.FailErr(t, "tracker.ProjectReport", err)
	if got := report.Summary.TokenTotals.Prompt + report.Summary.TokenTotals.Completion; got != 50 {
		t.Fatalf("project total tokens = %d, want 50", got)
	}
	session := report.Sessions[0].Cost
	if session.Coordinator.TokenTotals.Prompt != 40 {
		t.Fatalf("session-1 coordinator prompt tokens = %d, want 40", session.Coordinator.TokenTotals.Prompt)
	}
}
