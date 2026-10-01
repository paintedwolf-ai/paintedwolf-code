package session

import (
	"context"
	"github.com/lycaon/lycaon/internal/promptresult"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testdbseed"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestManifestCoordinatorProfileOverridesAgent(t *testing.T) {
	agents := loadAgentsForTest(t)
	sess := &api.Session{Posture: api.SessionPostureSpec, AgentType: orchestration.ProfileCoordinator}
	got := ResolveToolProfile(sess, agents, "worker_readonly")
	if got != "worker_readonly" {
		t.Fatalf("profile = %q want worker_readonly", got)
	}
}

func loadAgentsForTest(t *testing.T) AgentProfileResolver {
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
	reg, err := LoadPostureRegistry()
	testutil.FailErr(t, "LoadPostureRegistry failed", err)
	mgr := NewManager(store, nil, nil, settings.DefaultSessionLimits())
	mgr.SetPostureRegistry(reg)
	mgr.SetAgentRegistry(loadAgentsForTest(t))
	got, err := mgr.promptToolProfile(ctx, sess)
	testutil.FailErr(t, "mgr.promptToolProfile failed", err)
	if got != "coordinator" {
		t.Fatalf("profile = %q want coordinator", got)
	}
}

func TestManagerPromptToolProfileUsesWorkflowManifest(t *testing.T) {
	reg, err := LoadPostureRegistry()
	testutil.FailErr(t, "LoadPostureRegistry failed", err)
	mgr := NewManager(store.NewMemory(), nil, nil, settings.DefaultSessionLimits())
	mgr.SetPostureRegistry(reg)
	mgr.SetAgentRegistry(loadAgentsForTest(t))
	mgr.SetWorkflowSessionView(stubWorkflowManifest{
		manifest: ActiveWorkflowManifest{CoordinatorProfile: "worker_readonly"},
		ok:       true,
	})
	sess := &api.Session{ID: "s1", Posture: api.SessionPostureSpec, AgentType: orchestration.ProfileCoordinator}
	got, err := mgr.promptToolProfile(context.Background(), sess)
	testutil.FailErr(t, "mgr.promptToolProfile failed", err)
	if got != "worker_readonly" {
		t.Fatalf("profile = %q want worker_readonly", got)
	}
}

func TestProjectPostureOverlayCannotSelectToolProfile(t *testing.T) {
	reg, err := LoadPostureRegistry()
	testutil.FailErr(t, "LoadPostureRegistry failed", err)
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
	mgr := NewManager(store.NewMemory(), nil, nil, settings.DefaultSessionLimits())
	mgr.SetPostureRegistry(reg)
	mgr.SetAgentRegistry(loadAgentsForTest(t))
	projectID := RegisterProjectContextForTest(t, mgr, dir)
	sess := &api.Session{ID: "s1", ProjectID: projectID, Posture: api.SessionPostureSpec, AgentType: orchestration.ProfileCoordinator, WorkspacePath: dir}
	got, err := mgr.promptToolProfile(context.Background(), sess)
	testutil.FailErr(t, "mgr.promptToolProfile failed", err)
	if got != orchestration.ProfileCoordinator {
		t.Fatalf("overlay profile = %q want %q", got, orchestration.ProfileCoordinator)
	}
}

type stubWorkflowManifest struct {
	manifest ActiveWorkflowManifest
	ok       bool
}

func (s stubWorkflowManifest) AssertSessionRunnable(context.Context, string) error { return nil }
func (s stubWorkflowManifest) CurrentPhase(context.Context, string) string         { return "" }
func (s stubWorkflowManifest) ActiveReviewVerdictPending(context.Context, string) bool {
	return false
}

func (s stubWorkflowManifest) ActiveCloseoutGateState(context.Context, string) WorkflowCloseoutGateState {
	return WorkflowCloseoutGateState{}
}

func (s stubWorkflowManifest) ActivePhaseHasReviewLoop(context.Context, string) bool {
	return false
}
func (s stubWorkflowManifest) ActivePhaseGuardState(context.Context, string) WorkflowPhaseGuardState {
	return WorkflowPhaseGuardState{}
}
func (s stubWorkflowManifest) AllowedAgents(context.Context, string) []string { return nil }
func (s stubWorkflowManifest) ResolvedRequest(context.Context, string) ResolvedWorkflowRequest {
	return ResolvedWorkflowRequest{}
}

func (s stubWorkflowManifest) ActiveManifest(context.Context, string) (ActiveWorkflowManifest, bool) {
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
func (s stubWorkflowManifest) GetActive(context.Context, string) (*api.WorkflowRun, error) {
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
