package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workflowfacts"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/pkg/api"
)

func newSynthesisDelayManager(t *testing.T) (*Host, *api.Session) {
	t.Helper()
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint registry", err)
	store := store.NewMemory()

	sess, err := store.Create(context.Background(), api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create coordinator session", err)
	rejectFmt := guidance.NewStaticRejectFormatter(hints)
	mgr := NewHost(store, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	mgr.SetProgressStore(progress.NewMemoryStore())
	mgr.SetRejectFormatter(rejectFmt)
	schemaDir := filepath.Join("..", "..", "..", "schemas")
	loader, err := oar.NewLoader(schemaDir)
	testutil.FailErr(t, "oar.NewLoader", err)
	rs, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "oar.LoadStock", err)
	pipeline := oar.NewGuardPipeline(rs, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorCoordinatorCloseoutCheck)
	pipeline.EnableAnchor(oar.AnchorCoordinatorPreInvoke)
	pipeline.EnableAnchor(oar.AnchorWorkerReportCheck)
	mgr.SetOARPipeline(pipeline, oar.NewRenderer(rejectFmt, synthesisDelayNudge{f: rejectFmt}))
	return mgr, sess
}

type synthesisDelayNudge struct {
	f *guidance.StaticRejectFormatter
}

func (n synthesisDelayNudge) FormatNudge(ctx context.Context, code string, data map[string]any) (string, error) {
	return guidance.FormatCoordinatorNudge(ctx, n.f, code, data)
}

func rejectCloseout(t *testing.T, mgr *Host, sess *api.Session, surfaceID string, workersIdle bool) (string, bool) {
	t.Helper()
	reject, blocked := mgr.Coordinator.Guards.OpenProgress(context.Background(), sess, nil, surfaceID, workersIdle, surface.ImplementSessionState{}, true)
	return reject.Error(), blocked
}

func TestCloseoutSkipsOpenProgressWhenInvokeGated(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.RewindRuntime.Progress.Set(sess.ID, "## Progress\n- [ ] review")

	if _, block := mgr.Coordinator.Guards.OpenProgress(
		context.Background(), sess, nil, "implement_investigate", true, surface.ImplementSessionState{}, false,
	); block {
		t.Fatal("expected no open-progress hold when invokeAllowed=false")
	}
}

func TestCloseoutBlocksOnOpenSteps(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.RewindRuntime.Progress.Set(sess.ID, "## Progress\n- [x] survey\n- [ ] review")

	reject, block := rejectCloseout(t, mgr, sess, "implement_investigate", true)
	if !block {
		t.Fatal("expected the closeout to be held back while a step is open")
	}
	if !strings.Contains(reject, "PROGRESS_OPEN_BEFORE_CLOSEOUT") {
		t.Fatalf("reject should carry the hint code, got %q", reject)
	}
}

func TestCloseoutAllowsWhenTerminal(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	// Done, skipped, and optional steps do not block closeout.
	mgr.RewindRuntime.Progress.Set(sess.ID, "## Progress\n- [x] survey\n- [~] review\n- [>] synthesize report")

	if _, block := rejectCloseout(t, mgr, sess, "implement_investigate", true); block {
		t.Fatal("a terminal plan (done/na/optional) must let the closeout through")
	}
}

func TestCloseoutSkipsReadOnlySynthesisAndNonProseSurfaces(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.RewindRuntime.Progress.Set(sess.ID, "## Progress\n- [ ] review")

	if _, block := rejectCloseout(t, mgr, sess, "implement_synthesis", true); block {
		t.Fatal("the read-only synthesis surface must never carry the open-plan push")
	}
	// Dispatch ends through a tool call.
	if _, block := rejectCloseout(t, mgr, sess, "implement_dispatch", true); block {
		t.Fatal("non-prose surfaces must not carry the open-plan push")
	}
	if _, block := rejectCloseout(t, mgr, sess, "implement_investigate", false); block {
		t.Fatal("the push must not apply while workers are still in flight")
	}
}

func TestCloseoutBoundedPerPrompt(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.RewindRuntime.Progress.Set(sess.ID, "## Progress\n- [ ] review")
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, block := rejectCloseout(t, mgr, sess, "implement_investigate", true); !block {
			t.Fatalf("delay %d should still block", i)
		}
	}
	if _, block := rejectCloseout(t, mgr, sess, "implement_investigate", true); block {
		t.Fatal("the closeout must be let through once the per-prompt delay cap is reached")
	}
	mgr.Runner.Closeouts.BeginPrompt(ctx, sess, promptinput.Input{Text: "continue"})
	if _, block := rejectCloseout(t, mgr, sess, "implement_investigate", true); !block {
		t.Fatal("reset should re-arm the push for the next prompt")
	}
}

func TestCloseoutPendingProgressDoesNotArmWorkerCompletionLatch(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register progress", native.RegisterUpdateProgressTool(reg, mgr.RewindRuntime.Progress, func(_ context.Context, id string) string { return id }))
	exec := toolexecution.NewExecutor(nil, reg, "coordinator")
	tctx := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sess.ID}}
	pending := "## Progress\n- [ ] Build feature"
	for attempt := 0; attempt < 2; attempt++ {
		_, err := exec.Invoke(t.Context(), "update_progress", map[string]any{"content": pending}, tctx)
		testutil.FailErr(t, "record pending work", err)
		refusal, blocked := mgr.Coordinator.Guards.OpenProgress(t.Context(), sess, nil, "implement_investigate", true, surface.ImplementSessionState{}, true)
		if !blocked || refusal == nil || refusal.Code() != "PROGRESS_OPEN_BEFORE_CLOSEOUT" {
			t.Fatalf("pending closeout = %v, %v", refusal, blocked)
		}
		if got := mgr.RewindRuntime.Progress.Get(t.Context(), sess.ID); got != pending {
			t.Fatalf("closeout recovery changed unfinished work: %q", got)
		}
		if _, armed := mgr.Coordinator.ProgressClosure.Baseline(sess.ID); armed {
			t.Fatal("pending work armed a worker-completion latch")
		}
	}
	_, err := exec.Invoke(t.Context(), "update_progress", map[string]any{"content": "## Progress\n- [x] Build feature"}, tctx)
	testutil.FailErr(t, "record completed work", err)
	if _, blocked := mgr.Coordinator.Guards.OpenProgress(t.Context(), sess, nil, "implement_investigate", true, surface.ImplementSessionState{}, true); blocked {
		t.Fatal("completed work still blocks closeout")
	}
}

func TestManifestCoordinatorProfileOverridesAgent(t *testing.T) {
	agents := loadAgentsForTest(t)
	sess := &api.Session{Posture: api.SessionPostureSpec, AgentType: orchestration.ProfileCoordinator}
	got := profiles.ResolveToolProfile(sess, agents, "worker_readonly")
	if got != "worker_readonly" {
		t.Fatalf("profile = %q want worker_readonly", got)
	}
}

func loadAgentsForTest(t *testing.T) profiles.AgentProfileResolver {
	t.Helper()
	agents := orchestration.NewMemoryAgentRegistry()
	testutil.FailErr(t, "LoadRequiredAgentRegistry", orchestration.LoadRequiredAgentRegistry(t.Context(), agents))
	return agents
}

func TestBuildSessionCreateDefaultsCoordinatorProfile(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	if sess.AgentType != orchestration.ProfileCoordinator {
		t.Fatalf("agent_type = %q want coordinator", sess.AgentType)
	}
	reg, err := profiles.LoadPostureRegistry()
	testutil.FailErr(t, "profiles.LoadPostureRegistry failed", err)
	mgr := NewHost(store, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	mgr.Profiles.SetPostureRegistry(reg)
	mgr.Profiles.SetAgentRegistry(loadAgentsForTest(t))
	got, err := mgr.Profiles.PromptToolProfile(ctx, sess)
	testutil.FailErr(t, "mgr.Profiles.PromptToolProfile failed", err)
	if got != "coordinator" {
		t.Fatalf("profile = %q want coordinator", got)
	}
}

func TestManagerPromptToolProfileUsesWorkflowManifest(t *testing.T) {
	reg, err := profiles.LoadPostureRegistry()
	testutil.FailErr(t, "profiles.LoadPostureRegistry failed", err)
	mgr := NewHost(store.NewMemory(), Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	mgr.Profiles.SetPostureRegistry(reg)
	mgr.Profiles.SetAgentRegistry(loadAgentsForTest(t))
	mgr.SetWorkflowDomains(workflowDomainFixture(stubWorkflowManifest{
		manifest: workflowfacts.ActiveWorkflowManifest{CoordinatorProfile: "worker_readonly"},
		ok:       true,
	}))
	sess := &api.Session{ID: "s1", Posture: api.SessionPostureSpec, AgentType: orchestration.ProfileCoordinator}
	got, err := mgr.Profiles.PromptToolProfile(context.Background(), sess)
	testutil.FailErr(t, "mgr.Profiles.PromptToolProfile failed", err)
	if got != "worker_readonly" {
		t.Fatalf("profile = %q want worker_readonly", got)
	}
}

func TestProjectPostureOverlayCannotSelectToolProfile(t *testing.T) {
	reg, err := profiles.LoadPostureRegistry()
	testutil.FailErr(t, "profiles.LoadPostureRegistry failed", err)
	dir := t.TempDir()
	overlayDir := filepath.Join(dir, settingsoverlay.DirName())
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	overlay := []byte(`
postures:
  spec:
    label: Audit
`)
	if err := os.WriteFile(filepath.Join(overlayDir, "postures.yaml"), overlay, 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	mgr := NewHost(store.NewMemory(), Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	mgr.Profiles.SetPostureRegistry(reg)
	mgr.Profiles.SetAgentRegistry(loadAgentsForTest(t))
	projectID := RegisterProjectContextForTest(t, mgr, dir)
	sess := &api.Session{ID: "s1", ProjectID: projectID, Posture: api.SessionPostureSpec, AgentType: orchestration.ProfileCoordinator, WorkspacePath: dir}
	got, err := mgr.Profiles.PromptToolProfile(context.Background(), sess)
	testutil.FailErr(t, "mgr.Profiles.PromptToolProfile failed", err)
	if got != orchestration.ProfileCoordinator {
		t.Fatalf("overlay profile = %q want %q", got, orchestration.ProfileCoordinator)
	}
}

type stubWorkflowManifest struct {
	manifest workflowfacts.ActiveWorkflowManifest
	ok       bool
}

func (s stubWorkflowManifest) AssertSessionRunnable(context.Context, string) error { return nil }
func (s stubWorkflowManifest) CurrentPhase(context.Context, string) string         { return "" }
func (s stubWorkflowManifest) ActiveReviewVerdictPending(context.Context, string) bool {
	return false
}

func (s stubWorkflowManifest) ActiveCloseoutGateState(context.Context, string) (workflowfacts.WorkflowCloseoutGateState, error) {
	return workflowfacts.WorkflowCloseoutGateState{}, nil
}

func (s stubWorkflowManifest) ActivePhaseHasReviewLoop(context.Context, string) bool {
	return false
}
func (s stubWorkflowManifest) ActivePhaseGuardState(context.Context, string) workflowfacts.WorkflowPhaseGuardState {
	return workflowfacts.WorkflowPhaseGuardState{}
}
func (s stubWorkflowManifest) AllowedAgents(context.Context, string) []string { return nil }
func (s stubWorkflowManifest) ResolvedRequest(context.Context, string) workflowfacts.ResolvedWorkflowRequest {
	return workflowfacts.ResolvedWorkflowRequest{}
}

func (s stubWorkflowManifest) ActiveManifest(context.Context, string) (workflowfacts.ActiveWorkflowManifest, bool) {
	return s.manifest, s.ok
}
func (s stubWorkflowManifest) ParallelTaskMaxWorkers(context.Context, string) int      { return 0 }
func (s stubWorkflowManifest) ParallelTaskMaxReadWorkers(context.Context, string) int  { return 0 }
func (s stubWorkflowManifest) ParallelTaskMaxWriteWorkers(context.Context, string) int { return 0 }
func (s stubWorkflowManifest) PhaseTouchPaths(context.Context, string) []string        { return nil }
func (s stubWorkflowManifest) ScaffoldVarsForSession(context.Context, string) (map[string]any, error) {
	return nil, nil
}
func (s stubWorkflowManifest) ActivePlan(context.Context, string) (string, string, bool) {
	return "", "", false
}
func (s stubWorkflowManifest) ActivePhaseRequiresEvidence(context.Context, string, string) bool {
	return false
}
func (s stubWorkflowManifest) ActiveBySession(context.Context, string) (*api.WorkflowRun, error) {
	return nil, nil
}
func (s stubWorkflowManifest) IsAmbientRun(*api.WorkflowRun) bool {
	return false
}
func (s stubWorkflowManifest) TrySlashPrompt(context.Context, string, string, string) (*promptresult.Result, bool, error) {
	return nil, false, nil
}
func (s stubWorkflowManifest) AcceptsEmptyRequest(context.Context, string) bool { return false }
func (s stubWorkflowManifest) PrepareUserRequest(_ context.Context, _, text string) (string, *promptresult.Result, bool, error) {
	return text, nil, false, nil
}
func (s stubWorkflowManifest) TryResolveUserFeedback(context.Context, string, string, string, string) error {
	return nil
}
func (s stubWorkflowManifest) StampAndAppendMessages(context.Context, string, ...api.Message) error {
	return nil
}
func (s stubWorkflowManifest) AnnouncePendingAsk(context.Context, string) {}
func (s stubWorkflowManifest) RecordWorkerTerminalProof(context.Context, string, string, string) error {
	return nil
}
func (s stubWorkflowManifest) RecordBoardOrientReady(context.Context, string, string) error {
	return nil
}
func (s stubWorkflowManifest) ReconcileTurnCompletion(context.Context, string) error { return nil }
func (s stubWorkflowManifest) MaybeDeliverTopologyReport(context.Context, string, string) error {
	return nil
}
func (s stubWorkflowManifest) RecordReviewLoopVerdict(context.Context, string, map[string]string, []api.CitationGroundingCitedEvidence) error {
	return nil
}
func (s stubWorkflowManifest) ReconcileOrphanedRuns(context.Context, string) error { return nil }
func (s stubWorkflowManifest) ApplyCoordinatorBatchEvent(context.Context, string, batch.Event, int) error {
	return nil
}
func (s stubWorkflowManifest) ForgetSession(string) {}

func (s stubWorkflowManifest) RecordReviewToolResult(context.Context, string, api.Message) error {
	return nil
}

func (s stubWorkflowManifest) AssertSessionCompletion(context.Context, string) error { return nil }

func (s stubWorkflowManifest) PolicySnapshot(context.Context, string) (toolpolicy.WorkflowSnapshot, error) {
	return toolpolicy.WorkflowSnapshot{ManifestRules: s.manifest.Rules}, nil
}

func (s stubWorkflowManifest) ReconcileWorkerCycle(context.Context, string) error { return nil }
