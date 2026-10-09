package security

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/approvaloutcome"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

type approvalHITLHarness struct {
	ctx           context.Context
	ownerCtx      context.Context
	store         session.Store
	projectDir    string
	mgr           *session.Manager
	checkpointMgr hitl.CheckpointManager
	hub           *events.MemoryHub
	srv           *api.Server
	sess          *wire.Session
}

// forceWriteAsk makes contained writes require a checkpoint.
func forceWriteAsk(t *testing.T, projectDir string) {
	t.Helper()
	overlayDir := filepath.Join(projectDir, settingsoverlay.DirName())
	testutil.FailErr(t, "mkdir approval overlay", os.MkdirAll(overlayDir, 0o700))
	const overlay = "rules:\n  - category: tool\n    pattern: write\n    effect: ask\n"
	testutil.FailErr(t, "write approval overlay",
		os.WriteFile(filepath.Join(overlayDir, "approvals.yaml"), []byte(overlay), 0o600))
}

func newApprovalHITLHarness(t *testing.T) *approvalHITLHarness {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: "write",
		ToolCalls: []llm.MockToolCall{{
			ID: "call_write", Name: "write", Args: map[string]any{"path": "a.txt", "content": "x"},
		}},
		FollowUpText: "done",
	}}})
	h := wiring.BuildForTest(t, wiring.WithLLMClient(mock))
	// Tool authorization is independent of workflow content review.
	manifest, err := h.WorkflowMgr.Resolver.Overlay.Get("implement", "1.0.0")
	testutil.FailErr(t, "load approval fixture workflow", err)
	manifest.Controls.ContentReview = nil
	for i := range manifest.PhaseDefs {
		manifest.PhaseDefs[i].ContentReview = nil
	}
	h.RegisterManifest(manifest)
	ctx := context.Background()
	projectDir := t.TempDir()
	forceWriteAsk(t, projectDir)
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, projectDir)
	testutil.FailErr(t, "create session in store", err)
	if err := h.SessionMgr.Chats.SetAgentType(ctx, sess.ID, "implementer"); err != nil {
		testutil.FailErr(t, "h.SessionMgr.Chats.SetAgentType failed", err)
	}
	sess.AgentType = "implementer"
	// Progress lets write reach the approval gate.
	h.SeedProgress(t, ctx, sess.ID)
	return &approvalHITLHarness{
		ctx:           ctx,
		ownerCtx:      h.OwnerCtx(t, ctx),
		store:         h.Store,
		projectDir:    projectDir,
		mgr:           h.SessionMgr,
		checkpointMgr: h.CheckpointMgr,
		hub:           h.MemoryHub(),
		srv:           h.Server,
		sess:          sess,
	}
}

// deniedCopy returns the bundled approval denial.
func deniedCopy(t *testing.T) string {
	t.Helper()
	cfg, err := approvaloutcome.Load()
	testutil.FailErr(t, "load approval-outcome catalog", err)
	return approvaloutcome.NewCatalog(cfg).Message(approvaloutcome.CodeApprovalDenied, nil)
}

func (h *approvalHITLHarness) waitPending(t *testing.T) string {
	t.Helper()
	var checkpointID string
	testutil.WaitFor(t, 15*time.Second, func() bool {
		kind := wire.CheckpointKindToolApproval
		pending, err := h.checkpointMgr.ListPending(h.ctx, h.sess.ID, &kind)
		if err == nil && len(pending) == 1 {
			checkpointID = pending[0].ID
			return true
		}
		return false
	})
	return checkpointID
}

func (h *approvalHITLHarness) runPrompt(t *testing.T) chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := h.mgr.Submissions.Prompt(h.ctx, h.sess.ID, "please write file")
		done <- err
	}()
	return done
}

func TestApprovalHITLApproveCompletesTool(t *testing.T) {
	h := newApprovalHITLHarness(t)

	done := h.runPrompt(t)
	decisionID := h.waitPending(t)

	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+h.sess.ID+"/checkpoints/"+decisionID, strings.NewReader(`{"kind":"tool_approval","action":"approve","option_id":"approve_current_action"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("approve status = %d body = %s", w.Code, w.Body.String())
	}

	if err := <-done; err != nil {
		testutil.FailErr(t, "operation failed", err)
	}

	if _, err := os.Stat(filepath.Join(h.projectDir, "a.txt")); err != nil {
		t.Fatalf("expected written file: %v", err)
	}
}

func TestApprovalHITLRejectBlocksTool(t *testing.T) {
	h := newApprovalHITLHarness(t)

	done := h.runPrompt(t)
	decisionID := h.waitPending(t)
	if _, err := h.checkpointMgr.ResolveCheckpoint(h.ownerCtx, h.sess.ID, decisionID, wire.CheckpointKindToolApproval, &hitl.DecisionResult{Approved: false}, nil); err != nil {
		testutil.FailErr(t, "h.checkpointMgr.ResolveCheckpoint failed", err)
	}
	if err := <-done; err != nil {
		testutil.FailErr(t, "operation failed", err)
	}

	ok, err := h.checkpointMgr.SessionApprovalDenied(h.ctx, h.sess.ID)
	if err != nil || !ok {
		t.Fatalf("SessionApprovalDenied = %v err=%v", ok, err)
	}

	msgs, err := h.store.GetMessages(h.ctx, h.sess.ID)
	testutil.FailErr(t, "h.store.GetMessages failed", err)
	wantDenied := deniedCopy(t)
	foundDenied := false
	for _, m := range msgs {
		if m.Role == wire.MessageRoleTool && m.Content == wantDenied {
			foundDenied = true
		}
		if m.Role == wire.MessageRoleTool && m.Content != wantDenied {
			t.Fatalf("unexpected tool message after reject: %+v", m)
		}
	}
	if !foundDenied {
		t.Fatalf("expected approval denied tool message, got %+v", msgs)
	}
}

func TestApprovalHITLRejectViaHTTP(t *testing.T) {
	h := newApprovalHITLHarness(t)

	done := h.runPrompt(t)
	decisionID := h.waitPending(t)

	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+h.sess.ID+"/checkpoints/"+decisionID, strings.NewReader(`{"kind":"tool_approval","action":"reject"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("reject status = %d body = %s", w.Code, w.Body.String())
	}

	if err := <-done; err != nil {
		testutil.FailErr(t, "operation failed", err)
	}

	ok, err := h.checkpointMgr.SessionApprovalDenied(h.ctx, h.sess.ID)
	if err != nil || !ok {
		t.Fatalf("SessionApprovalDenied = %v err=%v", ok, err)
	}

	msgs, err := h.store.GetMessages(h.ctx, h.sess.ID)
	testutil.FailErr(t, "h.store.GetMessages failed", err)
	wantDenied := deniedCopy(t)
	foundDenied := false
	for _, m := range msgs {
		if m.Role == wire.MessageRoleTool && m.Content == wantDenied {
			foundDenied = true
		}
		if m.Role == wire.MessageRoleTool && m.Content != wantDenied {
			t.Fatalf("unexpected tool message after HTTP reject: %+v", m)
		}
	}
	if !foundDenied {
		t.Fatalf("expected approval denied tool message, got %+v", msgs)
	}
}

func TestApprovalHITLRejectsClientAuthoredEdit(t *testing.T) {
	h := newApprovalHITLHarness(t)

	done := h.runPrompt(t)
	decisionID := h.waitPending(t)

	body := `{"kind":"tool_approval","action":"edit","edited_args":{"path":"via-http.txt","content":"z"}}`
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+h.sess.ID+"/checkpoints/"+decisionID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("edit status = %d body = %s, want 400", w.Code, w.Body.String())
	}

	approve := authedRequest(t, http.MethodPost, "/v1/sessions/"+h.sess.ID+"/checkpoints/"+decisionID, strings.NewReader(`{"kind":"tool_approval","action":"approve","option_id":"approve_current_action"}`))
	approve.Header.Set("Content-Type", "application/json")
	approved := httptest.NewRecorder()
	h.srv.ServeHTTP(approved, approve)
	if approved.Code != http.StatusOK {
		t.Fatalf("approve status = %d body = %s", approved.Code, approved.Body.String())
	}
	if err := <-done; err != nil {
		testutil.FailErr(t, "operation failed", err)
	}
	if _, err := os.Stat(filepath.Join(h.projectDir, "a.txt")); err != nil {
		t.Fatalf("expected original approved file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.projectDir, "via-http.txt")); !os.IsNotExist(err) {
		t.Fatalf("client-authored replacement must not exist: %v", err)
	}
}
