package wiring

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/usernotice"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Every tool the served registry claims at boot has a registered handler.
func TestBootToolClaimsHonestOnTheServedRegistry(t *testing.T) {
	h := BuildForTest(t)
	if h.ToolRegistry == nil || len(h.ToolRegistry.List()) == 0 {
		t.Fatal("served app has no registered tools")
	}
	testutil.FailErr(t, "validate boot tool claims", tools.ValidateBootToolClaimsHonest(h.ToolRegistry))
}

// A release has no configuration tree on disk, so boot installs the anchor
// catalog from the embedded configuration.
func TestBootInstallsBundledAnchorCatalogWithoutAConfigRoot(t *testing.T) {
	anchorcatalog.Clear()
	t.Chdir(t.TempDir())
	BuildForTest(t, withConfigRoot(t.TempDir()))
	if !anchorcatalog.Loaded() {
		t.Fatal("boot without a configuration tree installed no anchor catalog")
	}
	testutil.FailErr(t, "require a shipped anchor", anchorcatalog.Require(string(anchor.WorkerCloseout)))
}

// HTTP errors from the served app carry the shipped notice copy for their code.
func TestHTTPErrorsCarryCatalogNoticeCopy(t *testing.T) {
	h := BuildForTest(t)
	notices, err := usernotice.LoadNoticeDir(filepath.Join(configlayout.FindModuleRoot(), "config", "packs", "painted-wolf", "platform", "host", "user-notices"))
	testutil.FailErr(t, "load shipped notices", err)
	want := usernotice.NewCatalog(notices).RenderWire(string(wire.ApiErrorCodeSessionNotFound), nil)
	if want.Title == "" {
		t.Fatal("shipped catalog has no title for session_not_found")
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/sessions/"+uuid.NewString(), nil)
	req.Header.Set("Authorization", "Bearer "+api.TestAPIToken)
	w := httptest.NewRecorder()
	h.Server.ServeHTTP(w, req)
	var got wire.ErrorResponse
	testutil.FailErr(t, "decode error response", json.Unmarshal(w.Body.Bytes(), &got))
	if got.Code != wire.ApiErrorCodeSessionNotFound {
		t.Fatalf("status %d code = %q, want session_not_found", w.Code, got.Code)
	}
	if got.Title != want.Title || got.Message != want.Message || got.SuggestedAction != want.SuggestedAction {
		t.Fatalf("error copy = %+v, want the catalog copy %+v", got, want)
	}
}

// Stopping a workflow run settles its worker through the session transcript:
// the parent's dispatch row carries the canceled worker summary.
func TestRunStopSettlesWorkersThroughTheSessionTranscript(t *testing.T) {
	h := BuildForTest(t, WithoutCoordinatorLoop())
	ctx := h.OwnerCtx(t, context.Background())
	dir := h.ProjectDir(t, "run-stop")
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{Posture: wire.SessionPostureOrchestrate}, dir)
	testutil.FailErr(t, "create session", err)
	run, err := h.Workflows.Manager.Starts.StartHuman(ctx, sess.ID, wire.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: activeWorkflowVersion("plan"),
	})
	testutil.FailErr(t, "start workflow", err)

	jobID := uuid.NewString()
	_, err = h.Workers.Queue.EnqueueWithProjectID(ctx, sess.ProjectID, wire.WorkerTask{
		ID: jobID, Prompt: "fixture", Brief: "fixture", ParentSessionID: sess.ID, ProjectID: sess.ProjectID,
		WorkflowRunID: run.ID, AgentType: "implementer", Status: wire.WorkerStatusPending,
	})
	testutil.FailErr(t, "enqueue worker", err)
	requested, err := h.Workers.Queue.RequestCancellation(ctx, jobID)
	testutil.FailErr(t, "request cancellation", err)
	if !requested {
		t.Fatal("cancellation request rejected")
	}
	dispatchID := uuid.NewString()
	enqueue := `{"job_id":"` + jobID + `","status":"enqueued"}`
	testutil.FailErr(t, "seed dispatch row", h.Store.AppendMessages(ctx, sess.ID, wire.Message{
		ID: dispatchID, Role: wire.MessageRoleTool, Content: enqueue,
		ToolResult: &wire.ToolResult{Tool: "task", Content: enqueue, Dispatch: &wire.WorkerDispatch{WorkerID: jobID}},
	}))

	testutil.FailErr(t, "stop run workers", h.Workflows.Manager.Controls.Cleanup.Workers.CancelWorkersByRunID(ctx, run.ID, "workflow stopped"))

	msgs, err := h.Store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "read parent transcript", err)
	var dispatch *wire.Message
	for i := range msgs {
		if msgs[i].ID == dispatchID {
			dispatch = &msgs[i]
		}
	}
	if dispatch == nil || dispatch.WorkerSummary == nil || dispatch.WorkerSummary.Status != wire.WorkerSummaryStatusCanceled {
		t.Fatalf("dispatch row = %+v, want a canceled worker summary", dispatch)
	}
	task, ok := h.Workers.Queue.Get(jobID)
	if !ok || task.Status != wire.WorkerStatusCanceled {
		t.Fatalf("queued worker = %+v, want canceled", task)
	}
}
