package security

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestWorkflowManifestModeTransitionsE2E(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	store := h.Store
	blueprintMgr := h.BlueprintMgr
	sess := createSessionHTTP(t, srv, t.TempDir())
	ctx := t.Context()
	_, err := store.Get(ctx, sess.ID)
	testutil.FailErr(t, "store.Get failed", err)

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
		t.Fatalf("session posture = %q", sessAfter.Posture)
	}
}

func TestWorkflowManifestGateBlockedE2E(t *testing.T) {
	gated := workflowdef.Manifest{
		ID:      "gated",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "verify", CompleteWhen: "gates_satisfied", Gates: []string{"evidence_passed:verify"}},
		},
	}
	gated.Phases = []string{"verify"}
	h := wiring.BuildForTest(t)
	h.RegisterManifest(gated)
	srv := h.Server
	projectDir := t.TempDir()
	sess := createSessionHTTP(t, srv, projectDir)

	run, err := h.WorkflowMgr.StartHuman(context.Background(), sess.ID, wire.StartWorkflowRunRequest{
		WorkflowID: "gated", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "h.WorkflowMgr.StartHuman failed", err)

	req := authedRequest(t, http.MethodPost, "/v1/workflow-runs/"+run.ID+"/advance", workflowCommandBody(t, srv, run.ID, nil))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("advance status = %d body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "phase_gate_unmet") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestWorkflowCoordinatorProfileOverridesPostureE2E(t *testing.T) {
	readonly := workflowdef.Manifest{
		ID:                 "readonly-coord",
		Version:            "1.0.0",
		InitialPosture:     "spec",
		CoordinatorProfile: "worker_readonly",
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "work", CompleteWhen: "always"},
		},
	}
	readonly.Phases = []string{"work"}
	h := wiring.BuildForTest(t)
	h.RegisterManifest(readonly)
	srv := h.Server
	mgr := h.SessionMgr

	projectDir := t.TempDir()
	sess := createSessionWithPostureHTTP(t, srv, projectDir, wire.SessionPostureSpec)
	ctx := t.Context()

	got, err := mgr.ResolvePromptToolProfile(ctx, sess.ID)
	testutil.FailErr(t, "mgr.ResolvePromptToolProfile failed", err)
	if got != "coordinator" {
		t.Fatalf("before workflow profile = %q want coordinator", got)
	}

	if _, err := h.WorkflowMgr.StartHuman(ctx, sess.ID, wire.StartWorkflowRunRequest{
		WorkflowID: "readonly-coord", WorkflowVersion: "1.0.0",
	}); err != nil {
		t.Fatal(err)
	}

	got, err = mgr.ResolvePromptToolProfile(ctx, sess.ID)
	testutil.FailErr(t, "mgr.ResolvePromptToolProfile failed", err)
	if got != "worker_readonly" {
		t.Fatalf("with manifest coordinator_profile profile = %q want worker_readonly", got)
	}
}
