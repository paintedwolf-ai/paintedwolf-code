package session_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/verification"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/session/workerworkspace"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHostEndToEndWorkerWorkspaceObservationVerificationAndNextTurnNotice(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	reg := tools.NewStubRegistry()
	readTool := &survey.ReadTool{}
	testutil.FailErr(t, "Register read tool", reg.Register(readTool.Name(), readTool.Run))

	mgr := session.NewHost(
		mem,
		session.Models{
			Client:   llm.NewMockProvider(&llm.MockConfig{}),
			Provider: nil,
			Limits:   settings.DefaultSessionLimits(),
			Cost:     nil,
		},
		reg,
	)
	t.Cleanup(mgr.BeginEngineShutdown)
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
		Prompt: "fixture", Brief: "fixture", ParentSessionID: parent.ID, ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: primary, WorkspaceRoot: overlay, ChildSessionID: child.ID,
		AgentType: "implementer", Status: api.WorkerStatusRunning, Scope: &scope, WorkspaceBaselinePath: baseline,
	})
	testutil.FailErr(t, "EnqueueWithProjectID", err)
	task, ok := q.Get(jobID)
	if !ok || task == nil {
		t.Fatalf("expected task %s in queue", jobID)
	}

	// Provision a second worker on an unrelated root to verify repochange root isolation.
	otherPrimary, otherOverlay, otherScope, otherBaseline := overlaySourceFixture(t)
	otherChild, err := mem.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "CreateChild other", err)
	otherChild.WorkspacePath = otherPrimary
	otherChild.ProjectID = testdbseed.DefaultProjectID
	otherJobID, err := q.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt: "other", Brief: "other", ParentSessionID: parent.ID, ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: otherPrimary, WorkspaceRoot: otherOverlay, ChildSessionID: otherChild.ID,
		AgentType: "implementer", Status: api.WorkerStatusRunning, Scope: &otherScope, WorkspaceBaselinePath: otherBaseline,
	})
	testutil.FailErr(t, "EnqueueWithProjectID other", err)
	otherTask, _ := q.Get(otherJobID)
	otherCtx := workercontext.WithJob(ctx, otherJobID)
	_, err = mgr.Workers.Workspaces.BindViewCapabilities(
		observation.MountedAdapterViewCapabilities(otherJobID, "fuselinux", true, true, true, observation.CompletenessComplete),
		otherJobID,
	)
	testutil.FailErr(t, "BindViewCapabilities other", err)
	otherTctx, err := mgr.Workers.Workspaces.Enrich(otherCtx, otherChild, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: otherChild.ID, WorkerJobID: otherJobID},
	})
	testutil.FailErr(t, "Workspaces.Enrich other", err)
	_, err = reg.Run(otherCtx, "read", map[string]any{"path": "pkg/dep.py"}, otherTctx)
	testutil.FailErr(t, "ordinary ReadTool other", err)
	otherVerifyMsg := currentVerifyPassedMessage(otherOverlay)
	testutil.FailErr(t, "AppendMessages other", mem.AppendMessages(ctx, otherChild.ID, otherVerifyMsg))
	mgr.Verification.RecordSourceRunTerminal(otherCtx, otherChild, parent.ID, verification.ProducerVerify, tools.SourceRunCapture{
		CheckID: "check-verify-other", IsCheck: true, Command: "selected-check", Cwd: ".",
		Verdict: api.SourceVerdictPassed, SourceRevision: rev, SourceRootDigest: rootDigest,
	}, time.Now().UTC())
	otherHistory, err := mem.GetMessages(ctx, otherChild.ID)
	testutil.FailErr(t, "GetMessages other", err)

	workerCtx := workercontext.WithJob(ctx, jobID)
	tctx, err := mgr.Workers.Workspaces.Enrich(workerCtx, child, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: child.ID, WorkerJobID: jobID},
	})
	testutil.FailErr(t, "Workspaces.Enrich", err)

	tracker := mgr.Workers.Workspaces.TrackerFor(jobID)
	if tracker == nil {
		t.Fatal("expected Enrich to provision a scoped observation.Tracker on mgr.Workers.Workspaces")
	}
	unboundFacts := tracker.Facts()
	if unboundFacts.Mode != observation.VisibilityModeUnknown || unboundFacts.LiveVisible || unboundFacts.SupportsInvalidation || unboundFacts.Completeness != observation.CompletenessUnknown {
		t.Fatalf("expected unbound tracker to default to conservative unknown visibility/completeness, got %+v", unboundFacts)
	}
	_, err = mgr.Workers.Workspaces.BindViewCapabilities(
		observation.MountedAdapterViewCapabilities(jobID, "fuselinux", true, true, true, observation.CompletenessComplete),
		jobID,
	)
	testutil.FailErr(t, "BindViewCapabilities job", err)

	// Execute ordinary ReadTool calls (without workerharness.Service.Read and without calling ObserveReadPath).
	_, err = reg.Run(workerCtx, "read", map[string]any{"path": "game.py"}, tctx)
	testutil.FailErr(t, "ordinary ReadTool game.py", err)
	_, err = reg.Run(workerCtx, "read", map[string]any{"path": "pkg/dep.py"}, tctx)
	testutil.FailErr(t, "ordinary ReadTool pkg/dep.py", err)

	depPath := layeredfs.QualifiedPath{RootID: "primary", Rel: "pkg/dep.py"}
	depObs, ok := tracker.LatestObservationForPath(depPath)
	if !ok || depObs.SHA256 != textfile.SHA256([]byte("dep = 1\n")) || depObs.Completeness != observation.CompletenessComplete || depObs.Layer != layeredfs.LayerTrunk {
		t.Fatalf("expected ordinary ReadTool to record complete structured observation for pkg/dep.py, got %+v (ok=%v)", depObs, ok)
	}

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

	// 1. Unobserved lower source change via real repochange.Notify keeps GateState and OverlaySourceProof valid.
	testutil.FailErr(t, "WriteFile unrelated", os.WriteFile(filepath.Join(primary, "unrelated.py"), []byte("u = 2\n"), 0o644))
	repochange.Notify(ctx, repochange.Event{
		ProjectDir: primary,
		Kind:       repochange.WorktreeChanged,
		Paths:      []string{"unrelated.py"},
		Source:     repochange.SourceWatcher,
	})
	if passed, _, _ := mgr.Verification.GateState(workerCtx, child, childHistory); !passed {
		t.Fatal("expected unobserved lower change to keep Verification.GateState passed")
	}
	proofFresh := session.OverlaySourceProofWithWorkspaces(workerCtx, mgr.Workers.Workspaces, task, childHistory, "selected-check", curRev)
	if status, _, _ := workercompletion.SourceEvidenceView(proofFresh); status != workercompletion.EvidenceStatusSatisfied {
		t.Fatalf("expected unobserved lower change to preserve satisfied OverlaySourceProof, got %s", status)
	}

	// 2. Lower source change on a dirty upper override (dirty_upper.py) via repochange.Notify stays pinned and immune.
	testutil.FailErr(t, "WriteFile dirty lower", os.WriteFile(filepath.Join(primary, "dirty_upper.py"), []byte("x = 999\n"), 0o644))
	repochange.Notify(ctx, repochange.Event{
		ProjectDir: primary,
		Kind:       repochange.WorktreeChanged,
		Paths:      []string{"dirty_upper.py"},
		Source:     repochange.SourceWatcher,
	})
	if passed, _, _ := mgr.Verification.GateState(workerCtx, child, childHistory); !passed {
		t.Fatal("expected dirty upper override to remain immune to lower trunk change")
	}
	if assembled, err := mgr.Runner.Preparation.Build(workerCtx, child, child.ID, promptinput.Input{}, ""); err != nil || assembled.TrunkNotice != nil {
		t.Fatalf("expected no trunk.changed notice for unobserved/dirty-upper paths, got err=%v notice=%+v", err, assembled.TrunkNotice)
	}

	// 3. Lower source change on observed pkg/dep.py via real repochange.Notify invalidates GateState & OverlaySourceProof and queues trunk.changed.
	testutil.FailErr(t, "WriteFile dep v2", os.WriteFile(filepath.Join(primary, "pkg", "dep.py"), []byte("dep = 2\n"), 0o644))
	repochange.Notify(ctx, repochange.Event{
		ProjectDir: primary,
		Kind:       repochange.WorktreeChanged,
		Changes:    []repochange.WorktreeChange{{Path: "pkg/dep.py", Kind: repochange.WorktreeChangeStructural}},
		Source:     repochange.SourceWatcher,
	})
	proofStale := session.OverlaySourceProof(workerworkspace.WithService(workerCtx, mgr.Workers.Workspaces), task, childHistory, "selected-check", curRev)
	if status, _, _ := workercompletion.SourceEvidenceView(proofStale); status == workercompletion.EvidenceStatusSatisfied {
		t.Fatalf("expected observed dependency change via repochange.Notify to invalidate OverlaySourceProof receipt, got %s", status)
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

	// Verify the second worker on otherPrimary was NOT invalidated or notified by events on primary.
	if passed, _, _ := mgr.Verification.GateState(otherCtx, otherChild, otherHistory); !passed {
		t.Fatal("expected worker on unrelated root to remain passed after repochange on primary")
	}
	if otherAssess, ok := session.OverlayObservationAssessmentWithWorkspaces(mgr.Workers.Workspaces, otherTask); !ok || otherAssess.VerificationInvalidated {
		t.Fatalf("expected worker on unrelated root to remain fresh, got %+v (ok=%v)", otherAssess, ok)
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

	// 4. Watcher overflow via real repochange.Notify (WorktreeChangeResync) marks matching root uncertain without affecting unrelated root.
	repochange.Notify(ctx, repochange.Event{
		ProjectDir: otherPrimary,
		Kind:       repochange.WorktreeChanged,
		Changes:    []repochange.WorktreeChange{{Path: ".", Kind: repochange.WorktreeChangeResync}},
		Source:     repochange.SourceWatcher,
	})
	if otherAssess, ok := session.OverlayObservationAssessmentWithWorkspaces(mgr.Workers.Workspaces, otherTask); !ok || !otherAssess.VerificationInvalidated || otherAssess.Freshness.StaleReason != observation.StaleReasonWatcherOverflowUncertain {
		t.Fatalf("expected watcher resync event on otherPrimary to mark otherTask stale with watcher_overflow_uncertain, got %+v (ok=%v)", otherAssess, ok)
	}

	// 5. ReleaseReservations cleans up the scoped workspace tracker.
	testutil.FailErr(t, "ReleaseReservations", mgr.Workers.Workspaces.ReleaseReservations(ctx, parent.ID, jobID))
	if mgr.Workers.Workspaces.TrackerFor(jobID) != nil {
		t.Fatal("expected ReleaseReservations to clean up scoped tracker for jobID")
	}
}

func TestWorkerReadAndWriteStructuredFactsWithoutSecondReadAndUnknownEvidence(t *testing.T) {
	svc := workerworkspace.New(nil, nil, nil)
	t.Cleanup(svc.Close)

	dir := t.TempDir()
	diskFile := filepath.Join(dir, "mutated.py")
	// Write mutated bytes on disk BEFORE calling ObserveReadWithFacts / RecordUpperWriteWithFacts
	// to prove neither method re-reads the file with os.ReadFile.
	testutil.FailErr(t, "WriteFile mutated", os.WriteFile(diskFile, []byte("disk-bytes-never-read-by-worker\n"), 0o644))

	// Before view capabilities are bound, a native read digest alone must not claim CompletenessComplete or LiveVisible.
	unboundObs, ok := svc.ObserveReadWithFacts("job-unbound", "sess-unbound", dir, "", nil, tools.WorkerFileObservationFacts{
		RootID:   "primary",
		RelPath:  "mutated.py",
		AbsPath:  diskFile,
		SHA256:   "sha-exact-from-read-stream",
		Size:     14,
		Complete: true,
	})
	if !ok || unboundObs.Completeness != observation.CompletenessUnknown {
		t.Fatalf("expected unbound tracker observation to report CompletenessUnknown despite native read digest, got %+v (ok=%v)", unboundObs, ok)
	}

	_, err := svc.BindViewCapabilities(
		observation.MountedAdapterViewCapabilities("job-facts", "fuselinux", true, true, true, observation.CompletenessComplete),
		"job-facts",
	)
	testutil.FailErr(t, "BindViewCapabilities job-facts", err)

	obs, ok := svc.ObserveReadWithFacts("job-facts", "sess-facts", dir, "", nil, tools.WorkerFileObservationFacts{
		RootID:   "primary",
		RelPath:  "mutated.py",
		AbsPath:  diskFile,
		SHA256:   "sha-exact-from-read-stream",
		Size:     14,
		Complete: true,
	})
	if !ok {
		t.Fatal("expected ObserveReadWithFacts to succeed")
	}
	if obs.SHA256 != "sha-exact-from-read-stream" || obs.Size != 14 || obs.Completeness != observation.CompletenessComplete || obs.Uncertain {
		t.Fatalf("expected observation to bind exact read-time facts without re-reading disk, got %+v", obs)
	}

	// RecordUpperWriteWithFacts binds exact write-time SHA256 and Size without re-reading disk,
	// and a subsequent RecordUpperWrite without facts preserves that digest.
	svc.RecordUpperWriteWithFacts("job-facts", "sess-facts", dir, "", nil, tools.WorkerFileObservationFacts{
		RootID:   "primary",
		RelPath:  "written.py",
		AbsPath:  diskFile,
		SHA256:   "sha-exact-from-write-commit",
		Size:     21,
		Layer:    "private",
		Complete: true,
	})
	svc.RecordUpperWrite("job-facts", "sess-facts", dir, "", nil, "written.py")
	tr := svc.TrackerFor("job-facts")
	writeObs, ok := tr.LatestObservationForPath(layeredfs.QualifiedPath{RootID: "primary", Rel: "written.py"})
	if !ok || writeObs.SHA256 != "sha-exact-from-write-commit" || writeObs.Size != 21 || writeObs.Layer != layeredfs.LayerPrivate {
		t.Fatalf("expected upper write observation to preserve exact write-time facts, got %+v (ok=%v)", writeObs, ok)
	}

	// When digest facts are unavailable (ObserveReadPath / Complete: false), explicit unknown/partial
	// evidence is preserved without reading the file from disk, and receipt freshness refuses to certify.
	unknownObs, ok := svc.ObserveReadPath("job-facts", "sess-facts", dir, "", nil, "mutated.py")
	if !ok {
		t.Fatal("expected ObserveReadPath to succeed")
	}
	if unknownObs.SHA256 != "" || unknownObs.Completeness != observation.CompletenessPartial || !unknownObs.Uncertain {
		t.Fatalf("expected unavailable digest facts to preserve explicit unknown/partial evidence, got %+v", unknownObs)
	}
	binding, ok := svc.BindVerificationReceipt("job-facts", "sess-facts", dir, "", "check-unknown", "ckpt-1", []string{"primary"})
	if !ok {
		t.Fatal("expected BindVerificationReceipt to succeed")
	}
	if binding.ReadAttributionCompleteness != observation.CompletenessPartial {
		t.Fatalf("expected binding completeness to downgrade to partial when observation lacks digest facts, got %s", binding.ReadAttributionCompleteness)
	}
	freshness := tr.EvaluateReceiptFreshness(binding, "ckpt-1")
	if !freshness.Stale {
		t.Fatalf("expected receipt bound to uncertain unknown-digest observation to refuse freshness certification, got %+v", freshness)
	}
}
