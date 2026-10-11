package session_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func boardInjectManager(t *testing.T) (*session.Host, *llm.RecordingClient, *store.Memory, repoinfo.Provider) {
	t.Helper()
	t.Setenv("LYCAON_LLM_MOCK", "1")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/inject\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}))
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: rec, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	oartest.InstallCloseoutPolicy(t, mgr)
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	reconP := repotest.NewProvider(t)
	builder := &board.SnapshotBuilder{
		Repo:        reconP,
		Delegations: delegation.NewMemoryStore(),
		Workers:     worker.NewInMemoryQueue(10),
	}
	mgr.Coordinator.ConfigureBoard(&board.InjectBuilder{SnapshotBuilder: builder}, board.DefaultInjectFormatter(), mgr.Promotion)
	return mgr, rec, store, reconP
}

func materializeRepoBrief(t *testing.T, p repoinfo.Provider, dir string) {
	t.Helper()
	p.Warm(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := repoinfo.AwaitBrief(ctx, p, dir); err != nil {
		testutil.FailErr(t, "AwaitBrief", err)
	}
}

func packBoardMessages(msgs []api.Message) []api.Message {
	var out []api.Message
	for _, m := range msgs {
		if m.Role == api.MessageRoleSystem && boardInjectMarker(m.Content) {
			out = append(out, m)
		}
	}
	return out
}

func boardInjectMarker(content string) bool {
	return strings.Contains(content, "lycaon-board-orientation:v1") ||
		strings.Contains(content, "pack-board:v1")
}

func TestInjectOnImplementDefaultFirstTurn(t *testing.T) {
	mgr, rec, store, repo := boardInjectManager(t)
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	materializeRepoBrief(t, repo, dir)

	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	testdbseed.BindSessionWorkspace(t, store, sess.ID, dir)
	testdbseed.BindSessionWorkspace(t, store, sess.ID, dir)
	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, "hello"); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
	if len(packBoardMessages(rec.LastRequest().Messages)) == 0 {
		t.Fatal("implement-default chat should inject pack board on first turn")
	}
}

func TestInjectSkipsWhenHashUnchanged(t *testing.T) {
	mgr, rec, store, repo := boardInjectManager(t)
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module stable\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	materializeRepoBrief(t, repo, dir)
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	testdbseed.BindSessionWorkspace(t, store, sess.ID, dir)
	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, "one"); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, "two"); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
	if len(packBoardMessages(rec.LastRequest().Messages)) != 0 {
		t.Fatal("expected no redundant pack board inject on stable second prompt")
	}
}

func TestInjectForcesOnPhaseChange(t *testing.T) {
	mgr, rec, store, repo := boardInjectManager(t)
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module phase\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	materializeRepoBrief(t, repo, dir)
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	testdbseed.BindSessionWorkspace(t, store, sess.ID, dir)
	mgr.SetCoordinatorTurnFrameSource(&phaseStubCoordinator{phase: "plan"})
	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, "a"); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
	mgr.SetCoordinatorTurnFrameSource(&phaseStubCoordinator{phase: "implement"})
	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, "b"); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
	if len(packBoardMessages(rec.LastRequest().Messages)) == 0 {
		t.Fatal("expected inject after phase change")
	}
}

func TestInjectOmitsWorkflowWhenRunContext(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module rally\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	depStore := delegation.NewMemoryStore()
	rec2 := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}))
	store := store.NewMemory()
	mgr2 := session.NewHost(store, session.Models{Client: rec2, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	oartest.InstallCloseoutPolicy(t, mgr2)
	wirePromptTestManager(t, mgr2)
	mgr2.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	builder := &board.SnapshotBuilder{Repo: repotest.NewProvider(t), Delegations: depStore, Workers: worker.NewInMemoryQueue(10)}
	mgr2.Coordinator.ConfigureBoard(&board.InjectBuilder{SnapshotBuilder: builder}, board.DefaultInjectFormatter(), mgr2.Promotion)
	mgr2.SetCoordinatorTurnFrameSource(&phaseStubCoordinator{phase: "implement"})

	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	testdbseed.BindSessionWorkspace(t, store, sess.ID, dir)
	if _, err := depStore.Create(ctx, api.Delegation{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir, Task: "task", Phase: api.DelegationPhaseWorker}, sess.ID, []api.Leg{{}}); err != nil {
		testutil.FailErr(t, "depStore.Create failed", err)
	}
	if _, err := mgr2.Submissions.Prompt(ctx, sess.ID, "go"); err != nil {
		testutil.FailErr(t, "mgr2.Prompt failed", err)
	}
	for _, msg := range rec2.LastRequest().Messages {
		// Match the emitted line, not the orientation prose that names the field.
		if boardInjectMarker(msg.Content) && strings.Contains(msg.Content, "\nDelegation:") {
			t.Fatalf("inject should omit the delegation line when run context is present: %q", msg.Content)
		}
	}
}

type phaseStubCoordinator struct {
	phase   string
	brief   string
	failed  []string
	surface string
}

func (p *phaseStubCoordinator) BuildCoordinatorTurnFrame(_ context.Context, _ string, _ *api.Session) (inject.CoordinatorTurnFrame, error) {
	return inject.CoordinatorTurnFrame{RunContext: api.CoordinatorRunContext{
		CurrentPhase:            p.phase,
		WorkflowID:              "plan",
		PhaseCoordinatorSurface: p.surface,
		CoordinatorBrief:        p.brief,
		FailedLeaves:            p.failed,
	}}, nil
}

func TestLegFinishedKickIncludesRelativeTime(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	store := store.NewMemory()
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}))
	mgr := session.NewHost(store, session.Models{Client: rec, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	oartest.InstallCloseoutPolicy(t, mgr)
	wirePromptTestManager(t, mgr)
	testutil.FailErr(t, "install anchor registry", mgr.Coordinator.Guidance.InstallAnchorRegistry())
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	done := time.Now().UTC().Add(-2 * time.Minute)
	mgr.Coordinator.Guidance.Emit(context.Background(), sess.ID, anchor.LegFinished, anchor.Envelope{CompletedAt: &done})
	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, "next"); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
	// Host-authored rows keep their system role and transcript position.
	found := false
	for _, msg := range rec.LastRequest().Messages {
		if msg.Role == api.MessageRoleSystem && strings.Contains(msg.Content, "finished") && strings.Contains(msg.Content, "ago") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected leg-finished kick with relative ago suffix")
	}
}
