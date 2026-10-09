package wiring

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func installDesignDocOverlay(t *testing.T, projectDir string) {
	t.Helper()
	src := filepath.Join(configlayout.FindModuleRoot(), "config", "fixtures", "workflows", "design-doc.yaml")
	data, err := os.ReadFile(src)
	testutil.FailErr(t, "read design-doc fixture", err)
	// Project workflows use <overlay>/<name>/workflow.yaml.
	dstDir := filepath.Join(projectDir, settingsoverlay.DirName(), "workflows", "design-doc")
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		testutil.FailErr(t, "mkdir project workflows", err)
	}
	if err := os.WriteFile(filepath.Join(dstDir, "workflow.yaml"), data, 0o644); err != nil {
		testutil.FailErr(t, "write design-doc overlay", err)
	}
}

func assertCoordinatorSurface(t *testing.T, h *Harness, ctx context.Context, sess *api.Session, userPrompt, wantSurface string) {
	t.Helper()
	runCtx, err := h.SessionMgr.Coordinator.Context.RunContext(ctx, sess.ID)
	testutil.FailErr(t, "CoordinatorRunContext", err)
	state := h.SessionMgr.Workers.State.ForSession(ctx, sess)
	profile := surface.ResolveTurnProfile(runCtx, sess, routingTurnHistory(nil, userPrompt), state)
	if profile.SurfaceID != wantSurface {
		t.Fatalf("surface = %q want %q (phase=%q workflow=%q)", profile.SurfaceID, wantSurface, runCtx.CurrentPhase, runCtx.WorkflowID)
	}
}

func assertManifestBoundSurface(t *testing.T, h *Harness, ctx context.Context, sess *api.Session, userPrompt, wantSurface string) {
	t.Helper()
	runCtx, err := h.SessionMgr.Coordinator.Context.RunContext(ctx, sess.ID)
	testutil.FailErr(t, "CoordinatorRunContext", err)
	if runCtx.PhaseCoordinatorSurface != wantSurface {
		t.Fatalf("phase_coordinator_surface = %q want %q (phase=%q)", runCtx.PhaseCoordinatorSurface, wantSurface, runCtx.CurrentPhase)
	}
	assertCoordinatorSurface(t, h, ctx, sess, userPrompt, wantSurface)
}

// TestCustomDesignDocWorkflowLiveGolden drives an overlay workflow through its child run.
func TestCustomDesignDocWorkflowLiveGolden(t *testing.T) {
	h := BuildForTest(t)
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)

	ctx := h.OwnerCtx(t, context.Background())
	dir := t.TempDir()
	installDesignDocOverlay(t, dir)

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{
		Posture: api.SessionPostureSpec,
	}, dir)
	testutil.FailErr(t, "create session", err)
	h.SessionMgr.Catalog.InvalidateEffectiveCatalog(sess.ProjectID)

	run, err := h.WorkflowMgr.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "design-doc", WorkflowVersion: "1.0.0", Request: "Design the REST API",
	})
	testutil.FailErr(t, "StartHuman design-doc", err)
	if run.CurrentPhase != "clarify" {
		t.Fatalf("phase = %q want clarify", run.CurrentPhase)
	}

	assertManifestBoundSurface(t, h, ctx, sess, "scope the API surface", "plan_stub")

	if _, err := h.WorkflowMgr.Feedback.ResolveUserFeedback(ctx, sess.ID, run.ID, "clarify", "REST API with OAuth2"); err != nil {
		testutil.FailErr(t, "ResolveUserFeedback clarify", err)
	}
	run, err = h.WorkflowMgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get run after clarify", err)
	if run.CurrentPhase != "approve" {
		t.Fatalf("phase = %q want approve after feedback", run.CurrentPhase)
	}

	assertManifestBoundSurface(t, h, ctx, sess, "review the approach", "plan_approve")

	if _, err := h.WorkflowMgr.Phases.Advance(ctx, run.ID); err == nil {
		t.Fatal("expected gate block before user decision on approve")
	}

	run, err = h.WorkflowMgr.Feedback.ResolveUserDecision(ctx, sess.ID, run.ID, "approve", []string{"approve"}, "")
	testutil.FailErr(t, "ResolveUserDecision approve", err)
	if run.CurrentPhase != "execute" {
		t.Fatalf("phase = %q want execute after approve", run.CurrentPhase)
	}

	parentID := run.ID
	child, err := h.WorkflowMgr.Store.Runs.ActiveBySession(ctx, sess.ID)
	testutil.FailErr(t, "GetActive child", err)
	if child == nil || child.WorkflowID != "implement" {
		t.Fatalf("active child = %+v want implement subroutine", child)
	}
	if child.ParentRunID == nil || *child.ParentRunID != parentID {
		t.Fatalf("child parent = %v want %q", child.ParentRunID, parentID)
	}
	waitWorkflowPhase(t, ctx, h.WorkflowMgr, child.ID, "work")

	assertCoordinatorSurface(t, h, ctx, sess, "map the codebase", tools.SurfaceImplementInvestigate)

	for range 4 {
		child, err = h.WorkflowMgr.Store.Runs.Get(ctx, child.ID)
		testutil.FailErr(t, "reload child run", err)
		now := time.Now().UTC()
		child.Status = api.WorkflowRunStatusComplete
		child.CompletedAt = &now
		child.UpdatedAt = now
		err = h.WorkflowMgr.Store.State.Update(ctx, child)
		if !errors.Is(err, runstate.ErrRevisionConflict) {
			break
		}
	}
	testutil.FailErr(t, "update child run", err)
	testutil.FailErr(t, "resume parent from child terminal", h.WorkflowMgr.Children.ReconcileTerminalRun(ctx, child))

	run, err = h.WorkflowMgr.Store.Runs.Get(ctx, parentID)
	testutil.FailErr(t, "Get parent after child", err)
	if run.Status != api.WorkflowRunStatusComplete {
		t.Fatalf("parent status = %q want complete", run.Status)
	}
	if run.CurrentPhase != "done" {
		t.Fatalf("parent phase = %q want done", run.CurrentPhase)
	}
}
