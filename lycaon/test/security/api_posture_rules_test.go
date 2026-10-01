package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func advancePlanRunToImplement(t *testing.T, h *wiring.Harness, srv *api.Server, store session.Store, blueprintMgr *blueprint.Manager, sess wire.Session) wire.WorkflowRun {
	t.Helper()
	ctx := t.Context()
	if _, err := store.Get(ctx, sess.ID); err != nil {
		testutil.FailErr(t, "store.Get failed", err)
	}

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
	run = advancePlanRunToExecuteHTTP(t, h, srv, blueprintMgr, sess, run)

	sessAfter, err := store.Get(ctx, sess.ID)
	testutil.FailErr(t, "store.Get failed", err)
	if sessAfter.Posture != wire.SessionPostureBuild {
		t.Fatalf("session posture = %q want build", sessAfter.Posture)
	}
	return run
}

func TestBuildPostureDisallowedAgentAfterImplementPhase(t *testing.T) {
	mock := llm.NewMockProvider(&llm.MockConfig{
		Responses: []llm.MockResponseEntry{{
			Pattern: "spawn task",
			ToolCalls: []llm.MockToolCall{{
				ID:   "call_task",
				Name: "task",
				Args: map[string]any{"agent_type": "sentinel"},
			}},
			FollowUpText: "task handled",
		}},
	})
	h := wiring.BuildForTest(t, wiring.WithLLMClient(mock), wiring.WithoutCoordinatorLoop())
	srv := h.Server
	store := h.Store
	blueprintMgr := h.BlueprintMgr
	sess := createSessionHTTP(t, srv, t.TempDir())
	ctx := t.Context()
	advancePlanRunToImplement(t, h, srv, store, blueprintMgr, sess)

	// Settle workflow setup before exercising its posture rules.
	h.SessionMgr.CancelInFlightPrompt(sess.ID)
	h.SessionMgr.WaitForCoordinatorAsyncTurns(ctx)
	h.SessionMgr.ClearPendingKickForTest(sess.ID)
	h.SeedProgress(t, ctx, sess.ID)
	accepted := acceptPromptHTTP(t, srv, sess.ID, "spawn task")

	if !testutil.WaitForNoFatal(promptIdleBudget, func() bool {
		submission, err := store.GetPromptSubmission(ctx, accepted.OperationID)
		testutil.FailErr(t, "read posture prompt receipt", err)
		if !submission.Status.Terminal() {
			return false
		}
		if submission.Status != sessionstore.PromptSubmissionComplete {
			t.Fatalf("posture prompt status = %s: %s", submission.Status, submission.Error)
		}
		return true
	}) {
		t.Fatal("posture prompt did not finish")
	}

	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	var toolDeny string
	for _, msg := range msgs {
		if msg.Role == wire.MessageRoleTool && strings.Contains(msg.Content, "DISALLOWED_AGENT") {
			toolDeny = msg.Content
			break
		}
	}
	if toolDeny == "" {
		t.Fatalf("expected DISALLOWED_AGENT tool denial in messages: %+v", msgs)
	}
	if strings.Contains(toolDeny, "SPEC_POSTURE") {
		t.Fatalf("expected build pack denial, got spec code in %q", toolDeny)
	}
}

func TestBuildPostureAllowsDelegationWithPlanManifestRules(t *testing.T) {
	// Build posture delegates through task under its active agent rules.
	mock := llm.NewMockProvider(&llm.MockConfig{
		Responses: []llm.MockResponseEntry{{
			Pattern: "spawn delegate",
			ToolCalls: []llm.MockToolCall{{
				ID:   "call_delegate",
				Name: "task",
				Args: wiring.TaskToolArgs("implementer", "implement the change", "feature.go"),
			}},
			FollowUpText: "delegated",
		}},
	})
	h := wiring.BuildForTest(t, wiring.WithLLMClient(mock), wiring.WithoutCoordinatorLoop())
	srv := h.Server
	store := h.Store
	blueprintMgr := h.BlueprintMgr
	sess := createSessionHTTP(t, srv, t.TempDir())
	ctx := t.Context()
	advancePlanRunToImplement(t, h, srv, store, blueprintMgr, sess)
	h.SessionMgr.CancelInFlightPrompt(sess.ID)
	h.SessionMgr.WaitForCoordinatorAsyncTurns(ctx)
	h.SessionMgr.ClearPendingKickForTest(sess.ID)

	// Pending workers keep the workflow active until a poller claims them.
	h.SeedProgress(t, ctx, sess.ID)
	acceptPromptHTTP(t, srv, sess.ID, "spawn delegate")
	if !testutil.WaitForNoFatal(promptIdleBudget, func() bool {
		tasks, err := h.WorkerQueue.ListBySession(ctx, sess.ProjectID, sess.ID, wire.WorkerStatusPending)
		return err == nil && len(tasks) == 1
	}) {
		messages, err := store.GetMessages(ctx, sess.ID)
		testutil.FailErr(t, "load failed delegation transcript", err)
		t.Fatalf("delegation was not enqueued: messages=%+v", messages)
	}

	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	for _, msg := range msgs {
		if msg.Role == wire.MessageRoleTool && strings.Contains(msg.Content, "SPEC_POSTURE_DELEGATION_FORBIDDEN") {
			t.Fatalf("manifest spec rules must not deny delegation in build posture: %q", msg.Content)
		}
	}
}
