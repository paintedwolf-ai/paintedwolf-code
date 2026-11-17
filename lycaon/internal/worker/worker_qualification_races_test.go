package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/platform"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	sessiondecisions "github.com/lycaon/lycaon/internal/session/decisions"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workerlane"
	"github.com/lycaon/lycaon/internal/workerops"
	"github.com/lycaon/lycaon/internal/workerplan/plantest"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

// faultWorkspaces wraps workspace.Manager with deterministic gate and failure hooks.
type faultWorkspaces struct {
	inner     *workspace.Manager
	mu        sync.Mutex
	enterGate chan struct{}
	exitGate  chan struct{}
	failErr   error
	calls     atomic.Int64
	evicted   []string
}

func (w *faultWorkspaces) CreateWorkerBranch(ctx context.Context, roots []projectroot.RootRef, activeRootID, viewID string) (*workspace.Binding, workspace.SandboxLayout, error) {
	w.calls.Add(1)
	w.mu.Lock()
	enter, exit, fail := w.enterGate, w.exitGate, w.failErr
	w.enterGate, w.exitGate = nil, nil
	w.mu.Unlock()
	if enter != nil {
		close(enter)
	}
	if exit != nil {
		<-exit
	}
	if fail != nil {
		return nil, workspace.SandboxLayout{}, fail
	}
	return w.inner.CreateWorkerBranch(ctx, roots, activeRootID, viewID)
}

func (w *faultWorkspaces) CreateStackedWorkerBranch(ctx context.Context, roots []projectroot.RootRef, activeRootID, viewID, parentCheckpointID string) (*workspace.Binding, workspace.SandboxLayout, error) {
	w.calls.Add(1)
	w.mu.Lock()
	fail := w.failErr
	w.mu.Unlock()
	if fail != nil {
		return nil, workspace.SandboxLayout{}, fail
	}
	return w.inner.CreateStackedWorkerBranch(ctx, roots, activeRootID, viewID, parentCheckpointID)
}

func (w *faultWorkspaces) EvictWorkerBranch(ctx context.Context, branchRoot string) error {
	w.mu.Lock()
	w.evicted = append(w.evicted, branchRoot)
	w.mu.Unlock()
	return w.inner.EvictWorkerBranch(ctx, branchRoot)
}

type mutableLaneState struct {
	inner   workerlane.LaneState
	mu      sync.Mutex
	recover bool
	holders bool
}

func (s *mutableLaneState) PromotionUnsettled(ctx context.Context, jobID string) (bool, error) {
	return s.inner.PromotionUnsettled(ctx, jobID)
}

func (s *mutableLaneState) Readiness(ctx context.Context, root string) (bool, bool, error) {
	s.mu.Lock()
	rec, hold := s.recover, s.holders
	s.mu.Unlock()
	if rec || hold {
		return rec, hold, nil
	}
	return s.inner.Readiness(ctx, root)
}

type raceQualificationEnv struct {
	sqlDB      *db.Store
	primary    string
	roots      []projectroot.RootRef
	queue      *SQLQueue
	records    *workerrecords.Store
	decisions  sessiondecisions.Store
	workspaces *faultWorkspaces
	laneState  *mutableLaneState
	lanes      *WorkerLanes
	ops        *workerops.Service
	merge      *MergeService
	answer     *AnswerDecisionService
	shown      *shownOnce
	cancel     *CancelService
	reg        *tools.DefaultRegistry
	coord      workerrecords.Actor
}

func newRaceQualificationEnv(t *testing.T) *raceQualificationEnv {
	t.Helper()
	primary := t.TempDir()
	testutil.FailErr(t, "seed main.go", os.WriteFile(filepath.Join(primary, "main.go"), []byte("package main\n"), 0o644))
	sqlDB := testdbfixture.Open(t, "worker-qual-races.db")
	testdbseed.InsertProjectRootWithID(t, sqlDB, testdbseed.DefaultProjectID, "root-id", primary)
	testdbseed.InsertSession(t, sqlDB, "coord", testdbseed.DefaultProjectID)
	projects := project.NewSQLRegistry(sqlDB)

	outbox := eventoutbox.New(sqlDB, nil)
	queue := NewSQLQueue(sqlDB, 4)
	queue.SetEventOutbox(outbox)
	queue.SetProjectStore(projects)
	dataDir := testbaseline.DataDir(t, sqlDB)
	ledger := sourceledger.New(sqlDB, filepath.Join(dataDir, "source-content"))
	queue.SetBaselineStore(ledger.Baselines)

	mgr := workspace.NewManager(filepath.Join(dataDir, "worker-branches"), platform.NewSelector(platform.Config{}))
	rec, err := mgr.CheckpointCoordinator()
	testutil.FailErr(t, "open checkpoint coordinator", err)
	mgr.WithRecovery(rec)
	queue.SetWorkerWorkspaceManager(mgr)
	fw := &faultWorkspaces{inner: mgr}
	records := workerrecords.New(sqlDB)
	ls := &mutableLaneState{inner: workerlane.NewM14State(queue)}
	lanes := &WorkerLanes{Lanes: workerlane.New(fw, records), Records: records, State: ls, Checkpoints: rec}
	queue.SetWorkerLanes(lanes)

	decisions := sessiondecisions.NewSQL(sqlDB)
	ops := &workerops.Service{
		Reader: workerops.Reader{
			Records: records, Jobs: queue, Decisions: decisions,
			Readiness: func(ctx context.Context, actor workerrecords.Actor, workerID string) (bool, error) {
				prep, ok, err := records.LatestPreparation(ctx, actor, workerID)
				if err != nil || !ok {
					return false, err
				}
				rec, hold, err := ls.Readiness(ctx, prep.PreparationRef)
				return rec || hold, err
			},
		},
		Agents:     prompts.AgentMutationCapable,
		WakeParked: func(context.Context, string) error { return nil },
		Release:    queue.ReleaseAdmitted,
	}

	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint config", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	rejectFmt := guidance.NewStaticRejectFormatter(cfg)

	merge := &MergeService{
		Queue:        queue,
		Store:        queue,
		Projects:     projects,
		Workspace:    mgr,
		Reject:       rejectFmt,
		Names:        records,
		Sessions:     queue,
		SourceLedger: ledger,
		DataDir:      dataDir,
		Lanes:        lanes,
	}

	msgStore := sessionstore.NewSQL(sqlDB)
	msgStore.SetEventOutbox(outbox)
	shown := &shownOnce{taken: map[string]bool{}, hidden: map[string]bool{}}
	answer := &AnswerDecisionService{
		Queue: queue, Decisions: decisions, Resolver: NewSQLDecisionResolver(msgStore, queue),
		Reject: rejectFmt, Shown: shown,
	}
	cancel := &CancelService{Queue: queue, Reject: rejectFmt, Names: records}

	reg := tools.NewDefaultRegistry()
	agents := orchestration.NewMemoryAgentRegistryForTest()
	plan := plantest.New(t, plantest.Deps(records))
	testutil.FailErr(t, "register task", RegisterTaskTool(reg, TaskToolDeps{
		Queue: queue, Agents: agents, Workers: DefaultWorkersConfig(), WorkerRecords: records, Reviews: ops, Plan: plan,
	}))
	testutil.FailErr(t, "register worker tools", RegisterWorkerTools(reg, WorkerToolDeps{Ops: ops, Queue: queue, Workers: DefaultWorkersConfig(), Plan: plan}))
	testutil.FailErr(t, "register overlay tools", RegisterOverlayTools(reg, OverlayToolDeps{Merge: merge, Results: ops}))
	testutil.FailErr(t, "register answer tool", RegisterAnswerDecisionTool(reg, AnswerDecisionToolDeps{Answer: answer, Workers: ops}))
	testutil.FailErr(t, "register cancel tool", RegisterWorkerCancelTool(reg, CancelToolDeps{Cancel: cancel, Workers: ops}))

	return &raceQualificationEnv{
		sqlDB:      sqlDB,
		primary:    primary,
		roots:      []projectroot.RootRef{{ID: "root-id", Label: "primary", Path: primary, IsPrimary: true}},
		queue:      queue,
		records:    records,
		decisions:  decisions,
		workspaces: fw,
		laneState:  ls,
		lanes:      lanes,
		ops:        ops,
		merge:      merge,
		answer:     answer,
		shown:      shown,
		cancel:     cancel,
		reg:        reg,
		coord:      workerrecords.Actor{SessionID: "coord", Role: workerrecords.RoleCoordinator, PrincipalID: "coord"},
	}
}

func (e *raceQualificationEnv) invoke(t *testing.T, tool, callID string, args map[string]any) (string, *tools.ToolInvocationOut, error) {
	t.Helper()
	effects := &tools.ToolInvocationOut{}
	out, err := e.reg.Run(t.Context(), tool, args, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "coord", ProjectID: testdbseed.DefaultProjectID, ToolCallID: callID},
		Source:   tools.InvocationSource{ActiveRootID: "root-id", Roots: e.roots},
		Effects:  tools.InvocationEffects{Out: effects},
	})
	return out, effects, err
}

func (e *raceQualificationEnv) insertChildSession(t *testing.T, childID, agent string) {
	t.Helper()
	now := timelayout.Format(time.Now().UTC())
	_, err := e.sqlDB.ExecContext(t.Context(), `INSERT INTO sessions (id, project_id, owner_person_id, posture, status, parent_session_id, agent_type, created_at, activity_at, updated_at)
		VALUES (?, ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', 'coord', ?, ?, ?, ?)`, childID, testdbseed.DefaultProjectID, agent, now, now, now)
	testutil.FailErr(t, "insert child "+childID, err)
}

func (e *raceQualificationEnv) claimAndBranch(t *testing.T, jobID, childID string) *api.WorkerTask {
	t.Helper()
	claimed, err := e.queue.ClaimNext(t.Context(), ClaimRequest{ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "qual", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "claim "+jobID, err)
	if claimed == nil || claimed.ID != jobID {
		t.Fatalf("claimed = %+v, want %s", claimed, jobID)
	}
	if claimed.ChildSessionID == "" && childID != "" {
		testutil.FailErr(t, "bind child", e.queue.store.SetChildSessionID(t.Context(), jobID, childID))
		claimed.ChildSessionID = childID
	}
	branched, err := e.queue.ClaimWorkerBranch(t.Context(), jobID)
	testutil.FailErr(t, "claim branch "+jobID, err)
	branched.ClaimToken = claimed.ClaimToken
	return branched
}

func lanePrimaryDir(t *testing.T, branchRoot string) string {
	t.Helper()
	refs, err := workspace.BranchRootRefs(branchRoot)
	testutil.FailErr(t, "branch root refs", err)
	for _, r := range refs {
		if r.ID == "root-id" || r.IsPrimary {
			return r.Path
		}
	}
	if _, err := os.Stat(filepath.Join(branchRoot, "main.go")); err == nil {
		return branchRoot
	}
	t.Fatalf("primary root not found in %s", branchRoot)
	return ""
}

func TestSerializedPromotionAndActiveExecutionRaces(t *testing.T) {
	e := newRaceQualificationEnv(t)
	e.insertChildSession(t, "child-fixer", "implementer")

	_, effects1, err := e.invoke(t, "task", "start-fixer", map[string]any{
		"name": "fixer", "agent_type": "implementer", "scope": map[string]any{"mode": "write"},
		"brief": map[string]any{"goal": "Fix main.go", "done_when": []any{"main.go updated"}},
	})
	testutil.FailErr(t, "start fixer", err)
	job1 := e.claimAndBranch(t, effects1.Dispatch.WorkerID, "child-fixer")
	lane1Dir := lanePrimaryDir(t, job1.WorkspaceRoot)
	testutil.FailErr(t, "edit v1", os.WriteFile(filepath.Join(lane1Dir, "main.go"), []byte("package main\n// v1\n"), 0o644))

	won, err := e.queue.Complete(t.Context(), job1, api.WorkerResult{
		Status:           "complete",
		Summary:          "wrote v1",
		ChangeReport:     &api.WorkerChangeReport{ChangedPaths: []string{"main.go"}},
		CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"},
	})
	testutil.FailErr(t, "complete job 1", err)
	if !won {
		t.Fatal("job 1 completion lost claim")
	}
	names1, ok, err := e.records.NamesByJob(t.Context(), job1.ID)
	testutil.FailErr(t, "names job 1", err)
	if !ok || names1.Handle == "" {
		t.Fatalf("missing result handle for job 1: %+v", names1)
	}

	// promote_overlay enters first and pauses inside idle lane refresh;
	// concurrent instruct_worker waits on the worker lock and runs on generation 2.
	enterRefresh := make(chan struct{})
	exitRefresh := make(chan struct{})
	e.workspaces.mu.Lock()
	e.workspaces.enterGate = enterRefresh
	e.workspaces.exitGate = exitRefresh
	e.workspaces.mu.Unlock()

	var promoteErr error
	promoteDone := make(chan struct{})
	go func() {
		defer close(promoteDone)
		_, _, promoteErr = e.invoke(t, "promote_overlay", "promote-r1", map[string]any{"overlay_id": names1.Handle})
	}()
	select {
	case <-enterRefresh:
	case <-promoteDone:
		t.Fatalf("promote_overlay exited before refresh: %v", promoteErr)
	}

	instructDone := make(chan struct{})
	var instructOut string
	var instructEffects *tools.ToolInvocationOut
	var instructErr error
	go func() {
		defer close(instructDone)
		instructOut, instructEffects, instructErr = e.invoke(t, InstructWorkerTool, "instruct-r2", map[string]any{
			"worker": "fixer", "instructions": "now add v2",
		})
	}()

	select {
	case <-instructDone:
		t.Fatal("instruct_worker bypassed in-flight promote_overlay lock")
	default:
	}
	close(exitRefresh)
	<-promoteDone
	<-instructDone
	testutil.FailErr(t, "promote r1", promoteErr)
	testutil.FailErr(t, "instruct r2", instructErr)

	var receipt2 map[string]any
	testutil.FailErr(t, "decode r2", json.Unmarshal([]byte(instructOut), &receipt2))
	if receipt2["outcome"] != string(workerops.OutcomeStarted) || receipt2["round"] != float64(2) {
		t.Fatalf("round 2 receipt = %s", instructOut)
	}
	job2 := e.claimAndBranch(t, instructEffects.Dispatch.WorkerID, "")
	if job2.WorkspaceRoot == job1.WorkspaceRoot {
		t.Fatalf("round 2 reused pre-promotion lane %s", job2.WorkspaceRoot)
	}
	lane2Dir := lanePrimaryDir(t, job2.WorkspaceRoot)
	gotMain, err := os.ReadFile(filepath.Join(lane2Dir, "main.go"))
	testutil.FailErr(t, "read lane 2 main.go", err)
	if string(gotMain) != "package main\n// v1\n" {
		t.Fatalf("lane 2 main.go = %q, want landed v1", string(gotMain))
	}

	// Deliver R2, then admit R3 first: promoting or rejecting R2 is refused with active-execution conflict.
	testutil.FailErr(t, "edit v2", os.WriteFile(filepath.Join(lane2Dir, "main.go"), []byte("package main\n// v2\n"), 0o644))
	won, err = e.queue.Complete(t.Context(), job2, api.WorkerResult{
		Status:           "complete",
		Summary:          "wrote v2",
		ChangeReport:     &api.WorkerChangeReport{ChangedPaths: []string{"main.go"}},
		CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"},
	})
	testutil.FailErr(t, "complete job 2", err)
	if !won {
		t.Fatal("job 2 completion lost claim")
	}
	names2, _, err := e.records.NamesByJob(t.Context(), job2.ID)
	testutil.FailErr(t, "names job 2", err)

	_, _, err = e.invoke(t, InstructWorkerTool, "instruct-r3", map[string]any{
		"worker": "fixer", "instructions": "also add extra.go",
	})
	testutil.FailErr(t, "instruct r3", err)

	_, _, err = e.invoke(t, "promote_overlay", "promote-while-working", map[string]any{"overlay_id": names2.Handle})
	wantReject(t, err, "RESULT_CHANGES_NOT_ADMITTED")
	_, _, err = e.invoke(t, "reject_overlay", "reject-while-working", map[string]any{"overlay_id": names2.Handle})
	wantReject(t, err, "RESULT_CHANGES_NOT_ADMITTED")
	_, err = e.merge.PromoteOverlay(t.Context(), "coord", job2.ID, api.PromoteOverlayInput{})
	wantReject(t, err, OverlayPromoteNotPendingCode)
	_, err = e.merge.RejectOverlay(t.Context(), "coord", job2.ID, "discard")
	wantReject(t, err, OverlayRejectWorkerWorkingCode)
}

func TestRejectionResetAndConcurrentIdleInstructions(t *testing.T) {
	e := newRaceQualificationEnv(t)
	e.insertChildSession(t, "child-fixer", "implementer")

	_, effects1, err := e.invoke(t, "task", "start-fixer", map[string]any{
		"name": "fixer", "agent_type": "implementer", "scope": map[string]any{"mode": "write"},
		"brief": map[string]any{"goal": "Fix main.go", "done_when": []any{"main.go updated"}},
	})
	testutil.FailErr(t, "start fixer", err)
	job1 := e.claimAndBranch(t, effects1.Dispatch.WorkerID, "child-fixer")
	lane1Dir := lanePrimaryDir(t, job1.WorkspaceRoot)
	testutil.FailErr(t, "edit v1", os.WriteFile(filepath.Join(lane1Dir, "main.go"), []byte("package main\n// v1\n"), 0o644))
	won, err := e.queue.Complete(t.Context(), job1, api.WorkerResult{
		Status:           "complete",
		Summary:          "wrote v1",
		ChangeReport:     &api.WorkerChangeReport{ChangedPaths: []string{"main.go"}},
		CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"},
	})
	testutil.FailErr(t, "complete job 1", err)
	if !won {
		t.Fatal("job 1 completion lost claim")
	}
	names1, _, err := e.records.NamesByJob(t.Context(), job1.ID)
	testutil.FailErr(t, "names job 1", err)
	_, _, err = e.invoke(t, "promote_overlay", "promote-r1", map[string]any{"overlay_id": names1.Handle})
	testutil.FailErr(t, "promote r1", err)

	_, effects2, err := e.invoke(t, InstructWorkerTool, "instruct-r2", map[string]any{
		"worker": "fixer", "instructions": "write v2",
	})
	testutil.FailErr(t, "instruct r2", err)
	job2 := e.claimAndBranch(t, effects2.Dispatch.WorkerID, "")
	lane2Dir := lanePrimaryDir(t, job2.WorkspaceRoot)
	testutil.FailErr(t, "edit v2", os.WriteFile(filepath.Join(lane2Dir, "main.go"), []byte("package main\n// v2\n"), 0o644))
	won, err = e.queue.Complete(t.Context(), job2, api.WorkerResult{
		Status:           "complete",
		Summary:          "wrote v2",
		ChangeReport:     &api.WorkerChangeReport{ChangedPaths: []string{"main.go"}},
		CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"},
	})
	testutil.FailErr(t, "complete job 2", err)
	if !won {
		t.Fatal("job 2 completion lost claim")
	}

	_, effects3, err := e.invoke(t, InstructWorkerTool, "instruct-r3", map[string]any{
		"worker": "fixer", "instructions": "also add extra.go",
	})
	testutil.FailErr(t, "instruct r3", err)
	job3 := e.claimAndBranch(t, effects3.Dispatch.WorkerID, "")
	lane3Dir := lanePrimaryDir(t, job3.WorkspaceRoot)
	testutil.FailErr(t, "write extra.go", os.WriteFile(filepath.Join(lane3Dir, "extra.go"), []byte("package main\n"), 0o644))
	won, err = e.queue.Complete(t.Context(), job3, api.WorkerResult{
		Status:           "complete",
		Summary:          "added extra.go",
		ChangeReport:     &api.WorkerChangeReport{ChangedPaths: []string{"main.go", "extra.go"}},
		CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"},
	})
	testutil.FailErr(t, "complete job 3", err)
	if !won {
		t.Fatal("job 3 completion lost claim")
	}
	names3, _, err := e.records.NamesByJob(t.Context(), job3.ID)
	testutil.FailErr(t, "names job 3", err)

	_, _, err = e.invoke(t, "reject_overlay", "reject-r3", map[string]any{"overlay_id": names3.Handle, "reason": "start clean"})
	testutil.FailErr(t, "reject r3", err)

	var wg sync.WaitGroup
	outcomes := make([]string, 2)
	var r4Dispatch *api.WorkerDispatch
	var mu sync.Mutex
	for i, text := range []string{"fresh approach part A", "fresh approach part B"} {
		wg.Add(1)
		go func(idx int, body string) {
			defer wg.Done()
			raw, eff, callErr := e.invoke(t, InstructWorkerTool, "dual-"+string(rune('a'+idx)), map[string]any{
				"worker": "fixer", "instructions": body,
			})
			testutil.FailErr(t, "dual instruct", callErr)
			var parsed map[string]any
			testutil.FailErr(t, "decode dual", json.Unmarshal([]byte(raw), &parsed))
			mu.Lock()
			outcomes[idx], _ = parsed["outcome"].(string)
			if eff.Dispatch != nil {
				r4Dispatch = eff.Dispatch
			}
			mu.Unlock()
		}(i, text)
	}
	wg.Wait()
	if !((outcomes[0] == string(workerops.OutcomeStarted) && outcomes[1] == string(workerops.OutcomeQueued)) ||
		(outcomes[1] == string(workerops.OutcomeStarted) && outcomes[0] == string(workerops.OutcomeQueued))) {
		t.Fatalf("concurrent idle instructions outcomes = %v, want one started and one queued", outcomes)
	}

	job4 := e.claimAndBranch(t, r4Dispatch.WorkerID, "")
	lane4Dir := lanePrimaryDir(t, job4.WorkspaceRoot)
	if _, statErr := os.Stat(filepath.Join(lane4Dir, "extra.go")); !os.IsNotExist(statErr) {
		t.Fatalf("reset lane retained rejected extra.go: %v", statErr)
	}
	gotPrimaryMain, err := os.ReadFile(filepath.Join(e.primary, "main.go"))
	testutil.FailErr(t, "read primary main.go", err)
	if string(gotPrimaryMain) != "package main\n// v1\n" {
		t.Fatalf("primary main.go = %q, want landed v1", string(gotPrimaryMain))
	}
	gotResetMain, err := os.ReadFile(filepath.Join(lane4Dir, "main.go"))
	testutil.FailErr(t, "read reset main.go", err)
	if string(gotResetMain) != "package main\n// v2\n" {
		t.Fatalf("reset lane main.go = %q, want unrejected r2 v2", string(gotResetMain))
	}
}

func TestPromotionRefreshFailureHoldAndClosedWorkerCallback(t *testing.T) {
	e := newRaceQualificationEnv(t)
	e.insertChildSession(t, "child-lane", "implementer")

	_, eff1, err := e.invoke(t, "task", "start-lane", map[string]any{
		"name": "builder", "agent_type": "implementer", "scope": map[string]any{"mode": "write"},
		"brief": map[string]any{"goal": "Build feature", "done_when": []any{"feature built"}},
	})
	testutil.FailErr(t, "start builder", err)
	job1 := e.claimAndBranch(t, eff1.Dispatch.WorkerID, "child-lane")
	lane1Dir := lanePrimaryDir(t, job1.WorkspaceRoot)
	testutil.FailErr(t, "write feature.go", os.WriteFile(filepath.Join(lane1Dir, "feature.go"), []byte("package main\n"), 0o644))
	won, err := e.queue.Complete(t.Context(), job1, api.WorkerResult{
		Status:           "complete",
		Summary:          "added feature.go",
		ChangeReport:     &api.WorkerChangeReport{ChangedPaths: []string{"feature.go"}},
		CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"},
	})
	testutil.FailErr(t, "complete job 1", err)
	if !won {
		t.Fatal("complete lost claim")
	}

	// Inject workspace preparation failure during promote_overlay's idle refresh:
	// promotion still commits landed files, surfaces Error, and next round retries preparation only.
	e.workspaces.mu.Lock()
	e.workspaces.failErr = errors.New("disk full during refresh")
	e.workspaces.mu.Unlock()

	mergedOut, err := e.merge.PromoteOverlay(t.Context(), "coord", job1.ID, api.PromoteOverlayInput{})
	testutil.FailErr(t, "promote with refresh failure", err)
	if mergedOut.Status != api.WorkerMergeStatusMerged || mergedOut.Error == "" {
		t.Fatalf("merge result = %+v, want merged status with refresh error recorded", mergedOut)
	}
	if _, statErr := os.Stat(filepath.Join(e.primary, "feature.go")); statErr != nil {
		t.Fatalf("landed file missing on primary after refresh failure: %v", statErr)
	}

	// Unresolved writer custody / recovery hold blocks new round admission.
	e.laneState.mu.Lock()
	e.laneState.holders = true
	e.laneState.mu.Unlock()
	_, _, err = e.invoke(t, InstructWorkerTool, "instruct-held", map[string]any{
		"worker": "builder", "instructions": "continue while writer held",
	})
	wantReject(t, err, "INSTRUCT_WORKER_RECOVERING")

	// Clear writer hold; workspace fault still holds ClaimWorkerBranch without losing landed state;
	// clearing workspace fault lets ClaimWorkerBranch retry preparation only and succeed.
	e.laneState.mu.Lock()
	e.laneState.holders = false
	e.laneState.mu.Unlock()

	_, eff2, err := e.invoke(t, InstructWorkerTool, "instruct-retry", map[string]any{
		"worker": "builder", "instructions": "add tests for feature.go",
	})
	testutil.FailErr(t, "instruct r2", err)
	claimed2, err := e.queue.ClaimNext(t.Context(), ClaimRequest{ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "qual", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "claim r2", err)
	if _, branchErr := e.queue.ClaimWorkerBranch(t.Context(), claimed2.ID); branchErr == nil {
		t.Fatal("ClaimWorkerBranch succeeded while workspace fault remained active")
	}

	e.workspaces.mu.Lock()
	e.workspaces.failErr = nil
	e.workspaces.mu.Unlock()

	branched2, err := e.queue.ClaimWorkerBranch(t.Context(), eff2.Dispatch.WorkerID)
	testutil.FailErr(t, "retry branch preparation", err)
	if branched2.WorkspaceRoot == job1.WorkspaceRoot {
		t.Fatalf("retried preparation kept stale lane %s", branched2.WorkspaceRoot)
	}
	if _, statErr := os.Stat(filepath.Join(lanePrimaryDir(t, branched2.WorkspaceRoot), "feature.go")); statErr != nil {
		t.Fatalf("refreshed lane missing landed feature.go: %v", statErr)
	}

	// Late preparation callback on a closed worker is ignored and evicts the orphan branch.
	w, err := e.records.Resolve(t.Context(), e.coord, "builder")
	testutil.FailErr(t, "resolve builder", err)
	won, err = e.queue.Fail(t.Context(), &api.WorkerTask{ID: claimed2.ID, ClaimToken: claimed2.ClaimToken}, errors.New("session lost"))
	if err != nil || !won {
		t.Fatalf("fail r2: won=%v err=%v", won, err)
	}
	_, err = e.records.Close(t.Context(), e.coord, workerrecords.Op{ID: "close-builder"}, w.ID, "session_unusable")
	testutil.FailErr(t, "close builder", err)

	prepBefore, okBefore, err := e.records.LatestPreparation(t.Context(), e.coord, w.ID)
	testutil.FailErr(t, "latest prep before", err)
	_, refreshed, err := e.lanes.Lanes.RefreshIdle(t.Context(), workerlane.Request{
		Actor: e.coord, Op: workerrecords.Op{ID: "late-prep"}, WorkerID: w.ID,
		Facts: workerlane.Facts{Write: true, HasLane: true, LastChange: workerlane.ChangeLanded},
		Roots: e.roots, ActiveRootID: "root-id",
	})
	if err != nil || refreshed {
		t.Fatalf("late idle refresh on closed worker = (refreshed=%v, err=%v), want (false, nil)", refreshed, err)
	}
	_, admitErr := e.lanes.Lanes.Admit(t.Context(), workerlane.Request{
		Actor: e.coord, Op: workerrecords.Op{ID: "late-admit"}, WorkerID: w.ID,
		Facts: workerlane.Facts{Write: true, HasLane: true, LastChange: workerlane.ChangeLanded},
		Roots: e.roots, ActiveRootID: "root-id",
	})
	if !errors.Is(admitErr, workerlane.ErrClosed) {
		t.Fatalf("late admit on closed worker = %v, want ErrClosed", admitErr)
	}
	prepAfter, okAfter, err := e.records.LatestPreparation(t.Context(), e.coord, w.ID)
	testutil.FailErr(t, "latest prep after", err)
	if okAfter != okBefore || prepAfter != prepBefore {
		t.Fatalf("closed worker recorded late preparation: before=%+v after=%+v", prepBefore, prepAfter)
	}
}

func TestBlockingQuestionStopLineageAndOrdinaryPathContracts(t *testing.T) {
	e := newRaceQualificationEnv(t)
	e.insertChildSession(t, "child-asker", "implementer")
	e.insertChildSession(t, "child-sibling", "implementer")

	_, askEff, err := e.invoke(t, "task", "start-asker", map[string]any{
		"name": "asker", "agent_type": "implementer", "scope": map[string]any{"mode": "write"},
		"brief": map[string]any{"goal": "Check caller encoding", "done_when": []any{"encoding fixed"}},
	})
	testutil.FailErr(t, "start asker", err)
	_, sibEff, err := e.invoke(t, "task", "start-sibling", map[string]any{
		"name": "sibling", "agent_type": "implementer", "scope": map[string]any{"mode": "write"},
		"brief": map[string]any{"goal": "Independent fix", "done_when": []any{"done"}},
	})
	testutil.FailErr(t, "start sibling", err)

	askJob := e.claimAndBranch(t, askEff.Dispatch.WorkerID, "child-asker")
	sibJob := e.claimAndBranch(t, sibEff.Dispatch.WorkerID, "child-sibling")

	// Suspend askJob on a blocking question: round stays unsettled, zero terminal results fabricated.
	testutil.FailErr(t, "put decision", e.decisions.Put(t.Context(), api.WorkerDecisionRequest{
		ChildSessionID: "child-asker", WorkerID: askJob.ID, Question: "UTF-8 or Latin-1?",
	}))
	won, err := e.queue.Complete(t.Context(), askJob, api.WorkerResult{Status: "needs_decision", Summary: "Waiting for decision"})
	testutil.FailErr(t, "suspend askJob", err)
	if !won {
		t.Fatal("suspend lost claim")
	}
	round, ok, err := e.records.RoundByJob(t.Context(), askJob.ID)
	testutil.FailErr(t, "round by job", err)
	if !ok || round.SettledAt != "" || round.ResultID != "" {
		t.Fatalf("suspended round settled or fabricated result: %+v", round)
	}

	// Unshown question is refused with DECISION_NOT_SHOWN; instruct_worker while asking is refused.
	e.shown.hidden[askJob.ID] = true
	_, _, err = e.invoke(t, "answer_decision", "ans-hidden", map[string]any{"worker": "asker", "answer": "UTF-8"})
	wantReject(t, err, DecisionNotShownCode)
	_, _, err = e.invoke(t, InstructWorkerTool, "instruct-while-asking", map[string]any{"worker": "asker", "instructions": "guess UTF-8"})
	wantReject(t, err, "INSTRUCT_WORKER_ASKING")

	// Shown question resumes the same job; second answer in the same batch is refused.
	delete(e.shown.hidden, askJob.ID)
	_, _, err = e.invoke(t, "answer_decision", "ans-ok", map[string]any{"worker": "asker", "answer": "Use UTF-8 everywhere"})
	testutil.FailErr(t, "answer asking worker", err)
	_, _, err = e.invoke(t, "answer_decision", "ans-twice", map[string]any{"worker": "asker", "answer": "Actually Latin-1"})
	wantReject(t, err, DecisionNotFoundCode)

	resumed, ok := e.queue.Get(askJob.ID)
	if !ok || resumed.Status != api.WorkerStatusPending {
		t.Fatalf("resumed job = %+v, want same job pending again", resumed)
	}
	if n := countJobs(t, e.sqlDB); n != 2 {
		t.Fatalf("answer_decision created competing job: count=%d, want 2", n)
	}

	// Stopping asker cancels only asker's lineage; sibling's running job and lane stay untouched.
	_, _, err = e.invoke(t, "worker_cancel", "stop-asker", map[string]any{"worker": "asker", "reason": "no longer needed"})
	testutil.FailErr(t, "cancel asker", err)
	sibAfter, ok := e.queue.Get(sibJob.ID)
	if !ok || sibAfter.Status != api.WorkerStatusRunning {
		t.Fatalf("sibling disturbed by cancelling asker: %+v", sibAfter)
	}

	// Stale/unknown result handle returns structured recovery facts.
	_, _, err = e.invoke(t, "promote_overlay", "stale-handle", map[string]any{"overlay_id": "result-nonexistent"})
	wantReject(t, err, "RESULT_HANDLE_UNKNOWN")
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Data["overlay_id"] != "result-nonexistent" {
		t.Fatalf("unknown handle reject data = %+v", reject)
	}

	assertCollaborationSchemasExcludeInternalTokens(t)
}

func assertCollaborationSchemasExcludeInternalTokens(t *testing.T) {
	t.Helper()
	nativeCfg, err := nativemanifest.Load()
	testutil.FailErr(t, "load native manifest", err)
	schemas, err := extpacks.LoadEffectiveRegistrySchemas(extpackstest.StockCatalog(t))
	testutil.FailErr(t, "load stock schemas", err)
	collabTools := []string{
		"task", "instruct_worker", "answer_decision", "worker_cancel",
		"promote_overlay", "reject_overlay", "shelve_overlay", "preview_overlay",
		"message_coordinator", "request_decision", "complete_leg",
	}
	forbiddenParams := []string{"round_ordinal", "claim_token", "cursor", "page_token", "revision"}
	for _, name := range collabTools {
		entry, ok := schemas.Tools[name]
		if !ok {
			t.Fatalf("missing collaboration tool %s in stock catalog", name)
		}
		if nativeCfg.ApprovalReversibility[name] == "" || nativeCfg.Owner[name] == "" {
			t.Fatalf("tool %s missing single owner or approval reversibility", name)
		}
		raw, err := json.Marshal(entry.Schema)
		testutil.FailErr(t, "marshal params "+name, err)
		var params struct {
			Properties map[string]any `json:"properties"`
		}
		_ = json.Unmarshal(raw, &params)
		for _, forbidden := range forbiddenParams {
			if _, exists := params.Properties[forbidden]; exists {
				t.Fatalf("collaboration tool %s exposes internal protocol field %q", name, forbidden)
			}
		}
	}
}
