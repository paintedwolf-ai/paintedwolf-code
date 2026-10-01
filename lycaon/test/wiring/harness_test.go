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
	if h.Server == nil || h.SessionMgr == nil || h.WorkflowMgr == nil || h.DB == nil || h.ToolRegistry == nil {
		t.Fatalf("incomplete harness composition: %+v", h)
	}
	if h.SessionMgr.PromptToolPolicy() == nil {
		t.Fatal("rule engine required for posture-aware prompts")
	}
	if h.Recording == nil {
		t.Fatal("recording client required")
	}
	got, err := h.WorkflowMgr.Manifests.Get(manifest.ID, manifest.Version)
	testutil.FailErr(t, "h.WorkflowMgr.Manifests.Get failed", err)
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

	r, err := h.DelegationMgr.Create(ctx, wire.CreateDelegationRequest{
		ProjectID: testdbseed.DefaultProjectID,
		Task:      "implement feature",
		Strategy:  wire.HuntStrategyFileBased,
	})
	testutil.FailErr(t, "h.DelegationMgr.Create failed", err)
	if _, err := h.DelegationMgr.DispatchLeg(ctx, r.ID, r.Legs[0].ID, ""); err != nil {
		testutil.FailErr(t, "h.DelegationMgr.DispatchLeg failed", err)
	}

	testutil.WaitFor(t, 8*time.Second, func() bool {
		st, err := h.DelegationMgr.GetStatus(ctx, r.ID)
		return err == nil && st.Phase == wire.DelegationPhaseDone
	})
}
