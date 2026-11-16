package wiring

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestBuildForTestComposition(t *testing.T) {
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "custom-test",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "only"},
		},
	})
	h := BuildForTest(t, WithRecordingLLM(), WithManifestRegistry(map[string]workflowdef.Manifest{
		manifest.ID + "@" + manifest.Version: manifest,
	}))
	if h.Server == nil || h.Sessions.Manager == nil || h.Workflows.Manager == nil || h.DB == nil || h.ToolRegistry == nil {
		t.Fatalf("incomplete harness composition: %+v", h)
	}
	if h.Sessions.Manager.Coordinator.Guards.Policy() == nil {
		t.Fatal("rule engine required for posture-aware prompts")
	}
	if h.Recording == nil {
		t.Fatal("recording client required")
	}
	got, err := h.Workflows.Manager.Resolver.Overlay.Get(manifest.ID, manifest.Version)
	testutil.FailErr(t, "h.Workflows.Manager.Resolver.Overlay.Get failed", err)
	if got.ID != manifest.ID {
		t.Fatalf("manifest = %+v", got)
	}
}

func TestBuildForTestBackgroundWorkersCompleteJob(t *testing.T) {
	h := BuildForTest(t)
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)
	ctx := context.Background()
	dir := h.ProjectDir(t, "worker")
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, dir)
	testutil.FailErr(t, "CreateHarnessSession failed", err)
	h.SeedProgress(t, ctx, sess.ID)

	_, err = h.ToolRegistry.Run(
		ctx,
		"task",
		TaskToolArgs(orchestration.ProfileRepoResearcher, "inspect feature"),
		coordToolCtx(t, h, ctx, sess, dir),
	)
	testutil.FailErr(t, "task tool failed", err)

	testutil.WaitFor(t, func() bool {
		jobs, err := h.Workers.Queue.ListBySession(ctx, sess.ProjectID, sess.ID, wire.WorkerStatusComplete)
		if err != nil || len(jobs) != 1 {
			return false
		}
		pending, err := h.Workers.Queue.ListPendingOutcomes(ctx)
		return err == nil && len(pending) == 0
	})
}
