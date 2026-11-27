package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/platform"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/orchestration/orchestrationtest"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	sessiondecisions "github.com/lycaon/lycaon/internal/session/decisions"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workerops"
	"github.com/lycaon/lycaon/internal/workerplan/plantest"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

type tb03RoundsFixture struct {
	db        *db.Store
	queue     *SQLQueue
	records   *workerrecords.Store
	decisions sessiondecisions.Store
	ops       *workerops.Service
	reg       *tools.DefaultRegistry
	coord     workerrecords.Actor
	rootDir   string
	dataDir   string
}

func tb03NewRoundsFixture(t *testing.T, maxRunning int) *tb03RoundsFixture {
	t.Helper()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "tb03-coord", testdbseed.DefaultProjectID)
	rootDir := t.TempDir()
	testdbseed.InsertProjectRootWithID(t, database, testdbseed.DefaultProjectID, "tb03-root", rootDir)

	dataDir := testbaseline.DataDir(t, database)
	branchesDir := filepath.Join(dataDir, "worker-branches")
	selector := platform.NewSelector(platform.Config{})
	selector.BudgetFor = func(class adapter.BudgetClass) adapter.BudgetFact {
		return adapter.BudgetFact{Class: class, Limit: 1 << 20, EntryCost: 1}
	}
	selector.Confinement = nil
	privateGit, err := workspace.OpenPrivateGit(branchesDir)
	testutil.FailErr(t, "tb03 open private git", err)
	recRoot := filepath.Join(branchesDir, ".adapter", "recovery")
	cpStore, err := checkpoint.NewStore(filepath.Join(recRoot, "checkpoints"))
	testutil.FailErr(t, "tb03 open checkpoint store", err)
	recCoord, err := recovery.NewCoordinator(recovery.CoordinatorConfig{
		RootDir: recRoot, CheckpointStore: cpStore, GitStore: privateGit.Store(), GitManager: privateGit,
	})
	testutil.FailErr(t, "tb03 open recovery coordinator", err)
	manager := workspace.NewManager(branchesDir, selector, workspace.Filesystem{}).WithRecovery(recCoord)

	services := NewSQLServices(database, maxRunning)
	services.Queue.Branches.SetBaselineStore(sourceledger.New(database, filepath.Join(dataDir, "source-content"), sourceledger.Timing{}).Baselines)
	services.Queue.Branches.SetWorkerWorkspaceManager(manager)

	records := workerrecords.New(database)
	decisions := sessiondecisions.NewSQL(database)
	ops := &workerops.Service{
		Reader:     workerops.Reader{Records: records, Jobs: services.Queue, Decisions: decisions},
		Agents:     prompts.AgentMutationCapable,
		WakeParked: func(context.Context, string) error { return nil },
		Release:    services.Queue.ReleaseAdmitted,
	}
	reg := tools.NewDefaultRegistry()
	plan := plantest.New(t, plantest.Deps(records))
	testutil.FailErr(t, "tb03 register task", RegisterTaskTool(reg, TaskToolDeps{
		Queue:         services.Queue,
		Agents:        orchestrationtest.Registry(t),
		Workers:       DefaultWorkersConfig(),
		WorkerRecords: records,
		Reviews:       ops,
		Plan:          plan,
	}))
	testutil.FailErr(t, "tb03 register worker tools", RegisterWorkerTools(reg, WorkerToolDeps{
		Ops:     ops,
		Queue:   services.Queue,
		Workers: DefaultWorkersConfig(),
		Plan:    plan,
	}))
	testutil.FailErr(t, "tb03 register worker_cancel", RegisterWorkerCancelTool(reg, CancelToolDeps{
		Cancel:  &CancelService{Queue: services.Queue},
		Workers: ops,
	}))
	resolver := NewSQLDecisionResolver(sessionstore.NewSQL(database), services.Queue)
	answers := &AnswerDecisionService{
		Queue:     services.Queue,
		Decisions: decisions,
		Resolver:  resolver,
		Shown:     &tb03ShownTracker{shown: map[string]bool{}},
	}
	testutil.FailErr(t, "tb03 register answer_decision", RegisterAnswerDecisionTool(reg, AnswerDecisionToolDeps{
		Answer:  answers,
		Workers: ops,
	}))

	return &tb03RoundsFixture{
		db:        database,
		queue:     services.Queue,
		records:   records,
		decisions: decisions,
		ops:       ops,
		reg:       reg,
		coord:     workerrecords.Actor{SessionID: "tb03-coord", Role: workerrecords.RoleCoordinator, PrincipalID: "tb03-coord"},
		rootDir:   rootDir,
		dataDir:   dataDir,
	}
}

type tb03ShownTracker struct {
	mu    sync.Mutex
	shown map[string]bool
}

func (s *tb03ShownTracker) Take(_ string, jobID string, _ api.WorkerDecisionRequest) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shown[jobID] {
		return false
	}
	s.shown[jobID] = true
	return true
}

func (s *tb03ShownTracker) Hide(_, jobID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.shown, jobID)
}

func (f *tb03RoundsFixture) insertChildSession(t *testing.T, childID, agentType string) {
	t.Helper()
	now := timelayout.Format(time.Now().UTC())
	_, err := f.db.ExecContext(t.Context(), `INSERT INTO sessions (id, project_id, owner_person_id, posture, status, parent_session_id, agent_type, created_at, activity_at, updated_at, root_session_id, mode)
		VALUES (?, ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', 'tb03-coord', ?, ?, ?, ?, 'tb03-coord', NULL)`,
		childID, testdbseed.DefaultProjectID, agentType, now, now, now)
	testutil.FailErr(t, "tb03 insert child session", err)
}

func (f *tb03RoundsFixture) runTool(t *testing.T, tool, callID string, args map[string]any, askMode bool) (string, *tools.ToolInvocationOut, error) {
	t.Helper()
	effects := &tools.ToolInvocationOut{}
	out, err := f.reg.Run(t.Context(), tool, args, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "tb03-coord", ProjectID: testdbseed.DefaultProjectID, ToolCallID: callID},
		Source: tools.InvocationSource{
			ActiveRootID: "tb03-root",
			Roots:        []projectroot.RootRef{{ID: "tb03-root", Label: "root", Path: f.rootDir, IsPrimary: true}},
			AskMode:      askMode,
		},
		Effects: tools.InvocationEffects{Out: effects},
	})
	return out, effects, err
}

func (f *tb03RoundsFixture) countJobs(t *testing.T) int {
	t.Helper()
	var n int
	testutil.FailErr(t, "tb03 count jobs", f.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM worker_jobs`).Scan(&n))
	return n
}

func (f *tb03RoundsFixture) settleJobWithCheckpoint(t *testing.T, jobID, checkpointID string) string {
	t.Helper()
	now := timelayout.Format(time.Now().UTC())
	resultID := "result-" + jobID
	report, _ := json.Marshal(api.WorkerResult{
		Status:           "complete",
		Summary:          "completed " + jobID,
		CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"},
	})
	_, err := f.db.ExecContext(t.Context(), `UPDATE worker_jobs SET status = 'complete', result_json = ?, completed_at = ? WHERE id = ?`, string(report), now, jobID)
	testutil.FailErr(t, "tb03 complete job", err)
	_, err = f.db.ExecContext(t.Context(), `INSERT INTO worker_results (id, worker_job_id, status, created_at) VALUES (?, ?, 'complete', ?)`, resultID, jobID, now)
	testutil.FailErr(t, "tb03 insert result", err)
	if checkpointID != "" {
		_, err = f.db.ExecContext(t.Context(), `UPDATE worker_jobs SET merge_status = 'pending' WHERE id = ?`, jobID)
		testutil.FailErr(t, "tb03 mark pending changes", err)
	}
	n, err := db.New(f.db).SettleWorkerRoundByJob(t.Context(), db.SettleWorkerRoundByJobParams{
		SettledAt: db.NullString(now), CheckpointID: checkpointID, JobID: jobID,
	})
	testutil.FailErr(t, "tb03 settle round", err)
	if n != 1 {
		t.Fatalf("settle %s touched %d rounds, want 1", jobID, n)
	}
	return resultID
}

func tb03Brief(goal string) map[string]any {
	return map[string]any{"goal": goal, "done_when": []any{"Return grounded results."}}
}

// ---------------------------------------------------------------------------
// W01 · Identity, Dispatch, Isolation, Scope Enforcement & Typed Refusals
// ---------------------------------------------------------------------------

func TestWEATB03_W01_01_DispatchIdentityAndIsolation(t *testing.T) {
	f := tb03NewRoundsFixture(t, 4)
	testutil.FailErr(t, "seed primary shared.txt", os.WriteFile(filepath.Join(f.rootDir, "shared.txt"), []byte("primary-base\n"), 0o644))

	// Dispatch two sibling write-scoped workers and one read-scoped worker.
	alphaArgs := map[string]any{
		"agent_type": "implementer", "name": "alpha",
		"scope": map[string]any{"mode": "write", "paths": []any{"."}},
		"brief": tb03Brief("edit alpha files"),
	}
	alphaOut, alphaEff, err := f.runTool(t, "task", "w01-alpha", alphaArgs, false)
	testutil.FailErr(t, "dispatch alpha", err)

	// Idempotent replay of the exact same ToolCallID must not enqueue a second job.
	alphaReplay, _, err := f.runTool(t, "task", "w01-alpha", alphaArgs, false)
	testutil.FailErr(t, "replay alpha", err)
	if alphaReplay != alphaOut || f.countJobs(t) != 1 {
		t.Fatalf("alpha replay mismatch: replay=%s first=%s jobs=%d", alphaReplay, alphaOut, f.countJobs(t))
	}

	var alphaReceipt map[string]any
	testutil.FailErr(t, "decode alpha receipt", json.Unmarshal([]byte(alphaOut), &alphaReceipt))
	if alphaReceipt["worker"] != "alpha" || alphaReceipt["workspace_kind"] != "private_snapshot" || alphaReceipt["sibling_files_live"] != false {
		t.Fatalf("alpha receipt = %+v", alphaReceipt)
	}

	betaOut, betaEff, err := f.runTool(t, "task", "w01-beta", map[string]any{
		"agent_type": "implementer", "name": "beta",
		"scope": map[string]any{"mode": "write", "paths": []any{"."}},
		"brief": tb03Brief("edit beta files"),
	}, false)
	testutil.FailErr(t, "dispatch beta", err)
	var betaReceipt map[string]any
	testutil.FailErr(t, "decode beta receipt", json.Unmarshal([]byte(betaOut), &betaReceipt))
	if betaReceipt["worker"] != "beta" || betaReceipt["workspace_kind"] != "private_snapshot" {
		t.Fatalf("beta receipt = %+v", betaReceipt)
	}

	readOut, _, err := f.runTool(t, "task", "w01-scout", map[string]any{
		"agent_type": "repo-researcher", "name": "scout",
		"scope": map[string]any{"mode": "read"},
		"brief": tb03Brief("survey repo"),
	}, false)
	testutil.FailErr(t, "dispatch scout", err)
	var readReceipt map[string]any
	testutil.FailErr(t, "decode scout receipt", json.Unmarshal([]byte(readOut), &readReceipt))
	if readReceipt["workspace_kind"] != "primary" || readReceipt["scope_mode"] != "read" {
		t.Fatalf("scout receipt = %+v, want primary/read", readReceipt)
	}

	// Identity is committed in workerrecords before any worker claims its job.
	for _, tc := range []struct {
		name  string
		jobID string
	}{
		{"alpha", alphaEff.Dispatch.WorkerID},
		{"beta", betaEff.Dispatch.WorkerID},
	} {
		w, err := f.records.Resolve(t.Context(), f.coord, tc.name)
		testutil.FailErr(t, "resolve "+tc.name, err)
		rounds, err := f.records.Rounds(t.Context(), f.coord, w.ID)
		testutil.FailErr(t, "rounds "+tc.name, err)
		if len(rounds) != 1 || rounds[0].JobID != tc.jobID {
			t.Fatalf("%s rounds = %+v, want round 1 job %s", tc.name, rounds, tc.jobID)
		}
	}

	// Claim both write workers and provision their isolated branch workspaces.
	claim1 := tb03ClaimNext(t, f.queue)
	claim2 := tb03ClaimNext(t, f.queue)
	if claim1.ID == claim2.ID {
		t.Fatalf("claimed duplicate job %s", claim1.ID)
	}
	alphaBranch, err := f.queue.Branches.ClaimWorkerBranch(t.Context(), alphaEff.Dispatch.WorkerID)
	testutil.FailErr(t, "claim alpha branch", err)
	betaBranch, err := f.queue.Branches.ClaimWorkerBranch(t.Context(), betaEff.Dispatch.WorkerID)
	testutil.FailErr(t, "claim beta branch", err)
	if alphaBranch.WorkspaceRoot == betaBranch.WorkspaceRoot {
		t.Fatalf("alpha and beta shared workspace root %s", alphaBranch.WorkspaceRoot)
	}

	// Each sibling writes distinct files and edits shared.txt; neither sibling nor primary sees unlanded bytes.
	testutil.FailErr(t, "alpha write", os.WriteFile(filepath.Join(alphaBranch.WorkspaceRoot, "alpha.txt"), []byte("from-alpha\n"), 0o644))
	testutil.FailErr(t, "alpha edit shared", os.WriteFile(filepath.Join(alphaBranch.WorkspaceRoot, "shared.txt"), []byte("alpha-edit\n"), 0o644))
	testutil.FailErr(t, "beta write", os.WriteFile(filepath.Join(betaBranch.WorkspaceRoot, "beta.txt"), []byte("from-beta\n"), 0o644))

	if _, err := os.Stat(filepath.Join(alphaBranch.WorkspaceRoot, "beta.txt")); !os.IsNotExist(err) {
		t.Fatalf("alpha saw beta's unlanded file: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(betaBranch.WorkspaceRoot, "alpha.txt")); !os.IsNotExist(err) {
		t.Fatalf("beta saw alpha's unlanded file: err=%v", err)
	}
	betaShared, err := os.ReadFile(filepath.Join(betaBranch.WorkspaceRoot, "shared.txt"))
	testutil.FailErr(t, "read beta shared.txt", err)
	if string(betaShared) != "primary-base\n" {
		t.Fatalf("beta shared.txt = %q, want primary-base\\n", string(betaShared))
	}
	primaryShared, err := os.ReadFile(filepath.Join(f.rootDir, "shared.txt"))
	testutil.FailErr(t, "read primary shared.txt", err)
	if string(primaryShared) != "primary-base\n" {
		t.Fatalf("primary shared.txt mutated to %q before landing", string(primaryShared))
	}
	if _, err := os.Stat(filepath.Join(f.rootDir, "alpha.txt")); !os.IsNotExist(err) {
		t.Fatalf("primary saw alpha's unlanded file: err=%v", err)
	}
}

func TestWEATB03_W01_02_DispatchTypedRefusalsAndScopeEnforcement(t *testing.T) {
	f := tb03NewRoundsFixture(t, 4)

	// 1. Unknown agent_type -> WORKER_TYPE_UNAVAILABLE.
	_, _, err := f.runTool(t, "task", "w01-bad-agent", map[string]any{
		"agent_type": "nonexistent-agent-type",
		"name":       "ghost",
		"brief":      tb03Brief("impossible"),
	}, false)
	tb03AssertRejectCode(t, err, "WORKER_TYPE_UNAVAILABLE")

	// 2. Missing agent_type on fresh dispatch -> TOOL_ARGS_INVALID.
	_, _, err = f.runTool(t, "task", "w01-no-agent", map[string]any{
		"name":  "nameless",
		"brief": tb03Brief("missing agent"),
	}, false)
	tb03AssertRejectCode(t, err, "TOOL_ARGS_INVALID")

	// 3. Ask mode refuses write-scoped dispatch, admits read-scoped dispatch.
	_, _, err = f.runTool(t, "task", "w01-ask-write", map[string]any{
		"agent_type": "implementer",
		"name":       "ask-writer",
		"scope":      map[string]any{"mode": "write", "paths": []any{"."}},
		"brief":      tb03Brief("write in ask mode"),
	}, true)
	if err == nil {
		t.Fatal("expected write-scoped task in ask mode to be refused")
	}

	_, _, err = f.runTool(t, "task", "w01-ask-read", map[string]any{
		"agent_type": "repo-researcher",
		"name":       "ask-reader",
		"scope":      map[string]any{"mode": "read"},
		"brief":      tb03Brief("read in ask mode"),
	}, true)
	testutil.FailErr(t, "read-scoped task in ask mode", err)
	if f.countJobs(t) != 1 {
		t.Fatalf("expected 1 enqueued job after refusals + 1 valid read task, got %d", f.countJobs(t))
	}

	// 4. Duplicate worker name -> TASK_WORKER_NAME_TAKEN (no extra job enqueued).
	_, _, err = f.runTool(t, "task", "w01-dup-name", map[string]any{
		"agent_type": "repo-researcher",
		"name":       "ask-reader",
		"scope":      map[string]any{"mode": "read"},
		"brief":      tb03Brief("duplicate name"),
	}, false)
	tb03AssertRejectCode(t, err, TaskWorkerNameTakenCode)
	if f.countJobs(t) != 1 {
		t.Fatalf("duplicate worker name enqueued extra job: count=%d", f.countJobs(t))
	}
}

// ---------------------------------------------------------------------------
// W05 · Multi-Round Reuse, Sequencing, History & State-Gated Refusals
// ---------------------------------------------------------------------------

func TestWEATB03_W05_01_MultiRoundReuseSequencingAndHistory(t *testing.T) {
	f := tb03NewRoundsFixture(t, 2)
	f.insertChildSession(t, "tb03-w05-child", "repo-researcher")

	// Start round 1 via task tool.
	_, eff1, err := f.runTool(t, "task", "w05-start", map[string]any{
		"agent_type": "repo-researcher",
		"name":       "investigator",
		"scope":      map[string]any{"mode": "read"},
		"brief":      tb03Brief("locate bug"),
	}, false)
	testutil.FailErr(t, "dispatch round 1", err)
	job1ID := eff1.Dispatch.WorkerID
	testutil.FailErr(t, "bind child session", f.queue.SetChildSessionID(t.Context(), job1ID, "tb03-w05-child"))

	// Claim and complete round 1 through SQLQueue so round 1 settles with its own result handle.
	claimed1 := tb03ClaimNext(t, f.queue)
	if claimed1.ID != job1ID {
		t.Fatalf("claimed1=%s want %s", claimed1.ID, job1ID)
	}
	won, err := f.queue.Complete(t.Context(), claimed1, api.WorkerResult{
		Status:           "complete",
		Summary:          "found root cause in parser",
		CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"},
	})
	testutil.FailErr(t, "complete round 1", err)
	if !won {
		t.Fatal("round 1 completion did not commit")
	}

	// Start round 2 on the SAME worker via instruct_worker, upgrading agent_type to implementer.
	r2Args := map[string]any{
		"worker":       "investigator",
		"instructions": "implement the parser fix",
		"agent_type":   "implementer",
	}
	r2Out, eff2, err := f.runTool(t, InstructWorkerTool, "w05-r2", r2Args, false)
	testutil.FailErr(t, "instruct round 2", err)
	var r2Receipt map[string]any
	testutil.FailErr(t, "decode r2 receipt", json.Unmarshal([]byte(r2Out), &r2Receipt))
	if r2Receipt["outcome"] != string(workerops.OutcomeStarted) || r2Receipt["round"] != float64(2) {
		t.Fatalf("r2 receipt = %s", r2Out)
	}
	if eff2.Dispatch == nil || eff2.Dispatch.ChildSessionID != "tb03-w05-child" || eff2.Dispatch.AgentType != "implementer" {
		t.Fatalf("r2 dispatch = %+v, want reused child session + implementer", eff2.Dispatch)
	}

	// Idempotent replay of instruct_worker enqueues nothing new.
	r2Replay, _, err := f.runTool(t, InstructWorkerTool, "w05-r2", r2Args, false)
	testutil.FailErr(t, "replay r2", err)
	if r2Replay != r2Out || f.countJobs(t) != 2 {
		t.Fatalf("r2 replay mismatch: jobs=%d", f.countJobs(t))
	}

	// Claim and complete round 2.
	claimed2 := tb03ClaimNext(t, f.queue)
	if claimed2.ID != eff2.Dispatch.WorkerID || claimed2.ChildSessionID != "tb03-w05-child" {
		t.Fatalf("claimed2 = %+v", claimed2)
	}
	won, err = f.queue.Complete(t.Context(), claimed2, api.WorkerResult{
		Status:           "complete",
		Summary:          "implemented parser fix",
		CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"},
	})
	testutil.FailErr(t, "complete round 2", err)
	if !won {
		t.Fatal("round 2 completion did not commit")
	}

	// worker_history returns both settled rounds with distinct result handles and the coordinator instruction.
	histOut, _, err := f.runTool(t, WorkerHistoryTool, "w05-hist", map[string]any{"worker": "investigator"}, false)
	testutil.FailErr(t, "worker_history", err)
	var hist struct {
		State    string           `json:"state"`
		Rounds   []map[string]any `json:"rounds"`
		Messages []map[string]any `json:"messages"`
	}
	testutil.FailErr(t, "decode history", json.Unmarshal([]byte(histOut), &hist))
	if len(hist.Rounds) != 2 {
		t.Fatalf("history rounds = %d, want 2 (%s)", len(hist.Rounds), histOut)
	}
	h1, _ := hist.Rounds[0]["overlay_id"].(string)
	h2, _ := hist.Rounds[1]["overlay_id"].(string)
	if h1 == "" || h2 == "" || h1 == h2 {
		t.Fatalf("expected 2 distinct result handles, got h1=%q h2=%q", h1, h2)
	}
	if got1, ok := f.queue.LookupByResult(t.Context(), h1); !ok || got1.ID != job1ID {
		t.Fatalf("LookupByResult(%q) = (%+v, %v), want %s", h1, got1, ok, job1ID)
	}
	if got2, ok := f.queue.LookupByResult(t.Context(), h2); !ok || got2.ID != eff2.Dispatch.WorkerID {
		t.Fatalf("LookupByResult(%q) = (%+v, %v), want %s", h2, got2, ok, eff2.Dispatch.WorkerID)
	}
	if len(hist.Messages) != 1 || hist.Messages[0]["from"] != "coordinator" || hist.Messages[0]["text"] != "implement the parser fix" {
		t.Fatalf("history messages = %+v", hist.Messages)
	}
}

func TestWEATB03_W05_02_InstructWorkerStateGatedRefusals(t *testing.T) {
	t.Run("WORKER_NAME_UNKNOWN", func(t *testing.T) {
		f := tb03NewRoundsFixture(t, 1)
		_, _, err := f.runTool(t, InstructWorkerTool, "ref-unknown", map[string]any{"worker": "nobody", "instructions": "go"}, false)
		tb03AssertRejectCode(t, err, "WORKER_NAME_UNKNOWN")
	})

	t.Run("INSTRUCT_WORKER_PLANNED", func(t *testing.T) {
		f := tb03NewRoundsFixture(t, 1)
		_, err := f.records.CreateWorker(t.Context(), f.coord, workerrecords.Op{ID: "create-planned"}, workerrecords.WorkerSpec{
			Name: "planned-w", AgentType: "implementer", ScopeMode: "write", DelegationID: "del-1", LegID: "leg-1",
		})
		testutil.FailErr(t, "create planned worker", err)
		_, _, err = f.runTool(t, InstructWorkerTool, "ref-planned", map[string]any{"worker": "planned-w", "instructions": "go"}, false)
		tb03AssertRejectCode(t, err, "INSTRUCT_WORKER_PLANNED")
	})

	t.Run("INSTRUCT_WORKER_WAITING", func(t *testing.T) {
		f := tb03NewRoundsFixture(t, 1)
		f.insertChildSession(t, "child-wait", "implementer")
		w, err := f.records.CreateWorker(t.Context(), f.coord, workerrecords.Op{ID: "create-wait"}, workerrecords.WorkerSpec{
			Name: "waiting-w", AgentType: "implementer", ScopeMode: "write", ChildSessionID: "child-wait",
		})
		testutil.FailErr(t, "create waiting worker", err)
		testutil.FailErr(t, "insert held job", f.queue.store.InsertTask(t.Context(), api.WorkerTask{
			ID: "job-wait", ProjectID: testdbseed.DefaultProjectID, ParentSessionID: "tb03-coord", ChildSessionID: "child-wait",
			AgentType: "implementer", Status: api.WorkerStatusHeld, ExecutionTarget: api.ExecutionTargetLocal,
			Prompt: "wait", Brief: "wait",
		}))
		_, err = f.records.AdmitRound(t.Context(), f.coord, workerrecords.Op{ID: "admit-wait"}, w.WorkerID, workerrecords.RoundSpec{
			JobID: "job-wait", ExecutionTarget: "local",
		})
		testutil.FailErr(t, "admit wait round", err)
		_, _, err = f.runTool(t, InstructWorkerTool, "ref-waiting", map[string]any{"worker": "waiting-w", "instructions": "go"}, false)
		tb03AssertRejectCode(t, err, "INSTRUCT_WORKER_WAITING")
	})

	t.Run("INSTRUCT_WORKER_ASKING", func(t *testing.T) {
		f := tb03NewRoundsFixture(t, 1)
		f.insertChildSession(t, "child-ask", "implementer")
		w, err := f.records.CreateWorker(t.Context(), f.coord, workerrecords.Op{ID: "create-ask"}, workerrecords.WorkerSpec{
			Name: "asking-w", AgentType: "implementer", ScopeMode: "write", ChildSessionID: "child-ask",
		})
		testutil.FailErr(t, "create asking worker", err)
		testutil.FailErr(t, "insert asking job", f.queue.store.InsertTask(t.Context(), api.WorkerTask{
			ID: "job-ask", ProjectID: testdbseed.DefaultProjectID, ParentSessionID: "tb03-coord", ChildSessionID: "child-ask",
			AgentType: "implementer", Status: api.WorkerStatusHeld, ExecutionTarget: api.ExecutionTargetLocal,
			Prompt: "ask", Brief: "ask",
		}))
		_, err = f.records.AdmitRound(t.Context(), f.coord, workerrecords.Op{ID: "admit-ask"}, w.WorkerID, workerrecords.RoundSpec{
			JobID: "job-ask", ExecutionTarget: "local",
		})
		testutil.FailErr(t, "admit ask round", err)
		testutil.FailErr(t, "put decision", f.decisions.Put(t.Context(), api.WorkerDecisionRequest{
			ChildSessionID: "child-ask", WorkerID: "job-ask", Question: "Choose option?",
		}))
		_, _, err = f.runTool(t, InstructWorkerTool, "ref-asking", map[string]any{"worker": "asking-w", "instructions": "go"}, false)
		tb03AssertRejectCode(t, err, "INSTRUCT_WORKER_ASKING")
	})

	t.Run("INSTRUCT_WORKER_RECOVERING", func(t *testing.T) {
		f := tb03NewRoundsFixture(t, 1)
		f.insertChildSession(t, "child-rec", "implementer")
		w, err := f.records.CreateWorker(t.Context(), f.coord, workerrecords.Op{ID: "create-rec"}, workerrecords.WorkerSpec{
			Name: "recovering-w", AgentType: "implementer", ScopeMode: "write", ChildSessionID: "child-rec",
		})
		testutil.FailErr(t, "create recovering worker", err)
		testutil.FailErr(t, "insert rec job", f.queue.store.InsertTask(t.Context(), api.WorkerTask{
			ID: "job-rec", ProjectID: testdbseed.DefaultProjectID, ParentSessionID: "tb03-coord", ChildSessionID: "child-rec",
			AgentType: "implementer", Status: api.WorkerStatusRunning, ExecutionTarget: api.ExecutionTargetLocal,
			Prompt: "rec", Brief: "rec",
		}))
		_, err = f.records.AdmitRound(t.Context(), f.coord, workerrecords.Op{ID: "admit-rec"}, w.WorkerID, workerrecords.RoundSpec{
			JobID: "job-rec", ExecutionTarget: "local",
		})
		testutil.FailErr(t, "admit rec round", err)
		f.settleJobWithCheckpoint(t, "job-rec", "cp-rec")
		f.ops.Reader.Readiness = func(context.Context, workerrecords.Actor, string) (bool, error) { return true, nil }
		_, _, err = f.runTool(t, InstructWorkerTool, "ref-rec", map[string]any{"worker": "recovering-w", "instructions": "go"}, false)
		tb03AssertRejectCode(t, err, "INSTRUCT_WORKER_RECOVERING")
	})

	t.Run("INSTRUCT_WORKER_CLOSED", func(t *testing.T) {
		f := tb03NewRoundsFixture(t, 1)
		f.insertChildSession(t, "child-closed", "implementer")
		w, err := f.records.CreateWorker(t.Context(), f.coord, workerrecords.Op{ID: "create-closed"}, workerrecords.WorkerSpec{
			Name: "closed-w", AgentType: "implementer", ScopeMode: "write", ChildSessionID: "child-closed",
		})
		testutil.FailErr(t, "create closed worker", err)
		testutil.FailErr(t, "insert closed job", f.queue.store.InsertTask(t.Context(), api.WorkerTask{
			ID: "job-closed", ProjectID: testdbseed.DefaultProjectID, ParentSessionID: "tb03-coord", ChildSessionID: "child-closed",
			AgentType: "implementer", Status: api.WorkerStatusRunning, ExecutionTarget: api.ExecutionTargetLocal,
			Prompt: "work", Brief: "work",
		}))
		_, err = f.records.AdmitRound(t.Context(), f.coord, workerrecords.Op{ID: "admit-closed"}, w.WorkerID, workerrecords.RoundSpec{
			JobID: "job-closed", ExecutionTarget: "local",
		})
		testutil.FailErr(t, "admit closed round", err)
		f.settleJobWithCheckpoint(t, "job-closed", "")
		_, err = f.db.ExecContext(t.Context(), `UPDATE workers SET child_session_id = NULL WHERE id = ?`, w.WorkerID)
		testutil.FailErr(t, "clear child session", err)
		_, _, err = f.runTool(t, InstructWorkerTool, "ref-closed", map[string]any{"worker": "closed-w", "instructions": "go"}, false)
		tb03AssertRejectCode(t, err, "INSTRUCT_WORKER_CLOSED")
	})

	t.Run("INSTRUCT_WORKER_AGENT_CHANGES_OPEN", func(t *testing.T) {
		f := tb03NewRoundsFixture(t, 1)
		f.insertChildSession(t, "child-open", "implementer")
		w, err := f.records.CreateWorker(t.Context(), f.coord, workerrecords.Op{ID: "create-open"}, workerrecords.WorkerSpec{
			Name: "open-changes-w", AgentType: "implementer", ScopeMode: "write", ChildSessionID: "child-open",
		})
		testutil.FailErr(t, "create open changes worker", err)
		testutil.FailErr(t, "insert open job", f.queue.store.InsertTask(t.Context(), api.WorkerTask{
			ID: "job-open", ProjectID: testdbseed.DefaultProjectID, ParentSessionID: "tb03-coord", ChildSessionID: "child-open",
			AgentType: "implementer", Status: api.WorkerStatusRunning, ExecutionTarget: api.ExecutionTargetLocal,
			Prompt: "work", Brief: "work",
		}))
		_, err = f.records.AdmitRound(t.Context(), f.coord, workerrecords.Op{ID: "admit-open"}, w.WorkerID, workerrecords.RoundSpec{
			JobID: "job-open", ExecutionTarget: "local",
		})
		testutil.FailErr(t, "admit open round", err)
		f.settleJobWithCheckpoint(t, "job-open", "cp-open")
		_, _, err = f.runTool(t, InstructWorkerTool, "ref-open-agent", map[string]any{
			"worker": "open-changes-w", "instructions": "switch to reader", "agent_type": "repo-researcher",
		}, false)
		tb03AssertRejectCode(t, err, "INSTRUCT_WORKER_AGENT_CHANGES_OPEN")
	})
}

// ---------------------------------------------------------------------------
// W07 · Decisions, Pending Hold, Stale Resolution Refusal & Sibling Isolation
// ---------------------------------------------------------------------------

func TestWEATB03_W07_01_DecisionRequestHoldAnswerAndStaleRefusal(t *testing.T) {
	f := tb03NewRoundsFixture(t, 2)
	f.insertChildSession(t, "tb03-w07-child", "implementer")

	_, eff, err := f.runTool(t, "task", "w07-start", map[string]any{
		"agent_type": "implementer",
		"name":       "decider",
		"scope":      map[string]any{"mode": "write", "paths": []any{"."}},
		"brief":      tb03Brief("implement feature"),
	}, false)
	testutil.FailErr(t, "dispatch decider", err)
	jobID := eff.Dispatch.WorkerID
	testutil.FailErr(t, "bind child", f.queue.SetChildSessionID(t.Context(), jobID, "tb03-w07-child"))

	// Answering before any decision is raised -> DecisionNotFoundCode.
	_, _, err = f.runTool(t, "answer_decision", "w07-early-ans", map[string]any{"worker": "decider", "answer": "A"}, false)
	tb03AssertRejectCode(t, err, DecisionNotFoundCode)

	claimed := tb03ClaimNext(t, f.queue)
	testutil.FailErr(t, "put decision", f.decisions.Put(t.Context(), api.WorkerDecisionRequest{
		ChildSessionID: "tb03-w07-child",
		WorkerID:       jobID,
		Question:       "Use JSON or CBOR?",
		Options:        []string{"JSON", "CBOR"},
	}))
	savedDecision, pending, err := f.decisions.Get(t.Context(), "tb03-w07-child")
	testutil.FailErr(t, "read saved decision", err)
	if !pending {
		t.Fatal("expected pending decision")
	}

	// Completing while decision is pending suspends the job to held.
	won, err := f.queue.Complete(t.Context(), claimed, api.WorkerResult{
		Status:  string(api.WorkerSummaryStatusNeedsDecision),
		Summary: "Waiting on serialization format",
	})
	testutil.FailErr(t, "suspend for decision", err)
	if !won {
		t.Fatal("Complete did not suspend job")
	}
	held, ok := f.queue.Get(jobID)
	if !ok || held.Status != api.WorkerStatusHeld {
		t.Fatalf("held job = %+v, want status held", held)
	}

	// Answer via answer_decision tool: resumes the job to pending and clears the pending decision.
	ansOut, _, err := f.runTool(t, "answer_decision", "w07-answer", map[string]any{
		"worker": "decider",
		"answer": "JSON",
	}, false)
	testutil.FailErr(t, "answer_decision", err)
	var ansResp map[string]any
	testutil.FailErr(t, "decode answer_decision", json.Unmarshal([]byte(ansOut), &ansResp))
	if ansResp["status"] != "resumed" || ansResp["worker"] != "decider" || ansResp["answer"] != "JSON" {
		t.Fatalf("answer response = %s", ansOut)
	}
	resumed, ok := f.queue.Get(jobID)
	if !ok || resumed.Status != api.WorkerStatusPending || resumed.ChildSessionID != "tb03-w07-child" {
		t.Fatalf("resumed job = %+v, want pending with same child session", resumed)
	}
	_, stillPending, err := f.decisions.Get(t.Context(), "tb03-w07-child")
	testutil.FailErr(t, "check cleared decision", err)
	if stillPending {
		t.Fatal("decision still pending after answer_decision")
	}

	// Second answer_decision when no longer asking -> DecisionNotFoundCode.
	_, _, err = f.runTool(t, "answer_decision", "w07-dup-ans", map[string]any{"worker": "decider", "answer": "CBOR"}, false)
	tb03AssertRejectCode(t, err, DecisionNotFoundCode)

	// Re-claim and complete the resumed job, then verify stale/changed resolution fails.
	claimed2 := tb03ClaimNext(t, f.queue)
	won, err = f.queue.Complete(t.Context(), claimed2, api.WorkerResult{Status: "complete", Summary: "used JSON"})
	testutil.FailErr(t, "complete resumed job", err)
	if !won {
		t.Fatal("resumed job did not complete")
	}

	resolver := NewSQLDecisionResolver(sessionstore.NewSQL(f.db), f.queue)
	// Already-answered decision was removed, so re-resolving it returns errDecisionMissing.
	err = resolver.Resolve(t.Context(), savedDecision, api.Message{Role: api.MessageRoleUser, Content: "CBOR"})
	if !errors.Is(err, errDecisionMissing) {
		t.Fatalf("stale resolution err = %v, want errDecisionMissing", err)
	}
	// If a stale decision row exists for an already-terminal job, Resolve distinguishes job state from a changed question.
	testutil.FailErr(t, "put stale decision on terminal job", f.decisions.Put(t.Context(), api.WorkerDecisionRequest{
		ChildSessionID: "tb03-w07-child", WorkerID: jobID, Question: "Use JSON or CBOR?", Options: []string{"JSON", "CBOR"},
	}))
	staleRec, _, err := f.decisions.Get(t.Context(), "tb03-w07-child")
	testutil.FailErr(t, "get stale decision", err)
	err = resolver.Resolve(t.Context(), staleRec, api.Message{Role: api.MessageRoleUser, Content: "CBOR"})
	var stateErr *DecisionStateError
	if !errors.As(err, &stateErr) || stateErr.Status != "complete" {
		t.Fatalf("terminal job stale resolution err = %v, want *DecisionStateError(complete)", err)
	}
	staleRec.Question = "Different question"
	if err := resolver.Resolve(t.Context(), staleRec, api.Message{Role: api.MessageRoleUser, Content: "CBOR"}); !errors.Is(err, errDecisionChanged) {
		t.Fatalf("changed question resolution err = %v, want errDecisionChanged", err)
	}
}

func TestWEATB03_W07_02_SiblingIsolationWhenOneAsksOrCancels(t *testing.T) {
	f := tb03NewRoundsFixture(t, 2)
	f.insertChildSession(t, "child-asker", "implementer")
	f.insertChildSession(t, "child-steady", "repo-researcher")

	_, askerEff, err := f.runTool(t, "task", "w07-asker", map[string]any{
		"agent_type": "implementer", "name": "asker",
		"scope": map[string]any{"mode": "write", "paths": []any{"."}},
		"brief": tb03Brief("needs decision"),
	}, false)
	testutil.FailErr(t, "dispatch asker", err)
	_, steadyEff, err := f.runTool(t, "task", "w07-steady", map[string]any{
		"agent_type": "repo-researcher", "name": "steady",
		"scope": map[string]any{"mode": "read"},
		"brief": tb03Brief("independent survey"),
	}, false)
	testutil.FailErr(t, "dispatch steady", err)

	testutil.FailErr(t, "bind asker child", f.queue.SetChildSessionID(t.Context(), askerEff.Dispatch.WorkerID, "child-asker"))
	testutil.FailErr(t, "bind steady child", f.queue.SetChildSessionID(t.Context(), steadyEff.Dispatch.WorkerID, "child-steady"))

	c1 := tb03ClaimNext(t, f.queue)
	c2 := tb03ClaimNext(t, f.queue)
	askerClaim, steadyClaim := c1, c2
	if c1.ID != askerEff.Dispatch.WorkerID {
		askerClaim, steadyClaim = c2, c1
	}

	// Asker raises a decision and suspends into held.
	testutil.FailErr(t, "asker decision", f.decisions.Put(t.Context(), api.WorkerDecisionRequest{
		ChildSessionID: "child-asker", WorkerID: askerClaim.ID, Question: "Which API?",
	}))
	won, err := f.queue.Complete(t.Context(), askerClaim, api.WorkerResult{Status: string(api.WorkerSummaryStatusNeedsDecision), Summary: "asking"})
	testutil.FailErr(t, "suspend asker", err)
	if !won {
		t.Fatal("asker did not suspend")
	}

	// Steady sibling is completely unaffected and completes cleanly.
	won, err = f.queue.Complete(t.Context(), steadyClaim, api.WorkerResult{
		Status: "complete", Summary: "steady finished", CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"},
	})
	testutil.FailErr(t, "complete steady", err)
	if !won {
		t.Fatal("steady did not complete")
	}

	// Cancel asker via worker_cancel while it is asking.
	_, _, err = f.runTool(t, "worker_cancel", "w07-cancel-asker", map[string]any{"worker": "asker", "reason": "obsolete"}, false)
	testutil.FailErr(t, "cancel asker", err)

	askerJob, _ := f.queue.Get(askerClaim.ID)
	steadyJob, _ := f.queue.Get(steadyClaim.ID)
	if askerJob.Status != api.WorkerStatusCanceled {
		t.Fatalf("asker status = %q, want canceled", askerJob.Status)
	}
	if steadyJob.Status != api.WorkerStatusComplete {
		t.Fatalf("steady status = %q, want complete (unaffected by sibling cancel)", steadyJob.Status)
	}

	// Steady can still start round 2 via instruct_worker.
	_, _, err = f.runTool(t, InstructWorkerTool, "w07-steady-r2", map[string]any{
		"worker": "steady", "instructions": "continue independent work",
	}, false)
	testutil.FailErr(t, "steady round 2 after sibling cancel", err)
}

// ---------------------------------------------------------------------------
// W08 · Review Workers, Read-Only Snapshot Binding & Refusals
// ---------------------------------------------------------------------------

func TestWEATB03_W08_01_ReviewWorkerBindsSealedSnapshotAndRefusals(t *testing.T) {
	f := tb03NewRoundsFixture(t, 4)
	targetFile := filepath.Join(f.rootDir, "target.go")
	testutil.FailErr(t, "seed target.go", os.WriteFile(targetFile, []byte("package target\n"), 0o644))

	f.insertChildSession(t, "tb03-prod-child", "implementer")
	f.insertChildSession(t, "tb03-rev-child", "repo-researcher")
	prod, err := f.records.CreateWorker(t.Context(), f.coord, workerrecords.Op{ID: "create-prod"}, workerrecords.WorkerSpec{
		Name: "producer", AgentType: "implementer", ScopeMode: "write", ChildSessionID: "tb03-prod-child",
	})
	testutil.FailErr(t, "create producer", err)

	admitAndSettle := func(jobID, cp string) string {
		testutil.FailErr(t, "insert "+jobID, f.queue.store.InsertTask(t.Context(), api.WorkerTask{
			ID: jobID, ProjectID: testdbseed.DefaultProjectID, ParentSessionID: "tb03-coord", ChildSessionID: "tb03-prod-child",
			AgentType: "implementer", Status: api.WorkerStatusRunning, ExecutionTarget: api.ExecutionTargetLocal,
			Prompt: "work", Brief: "work",
		}))
		_, err := f.records.AdmitRound(t.Context(), f.coord, workerrecords.Op{ID: "admit-" + jobID}, prod.WorkerID, workerrecords.RoundSpec{
			JobID: jobID, ExecutionTarget: "local",
		})
		testutil.FailErr(t, "admit "+jobID, err)
		return f.settleJobWithCheckpoint(t, jobID, cp)
	}

	// Round 1 has NO changes -> REVIEW_OF_NO_CHANGES.
	noChangesHandle := admitAndSettle("w08-job-nochanges", "")
	_, _, err = f.runTool(t, "task", "w08-rev-nochanges", map[string]any{
		"agent_type": "repo-researcher", "name": "rev-nochanges",
		"scope":     map[string]any{"mode": "read"},
		"review_of": noChangesHandle,
		"brief":     tb03Brief("review empty round"),
	}, false)
	tb03AssertRejectCode(t, err, ReviewOfNoChangesCode)

	// Round 2 delivers checkpoint cp-w08-2.
	changedHandle := admitAndSettle("w08-job-changed", "cp-w08-2")

	// Unknown handle -> RESULT_HANDLE_UNKNOWN.
	_, _, err = f.runTool(t, "task", "w08-rev-unknown", map[string]any{
		"agent_type": "repo-researcher", "name": "rev-unknown",
		"scope":     map[string]any{"mode": "read"},
		"review_of": "result-does-not-exist",
		"brief":     tb03Brief("review unknown"),
	}, false)
	tb03AssertRejectCode(t, err, "RESULT_HANDLE_UNKNOWN")

	// Write-scoped review -> TOOL_ARGS_INVALID.
	_, _, err = f.runTool(t, "task", "w08-rev-write", map[string]any{
		"agent_type": "implementer", "name": "rev-write",
		"scope":     map[string]any{"mode": "write"},
		"review_of": changedHandle,
		"brief":     tb03Brief("mutating review"),
	}, false)
	tb03AssertRejectCode(t, err, "TOOL_ARGS_INVALID")

	// Stacked review (base_overlay_id + review_of) -> TOOL_ARGS_INVALID.
	_, _, err = f.runTool(t, "task", "w08-rev-stacked", map[string]any{
		"agent_type": "repo-researcher", "name": "rev-stacked",
		"scope":     map[string]any{"mode": "read", "base_overlay_id": changedHandle},
		"review_of": changedHandle,
		"brief":     tb03Brief("stacked review"),
	}, false)
	tb03AssertRejectCode(t, err, "TOOL_ARGS_INVALID")

	// Refused reviews must not have enqueued any jobs (only the 2 producer jobs exist).
	if f.countJobs(t) != 2 {
		t.Fatalf("refused reviews enqueued jobs: count=%d, want 2", f.countJobs(t))
	}

	// Valid read-only review binds exact ReviewSubject and leaves primary tree untouched.
	revOut, revEff, err := f.runTool(t, "task", "w08-rev-ok", map[string]any{
		"agent_type": "repo-researcher", "name": "reviewer",
		"scope":     map[string]any{"mode": "read"},
		"review_of": changedHandle,
		"brief":     tb03Brief("review cp-w08-2"),
	}, false)
	testutil.FailErr(t, "dispatch valid reviewer", err)
	var revReceipt map[string]any
	testutil.FailErr(t, "decode reviewer receipt", json.Unmarshal([]byte(revOut), &revReceipt))
	if revReceipt["scope_mode"] != "read" {
		t.Fatalf("reviewer receipt = %+v, want scope_mode=read", revReceipt)
	}

	reviewer, err := f.records.Resolve(t.Context(), f.coord, "reviewer")
	testutil.FailErr(t, "resolve reviewer", err)
	revRounds, err := f.records.Rounds(t.Context(), f.coord, reviewer.ID)
	testutil.FailErr(t, "reviewer rounds", err)
	wantSubject := workerrecords.ReviewSubject{
		ResultID: changedHandle, WorkerID: prod.WorkerID, JobID: "w08-job-changed", CheckpointID: "cp-w08-2",
	}
	if sub, ok := revRounds[0].Subject(); !ok || sub != wantSubject {
		t.Fatalf("reviewer subject = %+v (ok=%v), want %+v", sub, ok, wantSubject)
	}

	// Producer starts a later round 3 while reviewer is running; reviewer's subject remains pinned to cp-w08-2,
	// and reviewer's agent is sealed against switching to implementer.
	testutil.FailErr(t, "bind rev child", f.queue.SetChildSessionID(t.Context(), revEff.Dispatch.WorkerID, "tb03-rev-child"))
	f.settleJobWithCheckpoint(t, revEff.Dispatch.WorkerID, "")
	_, _, err = f.runTool(t, InstructWorkerTool, "w08-rev-seal", map[string]any{
		"worker": "reviewer", "instructions": "modify the code directly", "agent_type": "implementer",
	}, false)
	tb03AssertRejectCode(t, err, "INSTRUCT_WORKER_AGENT_SEALED")

	// Primary tree bytes are untouched.
	afterBytes, err := os.ReadFile(targetFile)
	testutil.FailErr(t, "read primary target.go", err)
	if string(afterBytes) != "package target\n" {
		t.Fatalf("primary tree mutated during review: %q", string(afterBytes))
	}

	// Rejecting the subject result refuses subsequent reviews with REVIEW_OF_NOT_ADMITTED.
	_, err = f.db.ExecContext(t.Context(), `UPDATE worker_result_dispositions SET change_state = 'rejected', change_operation_id = 'rej-w08', changed_at = ? WHERE result_id = ?`,
		timelayout.Format(time.Now().UTC()), changedHandle)
	testutil.FailErr(t, "reject subject", err)
	_, _, err = f.runTool(t, "task", "w08-rev-after-reject", map[string]any{
		"agent_type": "repo-researcher", "name": "rev-after-reject",
		"scope":     map[string]any{"mode": "read"},
		"review_of": changedHandle,
		"brief":     tb03Brief("review rejected"),
	}, false)
	tb03AssertRejectCode(t, err, ReviewOfNotAdmittedCode)
}

// ---------------------------------------------------------------------------
// W11 · Outcome Delivery, Parent Projection, Bounded/Idempotent Wakeups & Fan-Out
// ---------------------------------------------------------------------------

type tb03OutcomeBridgeHarness struct {
	queue *SQLQueue

	mu           sync.Mutex
	order        []string
	projected    map[string]int
	proofs       map[string]int
	wakes        map[string]int
	terminals    map[string]int
	armed        []string
	failProofFor map[string]error
	shouldNudge  bool
}

func tb03NewOutcomeBridgeHarness(q *SQLQueue) *tb03OutcomeBridgeHarness {
	return &tb03OutcomeBridgeHarness{
		queue:        q,
		projected:    map[string]int{},
		proofs:       map[string]int{},
		wakes:        map[string]int{},
		terminals:    map[string]int{},
		failProofFor: map[string]error{},
		shouldNudge:  true,
	}
}

func (h *tb03OutcomeBridgeHarness) TaskByID(jobID string) (workeroutcomes.SummaryInput, bool) {
	task, ok := h.queue.Get(jobID)
	if !ok || task == nil {
		return workeroutcomes.SummaryInput{}, false
	}
	return workeroutcomes.SummaryInput{
		JobID:           task.ID,
		ParentSessionID: task.ParentSessionID,
		ProjectID:       task.ProjectID,
		AgentType:       task.AgentType,
	}, true
}

func (h *tb03OutcomeBridgeHarness) ProjectResult(_ context.Context, in workeroutcomes.SummaryInput, res api.WorkerResult) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.projected[in.JobID]++
	h.order = append(h.order, "project:"+in.JobID)
	return res.Status, nil
}

func (h *tb03OutcomeBridgeHarness) ProjectFailure(_ context.Context, in workeroutcomes.SummaryInput, _ error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.projected[in.JobID]++
	h.order = append(h.order, "project_fail:"+in.JobID)
	return nil
}

func (h *tb03OutcomeBridgeHarness) RecordTerminalProof(_ context.Context, _ string, jobID, _ string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.failProofFor[jobID]; err != nil {
		delete(h.failProofFor, jobID)
		return err
	}
	h.proofs[jobID]++
	h.order = append(h.order, "proof:"+jobID)
	return nil
}

func (h *tb03OutcomeBridgeHarness) EnvelopeForTerminal(context.Context, string, string) anchor.Envelope {
	return anchor.Envelope{}
}

func (h *tb03OutcomeBridgeHarness) ShouldNudge(context.Context, string, string, string) bool {
	return h.shouldNudge
}

func (h *tb03OutcomeBridgeHarness) AfterTerminal(_ context.Context, _ string, jobID string, _ anchor.Envelope) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.wakes[jobID]++
	h.order = append(h.order, "wake:"+jobID)
}

func (h *tb03OutcomeBridgeHarness) Terminal(_ context.Context, _ string, jobID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.terminals[jobID]++
}

func (h *tb03OutcomeBridgeHarness) NudgeLegFinished(context.Context, string, time.Time, string) {}

func (h *tb03OutcomeBridgeHarness) Arm(_ context.Context, _ string, jobID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.armed = append(h.armed, jobID)
}

func TestWEATB03_W11_01_OutcomeDeliveryParentProjectionAndIdempotentWakes(t *testing.T) {
	const fanoutWidth = 4
	database, q := tb03OpenQueue(t, fanoutWidth)
	harness := tb03NewOutcomeBridgeHarness(q)
	bridge := &SessionOutcomeBridge{
		Workers: harness,
		Loop:    harness,
		Results: harness,
		State:   harness,
		Closure: harness,
	}

	var ran atomic.Int32
	exec := &tb03BarrierExecutor{
		executeFn: func(_ context.Context, task api.WorkerTask, _ WorkerRunContext) (api.WorkerResult, error) {
			ran.Add(1)
			return api.WorkerResult{Status: "complete", Summary: "done " + task.ID}, nil
		},
	}
	cfg := DefaultWorkersConfig()
	cfg.Poller.MaxConcurrency = fanoutWidth
	poller := NewLocalWorkerPoller(q, exec, cfg, bridge)

	jobIDs := make([]string, fanoutWidth)
	for i := range fanoutWidth {
		jobIDs[i] = tb03EnqueueTask(t, q, api.WorkerTask{
			Brief: fmt.Sprintf("fanout worker %d", i),
		})
	}
	// Inject one transient terminal-proof failure on job 0 to verify deferred wake + single retry delivery.
	harness.mu.Lock()
	harness.failProofFor[jobIDs[0]] = errors.New("transient proof conflict")
	harness.mu.Unlock()

	poller.claimAvailable(t.Context())
	poller.inflight.Wait()

	if ran.Load() != fanoutWidth {
		t.Fatalf("executed workers = %d, want %d", ran.Load(), fanoutWidth)
	}

	// Job 0's outcome must still be pending because proof failed transiently; jobs 1..3 are already delivered.
	pending, err := q.ListPendingOutcomes(t.Context())
	testutil.FailErr(t, "list pending outcomes after transient proof failure", err)
	if len(pending) != 1 || pending[0].ID != jobIDs[0] {
		t.Fatalf("pending outcomes = %+v, want only %s", pending, jobIDs[0])
	}
	harness.mu.Lock()
	if harness.wakes[jobIDs[0]] != 0 {
		harness.mu.Unlock()
		t.Fatalf("job 0 woke parent %d times despite failed proof", harness.wakes[jobIDs[0]])
	}
	harness.mu.Unlock()

	// Retry delivery via deliverPendingOutcomes: job 0 now succeeds and wakes parent exactly once.
	testutil.FailErr(t, "deliverPendingOutcomes retry", poller.deliverPendingOutcomes(t.Context()))
	pending, err = q.ListPendingOutcomes(t.Context())
	testutil.FailErr(t, "list pending after retry", err)
	if len(pending) != 0 {
		t.Fatalf("pending outcomes after retry = %d, want 0", len(pending))
	}

	// Re-running deliverPendingOutcomes is strictly idempotent (0 extra wakes/projections/proofs).
	testutil.FailErr(t, "deliverPendingOutcomes idempotent pass", poller.deliverPendingOutcomes(t.Context()))

	harness.mu.Lock()
	defer harness.mu.Unlock()
	for i, id := range jobIDs {
		resCount, resStatuses := tb03CountResults(t, database, id)
		if resCount != 1 || resStatuses[0] != "complete" {
			t.Fatalf("job[%d] %s results=%v, want 1 complete", i, id, resStatuses)
		}
		if harness.proofs[id] != 1 {
			t.Fatalf("job[%d] %s proofs=%d, want 1", i, id, harness.proofs[id])
		}
		if harness.wakes[id] != 1 {
			t.Fatalf("job[%d] %s wakes=%d, want 1", i, id, harness.wakes[id])
		}
		if harness.terminals[id] != 1 {
			t.Fatalf("job[%d] %s terminal notices=%d, want 1", i, id, harness.terminals[id])
		}
	}
	if len(harness.armed) != fanoutWidth {
		t.Fatalf("armed progress closures = %d, want %d", len(harness.armed), fanoutWidth)
	}

	// Explicit context.Canceled failure projects failure + proof without waking the coordinator.
	cancelJobID := tb03EnqueueTask(t, q, api.WorkerTask{Brief: "explicit cancel no wake"})
	harness.mu.Unlock()
	testutil.FailErr(t, "OnWorkerFailed canceled", bridge.OnWorkerFailed(t.Context(), cancelJobID, context.Canceled))
	harness.mu.Lock()
	if harness.proofs[cancelJobID] != 1 || harness.wakes[cancelJobID] != 0 {
		t.Fatalf("explicit cancel job proofs=%d wakes=%d, want 1 and 0", harness.proofs[cancelJobID], harness.wakes[cancelJobID])
	}
}
