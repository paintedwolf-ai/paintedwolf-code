package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/enginepaths"
	hostexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session/promptsource"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/session/workerworkspace"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestParkBlockedLiveCommandsArmsExactProcessSubscription(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	ctx := context.Background()
	reg := bgprocess.NewRegistry(bgprocess.Config{}, bgprocess.Hooks{})
	t.Cleanup(func() {
		testutil.FailErr(t, "dispose process", reg.Lifecycle.DisposeSession(ctx, "session-1"))
	})
	mgr := NewHost(store.NewMemory(), Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetBackgroundRegistry(reg)
	handle, err := reg.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID:  "session-1",
		ProjectID:  "project-1",
		Mode:       bgprocess.JobModeBackground,
		OriginTool: "command",
		Timeout:    5 * time.Second,
		Runner:     hostcmd.NewRunner(),
		Request: hostcmd.Request{
			Launch:     hostexec.HostLaunch("blocked live command park test"),
			ProjectDir: t.TempDir(),
			Stages:     []hostexec.Stage{{Name: "sleep", Args: []string{"2"}}},
		},
	})
	testutil.FailErr(t, "start process", err)

	if !(&promptsource.Control{Processes: mgr.Processes, Runtime: mgr.Coordinator.Runtime}).ParkBlockedLiveCommands(ctx, "session-1") {
		t.Fatal("running visible command was not parked")
	}
	loop := mgr.Coordinator.Runtime.CoordinatorLoop()
	if !loop.Waits.IsSleeping("session-1") {
		t.Fatal("coordinator loop is not sleeping")
	}
	triggers := loop.Waits.Triggers("session-1")
	if len(triggers) != 2 || triggers[0] != loopwake.WaitTriggerTimer || triggers[1] != loopwake.WaitTriggerProcessDone {
		t.Fatalf("wait triggers = %v want [timer process_done]", triggers)
	}
	handles := loop.Waits.ActiveProcessHandles("session-1")
	if len(handles) != 1 || handles[0] != handle {
		t.Fatalf("wait handles = %v want [%s]", handles, handle)
	}
}

func TestParkBlockedLiveCommandsDoesNothingWithoutVisibleJob(t *testing.T) {
	mgr := NewHost(store.NewMemory(), Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetBackgroundRegistry(bgprocess.NewRegistry(bgprocess.Config{}, bgprocess.Hooks{}))
	if (&promptsource.Control{Processes: mgr.Processes, Runtime: mgr.Coordinator.Runtime}).ParkBlockedLiveCommands(context.Background(), "session-1") {
		t.Fatal("empty process registry must not arm a wait")
	}
}

func TestBranchWorkspaceRejectsMissingMeta(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "project")
	testutil.FailErr(t, "mkdir project", os.MkdirAll(dir, 0o755))
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "only.txt"), []byte("x"), 0o644))
	roots := []projectroot.RootRef{{ID: "p", Label: "root", Path: dir, IsPrimary: true}}
	mgr := workspace.NewManager(filepath.Join(base, "branches"), filepath.Join(base, "seeds"))
	binding, _, err := mgr.CreateWorkerWorkspaceFromSources(context.Background(), roots, roots, "p", "job-meta")
	testutil.FailErr(t, "CreateWorkerWorkspaceFromSources", err)
	testutil.FailErr(t, "remove meta", os.RemoveAll(enginepaths.MetaDirForBranchRoot(binding.Root)))

	branch, err := newBranchWorkspace(binding.Root)
	testutil.FailErr(t, "newBranchWorkspace", err)
	if err := branch.ValidateMeta(context.Background()); !os.IsNotExist(err) {
		t.Fatalf("ValidateMeta error = %v, want missing metadata", err)
	}
}

func TestBranchWorkspaceRejectsInvalidMultiRootPath(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "primary")
	secondary := filepath.Join(base, "secondary")
	testutil.FailErr(t, "mkdir primary", os.MkdirAll(primary, 0o755))
	testutil.FailErr(t, "mkdir secondary", os.MkdirAll(secondary, 0o755))
	roots := []projectroot.RootRef{
		{ID: "primary", Label: "app", Path: primary, IsPrimary: true},
		{ID: "secondary", Label: "docs", Path: secondary},
	}
	mgr := workspace.NewManager(filepath.Join(base, "branches"), filepath.Join(base, "seeds"))
	binding, _, err := mgr.CreateWorkerWorkspaceFromSources(context.Background(), roots, roots, "primary", "job-roots")
	testutil.FailErr(t, "create branch", err)
	branch, err := newBranchWorkspace(binding.Root)
	testutil.FailErr(t, "new branch workspace", err)

	if err := branch.EnsureParents(context.Background(), "../outside/file.txt"); err == nil {
		t.Fatal("EnsureParents accepted parent traversal")
	}
	if err := branch.EnsureParents(context.Background(), "unknown/file.txt"); err == nil {
		t.Fatal("EnsureParents accepted an unknown root")
	}
}

func TestEnsureWorkerBranchRestoresPrimaryReadDeniesFromMetadata(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "primary")
	testutil.FailErr(t, "mkdir primary", os.MkdirAll(primary, 0o755))
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(primary, "module.py"), []byte("value = 1\n"), 0o644))
	roots := []projectroot.RootRef{{ID: "primary", Label: "app", Path: primary, IsPrimary: true}}
	workspaceManager := workspace.NewManager(filepath.Join(base, "branches"), filepath.Join(base, "seeds"))
	binding, _, err := workspaceManager.CreateWorkerWorkspaceFromSources(
		context.Background(), roots, roots, "primary", "job-read-deny",
	)
	testutil.FailErr(t, "create branch", err)
	branch, err := newBranchWorkspace(binding.Root)
	testutil.FailErr(t, "new branch workspace", err)

	tctx, err := workerworkspace.New(nil, nil, newBranchWorkspace).EnsureBranch(context.Background(), tools.ToolContext{Source: tools.InvocationSource{WorkerBranchRoot: binding.Root, BranchWorkspace: branch}})
	testutil.FailErr(t, "ensure existing branch", err)
	if len(tctx.Source.WorkerSourceRoots) != 1 || tctx.Source.WorkerSourceRoots[0] != primary {
		t.Fatalf("worker source roots = %v want [%s]", tctx.Source.WorkerSourceRoots, primary)
	}
}

// jobLister serves fixed worker jobs for one parent session.
type jobLister struct {
	tasks []api.WorkerTask
}

func (j jobLister) ListBySession(_ context.Context, _, sessionID string, _ ...api.WorkerStatus) ([]api.WorkerTask, error) {
	var out []api.WorkerTask
	for _, task := range j.tasks {
		if task.ParentSessionID == sessionID {
			out = append(out, task)
		}
	}
	return out, nil
}
func (jobLister) ListPendingOutcomes(context.Context) ([]api.WorkerTask, error) { return nil, nil }
func (jobLister) Get(string) (*api.WorkerTask, bool)                            { return nil, false }
func (jobLister) ClaimWorkerBranch(context.Context, string) (*api.WorkerTask, error) {
	return nil, nil
}

func TestCloseoutEvidenceListsDispatchedLegsSinceTheIntent(t *testing.T) {
	st := store.NewMemory()
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	parent, err := st.Create(t.Context(), api.CreateSessionRequest{}, "project-1")
	testutil.FailErr(t, "create parent", err)
	intent := time.Unix(1_000, 0).UTC()
	mgr.SetWorkerQueue(jobLister{tasks: []api.WorkerTask{
		{ID: "old", ParentSessionID: parent.ID, ChildSessionID: "child-old", CreatedAt: intent.Add(-time.Minute)},
		{ID: "leg", ParentSessionID: parent.ID, ChildSessionID: "child-leg", LegID: "execute/leg-1", CreatedAt: intent.Add(time.Minute)},
		{ID: "plain", ParentSessionID: parent.ID, ChildSessionID: "child-plain", CreatedAt: intent.Add(2 * time.Minute)},
		{ID: "pending", ParentSessionID: parent.ID, CreatedAt: intent.Add(3 * time.Minute)},
		{ID: "other", ParentSessionID: "someone-else", ChildSessionID: "child-other", CreatedAt: intent.Add(time.Minute)},
	}})

	legs, err := mgr.Verification.Evidence.WorkerLegs(t.Context(), parent.ID, intent)
	testutil.FailErr(t, "worker legs", err)
	if len(legs) != 2 || legs[0].ChildSessionID != "child-leg" || legs[0].Namespace() != "execute/leg-1" ||
		legs[1].ChildSessionID != "child-plain" || legs[1].Namespace() != "child-plain" {
		t.Fatalf("legs = %+v, want the two legs dispatched since the intent", legs)
	}
	all, err := mgr.Verification.Evidence.WorkerLegs(t.Context(), parent.ID, time.Time{})
	testutil.FailErr(t, "every leg", err)
	if len(all) != 3 {
		t.Fatalf("legs without an intent = %+v, want every started leg", all)
	}
}

type evidenceWorkflowView struct {
	recordingWorkflowView
	run *api.WorkflowRun
}

func (v *evidenceWorkflowView) ActiveBySession(context.Context, string) (*api.WorkflowRun, error) {
	return v.run, nil
}

func TestWorkflowEvidenceSurvivesLaterUserMessagesAndCompaction(t *testing.T) {
	mgr, st := newTestManager(t)
	parent, err := st.Create(t.Context(), api.CreateSessionRequest{}, "project-1")
	testutil.FailErr(t, "create parent", err)
	child, err := st.CreateChild(t.Context(), parent, api.SpawnChildRequest{AgentType: "skeptic", Prompt: "Review"})
	testutil.FailErr(t, "create reviewer", err)
	run := &api.WorkflowRun{ID: "review-run", CurrentPhase: "challenge"}
	mgr.SetWorkflowDomains(workflowDomainFixture(&evidenceWorkflowView{run: run}))
	before := time.Unix(100, 0)
	tasks := []api.WorkerTask{
		{ID: "original", ParentSessionID: parent.ID, ChildSessionID: child.ID, AgentType: "skeptic", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}, WorkflowRunID: run.ID, WorkflowPhase: run.CurrentPhase, CreatedAt: before},
		{ID: "unrelated", ParentSessionID: parent.ID, ChildSessionID: "other", AgentType: "skeptic", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}, WorkflowRunID: "another-run", CreatedAt: before.Add(time.Hour)},
	}
	mgr.SetWorkerQueue(jobLister{tasks: tasks})
	legs, err := mgr.Verification.Evidence.WorkerLegs(t.Context(), parent.ID, before.Add(time.Minute))
	testutil.FailErr(t, "read run evidence", err)
	if len(legs) != 1 || legs[0].ChildSessionID != child.ID {
		t.Fatalf("run evidence = %+v", legs)
	}
	tasks = append(tasks, api.WorkerTask{ID: "previous-phase", ParentSessionID: parent.ID, ChildSessionID: "earlier-phase-child", AgentType: "skeptic", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}, WorkflowRunID: run.ID, WorkflowPhase: "claims", CreatedAt: before})
	mgr.SetWorkerQueue(jobLister{tasks: tasks})
	reviewers, err := mgr.Coordinator.Closeout.ReviewerEvidence(t.Context(), parent.ID, []api.Message{{Role: api.MessageRoleUser, CreatedAt: before.Add(time.Minute)}}, []string{"skeptic"})
	testutil.FailErr(t, "read reviewers without summaries", err)
	if len(reviewers) != 1 || len(reviewers[0].LegIDs) != 1 || reviewers[0].LegIDs[0] != child.ID {
		t.Fatalf("reviewer membership = %+v", reviewers)
	}
}

type failedCompactionSummarizer struct{}

func (failedCompactionSummarizer) Summarize(context.Context, string, string, int) (string, error) {
	return "", errors.New("local summarizer unavailable")
}

func TestFailedSynthesisKeepsPublishedRecoveryAndCanonicalHistory(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.ModelContextWindow = 1000
	cfg.BudgetTriggerPct = 50
	cfg.HardCeilingTokens = 500
	cfg.TargetTokens = 300
	cfg.ChunkTokenThreshold = 1_000_000
	cfg.KeepRecentMessages = 2

	mem := store.NewMemory()
	mgr := NewHost(mem, Models{Client: llm.NewMockProvider(testMockConfig(t)), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	oartest.InstallCloseoutPolicy(t, mgr)
	mgr.Runner.History.SetCompactor(compaction.NewSimpleCompactor(cfg, failedCompactionSummarizer{}))
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	block := strings.Repeat("retained fact ", 120)
	seed := make([]api.Message, 0, 20)
	for i := 0; i < 20; i++ {
		role := api.MessageRoleUser
		if i%2 == 1 {
			role = api.MessageRoleAssistant
		}
		seed = append(seed, api.Message{ID: fmt.Sprintf("seed-%d", i), Role: role, Content: block})
	}
	testutil.FailErr(t, "append canonical history", mem.AppendMessages(t.Context(), sess.ID, seed...))
	mgr.Runner.History.ObserveTokens(sess.ID, sess.ProviderID, sess.Model, 1000, 1000)

	_, err = mgr.Submissions.Prompt(t.Context(), sess.ID, "first attempt")
	testutil.FailErr(t, "first prompt", err)
	mgr.Runner.History.Wait()
	got, err := mem.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "get session after failure", err)
	if got.CompactionGeneration == 0 {
		t.Fatalf("failed synthesis lost its independent baseline: generation %d", got.CompactionGeneration)
	}
	if _, ok, viewErr := mem.GetCompactionView(t.Context(), sess.ID); viewErr != nil || !ok {
		t.Fatalf("failed synthesis did not retain a view: ok=%v err=%v", ok, viewErr)
	}

	// Prompt fitting does not lower the durable retry threshold.
	mgr.Runner.History.SetCompactor(compaction.NewSimpleCompactor(cfg, compaction.MockSummarizer{Text: "validated continuation"}))
	_, err = mgr.Submissions.Prompt(t.Context(), sess.ID, "retry")
	testutil.FailErr(t, "retry prompt", err)
	mgr.Runner.History.Wait()
	got, err = mem.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "get session after retry", err)
	if got.CompactionGeneration == 0 {
		t.Fatal("successful retry did not persist a compaction view")
	}
	if _, ok, viewErr := mem.GetCompactionView(t.Context(), sess.ID); viewErr != nil || !ok {
		t.Fatalf("successful retry view: ok=%v err=%v", ok, viewErr)
	}
}

func TestWorkerChildProjectDirIsDelegationNotOverlay(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	overlay := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-write")

	store := store.NewMemory()
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	child, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer", Prompt: "write leg"})
	testutil.FailErr(t, "create child", err)

	if child.WorkspacePath != parent.WorkspacePath {
		t.Fatalf("child ProjectDir = %q parent = %q", child.WorkspacePath, parent.WorkspacePath)
	}
	if child.WorkspacePath == overlay {
		t.Fatal("child ProjectDir must not be the overlay root")
	}
}

func TestRecentSiblingNotesExcludesPriorRunFindings(t *testing.T) {
	transcript := store.NewMemory()
	parent, err := transcript.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	root := parent.ID
	testutil.FailErr(t, "record human intent", transcript.AppendMessages(t.Context(), root, api.Message{ID: "intent", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser, Content: "build modules", CreatedAt: time.Now().UTC().Add(-time.Hour)}))
	sqlDB := testdbfixture.Open(t, "findings.db")
	findingsStore := findings.NewSQLStore(sqlDB)
	testdbseed.InsertSession(t, sqlDB, root, testdbseed.DefaultProjectID)

	oldTime := time.Now().UTC().Add(-2 * time.Hour)
	_, err = sqlDB.ExecContext(t.Context(),
		`INSERT INTO findings (session_id, agent, summary, ref, created_at) VALUES (?, ?, ?, ?, ?)`,
		root, "old-job", "prior run finding", "old.go", oldTime.Format(time.RFC3339),
	)
	testutil.FailErr(t, "insert old finding", err)

	_, err = sqlDB.ExecContext(t.Context(), `INSERT INTO findings(session_id,agent,summary,ref,created_at) VALUES (?,?,?,?,?)`, root, "job-before", "contract before spawn", "module.go", time.Now().UTC().Add(-time.Minute).Format(time.RFC3339))
	testutil.FailErr(t, "insert current batch contract", err)
	spawnAt := time.Now().UTC()
	m := workeroutcomes.NewNotes(transcript, findingsStore, &stubSiblingTaskQueue{byChild: map[string]*api.WorkerTask{root: {ID: "job-b", CreatedAt: spawnAt}}}, nil)

	ctx := workercontext.WithJob(context.Background(), "job-b")
	notes, _, err := m.RecentSiblingNotes(ctx, root, 0, 5)
	testutil.FailErr(t, "load prior notes", err)
	if len(notes) != 1 || notes[0].Summary != "contract before spawn" {
		t.Fatalf("batch notes = %+v want pre-spawn contract only", notes)
	}

	_, err = findingsStore.Append(context.Background(), root, "job-a", "fresh sibling note", "fresh.go", "")
	testutil.FailErr(t, "append finding", err)
	notes2, _, err := m.RecentSiblingNotes(ctx, root, 0, 5)
	testutil.FailErr(t, "load fresh notes", err)
	if len(notes2) != 2 || notes2[1].Summary != "fresh sibling note" {
		t.Fatalf("post-spawn notes = %+v", notes2)
	}
}

type stubSiblingTaskQueue struct {
	noopWorkerBranchClaim
	byChild map[string]*api.WorkerTask
}

func (s *stubSiblingTaskQueue) ListBySession(context.Context, string, string, ...api.WorkerStatus) ([]api.WorkerTask, error) {
	return nil, nil
}

func (s *stubSiblingTaskQueue) Get(jobID string) (*api.WorkerTask, bool) {
	for _, task := range s.byChild {
		if task != nil && task.ID == jobID {
			return task, true
		}
	}
	return nil, false
}

func (*stubSiblingTaskQueue) ListPendingOutcomes(context.Context) ([]api.WorkerTask, error) {
	return nil, nil
}
