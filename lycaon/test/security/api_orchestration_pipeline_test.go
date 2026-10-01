package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestStartBugbashViaOrchestrator(t *testing.T) {
	projectDir := t.TempDir()
	h := newOrchestrationPipelineServer(t, projectDir)

	run := startBugcommandRun(t, h)
	if run.CurrentPhase != "hunt" {
		t.Fatalf("phase = %q want hunt", run.CurrentPhase)
	}

	for _, stage := range []string{"hunt_correctness", "hunt_edges", "hunt_races"} {
		waitTopologyStageComplete(t, h.workflowMgr, run.ID, stage)
	}
	waitWorkflowPhase(t, h.workflowMgr, run.ID, "triage")
	waitTopologyStageComplete(t, h.workflowMgr, run.ID, "triage")
	blueprintPath := filepath.Join(h.projectDir, filepath.FromSlash(run.BlueprintPath))
	testutil.FailErr(t, "create Bugbash blueprint dir", os.MkdirAll(filepath.Dir(blueprintPath), 0o755))
	testutil.FailErr(t, "write Bugbash blueprint", os.WriteFile(blueprintPath, []byte(conditions.TestPlanContentStubOnly), 0o644))
	_, err := h.workflowMgr.TryAutoAdvanceThroughCommittedGates(t.Context(), run.ID, 8)
	testutil.FailErr(t, "advance Bugbash blueprint gate", err)
	waitWorkflowPhase(t, h.workflowMgr, run.ID, "approve")

	_, err = h.workflowMgr.SyncHumanApproval(h.ownerCtx, run.ID, h.projectDir)
	testutil.FailErr(t, "SyncHumanApproval approve", err)
	completeActiveChildRun(t, h)

	waitWorkflowPhase(t, h.workflowMgr, run.ID, "closeout")

	updated, err := h.workflowMgr.Get(t.Context(), run.ID)
	testutil.FailErr(t, "h.workflowMgr.Get failed", err)
	run = *updated
	if run.CurrentPhase != "closeout" {
		t.Fatalf("phase = %q want closeout after child completion", run.CurrentPhase)
	}

	var w *httptest.ResponseRecorder
	testutil.WaitFor(t, 3*time.Second, func() bool {
		req := authedRequest(t, http.MethodPost, "/v1/workflow-runs/"+run.ID+"/advance", workflowCommandBody(t, h.srv, run.ID, nil))
		w = httptest.NewRecorder()
		h.srv.ServeHTTP(w, req)
		return !strings.Contains(w.Body.String(), "workflow_revision_conflict")
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("advance before closeout gates status = %d body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "closeout_gates_passed") {
		t.Fatalf("expected closeout gate block, body = %s", w.Body.String())
	}
}

func TestPipelineWorkflowSSEOnPhaseChange(t *testing.T) {
	projectDir := t.TempDir()
	h := newOrchestrationPipelineServer(t, projectDir)

	ch, unsub, err := h.hub.Subscribe(t.Context(), events.Subscription{Project: h.sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "h.hub.Subscribe failed", err)
	defer unsub()

	run := startBugcommandRun(t, h)

	for _, stage := range []string{"hunt_correctness", "hunt_edges", "hunt_races"} {
		waitTopologyStageComplete(t, h.workflowMgr, run.ID, stage)
	}
	waitWorkflowPhase(t, h.workflowMgr, run.ID, "triage")

	testutil.WaitFor(t, 15*time.Second, func() bool {
		select {
		case envelope, ok := <-ch:
			if !ok {
				return false
			}
			if envelope.Topic != wire.EventTopicWorkflow {
				return false
			}
			var ev wire.WorkflowEvent
			if err := json.Unmarshal(envelope.Data, &ev); err != nil {
				testutil.FailErr(t, "unmarshal JSON document", err)
			}
			return ev.WorkflowRunID == run.ID &&
				ev.Event == "phase_advanced" &&
				ev.PreviousPhase == "hunt" &&
				ev.Phase == "triage"
		default:
			return false
		}
	})
}

func TestProductCatalogHidesNonVisibleBundledWorkflows(t *testing.T) {
	projectDir := t.TempDir()
	h := newOrchestrationPipelineServer(t, projectDir)

	req := authedRequest(t, http.MethodGet, "/v1/workflows", nil)
	w := httptest.NewRecorder()
	h.srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("catalog status = %d body = %s", w.Code, w.Body.String())
	}
	var listed wire.WorkflowListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	catalog := listed.Workflows
	// Workflows attached at session creation stay out of the catalog.
	hiddenBundled := map[string]bool{"implement": true}
	hasPlan, hasBugbash, hasOptions := false, false, false
	for _, row := range catalog {
		if row.Scope == wire.WorkflowScopeBundled && hiddenBundled[row.ID] {
			t.Fatalf("non-visible bundled workflow %q leaked into product catalog", row.ID)
		}
		switch row.ID {
		case "plan":
			hasPlan = true
		case "bugbash":
			hasBugbash = true
		case "options":
			hasOptions = true
		}
	}
	if !hasPlan {
		t.Fatalf("catalog missing plan: %+v", catalog)
	}
	if !hasBugbash || !hasOptions {
		t.Fatalf("catalog missing launch workflows: bugbash=%v options=%v", hasBugbash, hasOptions)
	}
}
