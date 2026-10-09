package wiring

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type usageMockLLM struct {
	*llm.MockProvider
}

func (u *usageMockLLM) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	c, err := u.MockProvider.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	if c != nil && c.Usage.PromptTokens == 0 && c.Usage.CompletionTokens == 0 {
		c.Usage = modelcall.TokenUsage{PromptTokens: 100, CompletionTokens: 50}
	}
	return c, nil
}

func (u *usageMockLLM) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk)
	go func() {
		defer close(ch)
		completion, err := u.Complete(ctx, req)
		if err != nil {
			return
		}
		ch <- modelcall.StreamChunk{Content: completion.Content, Usage: completion.Usage, Done: true}
	}()
	return ch, nil
}

func TestCostTrackingOnPricedSummaryAndSSE(t *testing.T) {
	mock := &usageMockLLM{MockProvider: llm.NewMockProvider(&llm.MockConfig{
		Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}},
	})}
	h := BuildForTest(t, WithLLMClient(mock))
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)
	hub := h.MemoryHub()
	if hub == nil {
		t.Fatal("expected MemoryHub")
	}

	ctx := context.Background()
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, t.TempDir())
	testutil.FailErr(t, "CreateHarnessSession", err)

	ch, unsub, err := hub.Subscribe(ctx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "Subscribe", err)
	defer unsub()

	if _, err := h.SessionMgr.Submissions.Prompt(ctx, sess.ID, "hello"); err != nil {
		testutil.FailErr(t, "Prompt", err)
	}

	summary, err := h.SessionMgr.CostTracker().Summary(ctx, wire.CostScopeSession, sess.ID, "")
	testutil.FailErr(t, "Summary", err)
	if summary.EstimateCoverage != wire.CostEstimateComplete || len(summary.PricingProvenance) != 1 || summary.PricingProvenance[0].Source != "fixture" || summary.PricingProvenance[0].PricedAt == nil {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.EstimatedNanoUsd <= 0 {
		t.Fatalf("estimated_nano_usd = %v", summary.EstimatedNanoUsd)
	}

	var got wire.CostEvent
	testutil.WaitFor(t, 5*time.Second, func() bool {
		select {
		case envelope, ok := <-ch:
			if !ok || envelope.Topic != wire.EventTopicCost {
				return false
			}
			if err := json.Unmarshal(envelope.Data, &got); err != nil {
				testutil.FailErr(t, "unmarshal cost event", err)
			}
			return got.Coordinator.EstimatedNanoUsd > 0
		default:
			hub.FlushDebounced()
			return false
		}
	})

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/v1/projects/"+sess.ProjectID+"/board?session_id="+sess.ID, nil)
	req.Header.Set("Authorization", "Bearer "+api.TestAPIToken)
	w := httptest.NewRecorder()
	h.Server.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("board status = %d body = %s", w.Code, w.Body.String())
	}
	var board wire.BoardView
	if err := json.Unmarshal(w.Body.Bytes(), &board); err != nil {
		testutil.FailErr(t, "unmarshal board", err)
	}
	if board.Cost == nil || board.Cost.EstimateCoverage != wire.CostEstimateComplete || board.Cost.EstimatedNanoUsd <= 0 {
		t.Fatalf("board.cost = %+v", board.Cost)
	}
}

func TestCostTrackingOffUnpricedNoFetch(t *testing.T) {
	mock := &usageMockLLM{MockProvider: llm.NewMockProvider(&llm.MockConfig{
		Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}},
	})}
	h := BuildForTest(t, WithProductionCostPricer(), WithLLMClient(mock))
	ctx := context.Background()

	// Tracking off suppresses pricing with a source still selected.
	patchBody := `{"cost_tracking_enabled":false,"sources":[{"id":"models-dev","enabled":true},{"id":"litellm","enabled":false},{"id":"ai-pricing-fyi","enabled":false}]}`
	patchReq := httptest.NewRequestWithContext(ctx, http.MethodPatch, "/v1/settings/pricing", strings.NewReader(patchBody))
	patchReq.Header.Set("Authorization", "Bearer "+api.TestAPIToken)
	patchReq.Header.Set("Content-Type", "application/json")
	patchW := httptest.NewRecorder()
	h.Server.ServeHTTP(patchW, patchReq)
	if patchW.Code != http.StatusOK {
		t.Fatalf("PATCH pricing status = %d body = %s", patchW.Code, patchW.Body.String())
	}

	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, t.TempDir())
	testutil.FailErr(t, "CreateHarnessSession", err)

	if _, err := h.SessionMgr.Submissions.Prompt(ctx, sess.ID, "hello"); err != nil {
		testutil.FailErr(t, "Prompt", err)
	}

	summary, err := h.SessionMgr.CostTracker().Summary(ctx, wire.CostScopeSession, sess.ID, "")
	testutil.FailErr(t, "Summary", err)
	if summary.EstimateCoverage != wire.CostEstimateUnpriced || len(summary.PricingProvenance) != 0 {
		t.Fatalf("tracking off must be unpriced: %+v", summary)
	}
	if summary.EstimatedNanoUsd != 0 {
		t.Fatalf("estimated_nano_usd = %v want 0", summary.EstimatedNanoUsd)
	}

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/v1/cost/summary?session_id="+sess.ID, nil)
	req.Header.Set("Authorization", "Bearer "+api.TestAPIToken)
	w := httptest.NewRecorder()
	h.Server.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"priced":true`) {
		t.Fatalf("HTTP summary must not report priced=true: %s", w.Body.String())
	}
}
