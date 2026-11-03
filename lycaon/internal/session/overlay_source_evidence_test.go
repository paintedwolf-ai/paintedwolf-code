package session_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func overlaySourceFixture(t *testing.T) (primary, overlay string, scope api.TaskScope, baseline string) {
	t.Helper()
	primary = t.TempDir()
	overlay = filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-source")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(overlay, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "game.py"), []byte("print(1)\n"), 0o644))
	scope = api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"game.py"}}
	snap := testbaseline.Capture(t, primary)
	raw := snap
	return primary, overlay, scope, raw
}

func currentVerifyPassedMessage(root string) api.Message {
	revision, digest := invocation.SourceRevisionForRoot(root)
	return api.Message{
		Role: api.MessageRoleTool,
		ToolResult: &api.ToolResult{
			Content:  `{"outcome":"passed"}`,
			ToolArgs: map[string]any{"command": "selected-check"},
			Invocation: &api.InvocationReceipt{
				ID: "receipt-verify", Tool: "verify", Status: api.InvocationStatusCompleted,
				Evidence:       api.InvocationEvidence{Kind: "result", Ref: "message-1"},
				SourceRevision: revision, SourceRootDigest: digest,
				SourceVerdict: api.SourceVerdictPassed,
			},
		},
	}
}

func TestBuildImplementSessionStateListsLedgerPendingOverlays(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	mgr := session.NewHost(mem, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	q := worker.NewInMemoryQueue(4)
	mgr.SetWorkerQueue(q)
	parent, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)

	primary, overlay, scope, baseline := overlaySourceFixture(t)
	child, err := mem.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "CreateChild", err)
	testutil.FailErr(t, "AppendMessages", mem.AppendMessages(ctx, child.ID, currentVerifyPassedMessage(overlay)))

	uncheckedPrimary, uncheckedOverlay, uncheckedScope, uncheckedBaseline := overlaySourceFixture(t)
	uncheckedChild, err := mem.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "CreateChild unchecked", err)

	_, err = q.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ParentSessionID: parent.ID, ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: primary, WorkspaceRoot: overlay, ChildSessionID: child.ID,
		AgentType: "implementer", Status: api.WorkerStatusComplete,
		MergeStatus: api.WorkerMergeStatusPending, Scope: &scope, WorkspaceBaselinePath: baseline,
	})
	testutil.FailErr(t, "enqueue checked", err)
	_, err = q.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ParentSessionID: parent.ID, ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: uncheckedPrimary, WorkspaceRoot: uncheckedOverlay, ChildSessionID: uncheckedChild.ID,
		AgentType: "implementer", Status: api.WorkerStatusComplete,
		MergeStatus: api.WorkerMergeStatusPending, Scope: &uncheckedScope, WorkspaceBaselinePath: uncheckedBaseline,
	})
	testutil.FailErr(t, "enqueue unchecked", err)

	state := mgr.Workers.State.ForSession(ctx, parent)
	if len(state.PendingOverlayIDs) != 2 {
		t.Fatalf("PendingOverlayIDs = %v want both ledger-pending overlays", state.PendingOverlayIDs)
	}
}

func TestCompleteWriteWorkerThenHostCycleSelectsOverlayPromote(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	mgr := session.NewHost(mem, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	q := worker.NewInMemoryQueue(4)
	mgr.SetWorkerQueue(q)
	parent, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)

	primary, overlay, scope, baseline := overlaySourceFixture(t)
	jobID, err := q.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ParentSessionID: parent.ID, ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: primary, WorkspaceRoot: overlay,
		AgentType: "implementer", Status: api.WorkerStatusPending,
		Scope: &scope, WorkspaceBaselinePath: baseline,
	})
	testutil.FailErr(t, "enqueue worker", err)
	claimed, err := q.ClaimNext(ctx, worker.ClaimRequest{ClaimedBy: "test", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "claim worker", err)
	won, err := q.Complete(ctx, claimed, api.WorkerResult{Status: "partial", Summary: "unmet verify"})
	testutil.FailErr(t, "complete", err)
	if !won {
		t.Fatal("completion claim lost")
	}

	state := mgr.Workers.State.ForSession(ctx, parent)
	if len(state.PendingOverlayIDs) != 1 || state.PendingOverlayIDs[0] != jobID {
		t.Fatalf("PendingOverlayIDs = %v want [%s] after Complete", state.PendingOverlayIDs, jobID)
	}

	history := []api.Message{
		{
			Role:          api.MessageRoleAssistant,
			Content:       `<task job_id="` + jobID + `" agent_type="implementer" state="partial" merge_status="pending"><summary>unmet verify</summary></task>`,
			WorkerSummary: &api.WorkerSummaryMeta{WorkerID: jobID, Status: "partial"},
		},
		{
			Role:       api.MessageRoleUser,
			Origin:     api.MessageOriginHost,
			Visibility: api.MessageVisibilityInternal,
			Kind:       api.MessageKindHostLoopWake,
			Content:    surface.HostLoopWakeSentinel,
		},
	}
	profile := surface.ResolveTurnProfile(
		api.CoordinatorRunContext{},
		parent,
		history,
		state,
	)
	if profile.SurfaceID != surface.SurfaceImplementOverlayPromote {
		t.Fatalf("surface = %q want implement_overlay_promote after complete+pending", profile.SurfaceID)
	}
}

func TestOverlaySourceProofInvalidatesStaleObservedDependenciesAndIncompleteAttribution(t *testing.T) {
	ctx := context.Background()
	primary, overlay, scope, baseline := overlaySourceFixture(t)
	rev, rootDigest := invocation.SourceRevisionForRoot(overlay)
	curRev := workercompletion.SourceRevision{Revision: rev, RootDigest: rootDigest}
	msgs := []api.Message{currentVerifyPassedMessage(overlay)}

	task := &api.WorkerTask{
		ID:                    "job-obs-1665",
		WorkspacePath:         primary,
		WorkspaceRoot:         overlay,
		ChildSessionID:        "child-obs-1665",
		Scope:                 &scope,
		WorkspaceBaselinePath: baseline,
	}

	facts := observation.LiveMountVisibilityFacts("fuse-linux", true, true, true, observation.CompletenessComplete)
	tracker, err := observation.NewTracker(observation.TrackerConfig{ViewID: "view-overlay-1", Facts: facts})
	testutil.FailErr(t, "NewTracker", err)

	depPath := layeredfs.QualifiedPath{RootID: "secondary", Rel: "pkg/dep.py"}
	tracker.RecordServerFSObservation(layeredfs.ObservationEvent{
		Path:   depPath,
		Layer:  layeredfs.LayerTrunk,
		Kind:   layeredfs.ObserveRead,
		Size:   10,
		SHA256: "sha-dep-v1",
	})
	binding := tracker.BindVerificationReceipt("receipt-verify", "ckpt-1", []string{"primary", "secondary"})
	session.RegisterOverlayObservationTracker(task.ID, tracker, binding, "ckpt-1")
	t.Cleanup(func() {
		session.UnregisterOverlayObservationTracker(task.ID)
	})

	// 1. Unrelated trunk change outside complete observed set keeps proof satisfied.
	tracker.RecordTrunkChange(observation.TrunkChangeEvent{
		Path:      layeredfs.QualifiedPath{RootID: "primary", Rel: "unrelated.py"},
		Kind:      observation.TrunkWriteInPlace,
		NewSHA256: "sha-unrelated-v2",
		NewSize:   15,
	})
	proofFresh := session.OverlaySourceProof(ctx, task, msgs, "selected-check", curRev)
	if status, _, _ := workercompletion.SourceEvidenceView(proofFresh); status != workercompletion.EvidenceStatusSatisfied {
		t.Fatalf("expected unrelated trunk change to preserve satisfied proof, got %s", status)
	}

	// 2. Cross-root observed dependency change marks proof receipt stale.
	tracker.RecordTrunkChange(observation.TrunkChangeEvent{
		Path:      depPath,
		Kind:      observation.TrunkAtomicReplace,
		NewSHA256: "sha-dep-v2",
		NewSize:   12,
	})
	proofStale := session.OverlaySourceProof(ctx, task, msgs, "selected-check", curRev)
	if status, _, _ := workercompletion.SourceEvidenceView(proofStale); status == workercompletion.EvidenceStatusSatisfied {
		t.Fatalf("expected cross-root dependency change to invalidate proof receipt, got %s", status)
	}
	assessment, ok := session.OverlayObservationAssessmentForTask(task)
	if !ok || !assessment.Freshness.Stale || assessment.Freshness.StaleReason != observation.StaleReasonObservedDependencyChanged {
		t.Fatalf("unexpected overlay assessment: %+v (ok=%v)", assessment, ok)
	}
	if assessment.TrunkChanged == nil || assessment.TrunkChanged.GrantsAuthority || assessment.TrunkChanged.EstablishesMergeBase {
		t.Fatalf("unexpected trunk.changed notice: %+v", assessment.TrunkChanged)
	}
	if len(assessment.PreviewHints) == 0 || assessment.PreviewHints[0].SubstitutesExactBaseCheck {
		t.Fatalf("unexpected promotion preview hints: %+v", assessment.PreviewHints)
	}

	// 3. Incomplete attribution refuses to certify freshness when scope generation advances.
	partialFacts := observation.LiveMountVisibilityFacts("nfs-loopback", false, false, false, observation.CompletenessPartialKernelCached)
	partialTracker, err := observation.NewTracker(observation.TrackerConfig{ViewID: "view-overlay-2", Facts: partialFacts})
	testutil.FailErr(t, "NewTracker partial", err)
	partialTracker.RecordServerFSObservation(layeredfs.ObservationEvent{
		Path:   layeredfs.QualifiedPath{RootID: "primary", Rel: "game.py"},
		Layer:  layeredfs.LayerTrunk,
		Kind:   layeredfs.ObserveRead,
		Size:   9,
		SHA256: "sha-game-v1",
	})
	partialBinding := partialTracker.BindVerificationReceipt("receipt-verify", "ckpt-2", []string{"primary"})
	partialTracker.RecordTrunkChange(observation.TrunkChangeEvent{
		Path:      layeredfs.QualifiedPath{RootID: "primary", Rel: "unlogged_kernel_cached.py"},
		Kind:      observation.TrunkWriteInPlace,
		NewSHA256: "sha-unlogged-v2",
		NewSize:   20,
	})
	baseProof := workercompletion.BuildWorkerCompletionProof(msgs, []string{"game.py"}, curRev)
	evalProof, evalAssess := session.EvaluateOverlaySourceObservations(baseProof, partialTracker, partialBinding, "ckpt-2")
	if !evalAssess.Freshness.Stale || evalAssess.Freshness.StaleReason != observation.StaleReasonIncompleteAttributionScopeAdvanced {
		t.Fatalf("expected incomplete attribution with advanced scope generation to be stale, got %+v", evalAssess.Freshness)
	}
	if status, _, _ := workercompletion.SourceEvidenceView(evalProof); status == workercompletion.EvidenceStatusSatisfied {
		t.Fatalf("expected incomplete attribution with advanced scope generation to invalidate proof receipt")
	}
}
