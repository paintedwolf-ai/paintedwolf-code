package security

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

// sequentialLLMClient supplies retry completions after rejected tool calls.
type sequentialLLMClient struct {
	mu          sync.Mutex
	completions []*modelcall.Completion
	idx         int
}

func (s *sequentialLLMClient) Complete(ctx context.Context, _ modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idx >= len(s.completions) {
		return &modelcall.Completion{Content: "unexpected extra turn"}, nil
	}
	out := *s.completions[s.idx]
	s.idx++
	return &out, nil
}

func (s *sequentialLLMClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk)
	go func() {
		defer close(ch)
		completion, err := s.Complete(ctx, req)
		if err != nil {
			return
		}
		if len(completion.ToolCalls) > 0 {
			if strings.TrimSpace(completion.Content) != "" {
				ch <- modelcall.StreamChunk{Content: completion.Content}
			}
			ch <- modelcall.StreamChunk{ToolCalls: completion.ToolCalls, Done: true}
			return
		}
		tokens := strings.Fields(completion.Content)
		if len(tokens) == 0 {
			ch <- modelcall.StreamChunk{Done: true}
			return
		}
		for i, token := range tokens {
			suffix := " "
			if i == len(tokens)-1 {
				suffix = ""
			}
			ch <- modelcall.StreamChunk{Content: token + suffix, Done: i == len(tokens)-1}
		}
	}()
	return ch, nil
}

func TestCoordinatorPromptRejectsOffSurfaceDelegate(t *testing.T) {
	mock := &sequentialLLMClient{completions: []*modelcall.Completion{
		{ToolCalls: []wire.ToolCall{{
			ID:   "call_delegate",
			Name: "delegate_dispatch",
			Args: map[string]any{},
		}}},
		{Content: "cannot delegate yet"},
	}}
	h := wiring.BuildForTest(t, wiring.WithLLMClient(mock), wiring.WithoutCoordinatorLoop())
	srv := h.Server
	store := h.Store
	blueprintMgr := h.BlueprintMgr
	sess := createSessionHTTP(t, srv, t.TempDir())
	ctx := t.Context()

	startBody := planStartBody
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflow-runs", strings.NewReader(startBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("start status = %d body = %s", w.Code, w.Body.String())
	}
	var run wire.WorkflowRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	seedPlanStub(t, blueprintMgr, run.ProjectID, run.BlueprintPath)

	if _, err := h.SessionMgr.Prompt(ctx, sess.ID, "delegate now"); err != nil {
		testutil.FailErr(t, "run coordinator prompt", err)
	}

	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	seenRejection := false
	for _, msg := range msgs {
		if result := msg.ToolResult; result != nil && result.ToolCallID == "call_delegate" {
			if result.Tool != "delegate_dispatch" || result.Outcome != wire.ToolResultOutcomeRejected || !slices.Contains(result.Codes, "SPEC_POSTURE_DELEGATION_FORBIDDEN") {
				t.Fatalf("off-surface delegate result = %+v", result)
			}
			seenRejection = true
		}
	}
	if !seenRejection {
		t.Fatal("off-surface delegate call has no structured rejection")
	}
}

func TestCoordinatorPromptSuccessWorkflowGateBanner(t *testing.T) {
	// A successful read reports the plan's unsatisfied exit gate.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("# plan notes\n"), 0o600); err != nil {
		testutil.FailErr(t, "write notes file", err)
	}
	mock := &sequentialLLMClient{completions: []*modelcall.Completion{
		{ToolCalls: []wire.ToolCall{{
			ID:   "call_read",
			Name: "read",
			Args: map[string]any{"path": "notes.md"},
		}}},
		{Content: "notes reviewed"},
	}}
	h := wiring.BuildForTest(t, wiring.WithLLMClient(mock), wiring.WithoutCoordinatorLoop())
	srv := h.Server
	store := h.Store
	sess := createSessionHTTP(t, srv, dir)
	ctx := t.Context()

	startBody := planStartBody
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflow-runs", strings.NewReader(startBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("start status = %d body = %s", w.Code, w.Body.String())
	}
	var run wire.WorkflowRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	run = completePlanIntakeHTTP(t, h, run.ID)
	run = completePlanDepthAtNoneHTTP(t, h, run.ID, "research")
	if run.CurrentPhase != "expand" {
		t.Fatalf("phase = %q want expand", run.CurrentPhase)
	}

	acceptPromptHTTP(t, srv, sess.ID, "review notes")
	waitToolFeedbackHTTP(t, srv, sess.ID, "WORKFLOW_GATE_BLOCKED", promptIdleBudget)

	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	var toolOut string
	for _, msg := range msgs {
		if msg.Role == wire.MessageRoleTool && strings.Contains(msg.Content, "Code: WORKFLOW_GATE_BLOCKED") {
			toolOut = msg.Content
			break
		}
	}
	if toolOut == "" {
		t.Fatalf("expected WORKFLOW_GATE_BLOCKED banner in tool messages: %+v", msgs)
	}
	if !strings.Contains(toolOut, ">>> Tool feedback") {
		t.Fatalf("expected success banner block in:\n%s", toolOut)
	}
}
