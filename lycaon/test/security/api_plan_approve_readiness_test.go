package security

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestApprovePlanHTTPRejectsWhenNotReady(t *testing.T) {
	h := wiring.BuildForTest(t, wiring.WithoutCoordinatorLoop())
	srv := h.Server
	proj := createProjectHTTP(t, srv, t.TempDir())
	sess := createSessionForProjectHTTP(t, srv, proj.ID, wire.SessionPostureBuild)
	ctx := context.Background()

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
		testutil.FailErr(t, "unmarshal start run", err)
	}
	run = completePlanIntakeHTTP(t, h, run.ID)
	run = completePlanDepthAtNoneHTTP(t, h, run.ID, "research")
	current, err := h.WorkflowMgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get run before forcing approve", err)
	run = *current

	vars, err := h.WorkflowMgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	vars = runstate.SetHostVar(vars, "phase_skipped.review", true)
	run.CurrentPhase = "approve"
	testutil.FailErr(t, "CommitState", h.WorkflowMgr.Store.State.CommitState(ctx, &run, sess.WorkspacePath, vars))

	plan, err := h.BlueprintMgr.Get(ctx, run.ProjectID, run.BlueprintPath)
	testutil.FailErr(t, "Get plan before reject", err)
	approveURL := "/v1/projects/" + url.PathEscape(proj.ID) + "/blueprints/" + url.PathEscape(plan.ID) + "/approve"
	req = authedRequest(t, http.MethodPost, approveURL, strings.NewReader(blueprintApprovalJSON(t, run, plan.Content)))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("approve status = %d body = %s want 409", w.Code, w.Body.String())
	}
	var resp wire.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		testutil.FailErr(t, "unmarshal approve error", err)
	}
	if resp.Code != "human_approval_not_ready" {
		t.Fatalf("code = %q want human_approval_not_ready (body = %s)", resp.Code, w.Body.String())
	}

	plan, err = h.BlueprintMgr.Get(ctx, run.ProjectID, run.BlueprintPath)
	testutil.FailErr(t, "Get plan after reject", err)
	if plan.Status != wire.BlueprintStatusDraft {
		t.Fatalf("plan status = %q want draft", plan.Status)
	}
	active, err := h.WorkflowMgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get run after reject", err)
	if active.CurrentPhase != "approve" {
		t.Fatalf("phase = %q want approve", active.CurrentPhase)
	}
}

func TestApprovePlanHTTPPersistsBeforeAdvanceAndWakesCoordinator(t *testing.T) {
	h := wiring.BuildForTest(t, wiring.WithRecordingLLM())
	limits := &approvalWakeLimits{}
	h.SessionMgr.Limits.SetProvider(limits)
	srv := h.Server
	proj := createProjectHTTP(t, srv, h.ProjectDir(t, "approval-wake"))
	sess := createSessionForProjectHTTP(t, srv, proj.ID, wire.SessionPostureBuild)
	ctx := context.Background()

	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/workflow-runs", strings.NewReader(planStartBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("start status = %d body = %s", w.Code, w.Body.String())
	}
	var run wire.WorkflowRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		testutil.FailErr(t, "unmarshal start run", err)
	}
	h.SessionMgr.Coordinator.WaitForTurns(ctx)

	run = completePlanIntakeHTTP(t, h, run.ID)
	run = completePlanDepthAtNoneHTTP(t, h, run.ID, "research")
	seedPlanStub(t, h.BlueprintMgr, run.ProjectID, run.BlueprintPath)
	run = advancePlanRunHTTP(t, srv, run.ID)
	if run.CurrentPhase == "review" {
		run = completePlanDepthAtNoneHTTP(t, h, run.ID, "review")
	}
	if run.CurrentPhase != "approve" {
		t.Fatalf("phase = %q want approve", run.CurrentPhase)
	}

	originalAdvanceHook := h.WorkflowMgr.Approvals.OnHumanApprovalAdvanced
	var sawApprovalAdvance atomic.Bool
	h.WorkflowMgr.Approvals.OnHumanApprovalAdvanced = func(ctx context.Context, advanced *wire.WorkflowRun) {
		if advanced.ID == run.ID && advanced.CurrentPhase == "execute" {
			plan, err := h.BlueprintMgr.Get(ctx, run.ProjectID, run.BlueprintPath)
			testutil.FailErr(t, "Get blueprint during workflow advance", err)
			if plan.Status != wire.BlueprintStatusApproved {
				t.Fatalf("blueprint status during workflow advance = %q want approved", plan.Status)
			}
			sawApprovalAdvance.Store(true)
		}
		if originalAdvanceHook != nil {
			originalAdvanceHook(ctx, advanced)
		}
	}
	requestCountBeforeApproval := len(h.Recording.AllRequests())
	if requestCountBeforeApproval != 0 {
		t.Fatalf("fixture preparation started %d model requests before approval", requestCountBeforeApproval)
	}
	limits.enabled.Store(true)
	current, err := h.WorkflowMgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get run before approve", err)
	plan, err := h.BlueprintMgr.Get(ctx, current.ProjectID, current.BlueprintPath)
	testutil.FailErr(t, "Get plan before approve", err)
	approveURL := "/v1/projects/" + url.PathEscape(proj.ID) + "/blueprints/" + url.PathEscape(plan.ID) + "/approve"
	req = authedRequest(t, http.MethodPost, approveURL, strings.NewReader(blueprintApprovalJSON(t, *current, plan.Content)))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("approve status = %d body = %s", w.Code, w.Body.String())
	}
	testutil.WaitFor(t, 5*time.Second, sawApprovalAdvance.Load)

	if !testutil.WaitForNoFatal(promptIdleBudget, func() bool {
		return len(h.Recording.AllRequests()) > requestCountBeforeApproval
	}) {
		active, activeErr := h.WorkflowMgr.Store.Runs.ActiveBySession(ctx, sess.ID)
		testutil.FailErr(t, "get active run after approval wake", activeErr)
		if active == nil {
			t.Fatal("approval did not leave an active workflow")
		}
		vars, varsErr := h.WorkflowMgr.Store.Runs.GetScaffoldVars(ctx, active.ID)
		messages, messageErr := h.Store.GetMessages(ctx, sess.ID)
		allowed, reason, wakeErr := h.SessionMgr.Coordinator.Runtime.CoordinatorLoop().ShouldLoopWake(ctx, sess.ID, anchor.PhaseAdvanced)
		t.Fatalf("approval did not wake coordinator: allowed=%v reason=%q wake_err=%v active=%+v vars=%+v messages=%+v active_err=%v vars_err=%v message_err=%v",
			allowed, reason, wakeErr, active, vars, messages, activeErr, varsErr, messageErr)
	}
	h.SessionMgr.Coordinator.WaitForTurns(ctx)
	history, err := h.WorkflowMgr.Store.Runs.ListBySession(ctx, sess.ID, 100, nil)
	testutil.FailErr(t, "list workflows after approval", err)
	for _, child := range history {
		if child.ParentRunID != nil && *child.ParentRunID == run.ID && child.WorkflowID == "implement" {
			if child.Status != wire.WorkflowRunStatusRunning && child.Status != wire.WorkflowRunStatusComplete {
				t.Fatalf("approved implementation workflow status = %s", child.Status)
			}
			return
		}
	}
	t.Fatalf("approval did not start an implementation child: %+v", history)
}

// Fixture preparation drives phases directly. Only the approval under test may
// start the model, otherwise a mock turn races those writes and consumes wakes.
type approvalWakeLimits struct{ enabled atomic.Bool }

func (l *approvalWakeLimits) SessionLimits(string) settings.SessionLimits {
	limits := settings.DefaultSessionLimits()
	enabled := l.enabled.Load()
	limits.CoordinatorLoop = &enabled
	return limits
}

func blueprintApprovalJSON(t *testing.T, run wire.WorkflowRun, content string) string {
	t.Helper()
	raw, err := json.Marshal(wire.BlueprintApproveRequest{
		WorkflowRunID:    run.ID,
		ExpectedRevision: run.Revision,
		ContentDigest:    workflowdef.HashBlueprintContent(content),
	})
	testutil.FailErr(t, "marshal blueprint approval", err)
	return string(raw)
}
