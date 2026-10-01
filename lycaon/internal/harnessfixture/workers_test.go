package harnessfixture

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

type countingFallback struct {
	calls   int
	stopped string
	stopErr error
}

func (f *countingFallback) AbortWorkerRuntime(_ context.Context, task api.WorkerTask) error {
	f.stopped = task.ChildSessionID
	return f.stopErr
}

func (f *countingFallback) Execute(context.Context, api.WorkerTask, worker.WorkerRunContext) (api.WorkerResult, error) {
	f.calls++
	return api.WorkerResult{Status: "complete"}, nil
}

func TestHarnessPreservesWorkerRuntimeCancellation(t *testing.T) {
	h := newScriptedHarness(t, map[string]string{"README.md": "fixture"})
	controller := h.workers()
	for _, stopErr := range []error{nil, errors.New("cleanup unavailable")} {
		h.fallback.stopErr = stopErr
		err := controller.AbortWorkerRuntime(t.Context(), api.WorkerTask{ID: "job", ChildSessionID: "child"})
		if !errors.Is(err, stopErr) || h.fallback.stopped != "child" || h.fallback.calls != 0 {
			t.Fatalf("runtime cancellation: err=%v stopped=%q executions=%d", err, h.fallback.stopped, h.fallback.calls)
		}
	}
}

type scriptedHarness struct {
	t         *testing.T
	root      string
	projects  *project.SQLRegistry
	attached  *project.Project
	sessions  *store.SQL
	parent    *api.Session
	queue     *worker.SQLQueue
	manager   *workspace.Manager
	ledger    *sourceledger.Store
	decisions session.DecisionStore
	database  db.Handle
	fallback  *countingFallback
	verified  []string
	reads     []string
	data      string
}

func newScriptedHarness(t *testing.T, files map[string]string) *scriptedHarness {
	t.Helper()
	t.Setenv(configdir.EnvDev, "1")
	t.Setenv(configdir.EnvHarness, "1")
	database := testdbfixture.Open(t, "scripted.db")
	h := &scriptedHarness{t: t, root: t.TempDir(), data: t.TempDir(), fallback: &countingFallback{}, database: database}
	for name, body := range files {
		testutil.FailErr(t, "write baseline", os.WriteFile(filepath.Join(h.root, name), []byte(body), 0o600))
	}
	h.projects = project.NewSQLRegistry(database)
	attached, err := h.projects.Create(t.Context(), project.CreateParams{Roots: []project.AttachRootParams{{Path: h.root}}})
	testutil.FailErr(t, "attach project", err)
	h.attached = attached
	h.sessions = store.NewSQL(database)
	parent, err := h.sessions.Create(t.Context(), api.CreateSessionRequest{WorkspaceRootID: attached.Roots[0].ID}, attached.ID)
	testutil.FailErr(t, "create parent", err)
	h.parent = parent
	h.queue = worker.NewSQLQueue(database, 8)
	h.ledger = sourceledger.New(database, filepath.Join(testbaseline.DataDir(t, database), "source-content"))
	h.queue.SetBaselineStore(h.ledger.BaselineStore())
	h.queue.SetProjectStore(h.projects)
	h.manager = workspace.NewManager(filepath.Join(testbaseline.DataDir(t, database), "worker-branches"), t.TempDir())
	h.queue.SetWorkerWorkspaceManager(h.manager)
	h.decisions = session.NewSQLDecisionStore(database)
	return h
}

func (h *scriptedHarness) workers() *Workers {
	verify := func(_ context.Context, child *api.Session, job *api.WorkerTask, command string) (*tools.SourceRunCapture, error) {
		h.verified = append(h.verified, command)
		if child == nil || job.WorkspaceRoot == "" {
			h.t.Fatal("verification ran outside a worker branch")
		}
		return &tools.SourceRunCapture{Verdict: api.SourceVerdictPassed, CheckID: uuid.NewString()}, nil
	}
	read := func(_ context.Context, child *api.Session, job *api.WorkerTask, path string) (string, string, error) {
		h.reads = append(h.reads, path)
		body, err := os.ReadFile(filepath.Join(h.root, path))
		return "read#1", string(body), err
	}
	controller, err := NewWorkers(h.data, h.sessions, h.queue, verify, read, h.decisions, h.fallback)
	testutil.FailErr(h.t, "create workers", err)
	return controller
}

func (h *scriptedHarness) dispatch(childID string, scope api.TaskScope, files []string) *api.WorkerTask {
	h.t.Helper()
	id, err := h.queue.Enqueue(h.t.Context(), api.WorkerTask{ID: uuid.NewString(), ProjectID: h.attached.ID, ParentSessionID: h.parent.ID, ChildSessionID: childID,
		WorkspaceRootID: h.attached.Roots[0].ID, WorkspacePath: h.root, AgentType: "implementer", Prompt: "Do the work", Brief: "Do the work", Scope: &scope, Files: files})
	testutil.FailErr(h.t, "enqueue", err)
	claimed, err := h.queue.ClaimNext(h.t.Context(), worker.ClaimRequest{ProjectID: h.attached.ID, ClaimedBy: "local", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(h.t, "claim", err)
	if claimed.ID != id {
		h.t.Fatal("claimed a different job")
	}
	return claimed
}

func (h *scriptedHarness) outcome(jobID string) Outcome {
	h.t.Helper()
	body, err := os.ReadFile(filepath.Join(h.data, "worker-scripts", "outcomes", jobID+".json"))
	testutil.FailErr(h.t, "read outcome receipt", err)
	var history []Outcome
	testutil.FailErr(h.t, "decode outcome history", json.Unmarshal(body, &history))
	if len(history) == 0 {
		h.t.Fatal("empty outcome history")
	}
	return history[len(history)-1]
}

func (h *scriptedHarness) promote(jobID string) string {
	h.t.Helper()
	service := &worker.MergeService{Queue: h.queue, Store: worker.NewSQLStore(h.database), Projects: h.projects, Workspace: h.manager, DataDir: h.t.TempDir(), SourceLedger: h.ledger}
	promoted, err := service.PromoteOverlay(h.t.Context(), h.parent.ID, jobID, api.PromoteOverlayInput{})
	testutil.FailErr(h.t, "promote", err)
	if promoted.Status != api.WorkerMergeStatusMerged {
		h.t.Fatalf("promotion: %+v", promoted)
	}
	body, err := os.ReadFile(filepath.Join(h.root, "app.py"))
	testutil.FailErr(h.t, "read promoted", err)
	return string(body)
}

func TestScriptedContinuationResumesTheSeededChildAndSurvivesRestart(t *testing.T) {
	h := newScriptedHarness(t, map[string]string{"app.py": "baseline"})
	setup := Setup{Overlays: []Overlay{{Label: "Change", ResultStatus: "partial", Files: map[string]string{"app.py": "partial"},
		Script: &WorkerScript{Stages: []WorkerStage{{Kind: StageComplete, Files: map[string]string{"app.py": "complete"}, Verify: "checks"}}}}}}
	evidence, err := Prepare(t.Context(), h.queue, h.sessions, h.parent, h.attached.Roots[0], setup, nil)
	testutil.FailErr(t, "prepare partial return", err)
	seed := evidence.Overlays[0]
	testutil.FailErr(t, "install", h.workers().Install(h.parent.ID, setup, evidence))
	controller := h.workers()
	_, err = controller.Execute(t.Context(), api.WorkerTask{ChildSessionID: uuid.NewString()}, worker.WorkerRunContext{})
	testutil.FailErr(t, "execute unrelated child", err)
	if h.fallback.calls != 1 {
		t.Fatal("unrelated child was intercepted")
	}
	if _, err = controller.Execute(t.Context(), api.WorkerTask{ID: uuid.NewString(), ChildSessionID: seed.ChildSessionID, ParentSessionID: uuid.NewString()}, worker.WorkerRunContext{}); err == nil {
		t.Fatal("another parent used the script")
	}
	claimed := h.dispatch(seed.ChildSessionID, api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"app.py"}}, nil)
	result, err := controller.Execute(t.Context(), *claimed, worker.WorkerRunContext{ProjectDir: h.root})
	testutil.FailErr(t, "execute resumed worker", err)
	completed, err := h.queue.Complete(t.Context(), claimed, result)
	testutil.FailErr(t, "complete resumed worker", err)
	if !completed || len(h.verified) != 1 || h.verified[0] != "checks" || h.fallback.calls != 1 {
		t.Fatal("script did not complete through its own executor")
	}
	if body, _ := os.ReadFile(filepath.Join(h.root, "app.py")); string(body) != "baseline" {
		t.Fatal("script modified integration before promotion")
	}
	if h.promote(claimed.ID) != "complete" {
		t.Fatal("promoted bytes differ from the script")
	}
	testutil.FailErr(t, "remove interrupted receipt publication", os.Remove(filepath.Join(h.data, "worker-scripts", "outcomes", claimed.ID+".json")))
	replayed, err := h.workers().Execute(t.Context(), *claimed, worker.WorkerRunContext{ProjectDir: h.root})
	testutil.FailErr(t, "replay committed execution", err)
	if replayed.Status != "complete" || len(h.verified) != 1 || h.outcome(claimed.ID).Kind != "complete" {
		t.Fatal("restart reran or lost a committed stage")
	}
	next := h.dispatch(seed.ChildSessionID, api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"app.py"}}, nil)
	_, err = h.workers().Execute(t.Context(), *next, worker.WorkerRunContext{ProjectDir: h.root})
	var permanent *worker.PermanentExecutionError
	if !errors.As(err, &permanent) || permanent.Error() != ScriptedFailureMessage || h.outcome(next.ID).Code != FailureScriptExhausted {
		t.Fatalf("exhausted script error: %v", err)
	}
}

func TestScriptedDispatchIsKeyedByDeclaredScopeAndSettlesOffPlanWork(t *testing.T) {
	h := newScriptedHarness(t, map[string]string{"app.py": "baseline", "lib.py": "lib"})
	setup := Setup{Dispatches: []DispatchScript{
		{Label: "App", Mode: "write", Paths: []string{"app.py"}, Stages: []WorkerStage{{Kind: StageComplete, Files: map[string]string{"app.py": "delivered"}, Verify: "checks"}}},
		{Label: "Survey", Mode: "read", Paths: []string{"lib.py"}, Stages: []WorkerStage{{Kind: StageComplete, Findings: []Finding{{Path: "lib.py", Line: 1, Note: "The library entry point."}}}}},
		{Label: "Broken", Mode: "write", Paths: []string{"lib.py"}, Stages: []WorkerStage{{Kind: StageFailed, Code: "FIXTURE_LEG_FAILED"}}},
	}}
	controller := h.workers()
	testutil.FailErr(t, "install", controller.Install(h.parent.ID, setup, Evidence{}))

	claimed := h.dispatch("", api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"app.py"}}, []string{"app.py"})
	result, err := controller.Execute(t.Context(), *claimed, worker.WorkerRunContext{ProjectDir: h.root})
	testutil.FailErr(t, "execute planned dispatch", err)
	bound, ok := h.queue.Get(claimed.ID)
	if !ok || bound.ChildSessionID == "" {
		t.Fatal("dispatch did not bind a child session")
	}
	child, err := h.sessions.Get(t.Context(), bound.ChildSessionID)
	testutil.FailErr(t, "load child", err)
	if child.ParentSessionID != h.parent.ID {
		t.Fatal("child belongs to another parent")
	}
	claimed.ChildSessionID = bound.ChildSessionID
	if completed, err := h.queue.Complete(t.Context(), claimed, result); err != nil || !completed {
		t.Fatalf("complete dispatch: %v %v", completed, err)
	}
	if h.promote(claimed.ID) != "delivered" || h.fallback.calls != 0 {
		t.Fatal("planned dispatch did not deliver its script")
	}

	survey := h.dispatch("", api.TaskScope{Mode: api.TaskScopeModeRead, Paths: []string{"lib.py"}}, nil)
	result, err = controller.Execute(t.Context(), *survey, worker.WorkerRunContext{ProjectDir: h.root})
	testutil.FailErr(t, "execute read leg", err)
	if len(h.reads) != 1 || result.CompletionReport == nil || len(result.CompletionReport.Findings) != 1 || result.CompletionReport.Findings[0].Excerpt != "lib" {
		t.Fatalf("read leg findings: %+v", result.CompletionReport)
	}

	broken := h.dispatch("", api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"lib.py"}}, nil)
	_, err = controller.Execute(t.Context(), *broken, worker.WorkerRunContext{ProjectDir: h.root})
	var permanent *worker.PermanentExecutionError
	if !errors.As(err, &permanent) || err.Error() != ScriptedFailureMessage || h.outcome(broken.ID).Code != "FIXTURE_LEG_FAILED" {
		t.Fatalf("failed stage error: %v", err)
	}

	repeat := h.dispatch("", api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"app.py"}}, nil)
	_, err = h.workers().Execute(t.Context(), *repeat, worker.WorkerRunContext{ProjectDir: h.root})
	if !errors.As(err, &permanent) || h.outcome(repeat.ID).Code != FailureDispatchUnplanned {
		t.Fatalf("second dispatch on a consumed scope: %v", err)
	}
	unplanned := h.dispatch("", api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"other.py"}}, nil)
	_, err = controller.Execute(t.Context(), *unplanned, worker.WorkerRunContext{ProjectDir: h.root})
	if !errors.As(err, &permanent) || h.outcome(unplanned.ID).Code != FailureDispatchUnplanned {
		t.Fatalf("unplanned dispatch: %v", err)
	}
	// Child identity connects the failure receipt to the coordinator's worker summary.
	if bound, _ := h.queue.Get(unplanned.ID); bound.ChildSessionID == "" || h.outcome(unplanned.ID).ChildSessionID != bound.ChildSessionID {
		t.Fatalf("unplanned dispatch settled without a bound child: %+v", h.outcome(unplanned.ID))
	}
	if h.fallback.calls != 0 {
		t.Fatal("a planned parent must never reach the live executor")
	}
}

func TestScriptedDecisionSuspendsThenDeliversTheAnsweredOutcome(t *testing.T) {
	h := newScriptedHarness(t, map[string]string{"app.py": "baseline"})
	setup := Setup{Dispatches: []DispatchScript{{Label: "App", Mode: "write", Paths: []string{"app.py"}, Stages: []WorkerStage{{
		Kind: StageNeedsDecision, Question: "Which default?", Options: []string{"three", "eight"},
		Outcomes: map[string]Delivery{"three": {Files: map[string]string{"app.py": "three"}, Verify: "checks"}, "eight": {Files: map[string]string{"app.py": "eight"}, Verify: "checks"}},
	}}}}}
	controller := h.workers()
	testutil.FailErr(t, "install", controller.Install(h.parent.ID, setup, Evidence{}))
	claimed := h.dispatch("", api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"app.py"}}, nil)
	result, err := controller.Execute(t.Context(), *claimed, worker.WorkerRunContext{ProjectDir: h.root})
	testutil.FailErr(t, "execute decision stage", err)
	bound, _ := h.queue.Get(claimed.ID)
	decision, ok, err := h.decisions.Get(t.Context(), bound.ChildSessionID)
	testutil.FailErr(t, "load decision", err)
	if !ok || decision.WorkerID != claimed.ID || result.Status != string(api.WorkerSummaryStatusNeedsDecision) || len(decision.Options) != 2 {
		t.Fatalf("decision stage: %+v %+v", decision, result)
	}
	claimed.ChildSessionID = bound.ChildSessionID
	completed, err := h.queue.Complete(t.Context(), claimed, result)
	testutil.FailErr(t, "suspend job", err)
	held, _ := h.queue.Get(claimed.ID)
	if !completed || held.Status != api.WorkerStatusHeld {
		t.Fatalf("job was not held for its decision: %+v", held)
	}
	result, err = h.workers().Execute(t.Context(), *claimed, worker.WorkerRunContext{ProjectDir: h.root})
	testutil.FailErr(t, "replay held execution after restart", err)
	if result.Status != string(api.WorkerSummaryStatusNeedsDecision) {
		t.Fatal("replayed execution lost its pending decision")
	}
	testutil.FailErr(t, "answer", h.sessions.AppendMessages(t.Context(), bound.ChildSessionID, worker.DecisionAnswerMessage("three", "coordinator")))
	result, err = h.workers().Execute(t.Context(), *claimed, worker.WorkerRunContext{ProjectDir: h.root})
	testutil.FailErr(t, "deliver answered outcome", err)
	if result.Status != "complete" {
		t.Fatalf("answered delivery: %+v", result)
	}
}

func TestWorkerScriptsAreValidatedAsClosedShapes(t *testing.T) {
	deliver := WorkerStage{Kind: StageComplete, Files: map[string]string{"app.py": "x"}, Verify: "checks"}
	valid := DispatchScript{Label: "App", Mode: "write", Paths: []string{"app.py"}, Stages: []WorkerStage{deliver}}
	testutil.FailErr(t, "valid dispatch", valid.Validate())
	for name, script := range map[string]DispatchScript{
		"unknown kind":        {Label: "A", Mode: "write", Paths: []string{"a"}, Stages: []WorkerStage{{Kind: "later"}}},
		"read leg with files": {Label: "A", Mode: "read", Paths: []string{"a"}, Stages: []WorkerStage{deliver}},
		"failed not last":     {Label: "A", Mode: "write", Paths: []string{"a"}, Stages: []WorkerStage{{Kind: StageFailed, Code: "X"}, deliver}},
		"decision missing outcome": {Label: "A", Mode: "write", Paths: []string{"a"}, Stages: []WorkerStage{{Kind: StageNeedsDecision, Question: "q", Options: []string{"1", "2"},
			Outcomes: map[string]Delivery{"1": {Files: map[string]string{"a": "b"}, Verify: "c"}}}}},
		"hidden path": {Label: "A", Mode: "write", Paths: []string{".env"}, Stages: []WorkerStage{deliver}},
		"no stages":   {Label: "A", Mode: "write", Paths: []string{"a"}},
	} {
		if script.Validate() == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	setup := Setup{Overlays: []Overlay{{Label: "Done", Files: map[string]string{"a": "b"}, Script: &WorkerScript{Stages: []WorkerStage{deliver}}}}}
	if setup.Validate() == nil {
		t.Fatal("a complete return cannot carry a continuation script")
	}
	if (Setup{Dispatches: []DispatchScript{valid, valid}}).Validate() == nil {
		t.Fatal("duplicate dispatch scopes were accepted")
	}
	if (Setup{}).Validate() == nil {
		t.Fatal("an empty setup was accepted")
	}
	testutil.FailErr(t, "scripted policy alone", (Setup{Policy: WorkerPolicyScripted}).Validate())
	if (Setup{Policy: "live"}).Validate() == nil {
		t.Fatal("an unknown worker policy was accepted")
	}
}
