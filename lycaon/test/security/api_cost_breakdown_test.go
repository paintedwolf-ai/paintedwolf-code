package security

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
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

type usageMockLLM struct {
	*llm.MockProvider
}

func (u *usageMockLLM) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	c, err := u.MockProvider.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	return u.withUsage(c), nil
}

func (u *usageMockLLM) withUsage(c *modelcall.Completion) *modelcall.Completion {
	if c == nil {
		return nil
	}
	if c.Usage.PromptTokens == 0 && c.Usage.CompletionTokens == 0 {
		c.Usage = modelcall.TokenUsage{PromptTokens: 100, CompletionTokens: 50}
	}
	return c
}

func (u *usageMockLLM) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk)
	go func() {
		defer close(ch)
		completion, err := u.Complete(ctx, req)
		if err != nil {
			return
		}
		if len(completion.ToolCalls) > 0 {
			ch <- modelcall.StreamChunk{ToolCalls: completion.ToolCalls, Usage: completion.Usage, Done: true}
			return
		}
		tokens := strings.Fields(completion.Content)
		if len(tokens) == 0 {
			ch <- modelcall.StreamChunk{Usage: completion.Usage, Done: true}
			return
		}
		for i, token := range tokens {
			suffix := " "
			if i == len(tokens)-1 {
				suffix = ""
			}
			ch <- modelcall.StreamChunk{
				Content: token + suffix,
				Usage:   completion.Usage,
				Done:    i == len(tokens)-1,
			}
		}
	}()
	return ch, nil
}

func newCostBreakdownHarness(t *testing.T) (*wiring.Harness, *api.Server, *session.Manager, *events.MemoryHub) {
	t.Helper()
	mock := &usageMockLLM{MockProvider: llm.NewMockProvider(loadMockConfig(t))}
	h := wiring.BuildForTest(t, wiring.WithLLMClient(mock))
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)
	hub := h.MemoryHub()
	return h, h.Server, h.SessionMgr, hub
}

func TestCostSummaryWorkerSplitAfterDelegation(t *testing.T) {
	h, srv, _, _ := newCostBreakdownHarness(t)
	ctx := context.Background()
	dir := t.TempDir()
	parent := createSessionHTTP(t, srv, dir)

	postPromptAndWaitIdle(t, srv, parent.ID, "plan the work")

	r, err := h.DelegationMgr.Init(ctx, parent.ID, wire.CreateDelegationRequest{
		ProjectID: testdbseed.DefaultProjectID,
		Task:      "implement feature",
		Strategy:  wire.HuntStrategyFileBased,
	})
	testutil.FailErr(t, "h.DelegationMgr.Init failed", err)
	if _, err := h.DelegationMgr.DispatchLeg(ctx, r.ID, r.Legs[0].ID, ""); err != nil {
		testutil.FailErr(t, "h.DelegationMgr.DispatchLeg failed", err)
	}

	if !testutil.WaitForNoFatal(promptIdleBudget, func() bool {
		st, err := h.DelegationMgr.GetStatus(ctx, r.ID)
		if err != nil || st == nil {
			return false
		}
		return st.Phase == wire.DelegationPhaseDone
	}) {
		status, err := h.DelegationMgr.GetStatus(ctx, r.ID)
		t.Fatalf("worker delegation did not finish within %s: status=%+v error=%v", promptIdleBudget, status, err)
	}

	req := authedRequest(t, http.MethodGet, "/v1/cost/summary?session_id="+parent.ID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var summary wire.CostSummary
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if summary.Coordinator.EstimatedNanoUsd <= 0 {
		t.Fatalf("coordinator nano usd = %v want > 0", summary.Coordinator.EstimatedNanoUsd)
	}
	if summary.Workers.EstimatedNanoUsd <= 0 {
		t.Fatalf("workers nano usd = %v want > 0", summary.Workers.EstimatedNanoUsd)
	}
}

func TestCostSSEIncludesBreakdown(t *testing.T) {
	_, srv, mgr, hub := newCostBreakdownHarness(t)
	ctx := context.Background()
	parent := createSessionHTTP(t, srv, t.TempDir())

	ch, unsub, err := hub.Subscribe(ctx, events.Subscription{Project: parent.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsub()

	if _, err := mgr.Submissions.Prompt(ctx, parent.ID, "hello"); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}

	var got wire.CostEvent
	testutil.WaitFor(t, 5*time.Second, func() bool {
		select {
		case envelope, ok := <-ch:
			if !ok || envelope.Topic != wire.EventTopicCost {
				return false
			}
			if err := json.Unmarshal(envelope.Data, &got); err != nil {
				testutil.FailErr(t, "unmarshal JSON document", err)
			}
			return got.Coordinator.EstimatedNanoUsd > 0 &&
				got.TokenTotals.Prompt+got.TokenTotals.Completion > 0
		default:
			hub.FlushDebounced()
			return false
		}
	})
}
