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
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestPlanImplementLegLoopWakesCoordinator(t *testing.T) {
	h := wiring.BuildForTest(t, wiring.WithRecordingLLM())
	srv := h.Server
	mgr := h.Sessions.Manager
	blueprintMgr := h.Workflows.Blueprints
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

	initialAssistants := 0
	for _, msg := range listMessagesHTTP(t, srv, sess.ID) {
		if msg.Role == wire.MessageRoleAssistant {
			initialAssistants++
		}
	}
	accepted := acceptPromptHTTP(t, srv, sess.ID, "plan implement leg")
	deadline := time.Now().Add(30 * time.Second)
	for {
		msgs := listMessagesHTTP(t, srv, sess.ID)
		seenReceipt, assistants := false, 0
		for _, msg := range msgs {
			seenReceipt = seenReceipt || msg.ID == accepted.OperationID && msg.Role == wire.MessageRoleUser
			if msg.Role == wire.MessageRoleAssistant {
				assistants++
			}
		}
		if seenReceipt && assistants > initialAssistants && getSessionHTTP(t, srv, sess.ID).Status == wire.SessionStatusIdle {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("prompt did not complete: receipt=%t assistants=%d initial=%d", seenReceipt, assistants, initialAssistants)
		}
		time.Sleep(20 * time.Millisecond)
	}
	child, err := mgr.Workers.SpawnChild(ctx, sess.ID, wire.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "spawn child", err)
	if _, err := mgr.Workers.Summaries.Append(ctx, sess.ID, workeroutcomes.SummaryInput{
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
	var allowed bool
	var reason string
	deadline = time.Now().Add(30 * time.Second)
	for {
		var err error
		allowed, reason, err = mgr.Coordinator.Runtime.CoordinatorLoop().Admission.ShouldLoopWake(ctx, sess.ID, anchor.LegFinished)
		if err == nil && allowed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("completed leg did not become eligible to wake coordinator: reason=%q error=%v", reason, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !allowed {
		t.Fatalf("completed leg cannot wake coordinator: %s", reason)
	}
	mgr.Coordinator.Runtime.CoordinatorLoop().Nudges.Nudge(ctx, sess.ID, anchor.LegFinished, anchor.LegFinished, "leg-e2e", anchor.Envelope{})

	testutil.WaitFor(t, func() bool {
		mgr.Runner.Coordinator.CoordinatorLoop().Nudges.DrainPending(ctx, sess.ID)
		msgs, err := mgr.Runner.Transcript.GetMessages(ctx, sess.ID)
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
