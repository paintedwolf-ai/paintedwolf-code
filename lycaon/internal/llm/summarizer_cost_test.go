package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/cost/costtest"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type costUsageProvider struct {
	stubCuratorProvider
	usage modelcall.TokenUsage
}

func (p *costUsageProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	c, err := p.stubCuratorProvider.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	c.Usage = p.usage
	return c, nil
}

func TestRegistrySummarizerRecordsLiteCost(t *testing.T) {
	tracker := costtest.NewTracker(t, cost.NoopPricer{})
	provider := &costUsageProvider{
		stubCuratorProvider: stubCuratorProvider{
			id:        "lite",
			responses: []string{"summary"},
		},
		usage: modelcall.TokenUsage{PromptTokens: 40, CompletionTokens: 8},
	}
	r := newTestRegistrySummarizer(t, provider)
	r.Cost = tracker
	r.ProjectID = "project-1"
	r.ProjectDir = "/tmp/proj"

	ctx := curationctx.WithSession(context.Background(), curationctx.Session{
		SessionID:  "sess-1",
		ProjectID:  "project-1",
		ProjectDir: "/tmp/proj",
	})
	out, err := r.Summarize(ctx, "system", "user", 100)
	testutil.FailErr(t, "Summarize", err)
	if out != "summary" {
		t.Fatalf("content = %q", out)
	}
	summary, err := tracker.Summary(ctx, api.CostScopeSession, "sess-1", "")
	testutil.FailErr(t, "Summary", err)
	if summary.Summarizer.TokenTotals.Prompt != 40 || summary.Summarizer.TokenTotals.Completion != 8 {
		t.Fatalf("summarizer tokens = %+v", summary.Summarizer.TokenTotals)
	}
	if summary.Coordinator.TokenTotals.Prompt != 0 {
		t.Fatalf("coordinator should be empty: %+v", summary.Coordinator)
	}
}

func TestRegistrySummarizerRecordsProjectOnlyCostWithoutSessionAttribution(t *testing.T) {
	tracker := costtest.NewTracker(t, cost.NoopPricer{})
	provider := &costUsageProvider{
		stubCuratorProvider: stubCuratorProvider{id: "lite", responses: []string{"summary"}},
		usage:               modelcall.TokenUsage{PromptTokens: 31, CompletionTokens: 7},
	}
	r := newTestRegistrySummarizer(t, provider)
	r.Cost, r.ProjectID, r.ProjectDir, r.Purpose = tracker, "project-briefing", "/tmp/project-briefing", "file_briefing"

	_, err := r.Summarize(context.Background(), "system", "user", 100)
	testutil.FailErr(t, "Summarize", err)
	project, err := tracker.Summary(context.Background(), api.CostScopeProject, "", "project-briefing")
	testutil.FailErr(t, "project Summary", err)
	if project.Summarizer.TokenTotals.Prompt != 31 || project.Summarizer.TokenTotals.Completion != 7 {
		t.Fatalf("project summarizer tokens = %+v", project.Summarizer.TokenTotals)
	}
	session, err := tracker.Summary(context.Background(), api.CostScopeSession, "unrelated-session", "")
	testutil.FailErr(t, "session Summary", err)
	if session.TokenTotals.Prompt != 0 || session.TokenTotals.Completion != 0 {
		t.Fatalf("unrelated session was charged: %+v", session.TokenTotals)
	}
}

func TestRegistrySummarizerRequiredDoesNotReturnFallbackText(t *testing.T) {
	r := &RegistrySummarizer{Fallback: compaction.TruncateSummarizer{}}
	_, err := r.SummarizeRequired(context.Background(), "system", "user", 100)
	if err == nil {
		t.Fatal("SummarizeRequired succeeded without a configured provider")
	}
}

// receiptLedgerTracker records receipt operations and context state.
type receiptLedgerTracker struct {
	ops         []string
	liveErr     []error
	sources     []cost.UsageSource
	events      []cost.UsageEvent
	estimateErr error
	recordErr   error
	beginErr    error
}

func (l *receiptLedgerTracker) BeginCall(ctx context.Context, _ cost.UsageEvent) error {
	l.ops = append(l.ops, "begin")
	l.liveErr = append(l.liveErr, ctx.Err())
	return l.beginErr
}

func (l *receiptLedgerTracker) MarkCallUnknown(ctx context.Context, _ string) error {
	l.ops = append(l.ops, "unknown")
	l.liveErr = append(l.liveErr, ctx.Err())
	return nil
}

func (l *receiptLedgerTracker) VoidCall(ctx context.Context, _ string) error {
	l.ops = append(l.ops, "void")
	l.liveErr = append(l.liveErr, ctx.Err())
	return nil
}

func (l *receiptLedgerTracker) RecordUsage(ctx context.Context, evt cost.UsageEvent) error {
	l.ops = append(l.ops, "record")
	l.liveErr = append(l.liveErr, ctx.Err())
	l.sources = append(l.sources, evt.UsageSource)
	l.events = append(l.events, evt)
	return l.recordErr
}

func (l *receiptLedgerTracker) Estimate(context.Context, string, string, cost.TokenUsage) (cost.CostEstimate, error) {
	if l.estimateErr != nil {
		return cost.CostEstimate{}, l.estimateErr
	}
	return cost.CostEstimate{Currency: "USD"}, nil
}

func (l *receiptLedgerTracker) Summary(context.Context, api.CostScope, string, string) (api.CostSummary, error) {
	return api.CostSummary{}, nil
}

func (l *receiptLedgerTracker) ProjectReport(context.Context, string, cost.ReportQuery) (api.ProjectCostReport, error) {
	return api.ProjectCostReport{}, nil
}

func (*receiptLedgerTracker) ClaimSpendWarning(context.Context, string, float64) (bool, error) {
	return false, nil
}

func (*receiptLedgerTracker) ClearSpendWarning(context.Context, string) error {
	return nil
}

var _ cost.CostTracker = (*receiptLedgerTracker)(nil)
var _ cost.CallLedger = (*receiptLedgerTracker)(nil)

func (l *receiptLedgerTracker) assertOps(t *testing.T, want ...string) {
	t.Helper()
	if len(l.ops) != len(want) {
		t.Fatalf("receipt ops = %v want %v", l.ops, want)
	}
	for i, op := range want {
		if l.ops[i] != op {
			t.Fatalf("receipt ops = %v want %v", l.ops, want)
		}
	}
}

// formatRejectingProvider rejects structured response requests.
type formatRejectingProvider struct {
	stubCuratorProvider
	usage      modelcall.TokenUsage
	rejections int
}

func (p *formatRejectingProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if req.ResponseFormat != nil {
		p.rejections++
		return nil, &providerretry.ProviderHTTPError{
			Status:  400,
			Param:   "response_format",
			Message: "structured response format rejected",
		}
	}
	c, err := p.stubCuratorProvider.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	c.Usage = p.usage
	return c, nil
}

func TestSummarizeWithVoidsFormatRejectedReceipt(t *testing.T) {
	tracker := costtest.NewTracker(t, cost.NoopPricer{})
	provider := &formatRejectingProvider{
		stubCuratorProvider: stubCuratorProvider{id: "lite", responses: []string{"summary"}},
		usage:               modelcall.TokenUsage{PromptTokens: 9, CompletionTokens: 4},
	}
	r := newTestRegistrySummarizer(t, provider)
	r.Cost = tracker

	ctx := compaction.WithSummaryFormat(curationctx.WithSession(context.Background(), curationctx.Session{SessionID: "sess-1"}))
	out, err := r.Summarize(ctx, "system", "user", 100)
	testutil.FailErr(t, "Summarize", err)
	if out != "summary" {
		t.Fatalf("content = %q", out)
	}
	if provider.rejections != 1 {
		t.Fatalf("format rejections = %d want 1", provider.rejections)
	}
	summary, err := tracker.Summary(ctx, api.CostScopeSession, "sess-1", "")
	testutil.FailErr(t, "Summary", err)
	if summary.UnknownCalls != 0 {
		t.Fatalf("pre-generation rejection minted %d unknown calls, want 0 (voided)", summary.UnknownCalls)
	}
	if summary.Summarizer.TokenTotals.Prompt != 9 || summary.Summarizer.TokenTotals.Completion != 4 {
		t.Fatalf("summarizer tokens = %+v", summary.Summarizer.TokenTotals)
	}
}

func TestSummarizeWithFormatRetryReceiptSequence(t *testing.T) {
	ledger := &receiptLedgerTracker{}
	provider := &formatRejectingProvider{
		stubCuratorProvider: stubCuratorProvider{id: "lite", responses: []string{"summary"}},
		usage:               modelcall.TokenUsage{PromptTokens: 1, CompletionTokens: 1},
	}
	r := newTestRegistrySummarizer(t, provider)
	r.Cost = ledger

	_, err := r.Summarize(compaction.WithSummaryFormat(context.Background()), "system", "user", 100)
	testutil.FailErr(t, "Summarize", err)
	ledger.assertOps(t, "begin", "void", "begin", "record")
}

func TestSummarizerSuccessNeverBooksUnknown(t *testing.T) {
	ledger := &receiptLedgerTracker{}
	provider := &costUsageProvider{
		stubCuratorProvider: stubCuratorProvider{id: "lite", responses: []string{"summary"}},
		usage:               modelcall.TokenUsage{PromptTokens: 2, CompletionTokens: 1},
	}
	r := newTestRegistrySummarizer(t, provider)
	r.Cost = ledger

	_, err := r.Summarize(context.Background(), "system", "user", 100)
	testutil.FailErr(t, "Summarize", err)
	ledger.assertOps(t, "begin", "record")
}

func TestSummarizerPreservesKnownUsageWhenPricingEstimateFails(t *testing.T) {
	ledger := &receiptLedgerTracker{estimateErr: errors.New("pricing unavailable")}
	provider := &costUsageProvider{
		stubCuratorProvider: stubCuratorProvider{id: "lite", responses: []string{"summary"}},
		usage:               modelcall.TokenUsage{PromptTokens: 2, CompletionTokens: 1},
	}
	r := newTestRegistrySummarizer(t, provider)
	r.Cost = ledger

	out, err := r.Summarize(context.Background(), "system", "user", 100)
	testutil.FailErr(t, "Summarize", err)
	if out != "summary" {
		t.Fatalf("summary = %q want provider output", out)
	}
	ledger.assertOps(t, "begin", "record")
	if len(ledger.events) != 1 || !ledger.events[0].Unpriced {
		t.Fatalf("events = %+v want known usage recorded as unpriced", ledger.events)
	}
}

// streamFailingProvider rejects streams and completes normally.
type streamFailingProvider struct {
	stubCuratorProvider
	usage modelcall.TokenUsage
}

func (p *streamFailingProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	c, err := p.stubCuratorProvider.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	c.Usage = p.usage
	return c, nil
}

func (p *streamFailingProvider) Stream(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	return nil, errors.New("stream rejected before generation")
}

func TestSummarizeStreamSetupFallbackVoidsReceipt(t *testing.T) {
	ledger := &receiptLedgerTracker{}
	provider := &streamFailingProvider{
		stubCuratorProvider: stubCuratorProvider{id: "lite", responses: []string{"summary"}},
		usage:               modelcall.TokenUsage{PromptTokens: 3, CompletionTokens: 2},
	}
	r := newTestRegistrySummarizer(t, provider)
	r.Cost = ledger

	out, err := r.SummarizeStream(context.Background(), "system", "user", 100, nil)
	testutil.FailErr(t, "SummarizeStream", err)
	if out != "summary" {
		t.Fatalf("content = %q", out)
	}
	ledger.assertOps(t, "begin", "void", "begin", "record")
}

// cancelingStreamProvider cancels after one chunk.
type cancelingStreamProvider struct {
	stubCuratorProvider
	cancel context.CancelFunc
}

func (p *cancelingStreamProvider) Stream(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Content: "partial"}
	p.cancel()
	close(ch)
	return ch, nil
}

func TestSummarizeStreamCancellationRecordsHostMeasuredUsage(t *testing.T) {
	ledger := &receiptLedgerTracker{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &cancelingStreamProvider{
		stubCuratorProvider: stubCuratorProvider{id: "lite"},
		cancel:              cancel,
	}
	r := newTestRegistrySummarizer(t, provider)
	r.Cost = ledger

	if _, err := r.SummarizeStream(ctx, "system", "user", 100, nil); err == nil {
		t.Fatal("canceled stream returned no error")
	}
	ledger.assertOps(t, "begin", "record")
	if got := ledger.sources[0]; got != cost.UsageFromHost {
		t.Fatalf("usage source = %q want %q", got, cost.UsageFromHost)
	}
	if got := ledger.liveErr[1]; got != nil {
		t.Fatalf("usage write saw ctx err %v, want cancellation stripped", got)
	}
}

// emptyCancelingStreamProvider cancels before content arrives.
type emptyCancelingStreamProvider struct {
	stubCuratorProvider
	cancel context.CancelFunc
}

func (p *emptyCancelingStreamProvider) Stream(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk)
	p.cancel()
	close(ch)
	return ch, nil
}

func TestSummarizeStreamCancellationWithoutContentBooksUnknown(t *testing.T) {
	ledger := &receiptLedgerTracker{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &emptyCancelingStreamProvider{
		stubCuratorProvider: stubCuratorProvider{id: "lite"},
		cancel:              cancel,
	}
	r := newTestRegistrySummarizer(t, provider)
	r.Cost = ledger

	if _, err := r.SummarizeStream(ctx, "system", "user", 100, nil); err == nil {
		t.Fatal("canceled stream returned no error")
	}
	ledger.assertOps(t, "begin", "unknown")
	if got := ledger.liveErr[1]; got != nil {
		t.Fatalf("unknown write saw ctx err %v, want cancellation stripped", got)
	}
}

type recordingLifecycle struct {
	evs []api.LLMCallEvent
}

func (r *recordingLifecycle) PublishCall(_ context.Context, ev api.LLMCallEvent) {
	r.evs = append(r.evs, ev)
}

func occupiedSessionCtx() context.Context {
	ctx := curationctx.WithSession(context.Background(), curationctx.Session{
		SessionID: "sess-1",
		ProjectID: "project-1",
	})
	return curationctx.WithLane(ctx)
}

func TestCompleteUtilityPublishesLaneOccupiedCall(t *testing.T) {
	life := &recordingLifecycle{}
	provider := &costUsageProvider{
		stubCuratorProvider: stubCuratorProvider{id: "lite", responses: []string{"summary"}},
		usage:               modelcall.TokenUsage{PromptTokens: 4, CompletionTokens: 2},
	}
	r := newTestRegistrySummarizer(t, provider)
	r.Lifecycle = life

	out, err := r.Summarize(occupiedSessionCtx(), "system", "user", 100)
	testutil.FailErr(t, "Summarize", err)
	if out != "summary" {
		t.Fatalf("content = %q", out)
	}
	if len(life.evs) != 2 {
		t.Fatalf("events = %d want 2: %+v", len(life.evs), life.evs)
	}
	if life.evs[0].Status != api.LLMCallStatusActive || life.evs[0].Provider != "lite" {
		t.Fatalf("active = %+v", life.evs[0])
	}
	if life.evs[1].Status != api.LLMCallStatusOK || life.evs[1].CallID != life.evs[0].CallID {
		t.Fatalf("terminal = %+v", life.evs[1])
	}
}

func TestCompleteUtilityDoesNotPublishWithoutLane(t *testing.T) {
	life := &recordingLifecycle{}
	provider := &costUsageProvider{
		stubCuratorProvider: stubCuratorProvider{id: "lite", responses: []string{"summary"}},
		usage:               modelcall.TokenUsage{PromptTokens: 4, CompletionTokens: 2},
	}
	r := newTestRegistrySummarizer(t, provider)
	r.Lifecycle = life

	ctx := curationctx.WithSession(context.Background(), curationctx.Session{SessionID: "sess-1"})
	_, err := r.Summarize(ctx, "system", "user", 100)
	testutil.FailErr(t, "Summarize", err)
	if len(life.evs) != 0 {
		t.Fatalf("session-only ctx published %+v", life.evs)
	}
}

func TestCompleteUtilityDoesNotPublishWithoutSession(t *testing.T) {
	life := &recordingLifecycle{}
	provider := &costUsageProvider{
		stubCuratorProvider: stubCuratorProvider{id: "lite", responses: []string{"summary"}},
		usage:               modelcall.TokenUsage{PromptTokens: 4, CompletionTokens: 2},
	}
	r := newTestRegistrySummarizer(t, provider)
	r.Lifecycle = life

	_, err := r.Summarize(curationctx.WithLane(context.Background()), "system", "user", 100)
	testutil.FailErr(t, "Summarize", err)
	if len(life.evs) != 0 {
		t.Fatalf("lane-only ctx published %+v", life.evs)
	}
}

func TestCompleteUtilityPublishesErrorOnFailure(t *testing.T) {
	life := &recordingLifecycle{}
	provider := &stubCuratorProvider{id: "lite", err: errors.New("provider down")}
	r := newTestRegistrySummarizer(t, provider)
	r.Lifecycle = life
	r.Fallback = nil

	_, err := r.SummarizeRequired(occupiedSessionCtx(), "system", "user", 100)
	if err == nil {
		t.Fatal("expected provider error")
	}
	if len(life.evs) != 2 {
		t.Fatalf("events = %d want 2: %+v", len(life.evs), life.evs)
	}
	if life.evs[0].Status != api.LLMCallStatusActive {
		t.Fatalf("active = %+v", life.evs[0])
	}
	if life.evs[1].Status != api.LLMCallStatusError {
		t.Fatalf("terminal = %+v", life.evs[1])
	}
}
