package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	anchortestsetup "github.com/lycaon/lycaon/internal/testsetup/anchor"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	guidancetestsetup "github.com/lycaon/lycaon/internal/testsetup/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
)

// Bounded stores are zero-value usable.
func newForgetTestManager(t *testing.T) *Host {
	m, _ := newTestManager(t)
	return m
}

func TestForgetSessionClearsEveryReleasedStore(t *testing.T) {
	m := newForgetTestManager(t)
	var forgot []string
	testutil.FailErr(t, "register approval cleanup", m.Resources.RegisterCleanup("approvals", 50, func(_ context.Context, id string) error {
		forgot = append(forgot, id)
		return nil
	}))

	const sid = "sess-1"
	progressStore := progress.NewMemoryStore()
	progressStore.Set(sid, "- [ ] task")
	m.SetProgressStore(progressStore)
	m.Coordinator.ProgressClosure.Arm(t.Context(), sid, "job-closure")
	m.Runner.Closeouts.RecordGroundingFriction(t.Context(), sid)
	m.Runner.Closeouts.NoteCloseoutGroundingReject(t.Context(), sid, "citation", "offender", "draft", nil)
	m.Coordinator.Batch.AcceptSynthesis(t.Context(), sid)
	checkpoints := sessioncheckpoint.New(t.TempDir(), t.TempDir(), sessionstore.NewMemory())
	testutil.FailErr(t, "open checkpoint capture", m.Chats.Captures.Capture.Open(t.Context(), checkpoints, sid, "anchor"))
	m.Promotion.SetMergeReconcilePaths(sid, []string{"a.go"})
	calibrationSession := &api.Session{ID: sid}
	providerID, model := m.Runner.History.Model(t.Context(), calibrationSession)
	m.Runner.History.ObserveTokens(sid, providerID, model, 150, 100)
	if !m.Runner.History.Calibration(t.Context(), calibrationSession).Calibrated() {
		t.Fatal("missing calibration before disposal")
	}
	m.Promotion.RecordPromotePathStatus(sid, "job", []api.WorkerPromotePathStatus{{Path: "a.go"}})
	writeRootRuntime := approvalstate.NewSandboxPathGrantRuntime()
	m.Resources.Authority.Writes = writeRootRuntime

	writeRootRuntime.GrantChat(sid, "/opt/cache", "grant-1", "cp-1", nil)

	m.Resources.Queue.AppendOrdered(sid, "", testutil.HostOwner().ID, "queued follow-up", 0, time.Time{})
	progress.TurnStarted(sid)

	m.Resources.Dispose(t.Context(), sid)

	m.Chats.Captures.Capture.RecordPath(t.Context(), checkpoints, sid, "after.go")
	manifest, err := checkpoints.Load(t.Context(), sid, "anchor")
	testutil.FailErr(t, "load released checkpoint", err)
	if len(manifest.Paths) != 0 {
		t.Fatalf("checkpoint capture survived ForgetSession: %+v", manifest.Paths)
	}

	held := map[string]func() bool{
		"progressClosureExpect": func() bool { _, ok := m.Coordinator.ProgressClosure.Baseline(sid); return ok },
		"coordinatorBatchTurn":  func() bool { return m.Coordinator.Batch.TurnGuard(sid).SynthesisAcceptedThisTurn },
		"mergeReconcile":        func() bool { return m.Promotion.Allowed(sid, "a.go") },
		"compactionTokenCalibration": func() bool {
			return m.Runner.History.Calibration(t.Context(), calibrationSession) != (compaction.PromptTokenCalibration{})
		},
		"promotePathStatus": func() bool { return len(m.Promotion.PromotePathBoardLines(sid)) > 0 },
	}
	for name, stillThere := range held {
		if stillThere() {
			t.Errorf("%s survived ForgetSession", name)
		}
	}
	if m.Runner.Closeouts.CloseoutStallState(t.Context(), sid).Active {
		t.Fatal("closeout cycle survived disposal")
	}
	if len(forgot) != 1 || forgot[0] != sid {
		t.Fatalf("approval gate release = %v, want [%s]", forgot, sid)
	}
	if len(writeRootRuntime.ListChatGrants(sid)) != 0 {
		t.Fatal("write-root runtime survived ForgetSession")
	}

	if got := m.Chats.Drafts.Snapshot(sid); len(got.QueueItems) != 0 || got.Revision != 0 {
		t.Fatalf("queue survived ForgetSession: %+v", got)
	}
	if clock := progress.Clock(sid); clock.Running() {
		t.Fatalf("turn clock survived ForgetSession: %+v", clock)
	}
}

// Stop releases one run; the chat's approvals last until the chat is disposed.
func TestStopKeepsTheChatsApprovedSandboxGrants(t *testing.T) {
	m := newForgetTestManager(t)
	var released, disposed []string
	testutil.FailErr(t, "register run cleanup", m.Resources.RegisterCleanup("approval-run", 50, func(_ context.Context, id string) error {
		released = append(released, id)
		return nil
	}))
	testutil.FailErr(t, "register disposal", m.Resources.RegisterDisposal("approvals", 50, func(_ context.Context, id string) error {
		disposed = append(disposed, id)
		return nil
	}))
	const sid = "sess-stop"
	writeRootRuntime := approvalstate.NewSandboxPathGrantRuntime()
	m.Resources.Authority.Writes = writeRootRuntime

	writeRootRuntime.GrantChat(sid, "/opt/cache", "grant-1", "cp-1", nil)
	writeRootRuntime.GrantSessionWriteRoot(sid, "/opt/derived")

	testutil.FailErr(t, "stop", m.Chats.ReleaseRuntime(t.Context(), sid))
	if roots := writeRootRuntime.SessionWriteRoots(sid); len(roots) != 1 || roots[0] != "/opt/cache" {
		t.Fatalf("roots after Stop = %v, want only the approved grant", roots)
	}
	if len(released) != 1 || len(disposed) != 0 {
		t.Fatalf("Stop ran release %v and disposal %v, want release only", released, disposed)
	}

	m.Resources.Dispose(t.Context(), sid)
	if roots := writeRootRuntime.SessionWriteRoots(sid); len(roots) != 0 {
		t.Fatalf("roots after delete = %v", roots)
	}
	if len(disposed) != 1 || disposed[0] != sid {
		t.Fatalf("disposal = %v, want [%s]", disposed, sid)
	}
}

func TestForgetJobClearsUnclaimedWakePayloads(t *testing.T) {
	m := newForgetTestManager(t)
	const job = "job-1"
	m.Workers.Digests.Put(job, "digest")

	m.Workers.Digests.Forget(job)

	if digest := m.Workers.Digests.Take(job); digest != "" {
		t.Error("workerDigests survived ForgetJob")
	}
}

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	guidancetestsetup.Install()
	anchortestsetup.Install()
	testutil.VerifyNoLeaks(m, func() {
		repochange.ResetWatchersForTest()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := sourcecatalog.Process().Drain(ctx); err != nil {
			panic(fmt.Errorf("drain source catalog: %w", err))
		}
	})
}

func TestResolveToolProfileDelegationUsesCoordinator(t *testing.T) {
	sess := &api.Session{Posture: api.SessionPostureOrchestrate}
	if got := profiles.ResolveToolProfile(sess, orchestration.NewMemoryAgentRegistry(), ""); got != "coordinator" {
		t.Fatalf("profile = %q want coordinator", got)
	}
}

func TestResolveToolProfileWorkerAgent(t *testing.T) {
	reg := orchestration.NewMemoryAgentRegistryForTest()
	sess := &api.Session{Posture: api.SessionPostureOrchestrate, AgentType: "implementer"}
	if got := profiles.ResolveToolProfile(sess, reg, ""); got != "implement" {
		t.Fatalf("profile = %q want implement", got)
	}
}

func TestResolveToolProfilePlanUsesCoordinator(t *testing.T) {
	sess := &api.Session{Posture: api.SessionPostureSpec}
	if got := profiles.ResolveToolProfile(sess, nil, ""); got != "coordinator" {
		t.Fatalf("profile = %q want coordinator", got)
	}
}

func TestSkillSelectorForSessionUsesAgentDefinition(t *testing.T) {
	t.Parallel()
	reg := orchestration.NewMemoryAgentRegistry()
	if err := reg.Register(agentdef.Profile{
		ID:     "curated-worker",
		Skills: skills.Selector{Names: []string{"verify-a-change"}},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	m := newSkillsTestManager(t)
	m.Profiles.SetAgentRegistry(reg)
	got := m.Profiles.SkillSelector(context.Background(), &api.Session{AgentType: "curated-worker"})
	want := skills.Selector{Names: []string{"verify-a-change"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("skill selector = %#v, want %#v", got, want)
	}
}

// Host turns leave pending user questions unanswered.
func TestVisibleUserIntentMessageExcludesHostRows(t *testing.T) {
	hostRows := []api.Message{
		{Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Visibility: api.MessageVisibilityInternal, Kind: api.MessageKindHostLoopWake},
		{Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Visibility: api.MessageVisibilityInternal, Kind: api.MessageKindHostKick, HostSignalID: "worker.task.finished"},
		{Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Visibility: api.MessageVisibilityInternal, Kind: api.MessageKindCoordinatorGuidance, HostSignalID: "PROGRESS_MISSING"},
	}
	for _, msg := range hostRows {
		if promptinput.VisibleIntent(msg) {
			t.Fatalf("%+v must not read as user intent", msg)
		}
	}
	if !promptinput.VisibleIntent(api.Message{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "please add a --version flag"}) {
		t.Fatal("real user chat must read as user intent")
	}
	if promptinput.VisibleIntent(api.Message{
		Role: api.MessageRoleUser, Origin: api.MessageOriginUser,
		Kind: api.MessageKindUserContinuation, Content: "use a different flag",
	}) {
		t.Fatal("an in-turn user continuation must not reset the coordinator batch")
	}
}

func TestHTTPRequestWebPageCardPointsAtFetchURL(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	sess := &api.Session{ID: "sess-web-page"}
	args := map[string]any{"url": "https://docs.example.test/guide"}
	raised := func(deferred bool) guidance.ToolResultFacts {
		return guidance.ToolResultFacts{}.WithFeedback(toolrejection.HTTPRequestWebPageCode, map[string]any{
			"host":               "docs.example.test",
			"bytes":              "612480",
			"content_type":       "text/html; charset=utf-8",
			"fetch_url_deferred": deferred,
		}, &api.FeedbackSubject{Kind: "host", ID: "docs.example.test"})
	}

	out, facts := mgr.ToolPolicy.AfterTool(context.Background(), sess, "http_request", args, `{"status":200}`, 1, raised(false))
	for _, want := range []string{
		`{"status":200}`,
		"Code: HTTP_REQUEST_WEB_PAGE",
		"612480-byte HTML document from docs.example.test",
		"call fetch_url with the same URL.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if !facts.HasCode(toolrejection.HTTPRequestWebPageCode) {
		t.Fatalf("facts = %#v, want the web page code", facts)
	}

	deferred, _ := mgr.ToolPolicy.AfterTool(context.Background(), sess, "http_request", args, `{"status":200}`, 1, raised(true))
	if !strings.Contains(deferred, "after loading it with request_tools") {
		t.Fatalf("deferred fetch_url card omits request_tools:\n%s", deferred)
	}

	plain, _ := mgr.ToolPolicy.AfterTool(context.Background(), sess, "http_request", args, `{"status":200}`, 1, guidance.ToolResultFacts{})
	if strings.Contains(plain, "HTTP_REQUEST_WEB_PAGE") {
		t.Fatalf("an exchange with no stated web page carried the card:\n%s", plain)
	}
}

func TestTryIdleMutationSerializesWithSessionLock(t *testing.T) {
	m := newForgetTestManager(t)
	unlock, ok := m.Runner.Execution.TryIdleMutation("sess-1")
	if !ok || unlock == nil {
		t.Fatal("expected idle mutation lock")
	}

	if _, ok := m.Runner.Execution.TryIdleMutation("sess-1"); ok {
		t.Fatal("second TryIdleMutation must fail while held")
	}

	held := make(chan struct{})
	released := make(chan struct{})
	go func() {
		lock := m.Runner.Execution.Prompt.Acquire("sess-1")
		close(held)
		lock.Lock()
		defer lock.Unlock()
		close(released)
	}()
	select {
	case <-held:
	case <-t.Context().Done():
		t.Fatal("sessionLock waiter did not start")
	}
	if m.Runner.Execution.Prompt.Acquire("sess-1").TryLock() {
		t.Fatal("sessionLock must be held while idle mutation holds the lock")
	}
	select {
	case <-released:
		t.Fatal("sessionLock must block while idle mutation holds the lock")
	default:
	}
	unlock()
	select {
	case <-released:
	case <-t.Context().Done():
		t.Fatal("sessionLock did not acquire after idle mutation release")
	}
}

func TestTryIdleMutationSameMutexAsPrompt(t *testing.T) {
	m := newForgetTestManager(t)
	unlock, ok := m.Runner.Execution.TryIdleMutation("sess-1")
	if !ok {
		t.Fatal("expected lock")
	}
	var wg sync.WaitGroup
	wg.Add(1)
	blocked := make(chan struct{})
	go func() {
		defer wg.Done()
		close(blocked)
		lock := m.Runner.Execution.Prompt.Acquire("sess-1")
		lock.Lock()
		defer lock.Unlock()
	}()
	<-blocked
	if m.Runner.Execution.Prompt.Acquire("sess-1").TryLock() {
		t.Fatal("expected prompt lock to be held")
	}
	unlock()
	wg.Wait()
}

func TestEffectiveLimitsAppliesLiveDerived(t *testing.T) {
	tmp := t.TempDir()
	bundledPolicy := filepath.Join(tmp, "policy.yaml")
	globalPolicy := filepath.Join(tmp, "global-policy.yaml")
	if err := os.WriteFile(bundledPolicy, []byte(`
coordinator:
  provider_id: fw
  model: accounts/fireworks/models/kimi-k2p7-code
lite:
  provider_id: ""
  model: ""
agent_pool:
  selection: first
  models: []
`), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := llm.NewPolicyStoreAt(globalPolicy)
	testutil.FailErr(t, "NewPolicyStoreAt", err)

	configtest.Overlay(t, map[config.Rel]string{
		config.SessionLimits: "max_iterations: 10\nmax_tool_spill_bytes: 1024\n",
	})
	globalLimits := filepath.Join(tmp, "global-limits.yaml")
	limStore, err := settings.NewLimitsStoreAt(globalLimits)
	testutil.FailErr(t, "NewLimitsStoreAt", err)

	mgr := NewHost(sessionstore.NewMemory(), Models{Client: nil, Provider: &llm.Service{Policy: policy}, Limits: settings.SessionLimits{}, Cost: nil}, nil)
	mgr.Limits.SetProvider(settings.ProjectLimitsAdapter{Store: limStore})

	sess := &api.Session{WorkspacePath: tmp}
	got := mgr.Limits.Effective(t.Context(), sess)
	if got.MaxToolResultBytes != 524288 || got.MaxCoordinatorLoopCycles != 256 {
		t.Fatalf("kimi derived limits = bytes %d cycles %d", got.MaxToolResultBytes, got.MaxCoordinatorLoopCycles)
	}

	testutil.FailErr(t, "PutGlobal overlay", limStore.PutGlobal(settings.SessionLimits{MaxToolResultBytes: 99999}))
	got = mgr.Limits.Effective(t.Context(), sess)
	if got.MaxToolResultBytes != 99999 {
		t.Fatalf("overlay MaxToolResultBytes = %d want 99999", got.MaxToolResultBytes)
	}
	if got.MaxCoordinatorLoopCycles != 256 {
		t.Fatalf("cycles = %d want derived 64", got.MaxCoordinatorLoopCycles)
	}
}
