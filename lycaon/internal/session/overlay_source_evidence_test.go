package session_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/verification"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/session/workerworkspace"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

func overlaySourceFixture(t *testing.T) (primary, overlay string, scope api.TaskScope, baseline string) {
	t.Helper()
	primary = t.TempDir()
	overlay = filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-source")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(overlay, 0o755))
	testutil.FailErr(t, "WriteFile primary game", os.WriteFile(filepath.Join(primary, "game.py"), []byte("print(0)\n"), 0o644))
	testutil.FailErr(t, "WriteFile primary dirty", os.WriteFile(filepath.Join(primary, "dirty_upper.py"), []byte("x = 1\n"), 0o644))
	testutil.FailErr(t, "WriteFile overlay", os.WriteFile(filepath.Join(overlay, "game.py"), []byte("print(1)\n"), 0o644))
	meta := enginepaths.MetaDirForBranchRoot(overlay)
	testutil.FailErr(t, "MkdirAll meta", os.MkdirAll(meta, 0o700))
	testutil.FailErr(t, "WriteJobMeta", workspace.WriteJobMeta(meta, workspace.JobMeta{
		SnapshotComplete: true,
		Roots:            []workspace.JobMetaRoot{{ID: "primary", Path: primary, IsPrimary: true}},
	}))
	scope = api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"game.py", "dirty_upper.py"}}
	snap := testbaseline.Capture(t, primary)
	return primary, overlay, scope, snap
}

func currentVerifyPassedMessage(root string) api.Message {
	revision, digest := invocation.SourceRevisionForRoot(root)
	return api.Message{
		Role: api.MessageRoleTool,
		ToolResult: &api.ToolResult{
			Content:  `{"outcome":"passed"}`,
			ToolArgs: map[string]any{"command": "selected-check"},
			Invocation: &api.InvocationReceipt{
				ID: "receipt-verify", Tool: "verify", ToolCallID: "check-verify-1", Status: api.InvocationStatusCompleted,
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

func TestHostEndToEndWorkerWorkspaceObservationVerificationAndNextTurnNotice(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	mgr := session.NewHost(
		mem,
		session.Models{
			Client:   llm.NewMockProvider(&llm.MockConfig{}),
			Provider: nil,
			Limits:   settings.DefaultSessionLimits(),
			Cost:     nil,
		},
		tools.NewStubRegistry(),
	)
	dataDir := t.TempDir()
	mgr.Verification.SetDataDir(dataDir)
	mgr.Verification.SetEvidenceStore(inspector.NewJSONLStore(inspector.DefaultEvidenceDir))

	q := worker.NewInMemoryQueue(4)
	mgr.SetWorkerQueue(q)

	parent, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)

	primary, overlay, scope, baseline := overlaySourceFixture(t)
	rev, rootDigest := invocation.SourceRevisionForRoot(overlay)
	curRev := workercompletion.SourceRevision{Revision: rev, RootDigest: rootDigest}
	mgr.Verification.SetRevisionSource(func(context.Context, string) (string, string) {
		return rev, rootDigest
	})

	child, err := mem.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "CreateChild", err)
	child.WorkspacePath = primary
	child.ProjectID = testdbseed.DefaultProjectID

	jobID, err := q.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt:                "fixture",
		Brief:                 "fixture",
		ParentSessionID:       parent.ID,
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         primary,
		WorkspaceRoot:         overlay,
		ChildSessionID:        child.ID,
		AgentType:             "implementer",
		Status:                api.WorkerStatusRunning,
		Scope:                 &scope,
		WorkspaceBaselinePath: baseline,
	})
	testutil.FailErr(t, "EnqueueWithProjectID", err)
	task, ok := q.Get(jobID)
	if !ok || task == nil {
		t.Fatalf("expected task %s in queue", jobID)
	}

	workerCtx := workercontext.WithJob(ctx, jobID)
	tctx, err := mgr.Workers.Workspaces.Enrich(workerCtx, child, tools.ToolContext{})
	testutil.FailErr(t, "Workspaces.Enrich", err)

	tracker := mgr.Workers.Workspaces.TrackerFor(jobID)
	if tracker == nil {
		t.Fatal("expected Enrich to provision a scoped observation.Tracker on mgr.Workers.Workspaces")
	}

	// Observe a lower file through the workspace service and a cross-root dependency.
	if _, ok := mgr.Workers.Workspaces.ObserveReadPath(jobID, child.ID, primary, overlay, tctx.Source.Roots, "game.py"); !ok {
		t.Fatal("expected ObserveReadPath(game.py) to succeed")
	}
	depPath := layeredfs.QualifiedPath{RootID: "secondary", Rel: "pkg/dep.py"}
	tracker.RecordServerFSObservation(layeredfs.ObservationEvent{
		Path:   depPath,
		Layer:  layeredfs.LayerTrunk,
		Kind:   layeredfs.ObserveRead,
		Size:   10,
		SHA256: "sha-dep-v1",
	})

	// Perform a worker write on dirty_upper.py so it is pinned in LayerPrivate.
	testutil.FailErr(t, "BeforeWorkerWrite", mgr.Workers.Workspaces.BeforeWorkerWrite(workerCtx, tctx, "dirty_upper.py"))
	mgr.Workers.Workspaces.AfterWorkerWrite(workerCtx, tctx, "dirty_upper.py")

	// Record passing verification evidence on the host's Verification service.
	verifyMsg := currentVerifyPassedMessage(overlay)
	testutil.FailErr(t, "AppendMessages", mem.AppendMessages(ctx, child.ID, verifyMsg))
	mgr.Verification.RecordSourceRunTerminal(workerCtx, child, parent.ID, verification.ProducerVerify, tools.SourceRunCapture{
		CheckID:          "check-verify-1",
		IsCheck:          true,
		Command:          "selected-check",
		Cwd:              ".",
		ExitCode:         0,
		Verdict:          api.SourceVerdictPassed,
		SourceRevision:   rev,
		SourceRootDigest: rootDigest,
	}, time.Now().UTC())

	childHistory, err := mem.GetMessages(ctx, child.ID)
	testutil.FailErr(t, "GetMessages", err)

	// 1. Unobserved lower source change keeps GateState and OverlaySourceProof valid and emits no notice.
	mgr.Workers.Workspaces.RecordLowerTrunkChange(jobID, observation.TrunkChangeEvent{
		Path:      layeredfs.QualifiedPath{RootID: "primary", Rel: "unrelated.py"},
		Kind:      observation.TrunkWriteInPlace,
		NewSHA256: "sha-unrelated-v2",
		NewSize:   15,
	})
	if passed, _, _ := mgr.Verification.GateState(workerCtx, child, childHistory); !passed {
		t.Fatal("expected unobserved lower change to keep Verification.GateState passed")
	}
	proofFresh := session.OverlaySourceProofWithWorkspaces(workerCtx, mgr.Workers.Workspaces, task, childHistory, "selected-check", curRev)
	if status, _, _ := workercompletion.SourceEvidenceView(proofFresh); status != workercompletion.EvidenceStatusSatisfied {
		t.Fatalf("expected unobserved lower change to preserve satisfied OverlaySourceProof, got %s", status)
	}

	// 2. Lower source change on a dirty upper override (dirty_upper.py) stays pinned and immune.
	mgr.Workers.Workspaces.RecordLowerTrunkChange(jobID, observation.TrunkChangeEvent{
		Path:      layeredfs.QualifiedPath{RootID: "primary", Rel: "dirty_upper.py"},
		Kind:      observation.TrunkWriteInPlace,
		NewSHA256: "sha-lower-clobber",
		NewSize:   99,
	})
	if passed, _, _ := mgr.Verification.GateState(workerCtx, child, childHistory); !passed {
		t.Fatal("expected dirty upper override to remain immune to lower trunk change")
	}
	if assembled, err := mgr.Runner.Preparation.Build(workerCtx, child, child.ID, promptinput.Input{}, ""); err != nil || assembled.TrunkNotice != nil {
		t.Fatalf("expected no trunk.changed notice for unobserved/dirty-upper paths, got err=%v notice=%+v", err, assembled.TrunkNotice)
	}

	// 3. Lower source change on an observed/verified path invalidates GateState & OverlaySourceProof and queues trunk.changed for next turn.
	mgr.Workers.Workspaces.RecordLowerTrunkChange(jobID, observation.TrunkChangeEvent{
		Path:      depPath,
		Kind:      observation.TrunkAtomicReplace,
		NewSHA256: "sha-dep-v2",
		NewSize:   12,
	})
	proofStale := session.OverlaySourceProof(workerworkspace.WithService(workerCtx, mgr.Workers.Workspaces), task, childHistory, "selected-check", curRev)
	if status, _, _ := workercompletion.SourceEvidenceView(proofStale); status == workercompletion.EvidenceStatusSatisfied {
		t.Fatalf("expected observed dependency change to invalidate OverlaySourceProof receipt, got %s", status)
	}
	assessment, ok := session.OverlayObservationAssessmentWithWorkspaces(mgr.Workers.Workspaces, task)
	if !ok || !assessment.VerificationInvalidated || !assessment.Freshness.Stale || assessment.Freshness.StaleReason != observation.StaleReasonObservedDependencyChanged {
		t.Fatalf("unexpected overlay assessment: %+v (ok=%v)", assessment, ok)
	}
	if len(assessment.StaleSourcePaths) != 1 || assessment.StaleSourcePaths[0] != depPath {
		t.Fatalf("expected StaleSourcePaths=[%v], got %+v", depPath, assessment.StaleSourcePaths)
	}
	if passed, _, _ := mgr.Verification.GateState(workerCtx, child, childHistory); passed {
		t.Fatal("expected Verification.GateState to be invalidated after observed lower path changed")
	}

	// Next model turn preparation delivers trunk.changed once without interrupting in-flight calls.
	turn1, err := mgr.Runner.Preparation.Build(workerCtx, child, child.ID, promptinput.Input{}, "")
	testutil.FailErr(t, "Preparation.Build turn1", err)
	if turn1.TrunkNotice == nil || turn1.TrunkNotice.GrantsAuthority || turn1.TrunkNotice.EstablishesMergeBase {
		t.Fatalf("expected non-authoritative trunk.changed notice on turn 1, got %+v", turn1.TrunkNotice)
	}
	if len(turn1.Notices) != 1 || !strings.Contains(turn1.Notices[0], "trunk.changed") || !strings.Contains(turn1.Notices[0], "pkg/dep.py") {
		t.Fatalf("unexpected turn 1 notices: %+v", turn1.Notices)
	}
	turn2, err := mgr.Runner.Preparation.Build(workerCtx, child, child.ID, promptinput.Input{}, "")
	testutil.FailErr(t, "Preparation.Build turn2", err)
	if turn2.TrunkNotice != nil || len(turn2.Notices) != 0 {
		t.Fatalf("expected turn 2 not to duplicate consumed trunk.changed notice, got %+v", turn2.TrunkNotice)
	}

	// 4. Incomplete attribution refuses to certify freshness when scope generation advances.
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
	baseProof := workercompletion.BuildWorkerCompletionProof(childHistory, []string{"game.py"}, curRev)
	evalProof, evalAssess := session.EvaluateOverlaySourceObservations(baseProof, partialTracker, partialBinding, "ckpt-2")
	if !evalAssess.Freshness.Stale || evalAssess.Freshness.StaleReason != observation.StaleReasonIncompleteAttributionScopeAdvanced {
		t.Fatalf("expected incomplete attribution with advanced scope generation to be stale, got %+v", evalAssess.Freshness)
	}
	if status, _, _ := workercompletion.SourceEvidenceView(evalProof); status == workercompletion.EvidenceStatusSatisfied {
		t.Fatalf("expected incomplete attribution with advanced scope generation to invalidate proof receipt")
	}

	// 5. ReleaseReservations cleans up the scoped workspace tracker.
	testutil.FailErr(t, "ReleaseReservations", mgr.Workers.Workspaces.ReleaseReservations(ctx, parent.ID, jobID))
	if mgr.Workers.Workspaces.TrackerFor(jobID) != nil {
		t.Fatal("expected ReleaseReservations to clean up scoped tracker for jobID")
	}
}
