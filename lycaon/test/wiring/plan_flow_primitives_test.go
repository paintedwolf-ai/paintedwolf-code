package wiring

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func installPlanFlowPrimitivesFixture(t *testing.T, projectDir string) {
	t.Helper()
	root := configlayout.FindModuleRoot()
	src := filepath.Join(root, "config", "fixtures", "workflows", "plan-flow-primitives.yaml")
	data, err := os.ReadFile(src)
	testutil.FailErr(t, "read plan-flow-primitives fixture", err)
	// One layout everywhere: <overlay>/<name>/workflow.yaml. A bare *.yaml at
	// the overlay root is skipped without an error.
	dstDir := filepath.Join(projectDir, settingsoverlay.DirName(), "workflows", "plan-flow-primitives")
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		testutil.FailErr(t, "mkdir workflows overlay", err)
	}
	if err := os.WriteFile(filepath.Join(dstDir, "workflow.yaml"), data, 0o644); err != nil {
		testutil.FailErr(t, "write plan-flow-primitives overlay", err)
	}
	spec := `---
scope: medium
breaking: none
---

# Spec

Generic fixture artifact.
`
	planFlowSpecContent = spec
}

// planFlowSpecContent is the approval-gate frontmatter the run's blueprint needs.
// It is written to the blueprint the workflow creates, not pre-seeded: the store
// mints a unique sibling when a file already occupies the fixed path.
var planFlowSpecContent string

func writePlanFlowBlueprint(t *testing.T, projectDir, blueprintPath string) {
	t.Helper()
	abs := filepath.Join(projectDir, filepath.FromSlash(blueprintPath))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		testutil.FailErr(t, "mkdir blueprint dir", err)
	}
	if err := os.WriteFile(abs, []byte(planFlowSpecContent), 0o644); err != nil {
		testutil.FailErr(t, "write blueprint content", err)
	}
}

func TestPlanFlowPrimitivesWorkflow(t *testing.T) {
	h := BuildForTest(t)
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)

	ctx := h.OwnerCtx(t, context.Background())
	dir := h.ProjectDir(t, "plan-flow-primitives")
	installPlanFlowPrimitivesFixture(t, dir)

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureSpec}, dir)
	testutil.FailErr(t, "create session", err)
	h.Sessions.Manager.Catalog.InvalidateEffectiveCatalog(sess.ProjectID)

	run, err := h.Workflows.Manager.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "plan-flow-primitives", WorkflowVersion: "1.0.0", Request: "Plan the fixture change",
	})
	testutil.FailErr(t, "StartHuman", err)
	if run.CurrentPhase != "intake" {
		t.Fatalf("phase = %q want intake", run.CurrentPhase)
	}
	writePlanFlowBlueprint(t, dir, run.BlueprintPath)

	run, err = h.Workflows.Manager.Feedback.ResolveUserFeedback(ctx, sess.ID, run.ID, "intake", "small")
	testutil.FailErr(t, "ResolveUserFeedback intake", err)
	run, err = h.Workflows.Manager.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get after intake", err)
	if run.CurrentPhase != "review" {
		t.Fatalf("phase = %q want review after intake", run.CurrentPhase)
	}

	vars, err := h.Workflows.Manager.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if got, _ := runstate.DotPathString(vars, "intake.change_size"); got != "small" {
		t.Fatalf("intake.change_size = %q want small", got)
	}
	if got, _ := runstate.DotPathString(vars, "params.review_depth"); got != "light" {
		t.Fatalf("params.review_depth = %q want light", got)
	}

	vars = runstate.SetGateSatisfied(vars, "evidence_passed:peer_review", true)
	if err := h.Workflows.Manager.Store.State.UpdateVars(ctx, run, dir, vars); err != nil {
		testutil.FailErr(t, "UpsertScaffoldVars review evidence", err)
	}
	run, err = h.Workflows.Manager.Phases.TryAutoAdvance(ctx, run.ID)
	testutil.FailErr(t, "TryAutoAdvance after review", err)
	if run.CurrentPhase != "approve" {
		t.Fatalf("phase = %q want approve", run.CurrentPhase)
	}

	run, err = h.Workflows.Manager.Approvals.SyncHumanApproval(ctx, run.ID, dir)
	testutil.FailErr(t, "SyncHumanApproval", err)
	if run.CurrentPhase != "done" {
		t.Fatalf("phase = %q want done", run.CurrentPhase)
	}

	if run.Status != api.WorkflowRunStatusComplete {
		t.Fatalf("run status = %q want complete", run.Status)
	}
	blueprint, err := h.Workflows.Blueprints.Get(ctx, run.ProjectID, run.BlueprintPath)
	testutil.FailErr(t, "get completed workflow blueprint", err)
	if !strings.HasSuffix(blueprint.Path, "spec.md") {
		t.Fatalf("blueprint path = %q want …/spec.md", blueprint.Path)
	}
}
