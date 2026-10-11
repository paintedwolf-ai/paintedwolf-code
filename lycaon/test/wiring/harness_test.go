package wiring

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testdbseed"
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
	testdbseed.InsertProjectRoot(t, h.DB, testdbseed.DefaultProjectID, dir)

	r, err := h.Delegations.Manager.Create(ctx, wire.CreateDelegationRequest{
		ProjectID: testdbseed.DefaultProjectID,
		Task:      "implement feature",
		Strategy:  wire.HuntStrategyFileBased,
	})
	testutil.FailErr(t, "h.Delegations.Manager.Create failed", err)
	if _, err := h.Delegations.Manager.DispatchLeg(ctx, r.ID, r.Legs[0].ID, ""); err != nil {
		testutil.FailErr(t, "h.Delegations.Manager.DispatchLeg failed", err)
	}

	testutil.WaitFor(t, 8*time.Second, func() bool {
		st, err := h.Delegations.Manager.GetStatus(ctx, r.ID)
		return err == nil && st.Phase == wire.DelegationPhaseDone
	})
}
