package security

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestPlanImplementLegLoopWakesCoordinator(t *testing.T) {
	h := wiring.BuildForTest(t, wiring.WithRecordingLLM())
	srv := h.Server
	mgr := h.SessionMgr
	blueprintMgr := h.BlueprintMgr
	ctx := context.Background()
	sess := createSessionHTTP(t, srv, t.TempDir())

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
		testutil.FailErr(t, "unmarshal run", err)
	}
	run = advancePlanRunToExecuteHTTP(t, h, srv, blueprintMgr, sess, run)
	if run.CurrentPhase != "execute" {
		t.Fatalf("phase = %q want execute", run.CurrentPhase)
	}

	if _, err := mgr.Prompt(ctx, sess.ID, "plan implement leg"); err != nil {
		testutil.FailErr(t, "mgr.Prompt failed", err)
	}
	child, err := mgr.SpawnChild(ctx, sess.ID, wire.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "spawn child", err)
	if _, err := mgr.AppendWorkerSummary(ctx, sess.ID, session.WorkerSummaryInput{
		Summary:        "implemented feature X",
		DelegationID:   "dep-e2e",
		LegID:          "leg-e2e",
		JobID:          "job-e2e",
		ChildSessionID: child.ID,
		AgentType:      "implementer",
		Status:         "complete",
	}); err != nil {
		testutil.FailErr(t, "append worker summary", err)
	}
	allowed, reason, err := mgr.ShouldLoopWake(ctx, sess.ID, anchor.LegFinished)
	testutil.FailErr(t, "evaluate loop wake", err)
	if !allowed {
		t.Fatalf("completed leg cannot wake coordinator: %s", reason)
	}
	mgr.NudgeCoordinatorLoop(ctx, sess.ID, anchor.LegFinished, anchor.LegFinished, "leg-e2e", anchor.Envelope{})

	testutil.WaitFor(t, 5*time.Second, func() bool {
		mgr.DrainLoopPendingForTest(ctx, sess.ID)
		msgs, err := mgr.GetMessages(ctx, sess.ID)
		if err != nil {
			return false
		}
		assistants := 0
		hasAuto := false
		for _, msg := range msgs {
			if msg.Role == wire.MessageRoleAssistant {
				assistants++
			}
			if msg.Kind == wire.MessageKindHostLoopWake {
				hasAuto = true
			}
		}
		return assistants >= 2 && hasAuto
	})

	req = authedRequest(t, http.MethodGet, "/v1/sessions/"+sess.ID+"/messages", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET messages status = %d", w.Code)
	}
}
